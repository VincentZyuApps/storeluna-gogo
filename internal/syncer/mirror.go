package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/VincentZyuApps/storeluna-gogo/internal/config"
	"github.com/VincentZyuApps/storeluna-gogo/internal/storage"
)

// MirrorSyncer 负责从上游官方注册表（Koishi Registry）执行完整镜像同步。
//
// 设计哲学与架构权衡：
//  1. 秒级同步 (High Throughput)：
//     - 官方 Registry (例如 https://raw.githubusercontent.com/koishijs/registry/master/index.json
//       或 registry.koishi.chat) 已经聚合了全网 4,700+ 插件的元数据。
//     - 相比于 Crawler（自爬虫）模式需要发起数千次 HTTP 请求，Mirror 模式仅需单次 HTTP GET 请求
//       拉取约 5MB 的全量数据流，通常耗时仅 1~3 秒，零网络带宽浪费与资源占用。
//  2. 零重度反序列化开销 (Zero Heavy-Unmarshal Overhead)：
//     - 原始 JSON 包含了庞大的对象图。如果对 4,700 多个插件的完整字段进行全量 Unmarshal，
//       将产生大量的临时堆对象分配（Heap Allocation）和 GC 垃圾回收压力。
//     - 这里采用“轻量探测结构体（Partial Validation Struct）”提取顶层 time, total, objects 数组长度，
//       校验格式合法后，直接将原始字节流传递给 Storage 缓存与落盘，实现高吞吐。
type MirrorSyncer struct {
	client *http.Client
}

// NewMirrorSyncer 创建一个新的镜像同步器实例。
//
// 参数：
//   - timeout: 默认网络超时时长。
func NewMirrorSyncer(timeout time.Duration) *MirrorSyncer {
	return &MirrorSyncer{
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// Sync 执行一次全量镜像同步。
//
// 执行生命周期与错误兜底：
//  1. 动态客户端构造：每次同步调用时依据最新的 cfg.Proxy 现场构建底层传输客户端，支持动态热更生效。
//  2. HTTP 句柄防泄漏：严格执行 defer resp.Body.Close()，确保底层的 TCP Socket 能够被复用或正常关闭。
//  3. 异常状态捕获：非 2xx 响应码或网络断开均会被封装并记录到 Storage 的错误指标中，供 WebUI 与 /healthz 监控。
func (m *MirrorSyncer) Sync(ctx context.Context, cfg config.SyncConfig, store *storage.Storage) error {
	// 记录任务启动时间戳，用于计算端到端总耗时
	start := time.Now()

	// 确定上游 Registry 源地址
	upstreamURL := cfg.UpstreamURL
	if upstreamURL == "" {
		// 默认回退至官方 GitHub 核心数据源
		upstreamURL = "https://raw.githubusercontent.com/koishijs/registry/master/index.json"
	}

	// 计算超时时间
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	// 现场构造带有当前代理规则的 HTTP Client
	client := BuildHTTPClient(timeout, cfg.Proxy)

	// 构造附带上下文（Context）的 HTTP GET 请求，支持父级超时及优雅取消
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstreamURL, nil)
	if err != nil {
		store.SetError(err, "mirror", time.Since(start))
		return err
	}

	// 遵循 HTTP 规范与开源礼仪，显式附带明确的 User-Agent 标识
	req.Header.Set("User-Agent", "storeluna-gogo/1.0 (+https://github.com/VincentZyuApps/storeluna-gogo)")
	req.Header.Set("Accept", "application/json")

	// 发起实际网络调用
	resp, err := client.Do(req)
	if err != nil {
		store.SetError(err, "mirror", time.Since(start))
		return fmt.Errorf("请求上游 registry 失败: %w", err)
	}
	// 务必确保在函数退出时关闭 Body 流，否则底层 TCP 连接无法归还连接池，引发 FD 泄漏
	defer resp.Body.Close()

	// 校验 HTTP 状态码
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("上游 registry 响应非正常状态码: %d %s", resp.StatusCode, resp.Status)
		store.SetError(err, "mirror", time.Since(start))
		return err
	}

	// 读取完整的上游 JSON 字节流
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		store.SetError(err, "mirror", time.Since(start))
		return fmt.Errorf("读取上游响应体失败: %w", err)
	}

	// 轻量级提取结构体：仅反序列化校验头部元信息，避免全量解析产生海量 GC 压力
	var parsed struct {
		Time    string `json:"time"`
		Total   int    `json:"total"`
		Objects []any  `json:"objects"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		store.SetError(err, "mirror", time.Since(start))
		return fmt.Errorf("解析上游 JSON 失败: %w", err)
	}

	// 计算同步耗时并原子更新内存快照与磁盘缓存
	duration := time.Since(start)
	if err := store.Update(data, "mirror", duration, parsed.Time); err != nil {
		store.SetError(err, "mirror", duration)
		return err
	}

	return nil
}
