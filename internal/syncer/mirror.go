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

type MirrorSyncer struct {
	client *http.Client
}

func NewMirrorSyncer(timeout time.Duration) *MirrorSyncer {
	return &MirrorSyncer{
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

func (m *MirrorSyncer) Sync(ctx context.Context, cfg config.SyncConfig, store *storage.Storage) error {
	start := time.Now()
	upstreamURL := cfg.UpstreamURL
	if upstreamURL == "" {
		upstreamURL = "https://raw.githubusercontent.com/koishijs/registry/master/index.json"
	}

	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client := BuildHTTPClient(timeout, cfg.Proxy)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstreamURL, nil)
	if err != nil {
		store.SetError(err, "mirror", time.Since(start))
		return err
	}
	req.Header.Set("User-Agent", "storeluna-gogo/1.0 (+https://github.com/VincentZyuApps/storeluna-gogo)")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		store.SetError(err, "mirror", time.Since(start))
		return fmt.Errorf("请求上游 registry 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("上游 registry 响应非正常状态码: %d %s", resp.StatusCode, resp.Status)
		store.SetError(err, "mirror", time.Since(start))
		return err
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		store.SetError(err, "mirror", time.Since(start))
		return fmt.Errorf("读取上游响应体失败: %w", err)
	}

	var parsed struct {
		Time    string `json:"time"`
		Total   int    `json:"total"`
		Objects []any  `json:"objects"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		store.SetError(err, "mirror", time.Since(start))
		return fmt.Errorf("解析上游 JSON 失败: %w", err)
	}

	duration := time.Since(start)
	if err := store.Update(data, "mirror", duration, parsed.Time); err != nil {
		store.SetError(err, "mirror", duration)
		return err
	}

	return nil
}
