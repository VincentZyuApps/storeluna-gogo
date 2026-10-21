> 💡 推荐前往 [GitHub](https://github.com/VincentZyuApps/storeluna-gogo) 或 [Gitee](https://gitee.com/vincent-zyu/storeluna-gogo) 阅读 README，体验更好。

![storeluna-gogo](https://socialify.git.ci/VincentZyuApps/storeluna-gogo/image?description=1&font=Inter&forks=1&issues=1&language=1&logo=https%3A%2F%2Fupload.wikimedia.org%2Fwikipedia%2Fcommons%2Ff%2Ff3%2FKoishi.js_Logo.png&name=1&owner=1&pattern=Circuit%20Board&pulls=1&stargazers=1&theme=Auto)

<p align="center">
  <img src="docs/images/logo.png" width="160" alt="storeluna-gogo Logo" style="border-radius: 50%; box-shadow: 0 4px 24px rgba(0,0,0,0.4);">
</p>

# storeluna-gogo

> 纯 Go 实现的超轻量、高性能、高可用 Koishi 插件市场索引镜像服务与管理面板。

[![Go Version](https://img.shields.io/badge/Go-1.24-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Koishi Ecosystem](https://img.shields.io/badge/Koishi-Ecosystem-9580ff?style=flat&logo=data%3Aimage%2Fpng%3Bbase64%2CiVBORw0KGgoAAAANSUhEUgAAABIAAAASCAYAAABWzo5XAAABU0lEQVR42p2UQSsFYRSGnxnqLuytKWKpKFkQNsS%2FsOHPWPADLCmxU5S7UzYWNrJR7lYiRF2FeWzOMKZ7mXHqNNP5vvP2nu%2B850CY2lP4X1K31ZbaDm%2BpO%2Bpyp5wfAXVEPfRvO1JHf4AVQGbUh7j4EZ4VkrNCXPVRnf3CUBN1SH2KC28VGOV3ntRhNclZHdcAKYM11QR1oVBOXctzFlNgBTC8qmXxPQEegbVeYApIgJT6tg%2F0AdMp0B%2FBpCabK2AAmAAa%2F2GRBft1oBFPkqTAba7LCiAfQC9wClwAY1HJHepuiO29Yrsf1Dn1uiDU3RTYCtTkl1Leg8k9MB4NGgReI28rV3azgyCz0og01Xl1Uz1QX8uCTELm3UbkTF1VJ9Wr0tn3iBSGdjYG0XivE3VN3VD31PM4a3cc2tIGGI0VkTO7rLxGuiy25ejmjfqsvkSXui62TxaK03td4FXTAAAAAElFTkSuQmCC&logoColor=white)](https://koishi.chat/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

[![GitHub](https://img.shields.io/badge/GitHub-181717?style=for-the-badge&logo=github&logoColor=white)](https://github.com/VincentZyuApps/storeluna-gogo)
[![Gitee](https://img.shields.io/badge/Gitee-C71D23?style=for-the-badge&logo=gitee&logoColor=white)](https://gitee.com/vincent-zyu/storeluna-gogo)

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
  vincentzyu233/storeluna-gogo:latest
```

> **📌 端口映射说明**：
> - `-p 55150:55150`：对外公共市场 API 映射至全部网卡，供 FRP 内网穿透、Nginx 反代或客户端拉取插件。
> - `-p 192.168.31.100:65150:65150`： **（安全最佳实践）** 将管理控制台仅绑定在宿主机局域网指定 IP（示例中为 `192.168.31.100`），公网扫描无法访问该端口，双重保险！

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

### 3. 公网暴露、反向代理与 HTTPS / CDN 最佳实践

<details>
<summary>💡 <b>提示：如果你想自己部署并配置 HTTPS + Cloudflare CDN，可以这样参考（点击展开）</b></summary>

无论你将 `storeluna-gogo` **直接运行在云服务器** 上，还是运行在 **家庭宽带内网并结合 FRP 穿透到云端**，都可以配合 Nginx、SSL 证书与 Cloudflare CDN 实现企业级加速与防刷：

#### 方案 A：家庭内网 + FRP 穿透到云端

1. **内网客户端 (`frpc.ini`)**（*提示：新版的 FRP 已经把配置文件改成 `.toml` 格式，不过配置项大同小异*）：
   ```ini
   [common]
   server_addr = your-vps-domain-or-ip
   server_port = 7000
   token = your_secure_token

   [storeluna-gogo-server]
   type = tcp
   local_ip = 127.0.0.1
   local_port = 55150
   remote_port = 55150
   ```
2. **云端服务端 (`frps.ini`)**：
   监听 `7000` 端口，并开放 `55150` 端口供 Nginx 本地反代。

---

#### 方案 B：直接在云服务器运行

直接在云服务器上通过 Docker 或原生方式启动 `storeluna-gogo`，监听本地 `127.0.0.1:55150` 即可。

---

#### 云服 Nginx 反向代理配置

在云服务器 `/etc/nginx/sites-available/storeluna` 中配置（**注**：`storeluna-gogo` 后端已原生支持并处理 CORS 跨域请求，Nginx 无需重复配置 `add_header Access-Control-Allow-*` 避免产生冲突）：

```nginx
server {
    listen 80;
    listen [::]:80;
    server_name market.example.com;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name market.example.com;

    # 证书路径配置（参见下方证书方案）
    ssl_certificate /etc/nginx/ssl/market.example.com.pem;
    ssl_certificate_key /etc/nginx/ssl/market.example.com.key;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!aNULL:!MD5;

    # 针对大型 index.json 启用实时 Gzip 压缩
    gzip on;
    gzip_types application/json text/plain;
    gzip_min_length 1k;
    gzip_comp_level 6;

    location / {
        proxy_pass http://127.0.0.1:55150;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header CF-Connecting-IP $http_cf_connecting_ip;

        # 大文件索引传输缓冲优化
        proxy_buffering on;
        proxy_buffer_size 128k;
        proxy_buffers 4 256k;
        proxy_busy_buffers_size 256k;

        proxy_http_version 1.1;
        proxy_set_header Connection "";
    }
}
```

---

#### SSL 证书方案选择（二选一）

##### ① Cloudflare 15 年免费 Origin CA 证书（推荐配合 Cloudflare 小黄云 CDN）
如果你启用了 Cloudflare CDN（点亮小黄云），推荐在 Cloudflare 控制台或通过 API 签发最长 **15 年有效期** 的 Origin 证书：
- **Web 控制台签发**：进入 Cloudflare 域名控制台 -> **SSL/TLS** -> **Origin Server (源服务器)** -> **Create Certificate (创建证书)**，主机名填写 `*.example.com` 与 `example.com`，有效期选择 15 年。
- **通过 Cloudflare API 签发**：
  ```bash
  curl -X POST "https://api.cloudflare.com/client/v4/certificates" \
       -H "X-Auth-Email: your_cf_email@example.com" \
       -H "X-Auth-Key: your_cf_global_api_key" \
       -H "Content-Type: application/json" \
       --data '{
         "hostnames": ["market.example.com", "*.example.com"],
         "requested_validity": 5475,
         "request_type": "origin-rsa"
       }'
  ```
  将返回的证书保存到云服 `/etc/nginx/ssl/`，并在 Cloudflare **SSL/TLS 模式** 中切换为 **Full (Strict)**。

##### ② Certbot 免费 Let's Encrypt 证书（直连或无需小黄云场景）
如果你的域名直接解析到 VPS 且不走 Cloudflare CDN：
```bash
# 安装 certbot
sudo apt-get install -y certbot python3-certbot-nginx

# 自动申请证书并配置 Nginx
sudo certbot --nginx -d market.example.com
```

---

#### 借助 Cloudflare API 自动配置 DNS 解析示范

```bash
# 1. 查询 Zone ID
ZONE_ID=$(curl -s -X GET "https://api.cloudflare.com/client/v4/zones?name=example.com" \
  -H "Authorization: Bearer YOUR_CF_API_TOKEN" \
  -H "Content-Type: application/json" | jq -r '.result[0].id')

# 2. 自动添加 A 记录并开启小黄云 CDN (proxied: true)
curl -s -X POST "https://api.cloudflare.com/client/v4/zones/$ZONE_ID/dns_records" \
  -H "Authorization: Bearer YOUR_CF_API_TOKEN" \
  -H "Content-Type: application/json" \
  --data '{
    "type": "A",
    "name": "market",
    "content": "YOUR_VPS_PUBLIC_IP",
    "ttl": 1,
    "proxied": true
  }'
```

</details>

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

| 默认端口 | 请求方式 | 路由路径 | 说明 |
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

---

## 💖 鸣谢与致敬

本项目的设计灵感、协议兼容性以及历史数据基础源于 **[koishi-plugin-storeluna](https://github.com/koishi-shangxue-plugins/koishi-shangxue-apps/blob/642111102ce22227185cff4af737ce3e436b0200/plugins/storeluna/readme.md)**。

- **原版插件仓库**：[koishi-shangxue-apps/plugins/storeluna](https://github.com/koishi-shangxue-plugins/koishi-shangxue-apps/tree/main/plugins/storeluna)
- **基准版本文档**：[koishi-plugin-storeluna v1.1.9 README (commit 6421111)](https://github.com/koishi-shangxue-plugins/koishi-shangxue-apps/blob/642111102ce22227185cff4af737ce3e436b0200/plugins/storeluna/readme.md)
- **开源贡献致敬**：衷心感谢 [@shangxueink](https://github.com/shangxueink) 与 Koishi 社区众多开发者的付出与探索！`storeluna-gogo` 作为使用纯 Go 独立重构的原生服务，在完全兼容原插件协议与缓存结构的同时，实现了微秒级响应与超低资源占用。
