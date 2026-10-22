// Package server 实现了面向全网公开的 Koishi 插件市场 HTTP API 服务。
//
// 架构设计与性能哲学：
//  1. 为什么坚守纯 Go 标准库 net/http，而不使用 Gin、Fiber 等第三方重型 Web 框架？
//     - 依赖最小化与极致二进制体积：Gin/Fiber 引入了大量的依赖包与路由树逻辑。在单可执行文件微服务场景下，
//       标准库 net/http 拥有出色的稳定性、极低的代码体积和零 GC 干扰。
//     - 吞吐性能：Go 1.24 的标准库 HTTP/1.1 与 HTTP/2 性能已属工业界第一梯队，其单连接协程调度模型
//       足以支撑数万 QPS 的静态 JSON 高并发直出。
//  2. 零 CPU 预压缩输出：
//     - 核心端点 /storeluna/index.json 与 /market 动态嗅探客户端请求头中的 Accept-Encoding: gzip。
//     - 命中时直接从 Storage 提取预先压缩好的 Gzip 字节切片并附带 Content-Encoding: gzip 响应头，
//       实现纯内存拷贝（Zero-CPU Gzip streaming）。
//  3. 代理反向穿透与 IP 溯源：
//     - 深度支持 Nginx, Caddy, FRP 内网穿透以及 Cloudflare CDN 反向代理，支持从 X-Forwarded-For
//       及 X-Real-IP 头安全解析真实客户端公网 IP 并执行 CIDR 拦截。
package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/VincentZyuApps/storeluna-gogo/internal/config"
	"github.com/VincentZyuApps/storeluna-gogo/internal/storage"
)

//go:embed favicon.ico
var faviconBytes []byte // 将图标资产在编译期直接打包进二进制，零外部文件依附

// Server 维护公开市场 API 的 HTTP 服务端实例。
type Server struct {
	cfgMgr  *config.Manager  // 动态配置中心句柄
	storage *storage.Storage // 存储引擎只读快照句柄
	srv     *http.Server     // 底层标准库 HTTP 服务实体
}

// NewServer 构造公开市场服务实例。
func NewServer(cfgMgr *config.Manager, store *storage.Storage) *Server {
	return &Server{
		cfgMgr:  cfgMgr,
		storage: store,
	}
}

// Start 启动公开市场服务监听，并绑定 Context 优雅停机生命周期。
//
// 知识点与生命周期管理：
//  1. 路由注册：使用标准库 http.NewServeMux()，支持精准路径匹配；
//  2. 中间件包装：洋葱模型包装 CORS 跨域支持与 IP 白名单安全检测；
//  3. 独立协程启动：在子 goroutine 中运行 ListenAndServe()，主协程监听 ctx.Done()；
//  4. 优雅关闭：当收到上下文退出信号，调用 srv.Shutdown()，平稳等待正在进行的连接处理完毕后释放端口。
func (s *Server) Start(ctx context.Context) error {
	cfg := s.cfgMgr.Get().Server

	// 注册路由多路复用器
	mux := http.NewServeMux()
	mux.HandleFunc("/favicon.ico", s.handleFavicon)
	mux.HandleFunc("/storeluna/index.json", s.handleMarketJSON)
	mux.HandleFunc("/market", s.handleMarketJSON)
	mux.HandleFunc("/healthz", s.handleStatus)
	mux.HandleFunc("/status", s.handleStatus)

	// 装配中间件处理链
	handler := s.corsAndSecurityMiddleware(mux)

	// 监听所有网卡 (0.0.0.0) 上的目标端口
	addr := fmt.Sprintf(":%d", cfg.Port)
	s.srv = &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	log.Printf("[Server] 市场公共 API 服务已在 http://0.0.0.0:%d 启动", cfg.Port)
	log.Printf("[Server] 可用端点: /storeluna/index.json , /market , /healthz")

	// 错误传递通道
	errChan := make(chan error, 1)
	go func() {
		// ListenAndServe 在正常 Shutdown 时会返回 http.ErrServerClosed，这属于预期正常退出
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	// 监听父级生命周期与底层异常
	select {
	case <-ctx.Done():
		// 执行平滑关机，停止接收新请求，等待旧请求处理完成
		return s.srv.Shutdown(context.Background())
	case err := <-errChan:
		return err
	}
}

// corsAndSecurityMiddleware 综合中间件：提供跨域资源共享 (CORS) 与 IP CIDR 访问控制。
//
// 设计细节：
//   - 支持全网跨域（Access-Control-Allow-Origin: *），允许浏览器端直接拉取市场数据；
//   - 拦截 OPTIONS 预检请求并快速响应 204 No Content，避免不必要的业务逻辑开销；
//   - 执行 CheckIPAllowed 校验，对不在白名单内的来源返回 403 Forbidden。
func (s *Server) corsAndSecurityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. 设置标准 CORS 跨域响应头
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Authorization")

		// 2. 拦截并处理浏览器预检请求 (Preflight)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// 3. 提取真实源 IP 并执行白名单校验
		clientIP := getClientIP(r)
		allowIPs := s.cfgMgr.Get().Server.AllowIPs
		if !config.CheckIPAllowed(clientIP, allowIPs) {
			http.Error(w, "403 Forbidden: IP not allowed", http.StatusForbidden)
			return
		}

		// 校验通过，放行至下游处理器
		next.ServeHTTP(w, r)
	})
}

// handleMarketJSON 是整个服务的核心数据输出接口。
//
// 知识点与协议支持：
//  1. 请求方法限制：仅允许 GET 与 HEAD 请求，其它方法返回 405 Method Not Allowed；
//  2. HTTP 压缩协商 (Content Negotiation)：
//     - 检查客户端请求头 Accept-Encoding 是否包含 gzip；
//     - 若支持，直接输出内存中预压缩的 s.storage.GetGzip()，并附带 Content-Encoding: gzip；
//     - 若不支持，则输出原始 rawJSON；
//  3. 缓存控制 (Cache-Control)：
//     - 返回 "public, max-age=60"，允许浏览器与 Nginx/CDN 代理缓存 60 秒，有效平抑毛刺并发；
//  4. 服务不可用兜底：若系统刚刚冷启动且尚未获取到任何有效数据，返回 503 状态码及友好提示。
func (s *Server) handleMarketJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 1. 协商 Gzip 压缩支持
	acceptGzip := strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
	if acceptGzip {
		if gzData, ok := s.storage.GetGzip(); ok {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Cache-Control", "public, max-age=60")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(gzData)
			return
		}
	}

	// 2. 直出未压缩的原始 JSON 数据
	rawData, ok := s.storage.GetRaw()
	if !ok {
		// 尚未拉取到可用索引
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":   "索引正在初始化拉取中，请稍后再试",
			"status":  s.storage.GetStatus(),
			"version": 6,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rawData)
}

// handleStatus 输出服务健康度指标与当前插件总数。
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := s.storage.GetStatus()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":    0,
		"status":  "ok",
		"service": "storeluna-gogo",
		"data":    status,
	})
}

// getClientIP 穿透多层代理获取真实的客户端源 IP。
//
// 溯源优先级：
//  1. X-Forwarded-For 请求头的第一项（最接近客户端的真实外部 IP）；
//  2. X-Real-IP 请求头（Nginx 常见传递方式）；
//  3. r.RemoteAddr（底层直连 TCP 链接的 Peer 地址，利用 net.SplitHostPort 剥离端口号）。
func getClientIP(r *http.Request) string {
	// 1. 优先提取 X-Forwarded-For（支持以逗号分隔的多层代理链路，如 "client, proxy1, proxy2"）
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			ip := strings.TrimSpace(parts[0])
			if net.ParseIP(ip) != nil {
				return ip
			}
		}
	}

	// 2. 检查 X-Real-IP
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		if net.ParseIP(xri) != nil {
			return xri
		}
	}

	// 3. 回退到底层 TCP 远端地址
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

// handleFavicon 提供嵌入式网站图标输出，并声明 7 天长期浏览器缓存。
func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(faviconBytes)
}
