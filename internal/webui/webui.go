// Package webui 实现了内嵌单页管理控制台服务。
//
// 架构设计与嵌入式文件系统：
//  1. Go 1.16+ 原生 embed.FS 虚拟文件系统：
//     - 使用 //go:embed assets/* 将 HTML、CSS、JS、图标等所有静态资产在编译期嵌入到只读数据段 (.rodata) 中；
//     - 打包后生成的是一个完全独立的单一静态二进制文件，不需要任何额外的 static/dist 目录，
//       彻底解决部署时因“静态文件路径丢失”引发的 404 问题。
//  2. 双端口与 CIDR 内网隔离：
//     - 控制台默认监听 65150 端口，并挂载 ipFilterMiddleware 中间件；
//     - 即使误将该端口映射到公网，CIDR 白名单也能在第一道关卡阻断外部请求。
//  3. 密码脱敏与双向热重载：
//     - GET /api/config 输出时自动将密码替换为 "******"，防止密码泄露；
//     - POST /api/config 接收前端修改后，触发 Manager.Update() 实现零重启动态热重载与磁盘持久化。
package webui

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/VincentZyuApps/storeluna-gogo/internal/config"
	"github.com/VincentZyuApps/storeluna-gogo/internal/storage"
	"github.com/VincentZyuApps/storeluna-gogo/internal/syncer"
)

//go:embed assets/*
var assetsFS embed.FS // 编译期内嵌的前端资源集合

// WebUI 管理控制台 HTTP 服务核心结构体。
type WebUI struct {
	cfgMgr  *config.Manager  // 动态配置管理器
	storage *storage.Storage // 存储引擎快照
	syncer  *syncer.Syncer   // 核心同步服务句柄
	srv     *http.Server     // 标准库 HTTP 服务实例

	tokenMu     sync.RWMutex    // 保护 validTokens 映射表的读写锁
	validTokens map[string]bool // 内存令牌会话表，用于保存已登录管理员的身份凭据
}

// NewWebUI 创建一个新的管理控制台服务实例。
func NewWebUI(cfgMgr *config.Manager, store *storage.Storage, syncer *syncer.Syncer) *WebUI {
	return &WebUI{
		cfgMgr:      cfgMgr,
		storage:     store,
		syncer:      syncer,
		validTokens: make(map[string]bool),
	}
}

// Start 启动管理控制台 HTTP 监听，并绑定 Context 优雅停机生命周期。
func (w *WebUI) Start(ctx context.Context) error {
	cfg := w.cfgMgr.Get().WebUI

	mux := http.NewServeMux()

	// 1. 挂载内嵌的静态前端文件系统 (FileServer)
	// 利用 fs.Sub 剥离外层 "assets" 目录前缀，使得 index.html 能作为根路由 "/" 访问
	subFS, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return fmt.Errorf("挂载静态资源失败: %w", err)
	}
	fileServer := http.FileServer(http.FS(subFS))
	mux.Handle("/", fileServer)

	// 2. 注册 RESTful API 端点
	mux.HandleFunc("/api/login", w.handleLogin)
	mux.HandleFunc("/api/status", w.authMiddleware(w.handleStatus))
	mux.HandleFunc("/api/config", w.authMiddleware(w.handleConfig))
	mux.HandleFunc("/api/password", w.authMiddleware(w.handlePassword))
	mux.HandleFunc("/api/sync", w.authMiddleware(w.handleSync))

	// 3. 挂载 CIDR 局域网白名单过滤中间件
	handler := w.ipFilterMiddleware(mux)

	// 监听内网端口
	addr := fmt.Sprintf(":%d", cfg.Port)
	w.srv = &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	log.Printf("[WebUI] 管理控制台已在 http://0.0.0.0:%d 启动 (局域网访问限制)", cfg.Port)

	errChan := make(chan error, 1)
	go func() {
		if err := w.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	select {
	case <-ctx.Done():
		// 平滑退出
		return w.srv.Shutdown(context.Background())
	case err := <-errChan:
		return err
	}
}

// ipFilterMiddleware 安全校验中间件：拦截所有非白名单 IP 对 WebUI 的访问。
func (w *WebUI) ipFilterMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		clientIP := getClientIP(r)
		allowIPs := w.cfgMgr.Get().WebUI.AllowIPs
		if !config.CheckIPAllowed(clientIP, allowIPs) {
			log.Printf("[WebUI] 拒绝非白名单 IP 访问: %s", clientIP)
			http.Error(rw, "403 Forbidden: WebUI 仅限指定内网/白名单 IP 访问", http.StatusForbidden)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

// authMiddleware 身份认证拦截器：检查请求头中的 Authorization Bearer 令牌是否有效。
func (w *WebUI) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		adminPwd := w.cfgMgr.Get().WebUI.AdminPassword
		// 若密码留空，表示免密模式（不推荐在生产使用）
		if adminPwd == "" {
			next(rw, r)
			return
		}

		// 解析 Bearer 令牌
		authHeader := r.Header.Get("Authorization")
		token := strings.TrimPrefix(authHeader, "Bearer ")
		token = strings.TrimSpace(token)

		w.tokenMu.RLock()
		isValid := w.validTokens[token]
		w.tokenMu.RUnlock()

		if !isValid {
			rw.Header().Set("Content-Type", "application/json; charset=utf-8")
			rw.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(rw).Encode(map[string]any{"error": "未授权访问，请先登录"})
			return
		}

		next(rw, r)
	}
}

// handleLogin 处理管理员口令登录验证，生成并颁发 128 位安全随机令牌。
func (w *WebUI) handleLogin(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(rw, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// 校验密码
	expectedPwd := w.cfgMgr.Get().WebUI.AdminPassword
	if payload.Password != expectedPwd {
		rw.Header().Set("Content-Type", "application/json; charset=utf-8")
		rw.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(rw).Encode(map[string]any{"error": "密码错误"})
		return
	}

	// 生成高强度安全随机 Token 并加入有效会话表
	token := generateRandomToken()
	w.tokenMu.Lock()
	w.validTokens[token] = true
	w.tokenMu.Unlock()

	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(map[string]any{
		"token": token,
	})
}

// handleStatus 输出控制台仪表盘所需的实时运行状态数据。
func (w *WebUI) handleStatus(rw http.ResponseWriter, r *http.Request) {
	status := w.storage.GetStatus()
	isDefault := w.cfgMgr.IsDefaultPassword()

	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(map[string]any{
		"code":                0,
		"data":                status,
		"is_default_password": isDefault,
		"is_syncing":          w.syncer.IsSyncing(),
	})
}

// handleConfig 提供服务配置的读取与热更新写入。
//
// 知识点：
//   - GET 请求：脱敏输出，将密码置为 "******"，防止在网络调试工具中明文回显；
//   - POST 请求：合并补齐底层缺省字段，调用 cfgMgr.Update 触发原子落盘与内存刷新。
func (w *WebUI) handleConfig(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")

	// 读取当前配置
	if r.Method == http.MethodGet {
		cfg := w.cfgMgr.Get()
		cfg.WebUI.AdminPassword = "******" // 脱敏输出保护
		_ = json.NewEncoder(rw).Encode(cfg)
		return
	}

	// 提交更新配置
	if r.Method == http.MethodPost {
		var incoming config.Config
		if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
			rw.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(rw).Encode(map[string]any{"error": "解析配置数据失败: " + err.Error()})
			return
		}

		current := w.cfgMgr.Get()
		// 若密码未提交修改或仍为脱敏掩码，保留现有原密码
		if incoming.WebUI.AdminPassword == "" || incoming.WebUI.AdminPassword == "******" {
			incoming.WebUI.AdminPassword = current.WebUI.AdminPassword
		}

		// 合并保留底层不可由前端随意清空的字段
		if incoming.Sync.DataPath == "" {
			incoming.Sync.DataPath = current.Sync.DataPath
		}
		if incoming.Sync.RegistryURL == "" {
			incoming.Sync.RegistryURL = current.Sync.RegistryURL
		}
		if incoming.Sync.Concurrency == 0 {
			incoming.Sync.Concurrency = current.Sync.Concurrency
		}
		if incoming.Sync.TimeoutSeconds == 0 {
			incoming.Sync.TimeoutSeconds = current.Sync.TimeoutSeconds
		}
		if incoming.Sync.Proxy.URL == "" {
			incoming.Sync.Proxy.URL = current.Sync.Proxy.URL
		}

		// 触发配置验证、文件保存与内存指针切换
		if err := w.cfgMgr.Update(&incoming); err != nil {
			rw.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(rw).Encode(map[string]any{"error": err.Error()})
			return
		}

		_ = json.NewEncoder(rw).Encode(map[string]any{"message": "配置更新成功并已保存热重载"})
		return
	}

	http.Error(rw, "Method Not Allowed", http.StatusMethodNotAllowed)
}

// handlePassword 提供单独的管理员修改密码接口。
func (w *WebUI) handlePassword(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		rw.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(rw).Encode(map[string]any{"error": "无效的请求格式"})
		return
	}

	newPwd := strings.TrimSpace(payload.NewPassword)
	if len(newPwd) < 4 {
		rw.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(rw).Encode(map[string]any{"error": "新密码长度不能少于 4 位"})
		return
	}

	cfg := w.cfgMgr.Get()
	cfg.WebUI.AdminPassword = newPwd

	if err := w.cfgMgr.Update(&cfg); err != nil {
		rw.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(rw).Encode(map[string]any{"error": "保存密码失败: " + err.Error()})
		return
	}

	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(map[string]any{
		"message": "管理员密码已成功修改",
	})
}

// handleSync 处理手动触发一次全量同步指令。
// 异步执行，不阻塞当前 HTTP 响应通道。
func (w *WebUI) handleSync(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 在独立后台协程中触发同步
	go func() {
		_ = w.syncer.TriggerSync(context.Background())
	}()

	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(map[string]any{"message": "已触发后台全量同步"})
}

// generateRandomToken 使用加密级伪随机数生成器产生 16 字节（128 位）的随机 Hex 字符串。
func generateRandomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// getClientIP 穿透代理提取客户端源 IP。
func getClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			ip := strings.TrimSpace(parts[0])
			if net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		if net.ParseIP(xri) != nil {
			return xri
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
