# x-ui 一键迁移包

内容：

| 文件 | 说明 |
| --- | --- |
| `install.sh` | 一键安装 / 迁移脚本（幂等，可重复执行） |
| `x-ui-image.tar.gz` | 应用镜像：x-ui 面板（harness 加固版）+ Xray **26.3.27** + Xray **25.6.8**(legacy) + provision 工具 |
| `caddy-2-alpine.tar.gz` | Caddy 镜像（可选，用于自动申请 Let's Encrypt 证书） |
| `compose.yaml` | 本机实际使用的 compose，作为参考 |
| `x-ui-src.tar.gz` | 源码（含 vendored 内核，可离线重建镜像） |

## 端口布局

| 端口 | 协议 | 用途 | 客户端要求 |
| --- | --- | --- | --- |
| TCP 4433 | x-ui | 面板（随机密码，内置登录限流） | 浏览器 |
| TCP 8443 | VLESS + TLS + **XTLS Vision** | 最新协议，HTTP 场景 CPU 减半 | Xray ≥ 1.8（建议 26.x） |
| UDP 8443 | **Hysteria 2** | 弱网/高延迟首选，QUIC + Brutal | 支持 hysteria2 的客户端 |
| TCP 3389 | VLESS + **REALITY** + Vision | 免证书、抗 SNI 封锁 | Xray ≥ 1.8 |
| UDP 3389 | VMess + mKCP（25.6.8 老内核） | **旧客户端配置原样可用** | 任意老版本 Xray |

TLS 证书：由 Caddy 通过 ACME（HTTP-01 / TLS-ALPN）自动申请与续期，续期后 cron 同步到
`/etc/x-ui/certs/` 并重启面板容器。

## 部署到新服务器

```bash
# 1. 准备（域名 A 记录指向新服务器；安全组放行 80/443/4433/8443(TCP+UDP)/3389(TCP+UDP)）
scp x-ui-release.tar.gz root@new-host:/root/ && ssh root@new-host
tar xzf x-ui-release.tar.gz && cd x-ui-release

# 2a. 让 Caddy 自动申请证书（推荐；要求 80/443 空闲）
./install.sh --domain p.example.com --email you@example.com \
             --image-tar ./x-ui-image.tar.gz --caddy-tar ./caddy-2-alpine.tar.gz

# 2b. 已有证书 / 80,443 被别的站点占用
./install.sh --domain p.example.com \
             --cert /path/fullchain.pem --key /path/privkey.pem \
             --image-tar ./x-ui-image.tar.gz

# 2c. 直接迁移旧客户端的 mKCP 配置（UUID/seed 保持原样，客户端零改动）
./install.sh --domain p.example.com --email you@example.com \
             --legacy-uuid f96ca0c3-fff4-460a-9e44-89b18a521daf --legacy-seed INuunm3VpC \
             --image-tar ./x-ui-image.tar.gz
```

脚本结束时打印：面板地址/密码 + 四条 `vless://`、`hysteria2://`、`vmess://` 链接
（同时写入 `/etc/x-ui/inbounds.json`，权限 600）。

## 运维

```bash
cd /opt/x-ui
docker compose ps            # 服务状态
docker compose logs -f x-ui  # 面板 + 26.x 内核日志
docker compose logs -f x-ui-legacy   # 旧客户端(mKCP)日志
docker exec x-ui ./x-ui setting -show true          # 面板设置
docker exec x-ui ./x-ui setting -username admin -password '<新密码>'
```

- 面板被限流锁定：`docker compose stop x-ui && rm /etc/x-ui/.login_guard.json && docker compose start x-ui`
- 证书续期同步：`/etc/cron.d/x-ui-cert-sync`（每天 04:20；仅 Caddy 托管时安装）
- 内核版本**固定**在镜像里（26.3.27 / 25.6.8），**不要**用面板的"切换版本"——它写容器可写层，
  重建即回滚，且 26.x 改了 mKCP 线上格式会导致旧客户端直接断流。
- 换内核：改 `Dockerfile` 里的 `XRAY_VERSION`/`XRAY_SHA256`（或 legacy 的 `XRAY_LEGACY_SHA256`），重建镜像。

## 安全模型

- 面板：bcrypt 存储、Cookie 仅存 id/签名（改密即失效）、`X-Forwarded-For` 默认不信任、
  per-IP 滑动窗口限流 + 全局熔断、`cap_drop: ALL`、内存上限 256M。
- 入站：TLS/REALITY/QUIC 全部启用，mKCP 仅保留给老客户端且运行在独立容器（25.6.8 内核）。
- 证书私钥仅 root 可读（`/etc/x-ui/certs/privkey.pem` 600）。

## 注意

- **UDP 端口**很多云厂商默认不放行：Hysteria2(UDP 8443)、legacy mKCP(UDP 3389) 需单独加安全组规则。
- REALITY 回落目标默认 `127.0.0.1:443`（即 Caddy 站点）；若本机没有真实 TLS 站点，请改成
  一个外部真实站点（如 `www.microsoft.com:443`），并保持 `serverNames` 与之一致。
- 镜像架构为 linux/amd64。

## 控制台（v2）

- 仪表盘：资源环 + 内核/运行时间/负载/连接数 + 网络与入站汇总 + **协议入站与客户端链接表**
  （协议徽标、传输/安全特性、流量、一键复制链接）。
- 入站列表：协议着色、"复制客户端链接"、未知协议（hysteria）安全降级展示，不破坏页面。
- **已移除"切换版本"**：内核随镜像固定；面板不再能下载/替换内核（也消除了对应攻击面）。
- 链接生成在服务端完成，包含 REALITY 公钥推导与 **VLESS Encryption 客户端串**，
  因此二维码/复制得到的链接可直接导入客户端。

## 协议密钥（VLESS Encryption）

「设置 → 协议密钥」可查看/复制服务端 `decryption` 与客户端 `encryption`，
支持**重新生成**（轮换 ML-KEM-768 密钥，Xray 自动重启，旧链接立即失效）与**关闭**。
轮换后请到仪表盘或入站列表重新复制客户端链接。

## 控制台主题（v3）

深色 Control Plane 主题：自托管字体（IBM Plex Sans / Saira Semi Condensed）、
`assets/css/theme.css` + `assets/css/custom.css`、`assets/img/favicon.svg`、zh-CN 语言包。
页面为 `page-head / stat-card / ib-card / settings-card` 卡片布局，仪表盘入站行与入站卡片均可一键复制客户端链接。
