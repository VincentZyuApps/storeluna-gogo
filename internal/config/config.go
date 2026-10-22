// Package config 实现了服务的动态配置管理、合法性校验与基于 CIDR 的访问控制逻辑。
//
// 核心设计原则：
//  1. 双协议数据绑定 (YAML on Disk, JSON on HTTP)：
//     - 磁盘上的持久化采用简洁、带注释支持的 YAML 格式（config.yaml）；
//     - 面对 WebUI 控制台的 RESTful API，各字段同时打上 json 标签，无需中间转换层即可与前端平滑对接。
//  2. 物理端口安全隔离策略：
//     - 严格约束 server.port（公开市场服务）与 webui.port（管理控制台）不得相同；
//     - 强行切断内网管理凭据与公网客户端流量在同一网络端口混用的安全隐患。
//  3. 线程安全与零重启热更：
//     - Manager 封装了基于 sync.RWMutex 的读写保护，支持服务在不重启进程的前提下被 WebUI 热更新，
//       并向后台各模块即时广播最新配置快照。
//  4. 容器化单文件挂载容错：
//     - 专门兼容 Docker 单文件 bind-mount 模式下的文件覆写限制，消除 rename(2) 产生的 EBUSY 异常。
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// Config 聚合整个 storeluna-gogo 服务的所有配置子项。
type Config struct {
	Server ServerConfig `yaml:"server" json:"server"` // 对外公开市场服务端口与白名单
	WebUI  WebUIConfig  `yaml:"webui" json:"webui"`   // 对内 WebUI 管理控制台端口与白名单
	Sync   SyncConfig   `yaml:"sync" json:"sync"`     // 同步调度模式、周期、代理与超时配置
}

// ServerConfig 定义公开市场 API 服务的网络监听与访问控制策略。
type ServerConfig struct {
	Port     int      `yaml:"port" json:"port"`           // 监听端口，默认 55150
	AllowIPs []string `yaml:"allow_ips" json:"allow_ips"` // 允许访问的 IP / 网段，留空或包含 0.0.0.0/0 表示全网开放
}

// WebUIConfig 定义管理控制台的端口、认证口令与局域网白名单。
type WebUIConfig struct {
	Port          int      `yaml:"port" json:"port"`                     // 监听端口，默认 65150（必须与 Server.Port 不同）
	AllowIPs      []string `yaml:"allow_ips" json:"allow_ips"`           // 允许访问控制台的内网 CIDR 列表（如 192.168.31.0/24）
	AdminPassword string   `yaml:"admin_password" json:"admin_password"` // 控制台管理员访问口令，默认 storeluna-gogo
}

// ProxyConfig 封装用于访问外部网络（GitHub / NPM）的上游代理配置。
type ProxyConfig struct {
	Enabled bool   `yaml:"enabled" json:"enabled"` // 是否开启网络代理中转
	URL     string `yaml:"url" json:"url"`         // 代理完整 URL，支持 http://, https://, socks5://, socks5h://
}

// SyncConfig 详尽定义插件市场索引的抓取与镜像策略。
type SyncConfig struct {
	Mode            string      `yaml:"mode" json:"mode"`                         // 同步模式: "mirror" (官方极速镜像) 或 "crawler" (全网 NPM 自爬)
	IntervalMinutes int         `yaml:"interval_minutes" json:"interval_minutes"` // 后台定时轮询周期（单位: 分钟）
	UpstreamURL     string      `yaml:"upstream_url" json:"upstream_url"`         // mirror 模式下的上游 Registry URL
	RegistryURL     string      `yaml:"registry_url" json:"registry_url"`         // crawler 模式下的 NPM 注册源地址
	Concurrency     int         `yaml:"concurrency" json:"concurrency"`           // crawler 模式下的工作池并发协程数
	TimeoutSeconds  int         `yaml:"timeout_seconds" json:"timeout_seconds"`   // 上游 HTTP 请求的超时控制（单位: 秒）
	DataPath        string      `yaml:"data_path" json:"data_path"`               // 索引文件落盘的相对或绝对路径，如 "data/index.json"
	Proxy           ProxyConfig `yaml:"proxy" json:"proxy"`                       // 网络代理配置项
}

// Manager 统一管理配置的内存常驻、读写互斥与磁盘同步。
type Manager struct {
	mu   sync.RWMutex // 读写互斥锁，保证配置在被读取与被更新时的线程安全
	path string       // 配置文件物理磁盘路径（如 config.yaml）
	cfg  *Config      // 当前内存中生效的配置快照指针
}

// DefaultAdminPassword 定义系统默认出厂管理员密码。
const DefaultAdminPassword = "storeluna-gogo"

// DefaultConfig 构造并返回一套开箱即用的工业级默认配置。
//
// 默认策略考量：
//   - 公开市场端口设为 55150，全网开放 (0.0.0.0/0, ::/0)；
//   - WebUI 控制台端口设为 65150，默认仅向回环地址（127.0.0.1, ::1）及典型私有局域网网段开放；
//   - 默认采用 mirror 模式，每 30 分钟轮询一次，兼顾低网络开销与数据时效性。
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port: 55150,
			AllowIPs: []string{
				"0.0.0.0/0",
				"::/0",
			},
		},
		WebUI: WebUIConfig{
			Port: 65150,
			AllowIPs: []string{
				"127.0.0.1",
				"::1",
				"192.168.31.0/24",
				"10.0.0.0/8",
				"172.16.0.0/12",
				"192.168.0.0/16",
			},
			AdminPassword: DefaultAdminPassword,
		},
		Sync: SyncConfig{
			Mode:            "mirror",
			IntervalMinutes: 30,
			UpstreamURL:     "https://raw.githubusercontent.com/koishijs/registry/master/index.json",
			RegistryURL:     "https://registry.npmmirror.com",
			Concurrency:     8,
			TimeoutSeconds:  30,
			DataPath:        "data/index.json",
			Proxy: ProxyConfig{
				Enabled: false,
				URL:     "http://127.0.0.1:7890",
			},
		},
	}
}

// Validate 对配置进行严格的业务规则与安全防御性校验。
//
// 校验规则：
//  1. 端口合法性 (1-65535)；
//  2. 物理端口互斥防呆：server.port 绝不允许与 webui.port 相同；
//  3. 工作模式有效性检查（仅限 mirror 或 crawler）；
//  4. 自动修正常见非法边界值（如非法的并发数、负数超时等自动回退保底安全值）。
func (c *Config) Validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port 必须在 1-65535 之间，当前为 %d", c.Server.Port)
	}
	if c.WebUI.Port <= 0 || c.WebUI.Port > 65535 {
		return fmt.Errorf("webui.port 必须在 1-65535 之间，当前为 %d", c.WebUI.Port)
	}
	if c.Server.Port == c.WebUI.Port {
		return fmt.Errorf("安全错误: server.port (%d) 与 webui.port (%d) 不能相同，必须实施物理端口隔离", c.Server.Port, c.WebUI.Port)
	}
	if c.Sync.Mode != "mirror" && c.Sync.Mode != "crawler" {
		return fmt.Errorf("sync.mode 必须为 'mirror' 或 'crawler'，当前为 '%s'", c.Sync.Mode)
	}
	if c.Sync.IntervalMinutes <= 0 {
		c.Sync.IntervalMinutes = 30
	}
	if c.Sync.Concurrency <= 0 {
		c.Sync.Concurrency = 5
	}
	if c.Sync.TimeoutSeconds <= 0 {
		c.Sync.TimeoutSeconds = 30
	}
	if c.Sync.DataPath == "" {
		c.Sync.DataPath = "data/index.json"
	}
	return nil
}

// NewManager 初始化一个配置管理器实例。若路径下文件不存在，将自动创建初始默认配置。
func NewManager(path string) (*Manager, error) {
	m := &Manager{path: path}
	if err := m.Reload(); err != nil {
		return nil, err
	}
	return m, nil
}

// Reload 重新从磁盘读取并解析配置文件，并更新内存快照（线程安全）。
func (m *Manager) Reload() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 若配置文件不存在，自动生成并持久化默认模板
	if _, err := os.Stat(m.path); os.IsNotExist(err) {
		cfg := DefaultConfig()
		if err := saveToFile(m.path, cfg); err != nil {
			return fmt.Errorf("创建默认配置文件失败: %w", err)
		}
		m.cfg = cfg
		return nil
	}

	data, err := os.ReadFile(m.path)
	if err != nil {
		return fmt.Errorf("读取配置文件失败: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("解析配置文件 YAML 失败: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	m.cfg = &cfg
	return nil
}

// Get 获取当前最新生效的配置结构体快照（只读浅拷贝，线程安全）。
func (m *Manager) Get() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return *m.cfg
}

// Update 接收外部（如 WebUI 控制台）传入的新配置，校验、原子落盘并更新内存快照。
func (m *Manager) Update(newCfg *Config) error {
	if err := newCfg.Validate(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 先持久化落盘
	if err := saveToFile(m.path, newCfg); err != nil {
		return fmt.Errorf("保存配置文件失败: %w", err)
	}

	// 落盘成功后原子替换内存指针
	m.cfg = newCfg
	return nil
}

// IsDefaultPassword 检查当前管理员口令是否仍为初始默认密码。
func (m *Manager) IsDefaultPassword() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.WebUI.AdminPassword == DefaultAdminPassword
}

// saveToFile 辅助函数：负责将配置结构体序列化为 YAML 并安全写入物理磁盘。
//
// 知识点与 Linux VFS / Docker 挂载深度解析：
//  1. 为什么采用临时文件 + Rename 机制？
//     - 直接 os.WriteFile 若遭遇进程异常崩溃或断电，可能导致目标文件内容残缺（只写了一半）。
//     - POSIX 标准下的 rename 系统调用是原子的，要么全替换，要么保留原状。
//  2. Docker 单文件挂载 (Single-File Bind-Mount) 下的设备繁忙限制：
//     - 当使用 docker run -v /host/config.yaml:/app/config.yaml 时，/app/config.yaml 作为
//       宿主机与容器之间的挂载锚点（Mount Point）。
//     - Linux 内核严格禁止任何进程对处于 mount point 上的目标调用 rename(2)，会抛出 EBUSY (Device or resource busy)。
//     - 因此在此处增加防御性回退分支：捕获到 rename 失败时，自动退化为直接对挂载文件进行原地覆盖写入（WriteFile），
//       从而在保证 Docker 容器稳定运行的同时兼顾热重载。
func saveToFile(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	bytes, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	tmpFile := path + ".tmp"
	if err := os.WriteFile(tmpFile, bytes, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmpFile, path); err != nil {
		// 在 Docker 单文件 bind-mount (-v /host/config.yaml:/app/config.yaml) 下，
		// 目标路径作为挂载点无法被 rename 覆盖（返回 device or resource busy），自动降级为直接覆盖写入
		_ = os.Remove(tmpFile)
		return os.WriteFile(path, bytes, 0644)
	}
	return nil
}

// CheckIPAllowed 校验目标客户端 IP 是否落在给定的白名单规则列表中。
//
// 支持的规则语法与匹配算法：
//   - 全放通规则：允许包含 "0.0.0.0/0", "::/0", "*" 等万能通配符；
//   - 标准 CIDR 无类域间路由子网掩码：例如 "192.168.31.0/24"、"10.0.0.0/8"、"fd00::/8"，通过 net.ParseCIDR 解析后借助 ipNet.Contains 进行高效率位掩码运算；
//   - 单一精确 IP 匹配：如 "127.0.0.1"、"::1"，利用 net.ParseIP 转换后通过 Equal 比较。
func CheckIPAllowed(clientIPStr string, allowedList []string) bool {
	// 若白名单列表为空，默认完全放通
	if len(allowedList) == 0 {
		return true
	}

	// 解析客户端传入的 IP 字符串
	ip := net.ParseIP(clientIPStr)
	if ip == nil {
		// 无法识别为合法的 IPv4/IPv6 地址，安全拒绝
		return false
	}

	// 逐项匹配白名单规则
	for _, entry := range allowedList {
		// 1. 通配符全量放行
		if entry == "0.0.0.0/0" || entry == "::/0" || entry == "*" {
			return true
		}
		// 2. CIDR 网段掩码匹配 (例如 192.168.1.0/24)
		if _, ipNet, err := net.ParseCIDR(entry); err == nil {
			if ipNet.Contains(ip) {
				return true
			}
			continue
		}
		// 3. 单一精确 IP 比对 (例如 127.0.0.1 或 192.168.1.50)
		targetIP := net.ParseIP(entry)
		if targetIP != nil && targetIP.Equal(ip) {
			return true
		}
	}

	// 未命中任何规则，拒绝访问
	return false
}
