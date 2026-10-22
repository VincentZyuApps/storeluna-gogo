// Package syncer 负责插件市场索引数据的同步与抓取。
// 该包封装了上游 HTTP 客户端构建、官方 Registry 镜像极速同步、全网 NPM 插件自爬虫以及定时任务生命周期调度。
package syncer

import (
	"crypto/tls"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/VincentZyuApps/storeluna-gogo/internal/config"
)

// BuildHTTPClient 根据给定的超时时长与代理配置，构造一个独立的 *http.Client 实例。
//
// 知识点与底层网络机制深度解析：
//  1. 为什么不直接使用全局 http.DefaultClient 或修改 http.DefaultTransport？
//     - Go 的 http.DefaultClient 默认没有超时时间（Timeout: 0），在遇到恶劣网络或连接挂死时会导致协程永久泄漏。
//     - 若直接篡改 http.DefaultTransport.Proxy，会在高并发下产生全局竞态，且无法支持 WebUI 界面上实时的热重载与隔离。
//  2. 独立 Transport 的连接池与资源回收：
//     - 每个独立初始化的 http.Transport 拥有自身的 TCP 连接池，能保证不同网络策略（走代理 vs 直连）互不干扰。
//     - 连接池会在闲置超时后自动向服务端发送 TCP FIN 包关闭连接，避免文件描述符（FD）积压。
//  3. 代理协议兼容性与 SOCKS5/SOCKS5H 处理：
//     - Go 原生 net/http 从 Go 1.10 开始在标准库层面直接支持 socks5:// 协议（通过内部集成 proxy 包）。
//     - 针对部分客户端常见的 socks5h://（带远程 DNS 解析意图的协议前缀），在此做兼容性字符串转换，确保 url.Parse 能够识别。
//  4. 环境回退机制 (ProxyFromEnvironment)：
//     - 若配置中未显式开启或未指定自定义代理，自动回退到 http.ProxyFromEnvironment。
//     - 这使得在容器或服务器环境中通过标准环境变量 HTTP_PROXY, HTTPS_PROXY, NO_PROXY 即可无缝生效。
//  5. TLS 安全防护：
//     - 显式配置 InsecureSkipVerify: false，严格校验 CA 根证书与域名证书链，杜绝中间人攻击（MITM）。
//  6. Timeout 语义：
//     - http.Client.Timeout 覆盖完整的请求生命周期：包括 TCP 拨号连接、TLS 握手、请求头与请求体发送、
//       服务端处理、直到完整的响应头与响应体全部读取完成。
func BuildHTTPClient(timeout time.Duration, proxyCfg config.ProxyConfig) *http.Client {
	// 初始化独立的传输层底层配置
	transport := &http.Transport{
		// 遵循工业级安全标准，严格校验证书
		TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
		// 连接池优化参数
		MaxIdleConns:        100,              // 最大全局闲置连接数
		MaxIdleConnsPerHost: 20,               // 每个目标 Host 允许保持的最大闲置连接数
		IdleConnTimeout:     90 * time.Second, // 闲置连接保活超时
	}

	// 检查是否启用了显式代理配置
	if proxyCfg.Enabled && strings.TrimSpace(proxyCfg.URL) != "" {
		rawURL := strings.TrimSpace(proxyCfg.URL)

		// 兼容 socks5h:// 前缀（socks5h 表示让代理服务器执行 DNS 域名解析）
		if strings.HasPrefix(rawURL, "socks5h://") {
			rawURL = "socks5://" + strings.TrimPrefix(rawURL, "socks5h://")
		}

		// 解析代理 URL 并挂载到 Transport 的 Proxy 函数指针上
		if u, err := url.Parse(rawURL); err == nil {
			transport.Proxy = http.ProxyURL(u)
		} else {
			// 若代理 URL 格式非法，安全回退到环境代理
			transport.Proxy = http.ProxyFromEnvironment
		}
	} else {
		// 未显式启用自定义代理时，检查系统 HTTP_PROXY / HTTPS_PROXY 环境变量
		transport.Proxy = http.ProxyFromEnvironment
	}

	// 组装并返回包含指定超时控制与自定义传输层的 HTTP 客户端
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}
