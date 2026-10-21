# storeluna-gogo

> 纯 Go 实现的超轻量、高性能、高可用 Koishi 插件市场索引镜像服务与管理面板。

[![Go Version](https://img.shields.io/badge/Go-1.24-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Koishi Ecosystem](https://img.shields.io/badge/Koishi-Ecosystem-9580ff?style=flat)](https://koishi.chat/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

---

## 🌟 核心特性

- **🚀 极致轻量与高吞吐**：采用静态 Go 编译，常驻内存仅约 **15MB**（相比 Node.js/Koishi 运行时降低 95% 以上），冷启动耗时 < 10ms。
- **🛡️ 并发安全与零死锁**：彻底重构同步调度器，内置 Worker Pool 信号量与请求超时熔断，告别 Node.js 版 250 高并发导致的套接字挂死。
- **⚡ 双模式智能同步**：
  - **Mirror 镜像模式（默认推荐）**：秒级从官方 `registry.koishi.chat` 或 GitHub 同步 4,700+ 全网插件索引，零网络消耗。
  - **Crawler 自爬模式（独立脱手）**：并发扫描 NPM 全量 `koishi-plugin-` 包，解析依赖与最新版本。
- **🔒 双端口物理安全隔离**：
  - `55150`：对外公开市场服务端口（监听 `0.0.0.0`），承载 `/storeluna/index.json`、`/market` 与 `/healthz`。
  - `65150`：对内 WebUI 管理控制台，内置 **CIDR 局域网白名单中间件**，仅限内网访问。
- **🎨 内置嵌入式 Web 控制台**：
  - 基于 `go:embed` 编译进单一可执行文件，无任何额外静态文件。
  - **Go 蓝 (`#00ADD8`) + Koishi 紫 (`#9580ff`)** 现代化视觉风格。
  - 支持 **系统跟随 / 亮色 / 暗色** 三态循环切换。
  - 配置参数与 `config.yaml` **双向实时绑定与热重载**。
  - 默认密码 (`storeluna-gogo`) 终端与 UI 双重高危安全警告。

---

## 🚀 部署指南

### 1. Docker 容器化运行 (推荐)

支持 `linux/amd64` 与 `linux/arm64` 多架构。

```bash
docker run -d \
  --name storeluna-gogo \
  --restart unless-stopped \
  -p 55150:55150 \
  -p 192.168.31.100:65150:65150 \
  -v /path/to/config.yaml:/app/config.yaml \
  -v /path/to/data:/app/data \
  vincentzyu/storeluna-gogo:latest
```

> **📌 端口映射说明**：
> - `-p 55150:55150`：对外公共市场 API 映射至全部网卡，供 FRP 内网穿透、Nginx 反代或客户端拉取插件。
> - `-p 192.168.31.100:65150:65150`：**（安全最佳实践）**将管理控制台仅绑定在宿主机局域网指定 IP（示例中为 `192.168.31.100`），公网扫描无法访问该端口，双重保险！

---

### 2. 原生二进制 / MCSManager 运行

1. 从 [Releases](../../releases) 页面下载适合你系统的可执行二进制文件（Windows / Linux）。
2. 解压并将 `storeluna-gogo` 放入目标目录（如 `/home/user/storeluna-gogo`）。
3. 初次运行直接执行：
   ```bash
   chmod +x storeluna-gogo
   ./storeluna-gogo -config config.yaml
   ```
   *服务启动时若检测到缺少 `config.yaml`，将自动生成默认配置文件。*
4. **MCSManager 配置**：在面板中创建通用进程实例，工作目录设置为程序所在文件夹，启动命令填入 `./storeluna-gogo` 即可享受自动拉起与实时控制台日志。

---

## ⚙️ 配置文件说明 (`config.yaml`)

```yaml
server:
  port: 55150                  # 市场公共接口端口
  allow_ips:                   # 访问白名单，默认全网开放
    - "0.0.0.0/0"
    - "::/0"

webui:
  port: 65150                  # 管理控制台端口 (必须与 server.port 不同)
  allow_ips:                   # 允许访问控制台的内网 CIDR 网段
    - "127.0.0.1"
    - "::1"
    - "192.168.31.0/24"        # 家庭局域网段
    - "10.0.0.0/8"
    - "172.16.0.0/12"
    - "192.168.0.0/16"
  admin_password: "storeluna-gogo" # 默认密码，首次进入请及时修改

sync:
  mode: "mirror"               # 同步模式: mirror (官方镜像秒拉) 或 crawler (自爬 NPM)
  interval_minutes: 30         # 自动同步刷新周期 (分钟)
  upstream_url: "https://raw.githubusercontent.com/koishijs/registry/master/index.json"
  registry_url: "https://registry.npmmirror.com"
  concurrency: 8               # 自爬并发数控制
  timeout_seconds: 30          # 网络超时时间 (秒)
  data_path: "data/index.json" # 索引落盘持久化路径
```

---

## 📡 API 端点列表

| 端口 | 请求方式 | 路由路径 | 说明 |
| :--- | :--- | :--- | :--- |
| **55150** | `GET` | `/storeluna/index.json` | 市场全量 JSON 直链（支持 Gzip 压缩） |
| **55150** | `GET` | `/market` | 兼容官方 Koishi 控制台插件市场端点 |
| **55150** | `GET` | `/healthz` | 服务健康检查与当前插件总数状态 |
| **65150** | `GET` | `/` | WebUI 可视化管理控制台页面 |
| **65150** | `POST`| `/api/sync` | 手动触发一次全量索引后台同步 |
| **65150** | `GET/POST`| `/api/config` | 查询或修改服务配置（实时持久化并热重载） |

---

## 🛠️ CI/CD 与自动化发布

详细构建与使用 GitHub CLI 配置 Docker Hub 密钥请参见：[CI/CD 自动化指南](.github/workflows/ci.md)。
