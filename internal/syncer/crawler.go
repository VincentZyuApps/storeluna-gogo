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

// CrawlerSyncer 负责以自爬虫（Crawler）模式并发抓取全网 NPM 中的 Koishi 插件。
//
// 架构设计与并发模型：
//  1. 为什么需要自爬模式？
//     - 当上游官方 Registry 服务由于网络故障、GitHub 访问受限或上游故障而不可用时，
//       Crawler 模式能够直接与 npmmirror（或 npmjs 官方源）通信，独立组装最新的市场索引。
//  2. 为什么在 Node.js 中会产生死锁而在 Go 中安全受控？
//     - 原版 Node.js 插件为了抓取 4,700 多个包，曾在没有严格背压（Backpressure）与超时熔断控制下，
//       瞬时产生数千个未决 Promise 和底层 socket，导致 libuv 线程池与事件循环挂起。
//     - 在 Go 中，我们采用经典的“生产者-消费者 Worker Pool（工作池）”模式，通过通道（Channel）作为任务队列，
//       严格限制常驻工作协程数量为 cfg.Concurrency（如 5~10），搭配显式 HTTP 请求超时与 context 级联取消，
//       从根本上消除了内存激增和网络死锁风险。
type CrawlerSyncer struct {
	client *http.Client
}

// NewCrawlerSyncer 创建一个自爬同步器实例。
func NewCrawlerSyncer(timeout time.Duration) *CrawlerSyncer {
	return &CrawlerSyncer{
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// searchResult 对应 NPM Registry 搜索端点 (/-/v1/search) 的分页响应结构体。
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

// npmPackageDetail 对应单个 NPM 包全量元数据端点 (/{package-name}) 的结构定义。
type npmPackageDetail struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	DistTags    map[string]string `json:"dist-tags"` // 包含 "latest" 等发布标签映射
	Versions    map[string]struct {
		Name        string         `json:"name"`
		Version     string         `json:"version"`
		Description string         `json:"description"`
		Keywords    []string       `json:"keywords"`
		License     any            `json:"license"`
		Koishi      map[string]any `json:"koishi"` // package.json 中的 koishi 专属声明清单
	} `json:"versions"`
	Time map[string]string `json:"time"` // 包含各版本发布的时间戳
}

// MarketItem 定义 Koishi 市场最终消费的单个插件对象规范。
type MarketItem struct {
	Package struct {
		Name        string            `json:"name"`
		Version     string            `json:"version"`
		Description string            `json:"description"`
		DistTags    map[string]string `json:"dist-tags"`
		Date        string            `json:"date,omitempty"`
		Keywords    []string          `json:"keywords,omitempty"`
	} `json:"package"`
	ShortName string         `json:"shortname"`          // 简短标识名（去除前缀）
	Manifest  map[string]any `json:"manifest,omitempty"` // 插件清单声明
	Category  string         `json:"category"`           // 插件分类，如 adapter
	Rating    float64        `json:"rating"`             // 评分排序基准
}

// MarketRegistry 定义全量市场索引文件的根 JSON 结构。
type MarketRegistry struct {
	Version int          `json:"version"` // 协议版本，当前标准为 6
	Time    string       `json:"time"`    // ISO / RFC1123 生成时间
	Total   int          `json:"total"`   // 插件总数
	Objects []MarketItem `json:"objects"` // 全量插件清单切片
}

// Sync 执行一次完整的全网自爬流程。
//
// 执行流程：
//  1. 依据当前配置的 Proxy 构造 HTTP 客户端；
//  2. 分页轮询 NPM Registry 搜索 API，收集全网所有匹配 "koishi-plugin-" 的包名；
//  3. 启动固定容量的 Worker Pool 并发抓取各包的最新版本详情并组装为 MarketItem；
//  4. 序列化为规范 JSON，原子写入内存与持久化文件。
func (c *CrawlerSyncer) Sync(ctx context.Context, cfg config.SyncConfig, store *storage.Storage) error {
	start := time.Now()

	// 计算超时时间并构造带有代理规则的 HTTP 客户端
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	c.client = BuildHTTPClient(timeout, cfg.Proxy)

	// 确定目标 Registry 地址（国内推荐 npmmirror，支持高速且无流控限制）
	registryURL := strings.TrimRight(cfg.RegistryURL, "/")
	if registryURL == "" {
		registryURL = "https://registry.npmmirror.com"
	}

	// 并发池大小控制
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 5
	}

	// 阶段 1：分页全量搜索包名
	names, err := c.searchAllKoishiPlugins(ctx, registryURL)
	if err != nil {
		store.SetError(err, "crawler", time.Since(start))
		return err
	}

	// 阶段 2：使用受控 Worker Pool 并发抓取各包的 dist-tags 与详细元数据
	items := c.fetchDetailsConcurrent(ctx, registryURL, names, concurrency)

	// 阶段 3：构建规范的市场根结构体
	registry := MarketRegistry{
		Version: 6,
		Time:    time.Now().UTC().Format(time.RFC1123),
		Total:   len(items),
		Objects: items,
	}

	// 序列化为紧凑 JSON 字节流
	bytes, err := json.Marshal(registry)
	if err != nil {
		store.SetError(err, "crawler", time.Since(start))
		return err
	}

	// 阶段 4：落盘更新并重置运行时状态
	duration := time.Since(start)
	if err := store.Update(bytes, "crawler", duration, registry.Time); err != nil {
		store.SetError(err, "crawler", duration)
		return err
	}

	return nil
}

// searchAllKoishiPlugins 分页搜索全网所有的 koishi-plugin- 相关包名。
//
// 知识点：
//   - NPM 搜索协议限制单页最大返回 size=250 条记录。
//   - 使用 from 参数作为游标分页拉取，直到 len(res.Objects) == 0 或 from >= res.Total 终止循环。
//   - 使用 map[string]bool 做原地去重，防止上游索引并发变动导致同一包名重复记录。
func (c *CrawlerSyncer) searchAllKoishiPlugins(ctx context.Context, registryURL string) ([]string, error) {
	var names []string
	seen := make(map[string]bool)

	from := 0
	size := 250 // NPM API 允许的最大单页大小

	for {
		// 每次外层循环前主动嗅探 Context 是否已被取消，提升响应灵敏度
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

		// 若当页没有数据，说明已遍历结束
		if len(res.Objects) == 0 {
			break
		}

		// 去重收集
		for _, item := range res.Objects {
			name := item.Package.Name
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}

		// 游标推进
		from += len(res.Objects)
		if from >= res.Total || len(res.Objects) < size {
			break
		}
	}

	return names, nil
}

// fetchDetailsConcurrent 使用固定容量 Worker Pool 并发抓取包详情。
//
// 核心并发模式剖析：
//  1. 任务队列 (Job Channel)：
//     - jobs := make(chan string, len(names))
//     - 一次性将所有待抓取的包名写入 Channel 后立即 close(jobs)。
//     - 知识点：关闭后的 Channel 仍可正常读取，直到缓冲区所有数据被消费完毕后，for name := range jobs 自动平滑退出。
//  2. 协程工作组 (Worker Goroutines)：
//     - 仅衍生固定数量 (concurrency) 的 goroutine，杜绝协程无限爆炸。
//  3. 线程安全切片追加：
//     - 多个 worker 在收到有效数据后，必须通过 sync.Mutex 保护临界区，避免切片底层扩容时发生写覆盖与数据竞态（Data Race）。
//  4. 同步栅栏 (sync.WaitGroup)：
//     - 使用 wg.Add(1) 与 defer wg.Done()，由主协程 wg.Wait() 阻塞等待全部任务归并完成。
func (c *CrawlerSyncer) fetchDetailsConcurrent(ctx context.Context, registryURL string, names []string, concurrency int) []MarketItem {
	var mu sync.Mutex
	var items []MarketItem

	// 初始化任务通道并填满任务
	jobs := make(chan string, len(names))
	for _, name := range names {
		jobs <- name
	}
	// 关闭输入端，告知消费者后续无新任务注入
	close(jobs)

	var wg sync.WaitGroup
	// 启动固定配额的工作协程
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for name := range jobs {
				// 支持外部优雅中断
				select {
				case <-ctx.Done():
					return
				default:
				}

				// 获取单个插件的最新版本详情
				item, err := c.fetchSinglePackage(ctx, registryURL, name)
				if err == nil && item != nil {
					// 加互斥锁保证切片并发写入安全
					mu.Lock()
					items = append(items, *item)
					mu.Unlock()
				}
			}
		}()
	}

	// 阻塞等待所有并发 worker 完成处理
	wg.Wait()
	return items
}

// fetchSinglePackage 拉取单个 NPM 包的完整 JSON，并依据 dist-tags.latest 解析目标版本数据。
//
// 知识点：
//   - 作用域包名转义：例如 @koishijs/plugin-market 包含斜杠和 @，必须使用 url.PathEscape 进行 URI 转义，
//     否则会被 HTTP 路由误拆为多层路径导致 404。
//   - 版本决策机制：严格遵循 NPM 标准，以 dist-tags["latest"] 指向的版本为权威最新版。
func (c *CrawlerSyncer) fetchSinglePackage(ctx context.Context, registryURL string, name string) (*MarketItem, error) {
	// 对包名进行安全 URL 转义（特别是处理 @scope/pkg 形式的作用域包）
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
		return nil, fmt.Errorf("上游状态码异常: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var pkg npmPackageDetail
	if err := json.Unmarshal(body, &pkg); err != nil {
		return nil, err
	}

	// 提取权威的最新版本号
	latestVer := pkg.DistTags["latest"]
	if latestVer == "" {
		return nil, fmt.Errorf("缺少 latest dist-tag")
	}

	// 定位该版本在 versions 映射表中的元数据
	verData, ok := pkg.Versions[latestVer]
	if !ok {
		return nil, fmt.Errorf("找不到 latest 版本对应的具体描述数据")
	}

	// 计算 shortname（去除 koishi-plugin- 或 @koishijs/plugin- 前缀）
	shortName := name
	if strings.HasPrefix(shortName, "koishi-plugin-") {
		shortName = strings.TrimPrefix(shortName, "koishi-plugin-")
	} else if strings.HasPrefix(shortName, "@koishijs/plugin-") {
		shortName = strings.TrimPrefix(shortName, "@koishijs/plugin-")
	}

	// 推导分类：如果包名包含 adapter- 则归类为 adapter，否则为 other
	category := "other"
	if strings.Contains(name, "adapter-") {
		category = "adapter"
	}

	// 组装最终条目
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
