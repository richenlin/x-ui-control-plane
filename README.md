# x-ui Control Plane


![Go](https://img.shields.io/badge/Go-1.21-00ADD8?logo=go&logoColor=white)
![Xray](https://img.shields.io/badge/Xray-26.3.27-1F6FEB)
![Platform](https://img.shields.io/badge/Platform-linux%2Famd64-lightgrey)
![License](https://img.shields.io/badge/License-GPL--3.0-blue)

> 基于 [vaxilu/x-ui](https://github.com/vaxilu/x-ui)（GPL-3.0）全面改造：前端界面、面板服务、部署脚本与运行时编排均已重写，仅保持数据模型与面板操作习惯的兼容。每一项改动都对应一个具体攻击面或运维故障，逐条根因与协议实测数据见 [docs/HARDENING.md](docs/HARDENING.md)。

## 目录

- [特性](#特性)
- [架构](#架构)
- [快速开始](#快速开始)
- [配置参考](#配置参考)
- [运维手册](#运维手册)
- [构建与发布](#构建与发布)
- [客户端兼容性](#客户端兼容性)
- [安全模型与已知限制](#安全模型与已知限制)
- [文档](#文档)
- [许可证](#许可证)


### 设计原则

| 原则 | 落地方式 |
| --- | --- |
| 默认安全 | 无默认口令、口令不可逆存储、会话可失效、登录防爆破、容器最小权限 |
| 声明式交付 | 一条命令完成「证书 → 数据库 → 编排 → 入站 → 链接」；重复执行得到同一结果 |
| 单一事实来源 | 客户端链接、REALITY 公钥、VLESS Encryption 串均以面板内的入站配置为准 |
| 可运维 | 健康检查、日志分级、限流状态可查可清、离线迁移包 |

## 特性

### 控制台界面

原版界面以浅色模板加固定表单呈现，REALITY、VLESS Encryption 等新能力只能靠手工拼配置，升级后还常因浏览器缓存继续执行旧脚本。本项目重做了整套前端：

| 方面 | 改造 |
| --- | --- |
| 视觉 | 深色主题（`web/assets/css/theme.css`），字体自托管（IBM Plex Sans / Plex Mono / Saira Semi Condensed），不依赖外部 CDN |
| 表单 | 按协议能力动态收敛：开启 REALITY 强制 `network=tcp`，流控只保留 `xtls-rprx-vision`，ALPN 统一归一化后再下发 |
| 密钥 | REALITY 密钥对与 VLESS Encryption 密钥对的生成器内置于入站弹窗，生成结果直接写回当前表单 |
| 兼容 | 缺少表单模型的入站（如 `hysteria2`）降级为原始 JSON 编辑，保存不会丢字段 |
| 缓存 | 静态资源 URL 携带构建指纹，发版后不会命中旧 JS/CSS |
| 适配 | 侧边栏在窄屏折叠为抽屉，表格与表单在手机上不横向溢出 |


### 协议与链接

默认入站布局（`deploy/provision.py` 生成，`install.sh` 调用）：

- `inbound-vision` — VLESS + TLS + XTLS Vision，TCP 8443，ALPN `http/1.1`，`flow=xtls-rprx-vision`
- `inbound-reality` — VLESS + REALITY + Vision，TCP 3389，X25519 密钥对与 8 字节随机 `shortId`（16 位十六进制），可选 VLESS Encryption
- `inbound-hysteria2` — Hysteria 2，UDP 3389，ALPN `h3`，需自有 TLS 证书

面板表单支持的协议与传输：

| 协议 | 传输 | 说明 |
| --- | --- | --- |
| `vmess` | tcp / ws / grpc / h2 / kcp | |
| `vless` | tcp / ws / grpc / h2 / kcp | Vision 的 flow 收敛为 `xtls-rprx-vision` |
| `trojan` | tcp | 可与 TLS / REALITY 组合 |
| `shadowsocks` | tcp | 常用 AEAD 方法 |
| `socks` / `http` | tcp | 本地代理场景 |
| `dokodemo-door` | tcp | 端口转发 |
| `hysteria2` | QUIC (h3) | 面板与数据库使用 `hysteria2`，下发内核时映射为 `hysteria`（[`database/model/model.go`](database/model/model.go)） |

协议能力边界：

- **REALITY** 仅绑定 TCP 入站（`vless` / `trojan`），开启时强制 `network=tcp`。
- **VLESS Encryption**（ML-KEM-768 后量子）绑定在 REALITY 载体入站上，由「设置 → 协议密钥」统一管理，不在入站表单里开关。
- 缺少表单模型的入站（例如 `hysteria2`）在编辑时降级为原始 JSON，避免保存时丢字段。

链接生成（[`web/service/inbound_link.go`](web/service/inbound_link.go)）：

- `POST /xui/inbound/links` 一次返回全部入站，`POST /xui/inbound/link/:id` 返回单条；每条包含协议、端口、传输、安全层、特性标签、上下行流量与完整 URI。
- 覆盖 `vless`（TLS / REALITY / Vision / 后量子）、`vmess`（tcp / ws / grpc / kcp / h2）、`trojan`、`shadowsocks`、`hysteria2`。
- REALITY 的 `pbk` 由入站私钥用 X25519 现场推导，VLESS Encryption 的客户端串取自设置表，因此复制或扫码得到的链接与当前服务端严格一致。


### 交付与运维

- **一条命令安装**：`install.sh` 依次完成镜像加载、证书准备、数据库初始化、编排渲染、入站生成与启动，并安装证书续期同步任务。
- **三种形态**：自签 ACME（域名 + Caddy）、自带证书、纯 REALITY（无证书、IP 直连）。
- **内核随镜像分发**：`vendor/xray/Xray-linux-64.zip` 入库并在构建时校验 SHA-256，构建与迁移都不依赖上游 Release CDN。
- **离线迁移包**：`make release` 产出镜像 tar.gz、源码、安装脚本与 `SHA256SUMS`。
- **攻击面自查**：登录限流状态可读、可清；面板口令存于 SQLite，可用 `x-ui setting -show` 校对。

## 架构

```
客户端
  │  TCP 4433
  ▼
x-ui 面板    Go 1.21 / Gin，单进程
  ├── web/guard     登录限流 + 全局熔断
  ├── web/session   签名会话（Cookie 不含凭据）
  └── web/html      嵌入式前端（深色主题）
  │
  │  写入 config.json，经 gRPC Stats API 读取流量
  ▼
Xray 26.3.27   同一容器内的子进程
  ├── TCP 8443   VLESS + TLS + XTLS Vision
  ├── TCP 3389   VLESS + REALITY + Vision
  └── UDP 3389   Hysteria 2
  │
  ▼
/etc/x-ui   唯一持久化目录（数据库、证书、限流状态）
```

面板与 Xray 运行在同一个容器内（`network_mode: host`），Xray 由面板以子进程方式启动并托管；宿主端口即入站端口，内核看到的是真实客户端地址，不做 NAT。


## 快速开始

### 前置条件

| 项 | 要求 |
| --- | --- |
| 主机 | Linux（Debian / Ubuntu 已验证），单机部署 |
| 权限 | root（需管理 Docker 与 `/etc/x-ui`） |
| Docker | 含 Compose v2 插件（使用 `docker compose` 子命令） |
| 依赖命令 | `openssl`、`ss`（`iproute2`，ACME 端口探测）；`curl` 用于部署后验收 |
| 端口 | TCP `4433` / `8443` / `3389`，UDP `3389`；ACME 模式另需 TCP `80` / `443` |
| 域名 | 可选。有域名可获得 TLS 与 Hysteria 2；无域名可只跑 REALITY |

### 方式 A：域名 + 自动证书（推荐）

```bash
git clone https://github.com/richenlin/x-ui-control-plane.git
cd x-ui-control-plane
sudo ./install.sh --domain p.example.com --email you@example.com
```

前置：域名的 A 记录已指向本机，且 80/443 空闲（脚本会探测，被占用则直接报错退出）。

### 方式 B：使用已有证书

```bash
sudo ./install.sh --domain p.example.com \
  --cert ./fullchain.pem --key ./privkey.pem
```

传入 `--cert/--key` 时不部署 Caddy，证书拷贝至 `/etc/x-ui/certs`。

### 方式 C：纯 REALITY（无域名、IP 直连）

```bash
sudo ./install.sh \
  --domain swdist.apple.com \
  --reality-dest swdist.apple.com:443 \
  --endpoint 203.0.113.10 \
  --no-tls --no-hy2
```

| 参数 | 作用 |
| --- | --- |
| `--domain` | 此形态下仅作 REALITY 的 `serverNames` 与链接 `sni`，必须是真实存在且支持 TLS 1.3 的站点 |
| `--reality-dest` | REALITY 回落目标，应与 `--domain` 同站点的 443 端口；默认 `127.0.0.1:443` 仅适用于本机有 TLS 站点的情况 |
| `--endpoint` | 写进客户端链接的服务器地址（IP 或域名），默认等于 `--domain` |
| `--no-tls` | 不创建 TLS 与 Hysteria 2 入站（二者都需要自有证书） |
| `--no-hy2` | 只跳过 Hysteria 2 |

> REALITY 的安全性来自「回落站点真实可用」：未通过认证的探测只会看到所填站点的证书与响应。请勿填写无法访问或 TLS 1.3 不完整的域名。


### 部署后验收

```bash
# 容器应为 healthy
docker ps --filter name=x-ui --format '{{.Names}}\t{{.Status}}'

# 面板可达：未登录访问应返回 200 登录页
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:4433/

# 面板设置（口令为 bcrypt，不可逆显示）
docker exec x-ui ./x-ui setting -show

# 纯 REALITY：回落站点证书应与所填 SNI 一致
openssl s_client -connect 203.0.113.10:3389 \
  -servername swdist.apple.com -tls1_3 -alpn h2 </dev/null 2>/dev/null \
  | openssl x509 -noout -subject
```

安全组需放行：`TCP 4433`、`TCP 8443`、`TCP 3389`、`UDP 3389`；ACME 模式另需 `TCP 80`、`TCP 443`。多数云厂商默认只放行 TCP，UDP 规则需单独确认。


## 运维手册

| 任务 | 命令 |
| --- | --- |
| 查看状态 | `cd /opt/x-ui && docker compose ps` |
| 查看日志 | `cd /opt/x-ui && docker compose logs -f x-ui` |
| 重启面板 | `cd /opt/x-ui && docker compose restart` |
| 修改管理员口令 | `docker exec x-ui ./x-ui setting -username admin -password '<新口令>'` |
| 查看面板设置 | `docker exec x-ui ./x-ui setting -show` |
| 修改面板端口 | `docker exec x-ui ./x-ui setting -port 4433`，随后 `docker compose restart` |
| 查看被限流的 IP | `cat /etc/x-ui/.login_guard.json` |
| 立即解锁 | `cd /opt/x-ui && docker compose stop x-ui && rm -f /etc/x-ui/.login_guard.json && docker compose start x-ui` |
| 备份 | `cd /opt/x-ui && docker compose stop && tar czf ~/x-ui-$(date +%F).tgz -C /etc x-ui && docker compose start` |
| 迁移到新机 | 安装 Docker → 恢复 `/etc/x-ui` → 将 `compose.yaml` 放入 `/opt/x-ui` → `docker compose up -d` |
| 证书续期 | Caddy 自动续期；`/etc/cron.d/x-ui-cert-sync` 每日 04:20 同步证书并重启面板 |

**密钥轮换**：设置 → 协议密钥，重新生成 VLESS Encryption 或轮换 REALITY（单条 / 全部）。轮换成功后 Xray 自动重启，需到仪表盘或入站列表重新复制链接。

**面板只监听回环**：在设置中把监听地址改为 `127.0.0.1`，用 `ssh -L 4433:127.0.0.1:4433 root@<host>` 访问；回环地址不受登录限流影响，是密码锁定后的自救通道。

## 构建与发布

```bash
make vet                     # go vet（容器内执行，不污染本机环境）
make image                   # 构建 x-ui:hardened，构建时校验内置内核 SHA-256
make image IMAGE=x-ui:dev    # 自定义镜像标签
make release                 # 产出 dist/：镜像 tar.gz、源码、install.sh、compose.yaml、SHA256SUMS
make clean
```

等价的手工构建与自检：

```bash
docker build --network host -t x-ui:dev .
docker run --rm x-ui:dev ./bin/xray-linux-amd64 -version
```

**内核升级**：同时修改 `Dockerfile` 中的 `XRAY_VERSION` 与 `XRAY_SHA256`，并替换 `vendor/xray/Xray-linux-64.zip`。哈希取自上游 release 的 `Xray-linux-64.zip.dgst`。不要依赖面板内的版本切换——本项目已移除该入口，且容器重建会回滚到镜像内的内核。

**CI**：`.github/workflows/build.yml` 在 push / PR 时构建镜像并输出版本；`.github/workflows/release.yml` 在 `v*` 标签时构建、校验并发布迁移包。

## 客户端兼容性

| 入站类型 | 客户端要求 |
| --- | --- |
| VLESS + TLS + XTLS Vision | Xray ≥ 1.8 |
| VLESS + REALITY + Vision | Xray ≥ 1.8；`pbk` / `sid` 由链接携带 |
| VLESS + REALITY + VLESS Encryption | Xray ≥ 26.3（ML-KEM-768）；更早版本请用 `--no-pq` 部署 |
| Hysteria 2 | 支持 `hysteria2://` 的客户端：sing-box、mihomo、Hysteria 官方客户端等 |
| VMess / Trojan / Shadowsocks | 各主流客户端 |

### mKCP 与内核版本

Xray 26.x 重写了 mKCP 的线上格式（Finalmask 迁移），与 1.4.x / 25.x 双向不兼容；`vmess + tcp` 则全版本互通。这就是本项目把内核固定在镜像内、不提供面板切换的原因。完整兼容矩阵与 8 协议回归结果见 [docs/HARDENING.md](docs/HARDENING.md)。


## 文档

| 文档 | 内容 |
| --- | --- |
| [docs/DEPLOY.md](docs/DEPLOY.md) | 迁移包清单、端口布局与旧版双内核（legacy mKCP）方案；单容器部署以本文件为准 |
| [docs/HARDENING.md](docs/HARDENING.md) | 加固项逐条根因、协议实测矩阵、控制台改造记录 |
| [LICENSE](LICENSE) | GPL-3.0 全文 |

## 许可证

本项目遵循 **GPL-3.0**，与上游 [vaxilu/x-ui](https://github.com/vaxilu/x-ui) 一致；Xray-core 遵循 MPL-2.0。详见 [LICENSE](LICENSE)。
