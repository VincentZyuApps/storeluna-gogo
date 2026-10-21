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
var assetsFS embed.FS

type WebUI struct {
	cfgMgr  *config.Manager
	storage *storage.Storage
	syncer  *syncer.Syncer
	srv     *http.Server

	tokenMu     sync.RWMutex
	validTokens map[string]bool
}

func NewWebUI(cfgMgr *config.Manager, store *storage.Storage, syncer *syncer.Syncer) *WebUI {
	return &WebUI{
		cfgMgr:      cfgMgr,
		storage:     store,
		syncer:      syncer,
		validTokens: make(map[string]bool),
	}
}

func (w *WebUI) Start(ctx context.Context) error {
	cfg := w.cfgMgr.Get().WebUI

	mux := http.NewServeMux()

	// 静态前端资源挂载
	subFS, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return fmt.Errorf("挂载静态资源失败: %w", err)
	}
	fileServer := http.FileServer(http.FS(subFS))
	mux.Handle("/", fileServer)

	// API 路由
	mux.HandleFunc("/api/login", w.handleLogin)
	mux.HandleFunc("/api/status", w.authMiddleware(w.handleStatus))
	mux.HandleFunc("/api/config", w.authMiddleware(w.handleConfig))
	mux.HandleFunc("/api/password", w.authMiddleware(w.handlePassword))
	mux.HandleFunc("/api/sync", w.authMiddleware(w.handleSync))

	handler := w.ipFilterMiddleware(mux)

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
		return w.srv.Shutdown(context.Background())
	case err := <-errChan:
		return err
	}
}

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

func (w *WebUI) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		adminPwd := w.cfgMgr.Get().WebUI.AdminPassword
		if adminPwd == "" {
			next(rw, r)
			return
		}

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

	expectedPwd := w.cfgMgr.Get().WebUI.AdminPassword
	if payload.Password != expectedPwd {
		rw.Header().Set("Content-Type", "application/json; charset=utf-8")
		rw.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(rw).Encode(map[string]any{"error": "密码错误"})
		return
	}

	token := generateRandomToken()
	w.tokenMu.Lock()
	w.validTokens[token] = true
	w.tokenMu.Unlock()

	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(map[string]any{
		"token": token,
	})
}

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

func (w *WebUI) handleConfig(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")

	if r.Method == http.MethodGet {
		cfg := w.cfgMgr.Get()
		cfg.WebUI.AdminPassword = "******" // 脱敏输出
		_ = json.NewEncoder(rw).Encode(cfg)
		return
	}

	if r.Method == http.MethodPost {
		var incoming config.Config
		if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
			rw.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(rw).Encode(map[string]any{"error": "解析配置数据失败: " + err.Error()})
			return
		}

		current := w.cfgMgr.Get()
		// 如果密码未传或保持掩码，则保留原密码
		if incoming.WebUI.AdminPassword == "" || incoming.WebUI.AdminPassword == "******" {
			incoming.WebUI.AdminPassword = current.WebUI.AdminPassword
		}

		// 合并保留底层字段
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

func (w *WebUI) handleSync(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	go func() {
		_ = w.syncer.TriggerSync(context.Background())
	}()

	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(map[string]any{"message": "已触发后台全量同步"})
}

func generateRandomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

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
