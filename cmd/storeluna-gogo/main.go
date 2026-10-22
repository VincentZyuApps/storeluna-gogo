// Package main 为 storeluna-gogo 服务的应用程序主入口。
//
// 架构设计总览：
// storeluna-gogo 是一个纯 Go 编写的高性能、轻量级 Koishi 插件市场索引镜像与管理服务。
// 它的核心目标是替代原先依赖完整 Node.js/Koishi 运行时的传统同步方案，将常驻内存从 300MB+ 降低至约 15MB，
// 并彻底消除由于单线程异步事件循环（Event Loop）与 libuv 线程池死锁导致的高并发挂死问题。
//
// 核心组件依赖关系与拓扑结构：
//   1. ConfigManager (配置中心): 负责 config.yaml 的加载、实时热重载（RWMutex）与落盘持久化。
//   2. Storage (内存存储引擎): 读多写少的高并发只读快照引擎，管理 index.json 内存数据与磁盘缓存。
//   3. Syncer (同步调度器): 包含 Mirror (官方秒级镜像拉取) 与 Crawler (npm 并发爬虫 Worker Pool) 双工作模式。
//   4. Server (公共市场服务): 监听 55150 端口，对外全网暴露 /storeluna/index.json, /market 等接口。
//   5. WebUI (管理控制台): 监听 65150 端口，双端口隔离 + 局域网 CIDR 拦截，提供嵌入式单页管理面板。
//
// 优雅停机（Graceful Shutdown）：
// 采用 Go 标准库的 signal.Notify 捕获 SIGINT / SIGTERM 信号，利用 context.Context 级联通知所有后台
// 协程（HTTP 服务器、定时同步任务）安全退出，释放网络连接与文件句柄。
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
	// Version 声明当前应用程序的版本号。
	// 在生产构建时，通过 Go 编译参数 -ldflags="-X main.Version=x.y.z" 在编译期动态注入。
	Version = "0.1.2"

	// BuildTime 声明应用程序编译构建的 UTC 日期。
	// 同样支持通过 -ldflags="-X main.BuildTime=YYYY-MM-DD" 动态注入。
	BuildTime = "2026-10-23"
)

func main() {
	// =========================================================================
	// 1. 命令行参数解析 (CLI Flags)
	// =========================================================================
	// Go 标准库 flag 包提供极简的命令行入参处理能力。
	// 这里支持通过 -config 指定自定义配置文件路径，通过 -version 查询版本信息。
	configPath := flag.String("config", "config.yaml", "配置文件路径 (YAML 格式)")
	showVersion := flag.Bool("version", false, "显示版本信息并退出")
	flag.Parse()

	// 若用户仅指定了 -version 参数，打印后直接安全退出，避免初始化重型组件
	if *showVersion {
		fmt.Printf("storeluna-gogo v%s (%s)\n", Version, BuildTime)
		return
	}

	// 打印 ASCII 启动横幅与版本信息
	log.Printf("==================================================")
	log.Printf("   storeluna-gogo v%s (Koishi 插件市场高速索引服务)", Version)
	log.Printf("==================================================")

	// =========================================================================
	// 2. 加载或自动初始化配置 (Config Manager)
	// =========================================================================
	// 配置管理器封装了文件的读取、YAML 反序列化、合法性校验 (Validate) 以及
	// 基于 sync.RWMutex 的并发安全热重载能力。若配置文件不存在，将自动生成默认模版。
	cfgMgr, err := config.NewManager(*configPath)
	if err != nil {
		log.Fatalf("[Config] 加载配置失败: %v", err)
	}

	// =========================================================================
	// 3. 初始默认凭据防呆安全检查
	// =========================================================================
	// 为防止将服务暴露在公网导致管理面板被未授权篡改，若检测到仍在使用默认密码，
	// 在终端高亮打印显式边框警告，督促管理员第一时间进行安全加固。
	if cfgMgr.IsDefaultPassword() {
		log.Println("┌────────────────────────────────────────────────────────┐")
		log.Println("│ [WARNING] 当前正在使用默认管理员密码 'storeluna-gogo' ! │")
		log.Println("│ 为了避免配置被篡改，请及时登录 WebUI 控制台修改密码！ │")
		log.Println("└────────────────────────────────────────────────────────┘")
	}

	// =========================================================================
	// 4. 初始化存储引擎与核心同步调度器
	// =========================================================================
	// 提取当前配置快照
	cfg := cfgMgr.Get()

	// 存储引擎负责管理 index.json 内存数据快照与磁盘持久化
	store := storage.NewStorage(cfg.Sync.DataPath)

	// 同步服务装配：持有配置管理器与存储引擎指针
	syncService := syncer.NewSyncer(cfgMgr, store)

	// 根上下文（Root Context）：作为整个服务生命周期的总开关
	// 当 cancel() 被调用时，所有监听 ctx.Done() 的下级协程（HTTP 服务、定时轮询等）都会收到退出通知
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 启动后台定时同步调度协程（定期从官方镜像或全网爬虫抓取最新插件）
	syncService.Start(ctx)

	// =========================================================================
	// 5. 启动公开市场服务 (Server - 监听 55150)
	// =========================================================================
	// 此服务专门用于承载高吞吐、只读的客户端市场请求，全网开放 (0.0.0.0)
	srv := server.NewServer(cfgMgr, store)
	go func() {
		if err := srv.Start(ctx); err != nil {
			log.Fatalf("[Server] 公开市场服务发生致命错误: %v", err)
		}
	}()

	// =========================================================================
	// 6. 启动管理控制台 (WebUI - 监听 65150)
	// =========================================================================
	// 管理服务集成静态资源嵌入（go:embed）与控制台 API，内置 CIDR 局域网白名单拦截
	ui := webui.NewWebUI(cfgMgr, store, syncService)
	go func() {
		if err := ui.Start(ctx); err != nil {
			log.Fatalf("[WebUI] 管理控制台服务发生致命错误: %v", err)
		}
	}()

	// =========================================================================
	// 7. 优雅停机信号监听 (Graceful Shutdown)
	// =========================================================================
	// 信号通知 Channel 必须设置缓冲区大小至少为 1，确保不会因为接收不及时导致系统信号丢失。
	// 监听 SIGINT (Ctrl+C), SIGTERM (Docker/systemd 停机通知), os.Interrupt
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)

	// 阻塞当前主 goroutine，静候系统退出信号
	<-quit

	log.Println("[Main] 接收到退出信号，正在平滑关闭服务...")

	// 触发上下文取消函数，通知下级所有正在运行的协程平稳关闭
	cancel()

	log.Println("[Main] storeluna-gogo 已安全退出。")
}
