package syncer

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/VincentZyuApps/storeluna-gogo/internal/config"
	"github.com/VincentZyuApps/storeluna-gogo/internal/storage"
)

// Syncer 是整个插件市场同步模块的顶层调度大脑。
//
// 架构职责与设计原理：
//  1. 状态机与防重入机制 (Reentrancy Protection)：
//     - 同步任务涉及大量网络 IO 与大块内存更新，绝不允许两个同步协程并发运行。
//     - 使用互斥锁保护 isSync 布尔标记，若当前已有同步在执行，新发起的请求将立即返回友好提示并拒绝重入。
//  2. 动态自适应定时轮询：
//     - 采用基于 Context 的轮询循环，每轮休眠时动态读取 cfgMgr.Get().Sync.IntervalMinutes。
//     - 这意味着管理员在 WebUI 修改同步周期并保存后，调度器无须重启即可在下一轮按新周期执行。
//  3. 上下文级联取消与超时控制：
//     - 无论是 Mirror 还是 Crawler，均受控于挂载在 Syncer 实例上的子 Context。
//     - 在 Crawler 模式下，全网自爬耗时较长，程序提供了更宽裕的上下文超时（10 分钟），并支持外界主动终止。
type Syncer struct {
	cfgMgr  *config.Manager  // 动态配置管理器句柄
	storage *storage.Storage // 内存与磁盘存储引擎句柄

	mu       sync.Mutex         // 保护 isSync 状态机与 cancelFn
	isSync   bool               // 标识当前是否正在执行同步任务
	cancelFn context.CancelFunc // 当前执行中任务的取消闭包函数

	mirror  *MirrorSyncer  // 官方镜像同步执行器
	crawler *CrawlerSyncer // NPM 全网自爬执行器
}

// NewSyncer 构造一个新的同步调度服务实例。
//
// 参数：
//   - cfgMgr: 配置管理器实例，用于每次同步时现场读取最新的代理及工作模式。
//   - store: 存储引擎实例，同步得到的数据直接交付存储引擎落盘。
func NewSyncer(cfgMgr *config.Manager, store *storage.Storage) *Syncer {
	timeout := time.Duration(cfgMgr.Get().Sync.TimeoutSeconds) * time.Second
	return &Syncer{
		cfgMgr:  cfgMgr,
		storage: store,
		mirror:  NewMirrorSyncer(timeout),
		crawler: NewCrawlerSyncer(timeout),
	}
}

// Start 启动后台定时同步调度协程。
//
// 行为逻辑：
//  1. 冷启动快照检查：若服务启动时发现磁盘与内存均无可用缓存，立即在独立协程中触发一次初次同步，
//     避免客户端在刚启动时访问市场返回 404/503。
//  2. 启动长生命周期的 loop 循环监听定时器和父级 Context。
func (s *Syncer) Start(ctx context.Context) {
	// 启动时若内存为空，立即执行一次初始同步
	if _, ok := s.storage.GetRaw(); !ok {
		go func() {
			log.Println("[Syncer] 初始缓存为空，立即触发首次同步...")
			_ = s.TriggerSync(context.Background())
		}()
	}

	// 启动主轮询协程
	go s.loop(ctx)
}

// loop 是后台常驻的定时轮询循环。
//
// 知识点：
//   - 动态周期读取：不在循环外写死 interval，而是每次 select 唤醒后重新从 cfgMgr 获取，支持免重启热重载。
//   - select 多路复用：同时监听父级 ctx.Done()（停机信号）与 time.After(interval)（定时超时）。
func (s *Syncer) loop(ctx context.Context) {
	for {
		// 动态获取当前配置中的同步间隔（分钟）
		interval := time.Duration(s.cfgMgr.Get().Sync.IntervalMinutes) * time.Minute
		if interval <= 0 {
			interval = 30 * time.Minute // 兜底保底周期 30 分钟
		}

		select {
		case <-ctx.Done():
			// 接收到父级优雅停机通知，主动结束协程
			return
		case <-time.After(interval):
			log.Printf("[Syncer] 定时触发同步 (周期: %v)...", interval)
			_ = s.TriggerSync(ctx)
		}
	}
}

// TriggerSync 手动或定时触发一次全量同步。
//
// 关键并发控制流程：
//  1. 加锁检查 isSync；若为 true 立即解锁并报错退出。
//  2. 动态读取当前配置（模式是 mirror 还是 crawler、代理配置、请求超时）。
//  3. 创建带有超时的派生 Context，并将 cancelFn 挂载到结构体字段中。
//  4. 依据 mode 分发给 mirror 或 crawler 执行具体网络拉取。
//  5. defer 块确保无论成功或 panic，isSync 都会被重置为 false，状态机不锁死。
func (s *Syncer) TriggerSync(parentCtx context.Context) error {
	s.mu.Lock()
	if s.isSync {
		s.mu.Unlock()
		return fmt.Errorf("当前已有同步任务正在进行中，请稍候")
	}
	s.isSync = true

	// 动态获取最新配置（支持代理、超时、模式热生效）
	cfg := s.cfgMgr.Get().Sync
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	// crawler 涉及全网数千个包的抓取，放宽上下文超时时间到 10 分钟
	if cfg.Mode == "crawler" {
		timeout = 10 * time.Minute
	}

	// 创建带独立超时的上下文
	ctx, cancel := context.WithTimeout(parentCtx, timeout)
	s.cancelFn = cancel
	s.mu.Unlock()

	// 退出时的防呆与状态重置保障
	defer func() {
		s.mu.Lock()
		s.isSync = false
		if s.cancelFn != nil {
			s.cancelFn()
			s.cancelFn = nil
		}
		s.mu.Unlock()
	}()

	log.Printf("[Syncer] 开始执行同步 (模式: %s)...", cfg.Mode)
	var err error
	if cfg.Mode == "crawler" {
		err = s.crawler.Sync(ctx, cfg, s.storage)
	} else {
		err = s.mirror.Sync(ctx, cfg, s.storage)
	}

	if err != nil {
		log.Printf("[Syncer] 同步失败: %v", err)
		return err
	}

	// 打印同步成功指标
	status := s.storage.GetStatus()
	log.Printf("[Syncer] 同步成功! 插件总数: %d, 耗时: %dms, 上游时间: %s",
		status.TotalPlugins, status.LastSyncDurationMs, status.UpstreamTime)
	return nil
}

// IsSyncing 查询当前是否正在执行同步任务（线程安全）。
func (s *Syncer) IsSyncing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isSync
}
