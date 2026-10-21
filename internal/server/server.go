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
var faviconBytes []byte

type Server struct {
	cfgMgr  *config.Manager
	storage *storage.Storage
	srv     *http.Server
}

func NewServer(cfgMgr *config.Manager, store *storage.Storage) *Server {
	return &Server{
		cfgMgr:  cfgMgr,
		storage: store,
	}
}

func (s *Server) Start(ctx context.Context) error {
	cfg := s.cfgMgr.Get().Server

	mux := http.NewServeMux()
	mux.HandleFunc("/favicon.ico", s.handleFavicon)
	mux.HandleFunc("/storeluna/index.json", s.handleMarketJSON)
	mux.HandleFunc("/market", s.handleMarketJSON)
	mux.HandleFunc("/healthz", s.handleStatus)
	mux.HandleFunc("/status", s.handleStatus)

	handler := s.corsAndSecurityMiddleware(mux)

	addr := fmt.Sprintf(":%d", cfg.Port)
	s.srv = &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	log.Printf("[Server] 市场公共 API 服务已在 http://0.0.0.0:%d 启动", cfg.Port)
	log.Printf("[Server] 可用端点: /storeluna/index.json , /market , /healthz")

	errChan := make(chan error, 1)
	go func() {
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	select {
	case <-ctx.Done():
		return s.srv.Shutdown(context.Background())
	case err := <-errChan:
		return err
	}
}

func (s *Server) corsAndSecurityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 跨域 CORS 支持
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// IP 白名单检查
		clientIP := getClientIP(r)
		allowIPs := s.cfgMgr.Get().Server.AllowIPs
		if !config.CheckIPAllowed(clientIP, allowIPs) {
			http.Error(w, "403 Forbidden: IP not allowed", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleMarketJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

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

	rawData, ok := s.storage.GetRaw()
	if !ok {
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

func getClientIP(r *http.Request) string {
	// 优先取 X-Forwarded-For
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

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(faviconBytes)
}
