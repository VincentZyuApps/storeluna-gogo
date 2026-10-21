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

type Syncer struct {
	cfgMgr  *config.Manager
	storage *storage.Storage

	mu       sync.Mutex
	isSync   bool
	cancelFn context.CancelFunc

	mirror  *MirrorSyncer
	crawler *CrawlerSyncer
}

func NewSyncer(cfgMgr *config.Manager, store *storage.Storage) *Syncer {
	timeout := time.Duration(cfgMgr.Get().Sync.TimeoutSeconds) * time.Second
	return &Syncer{
		cfgMgr:  cfgMgr,
		storage: store,
		mirror:  NewMirrorSyncer(timeout),
		crawler: NewCrawlerSyncer(timeout),
	}
}

func (s *Syncer) Start(ctx context.Context) {
	// 启动时若内存为空，立即执行一次初始同步
	if _, ok := s.storage.GetRaw(); !ok {
		go func() {
			log.Println("[Syncer] 初始缓存为空，立即触发首次同步...")
			_ = s.TriggerSync(context.Background())
		}()
	}

	go s.loop(ctx)
}

func (s *Syncer) loop(ctx context.Context) {
	for {
		interval := time.Duration(s.cfgMgr.Get().Sync.IntervalMinutes) * time.Minute
		if interval <= 0 {
			interval = 30 * time.Minute
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			log.Printf("[Syncer] 定时触发同步 (周期: %v)...", interval)
			_ = s.TriggerSync(ctx)
		}
	}
}

func (s *Syncer) TriggerSync(parentCtx context.Context) error {
	s.mu.Lock()
	if s.isSync {
		s.mu.Unlock()
		return fmt.Errorf("当前已有同步任务正在进行中，请稍候")
	}
	s.isSync = true

	cfg := s.cfgMgr.Get().Sync
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if cfg.Mode == "crawler" {
		timeout = 10 * time.Minute // crawler 涉及全网抓取，适当放宽上下文超时
	}

	ctx, cancel := context.WithTimeout(parentCtx, timeout)
	s.cancelFn = cancel
	s.mu.Unlock()

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

	status := s.storage.GetStatus()
	log.Printf("[Syncer] 同步成功! 插件总数: %d, 耗时: %dms, 上游时间: %s",
		status.TotalPlugins, status.LastSyncDurationMs, status.UpstreamTime)
	return nil
}

func (s *Syncer) IsSyncing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isSync
}
