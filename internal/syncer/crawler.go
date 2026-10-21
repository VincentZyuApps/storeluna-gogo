package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/VincentZyuApps/storeluna-gogo/internal/config"
	"github.com/VincentZyuApps/storeluna-gogo/internal/storage"
)

type CrawlerSyncer struct {
	client *http.Client
}

func NewCrawlerSyncer(timeout time.Duration) *CrawlerSyncer {
	return &CrawlerSyncer{
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

type searchResult struct {
	Objects []struct {
		Package struct {
			Name        string `json:"name"`
			Version     string `json:"version"`
			Description string `json:"description"`
			Date        string `json:"date"`
		} `json:"package"`
	} `json:"objects"`
	Total int `json:"total"`
}

type npmPackageDetail struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	DistTags    map[string]string `json:"dist-tags"`
	Versions    map[string]struct {
		Name        string         `json:"name"`
		Version     string         `json:"version"`
		Description string         `json:"description"`
		Keywords    []string       `json:"keywords"`
		License     any            `json:"license"`
		Koishi      map[string]any `json:"koishi"`
	} `json:"versions"`
	Time map[string]string `json:"time"`
}

type MarketItem struct {
	Package struct {
		Name        string            `json:"name"`
		Version     string            `json:"version"`
		Description string            `json:"description"`
		DistTags    map[string]string `json:"dist-tags"`
		Date        string            `json:"date,omitempty"`
		Keywords    []string          `json:"keywords,omitempty"`
	} `json:"package"`
	ShortName string         `json:"shortname"`
	Manifest  map[string]any `json:"manifest,omitempty"`
	Category  string         `json:"category"`
	Rating    float64        `json:"rating"`
}

type MarketRegistry struct {
	Version int          `json:"version"`
	Time    string       `json:"time"`
	Total   int          `json:"total"`
	Objects []MarketItem `json:"objects"`
}

func (c *CrawlerSyncer) Sync(ctx context.Context, cfg config.SyncConfig, store *storage.Storage) error {
	start := time.Now()
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	c.client = BuildHTTPClient(timeout, cfg.Proxy)
	registryURL := strings.TrimRight(cfg.RegistryURL, "/")
	if registryURL == "" {
		registryURL = "https://registry.npmmirror.com"
	}

	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 5
	}

	// 1. 分页搜索全网所有的 koishi-plugin- 包
	names, err := c.searchAllKoishiPlugins(ctx, registryURL)
	if err != nil {
		store.SetError(err, "crawler", time.Since(start))
		return err
	}

	// 2. 使用 Worker Pool 抓取每个包的详情
	items := c.fetchDetailsConcurrent(ctx, registryURL, names, concurrency)

	registry := MarketRegistry{
		Version: 6,
		Time:    time.Now().UTC().Format(time.RFC1123),
		Total:   len(items),
		Objects: items,
	}

	bytes, err := json.Marshal(registry)
	if err != nil {
		store.SetError(err, "crawler", time.Since(start))
		return err
	}

	duration := time.Since(start)
	if err := store.Update(bytes, "crawler", duration, registry.Time); err != nil {
		store.SetError(err, "crawler", duration)
		return err
	}

	return nil
}

func (c *CrawlerSyncer) searchAllKoishiPlugins(ctx context.Context, registryURL string) ([]string, error) {
	var names []string
	seen := make(map[string]bool)

	from := 0
	size := 250

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		u := fmt.Sprintf("%s/-/v1/search?text=koishi-plugin-&size=%d&from=%d", registryURL, size, from)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "storeluna-gogo/1.0")

		resp, err := c.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("搜索 NPM 失败 (from=%d): %w", from, err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		var res searchResult
		if err := json.Unmarshal(body, &res); err != nil {
			return nil, fmt.Errorf("解析 NPM 搜索响应失败: %w", err)
		}

		if len(res.Objects) == 0 {
			break
		}

		for _, item := range res.Objects {
			name := item.Package.Name
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}

		from += len(res.Objects)
		if from >= res.Total || len(res.Objects) < size {
			break
		}
	}

	return names, nil
}

func (c *CrawlerSyncer) fetchDetailsConcurrent(ctx context.Context, registryURL string, names []string, concurrency int) []MarketItem {
	var mu sync.Mutex
	var items []MarketItem

	jobs := make(chan string, len(names))
	for _, name := range names {
		jobs <- name
	}
	close(jobs)

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for name := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
				}

				item, err := c.fetchSinglePackage(ctx, registryURL, name)
				if err == nil && item != nil {
					mu.Lock()
					items = append(items, *item)
					mu.Unlock()
				}
			}
		}()
	}

	wg.Wait()
	return items
}

func (c *CrawlerSyncer) fetchSinglePackage(ctx context.Context, registryURL string, name string) (*MarketItem, error) {
	escaped := url.PathEscape(name)
	u := fmt.Sprintf("%s/%s", registryURL, escaped)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "storeluna-gogo/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var pkg npmPackageDetail
	if err := json.Unmarshal(body, &pkg); err != nil {
		return nil, err
	}

	latestVer := pkg.DistTags["latest"]
	if latestVer == "" {
		return nil, fmt.Errorf("no latest dist-tag")
	}

	verData, ok := pkg.Versions[latestVer]
	if !ok {
		return nil, fmt.Errorf("version not found")
	}

	shortName := name
	if strings.HasPrefix(shortName, "koishi-plugin-") {
		shortName = strings.TrimPrefix(shortName, "koishi-plugin-")
	} else if strings.HasPrefix(shortName, "@koishijs/plugin-") {
		shortName = strings.TrimPrefix(shortName, "@koishijs/plugin-")
	}

	category := "other"
	if strings.Contains(name, "adapter-") {
		category = "adapter"
	}

	item := &MarketItem{
		ShortName: shortName,
		Category:  category,
		Rating:    1.0,
	}
	item.Package.Name = name
	item.Package.Version = latestVer
	item.Package.Description = verData.Description
	item.Package.DistTags = pkg.DistTags
	item.Package.Keywords = verData.Keywords
	item.Package.Date = pkg.Time[latestVer]

	return item, nil
}
