// Package storage 实现了高性能、读多写少的内存快照与文件持久化存储引擎。
//
// 架构设计精髓：
//  1. 预压缩优化 (Precomputed Gzip Snapshot)：
//     - Koishi 插件市场的索引文件体积通常在 4MB ~ 6MB 左右。若每个 HTTP 请求到来时都由 Web 服务器
//       动态执行 gzip.NewWriter 压缩，在多客户端并发请求下会导致 CPU 瞬间飙升至 100% 并产生巨大的 GC 压力。
//     - Storage 引擎在每次后台同步（Update）数据时，直接在内存中预先完成一次 Gzip 压缩并长驻保留（gzipJSON）。
//       当客户端请求带有 Accept-Encoding: gzip 时，直接以 O(1) 零 CPU 额外消耗的方式返回预压缩切片！
//  2. 读写分离与零锁竞争 (RWMutex Double Buffering)：
//     - 对外服务（/storeluna/index.json 与 /market）以微秒级吞吐进行高并发只读访问（RLock）；
//     - 仅在同步完成落盘时进行极短时间的排他写锁（Lock）指针替换，实现完全无卡顿的原子切换。
//  3. Docker 容器挂载兼容性落盘：
//     - 采用“写临时文件 + 原子重命名（Atomic Rename）”确保磁盘数据不出现截断。
//     - 自动检测并捕获 Docker 单文件挂载点下的 EBUSY 限制，降级为直接覆盖写入，保证稳定性。
package storage

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Status 描述当前存储引擎与上游同步的整体健康度指标。
// 该结构体会被直接序列化输出到 /healthz 监控端点以及 WebUI 控制台仪表盘中。
type Status struct {
	TotalPlugins       int       `json:"total_plugins"`                 // 当前索引中收录的插件总数
	LastSyncTime       time.Time `json:"last_sync_time"`                 // 本地最近一次同步完成的时间
	LastSyncDurationMs int64     `json:"last_sync_duration_ms"`          // 最近一次同步耗时（毫秒）
	LastSyncError      string    `json:"last_sync_error,omitempty"`      // 若最近一次同步失败，记录错误信息
	Mode               string    `json:"mode"`                           // 当前数据来源模式: mirror / crawler / disk_cache
	UpstreamTime       string    `json:"upstream_time,omitempty"`        // 上游注册表自身声明的版本更新时间戳
}

// Storage 维护内存快照指针、预压缩字节切片及磁盘映射路径。
type Storage struct {
	mu       sync.RWMutex // 读写锁，保护 rawJSON, gzipJSON 与 status 的线程安全
	rawJSON  []byte       // 未压缩的原始 JSON 格式字节流
	gzipJSON []byte       // 预先压缩好的 Gzip 字节流，用于加速支持 gzip 的客户端响应
	status   Status       // 运行时状态聚合指标
	filePath string       // 持久化落盘的文件路径（如 data/index.json）
}

// NewStorage 创建存储引擎实例，并在启动时尝试从本地磁盘加载冷启动快照。
//
// 参数：
//   - filePath: 磁盘持久化存储路径。如果文件存在，启动时立即将其作为冷启动缓存载入内存。
func NewStorage(filePath string) *Storage {
	s := &Storage{
		filePath: filePath,
		status: Status{
			LastSyncTime: time.Time{},
			Mode:         "uninitialized",
		},
	}
	// 尝试从磁盘加载历史持久化缓存
	s.loadFromDisk()
	return s
}

// loadFromDisk 内部冷启动函数：在服务启动时自动读取本地已落盘的 index.json。
// 即使初次启动时外网暂时无法访问，服务也能立即对外提供上一次持久化的高可用市场服务。
func (s *Storage) loadFromDisk() {
	if s.filePath == "" {
		return
	}
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return
	}

	// 快速探测插件总数与生成时间
	var parsed struct {
		Total   int    `json:"total"`
		Time    string `json:"time"`
		Objects []any  `json:"objects"`
	}
	_ = json.Unmarshal(data, &parsed)

	total := parsed.Total
	if total == 0 && len(parsed.Objects) > 0 {
		total = len(parsed.Objects)
	}

	// 为冷数据预先生成 Gzip 切片
	gzData := compressGzip(data)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.rawJSON = data
	s.gzipJSON = gzData
	s.status.TotalPlugins = total
	s.status.UpstreamTime = parsed.Time
	s.status.LastSyncTime = time.Now()
	s.status.Mode = "disk_cache" // 标记数据源自磁盘冷缓存
}

// Update 在一次同步任务成功完成时调用，执行内存快照更新与磁盘文件覆盖。
//
// 知识点与并发保证：
//  1. 预压缩计算在持有写锁之前完成，尽量缩短持有写锁的临界区时间；
//  2. 磁盘文件 IO 在释放写锁之后执行，防止磁盘缓慢写入阻塞高并发读请求；
//  3. 针对 Docker 单文件 bind-mount (-v /host/config.yaml:/app/config.yaml) 的特殊文件系统保护，
//     若 rename(2) 遇到 Device or resource busy 限制，自动降级为直接覆盖写入。
func (s *Storage) Update(data []byte, mode string, duration time.Duration, upstreamTime string) error {
	// 1. 快速提取插件总数
	var parsed struct {
		Total   int   `json:"total"`
		Objects []any `json:"objects"`
	}
	_ = json.Unmarshal(data, &parsed)
	total := parsed.Total
	if total == 0 && len(parsed.Objects) > 0 {
		total = len(parsed.Objects)
	}

	// 2. 在锁外预先完成 Gzip 压缩，不占用互斥锁临界区
	gzData := compressGzip(data)

	// 3. 极速临界区：仅执行指针与结构体拷贝，耗时通常小于 1 微秒
	s.mu.Lock()
	s.rawJSON = data
	s.gzipJSON = gzData
	s.status = Status{
		TotalPlugins:       total,
		LastSyncTime:       time.Now(),
		LastSyncDurationMs: duration.Milliseconds(),
		LastSyncError:      "",
		Mode:               mode,
		UpstreamTime:       upstreamTime,
	}
	filePath := s.filePath
	s.mu.Unlock() // 立即释放写锁，读请求恢复畅通

	// 4. 在锁外安全执行磁盘持久化写入
	if filePath != "" {
		dir := filepath.Dir(filePath)
		if dir != "" && dir != "." {
			_ = os.MkdirAll(dir, 0755)
		}
		tmpFile := filePath + ".tmp"
		if err := os.WriteFile(tmpFile, data, 0644); err == nil {
			// 先尝试原子重命名（最安全的无中断落盘方式）
			if renameErr := os.Rename(tmpFile, filePath); renameErr != nil {
				// 在 Docker 单文件挂载环境下，挂载点无法被重命名覆盖，降级直接写入
				_ = os.Remove(tmpFile)
				_ = os.WriteFile(filePath, data, 0644)
			}
		}
	}
	return nil
}

// SetError 当同步过程发生异常时记录错误信息，重置状态指标供监控面板查询。
func (s *Storage) SetError(err error, mode string, duration time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.LastSyncError = err.Error()
	s.status.LastSyncDurationMs = duration.Milliseconds()
	s.status.Mode = mode
}

// GetRaw 获取未压缩的原始 JSON 数据切片（只读共享，线程安全）。
func (s *Storage) GetRaw() ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.rawJSON) == 0 {
		return nil, false
	}
	return s.rawJSON, true
}

// GetGzip 获取预压缩好的 Gzip 二进制切片（用于零 CPU 开销直接输出到 HTTP 客户端）。
func (s *Storage) GetGzip() ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.gzipJSON) == 0 {
		return nil, false
	}
	return s.gzipJSON, true
}

// GetStatus 获取当前系统运行状态与同步指标快照（线程安全）。
func (s *Storage) GetStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

// compressGzip 辅助函数：使用 gzip.Writer 将输入切片压缩并返回字节流。
func compressGzip(data []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(data)
	_ = zw.Close()
	return buf.Bytes()
}
