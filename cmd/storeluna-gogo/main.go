package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/VincentZyuApps/storeluna-gogo/internal/config"
	"github.com/VincentZyuApps/storeluna-gogo/internal/server"
	"github.com/VincentZyuApps/storeluna-gogo/internal/storage"
	"github.com/VincentZyuApps/storeluna-gogo/internal/syncer"
	"github.com/VincentZyuApps/storeluna-gogo/internal/webui"
)

var (
	Version   = "0.1.0"
	BuildTime = "2026-10-22"
)

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径 (YAML)")
	showVersion := flag.Bool("version", false, "显示版本信息")
	flag.Parse()

	if *showVersion {
		fmt.Printf("storeluna-gogo v%s (%s)\n", Version, BuildTime)
		return
	}

	log.Printf("==================================================")
	log.Printf("   storeluna-gogo v%s (Koishi 插件市场高速索引服务)", Version)
	log.Printf("==================================================")

	// 1. 加载或初始化配置
	cfgMgr, err := config.NewManager(*configPath)
	if err != nil {
		log.Fatalf("[Config] 加载配置失败: %v", err)
	}

	// 2. 默认密码安全检查与警告
	if cfgMgr.IsDefaultPassword() {
		log.Println("┌────────────────────────────────────────────────────────┐")
		log.Println("│ [WARNING] 当前正在使用默认管理员密码 'storeluna-gogo' ! │")
		log.Println("│ 为了避免配置被篡改，请及时登录 WebUI 控制台修改密码！ │")
		log.Println("└────────────────────────────────────────────────────────┘")
	}

	// 3. 初始化存储与同步调度器
	cfg := cfgMgr.Get()
	store := storage.NewStorage(cfg.Sync.DataPath)
	syncService := syncer.NewSyncer(cfgMgr, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 启动后台定时同步调度
	syncService.Start(ctx)

	// 4. 启动公开市场服务 (Server)
	srv := server.NewServer(cfgMgr, store)
	go func() {
		if err := srv.Start(ctx); err != nil {
			log.Fatalf("[Server] 发生致命错误: %v", err)
		}
	}()

	// 5. 启动管理控制台 (WebUI)
	ui := webui.NewWebUI(cfgMgr, store, syncService)
	go func() {
		if err := ui.Start(ctx); err != nil {
			log.Fatalf("[WebUI] 发生致命错误: %v", err)
		}
	}()

	// 6. 优雅关机监听
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-quit

	log.Println("[Main] 接收到退出信号，正在平滑关闭服务...")
	cancel()
	log.Println("[Main] storeluna-gogo 已安全退出。")
}
