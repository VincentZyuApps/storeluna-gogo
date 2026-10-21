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

type Status struct {
	TotalPlugins       int       `json:"total_plugins"`
	LastSyncTime       time.Time `json:"last_sync_time"`
	LastSyncDurationMs int64     `json:"last_sync_duration_ms"`
	LastSyncError      string    `json:"last_sync_error,omitempty"`
	Mode               string    `json:"mode"`
	UpstreamTime       string    `json:"upstream_time,omitempty"`
}

type Storage struct {
	mu           sync.RWMutex
	rawJSON      []byte
	gzipJSON     []byte
	status       Status
	filePath     string
}

func NewStorage(filePath string) *Storage {
	s := &Storage{
		filePath: filePath,
		status: Status{
			LastSyncTime: time.Time{},
			Mode:         "uninitialized",
		},
	}
	s.loadFromDisk()
	return s
}

func (s *Storage) loadFromDisk() {
	if s.filePath == "" {
		return
	}
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return
	}

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

	gzData := compressGzip(data)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.rawJSON = data
	s.gzipJSON = gzData
	s.status.TotalPlugins = total
	s.status.UpstreamTime = parsed.Time
	s.status.LastSyncTime = time.Now()
	s.status.Mode = "disk_cache"
}

func (s *Storage) Update(data []byte, mode string, duration time.Duration, upstreamTime string) error {
	var parsed struct {
		Total   int   `json:"total"`
		Objects []any `json:"objects"`
	}
	_ = json.Unmarshal(data, &parsed)
	total := parsed.Total
	if total == 0 && len(parsed.Objects) > 0 {
		total = len(parsed.Objects)
	}

	gzData := compressGzip(data)

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
	s.mu.Unlock()

	if filePath != "" {
		dir := filepath.Dir(filePath)
		if dir != "" && dir != "." {
			_ = os.MkdirAll(dir, 0755)
		}
		tmpFile := filePath + ".tmp"
		if err := os.WriteFile(tmpFile, data, 0644); err == nil {
			if renameErr := os.Rename(tmpFile, filePath); renameErr != nil {
				_ = os.Remove(tmpFile)
				_ = os.WriteFile(filePath, data, 0644)
			}
		}
	}
	return nil
}

func (s *Storage) SetError(err error, mode string, duration time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.LastSyncError = err.Error()
	s.status.LastSyncDurationMs = duration.Milliseconds()
	s.status.Mode = mode
}

func (s *Storage) GetRaw() ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.rawJSON) == 0 {
		return nil, false
	}
	return s.rawJSON, true
}

func (s *Storage) GetGzip() ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.gzipJSON) == 0 {
		return nil, false
	}
	return s.gzipJSON, true
}

func (s *Storage) GetStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func compressGzip(data []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(data)
	_ = zw.Close()
	return buf.Bytes()
}
