package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server ServerConfig `yaml:"server"`
	WebUI  WebUIConfig  `yaml:"webui"`
	Sync   SyncConfig   `yaml:"sync"`
}

type ServerConfig struct {
	Port     int      `yaml:"port"`
	AllowIPs []string `yaml:"allow_ips"` // 允许访问的网段，留空或包含 0.0.0.0/0 表示全网开放
}

type WebUIConfig struct {
	Port          int      `yaml:"port"`
	AllowIPs      []string `yaml:"allow_ips"`      // 允许访问 WebUI 的 CIDR 列表
	AdminPassword string   `yaml:"admin_password"` // 管理员密码，默认 storeluna-gogo
}

type SyncConfig struct {
	Mode            string `yaml:"mode"`             // "mirror" (推荐) 或 "crawler"
	IntervalMinutes int    `yaml:"interval_minutes"` // 定时更新周期 (分钟)
	UpstreamURL     string `yaml:"upstream_url"`     // mirror 模式源
	RegistryURL     string `yaml:"registry_url"`     // crawler 模式 NPM 镜像源
	Concurrency     int    `yaml:"concurrency"`      // crawler 并发数
	TimeoutSeconds  int    `yaml:"timeout_seconds"`  // 网络请求超时 (秒)
	DataPath        string `yaml:"data_path"`        // 索引持久化路径，如 "data/index.json"
}

type Manager struct {
	mu   sync.RWMutex
	path string
	cfg  *Config
}

const DefaultAdminPassword = "storeluna-gogo"

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
		},
	}
}

func (c *Config) Validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port 必须在 1-65535 之间，当前为 %d", c.Server.Port)
	}
	if c.WebUI.Port <= 0 || c.WebUI.Port > 65535 {
		return fmt.Errorf("webui.port 必须在 1-65535 之间，当前为 %d", c.WebUI.Port)
	}
	if c.Server.Port == c.WebUI.Port {
		return fmt.Errorf("安全错误: server.port (%d) 与 webui.port (%d) 不能相同", c.Server.Port, c.WebUI.Port)
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

func NewManager(path string) (*Manager, error) {
	m := &Manager{path: path}
	if err := m.Reload(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Reload() error {
	m.mu.Lock()
	defer m.mu.Unlock()

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

func (m *Manager) Get() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return *m.cfg
}

func (m *Manager) Update(newCfg *Config) error {
	if err := newCfg.Validate(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := saveToFile(m.path, newCfg); err != nil {
		return fmt.Errorf("保存配置文件失败: %w", err)
	}

	m.cfg = newCfg
	return nil
}

func (m *Manager) IsDefaultPassword() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.WebUI.AdminPassword == DefaultAdminPassword
}

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
	return os.Rename(tmpFile, path)
}

// CheckIPAllowed 校验 IP 是否落在白名单列表中
func CheckIPAllowed(clientIPStr string, allowedList []string) bool {
	if len(allowedList) == 0 {
		return true
	}

	ip := net.ParseIP(clientIPStr)
	if ip == nil {
		return false
	}

	for _, entry := range allowedList {
		if entry == "0.0.0.0/0" || entry == "::/0" || entry == "*" {
			return true
		}
		if _, ipNet, err := net.ParseCIDR(entry); err == nil {
			if ipNet.Contains(ip) {
				return true
			}
			continue
		}
		// 单 IP 比对
		if net.ParseIP(entry) != nil && net.ParseIP(entry).Equal(ip) {
			return true
		}
	}
	return false
}
