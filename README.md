# x-ui Control Plane (hardened)

一个**面向生产**的 [x-ui](https://github.com/vaxilu/x-ui) 改造分支：保留原面板的入站/流量管理能力，
把登录安全、协议现代化、容器化部署与可迁移性补齐；前端换成深色 Control Plane 主题，
并在面板内闭环管理 VLESS Encryption / REALITY / Hysteria2 的密钥与客户端链接。

> 基于 GPL-3.0 的上游项目 [vaxilu/x-ui](https://github.com/vaxilu/x-ui) 修改，遵循同一许可证。

---

## 1. 特性

### 面板安全
- **登录防爆破**：per-IP 滑动窗口（默认 10 分钟 5 次）+ 指数退避锁定（15 分钟 → 24 小时）+ 全局熔断
  （10 分钟 100 次失败暂停登录）；状态持久化到 `/etc/x-ui/.login_guard.json`，重启不清零；
  loopback 永不限流（可用 SSH 隧道急救）。
- **密码 bcrypt**（cost 12），旧明文密码首次登录自动升级；用户不存在时执行 dummy 校验，消除时间侧信道。
- **会话签名**：Cookie 只存 `id/username` + 凭据签名，`HttpOnly` + `SameSite=Lax`；改密/删用户后旧会话立即失效。
- **不信任 `X-Forwarded-For`**（除非显式 `XUI_TRUST_PROXY=true`），避免伪造 IP 绕过限流。
- 安全响应头（`nosniff` / `X-Frame-Options: DENY` / `Referrer-Policy` / 面板 `no-store`）；
  前端资源带构建指纹，升级后不会跑到旧 JS。
- 容器 `cap_drop: ALL`、内存上限、`no-new-privileges`、日志轮转。

### 协议
| 端口（默认） | 协议 | 说明 |
| --- | --- | --- |
| TCP 8443 | VLESS + TLS + **XTLS Vision** | 主用，HTTPS 场景 CPU 减半（splice） |
| TCP 3389 | VLESS + **REALITY** + Vision + **VLESS Encryption** | 免证书、抗 SNI 封锁/主动探测，后量子加密 |
| UDP 3389 | **Hysteria 2** | QUIC + Brutal，弱网/高延迟首选 |
| TCP 4433 | 面板 | 深色控制台 + 链接下发 |

面板表单支持：`vmess / vless / trojan / shadowsocks / socks / http / dokodemo-door / hysteria2`，
其中 vless/trojan 可开 **REALITY**，vless 可开 **VLESS Encryption**，flow 仅提供 `xtls-rprx-vision`。
四个协议均通过真实连通性回归（含 socks/http 直连、dokodemo 转发）。

### 密钥与链接
- 仪表盘 / 入站列表一键**复制客户端链接**（服务端生成，含 REALITY `pbk/sid` 与 VLESS Encryption 串）。
- 设置 → 协议密钥：**VLESS Encryption** 查看/复制/轮换/关闭，**REALITY** 逐条/全部轮换，均自动重启 Xray。
- 入站表单内可一键生成 REALITY 密钥对与 VLESS Encryption 密钥，**不依赖部署脚本**。

### 部署
- 单容器 + host 网络，仅持久化 `/etc/x-ui`；内核版本固定并校验 SHA-256（当前 **26.3.27**）。
- `install.sh` 一键安装/迁移：可用 **Caddy 自动申请 Let's Encrypt 证书**，并安装证书续期同步任务。
- 全协议回归脚本与迁移包（可选）见 `docs/`。

---

## 2. 快速开始（Docker）

```bash
git clone <this-repo> && cd <repo>
# 有域名：Caddy 自动申请证书（80/443 需空闲）
./install.sh --domain p.example.com --email you@example.com

# 已有证书 / 80,443 被占用
./install.sh --domain p.example.com --cert ./fullchain.pem --key ./privkey.pem

# 直接本机构建镜像
docker compose build && docker compose up -d
```

脚本结束会打印：面板地址/随机管理员密码，以及 `vless://`、`hysteria2://` 客户端链接
（同时写入 `/etc/x-ui/inbounds.json`，权限 600）。

**安全组需放行**：TCP 面板端口（默认 4433）、TCP 8443、TCP 3389、UDP 3389。

---

## 3. 目录结构

```
├── Dockerfile              # 多阶段构建：Go 面板 + 固定版本 Xray（SHA-256 校验）
├── compose.yaml            # host 网络、cap_drop、内存/日志限制
├── install.sh              # 一键安装/迁移（可选 Caddy ACME）
├── deploy/provision.py     # 生成 Vision / REALITY+PQ / Hysteria2 入站与链接
├── docs/DEPLOY.md          # 部署与运维手册（迁移、证书、排障）
├── docs/HARDENING.md       # 加固与改造记录（含性能/兼容性实测数据）
├── web/                    # 面板后端（Gin）+ 前端（Vue2 + Ant Design Vue，嵌入二进制）
├── web/service/inbound_link.go   # 客户端链接生成（vless/vmess/trojan/ss/hysteria2）
├── web/service/vlessenc.go       # VLESS Encryption 密钥管理
├── web/service/reality.go        # REALITY 密钥管理与轮换
├── web/guard/                    # 登录防爆破
├── util/password/                # bcrypt 封装
├── vendor/xray/                  # 固定版本的 Xray 发行包（构建用）
└── x-ui.sh / x-ui.service        # 可选：非容器化的 systemd 安装方式
```

---

## 4. 常用运维

```bash
cd /opt/x-ui
docker compose ps / logs -f x-ui
docker exec x-ui ./x-ui setting -show true
docker exec x-ui ./x-ui setting -username admin -password '<新密码>'

# 被登录限流锁定
docker compose stop x-ui && rm -f /etc/x-ui/.login_guard.json && docker compose start x-ui
```

环境变量（`compose.yaml` 已给默认值）：
`XUI_LOGIN_MAX_FAILS`、`XUI_LOGIN_WINDOW`、`XUI_LOGIN_LOCK`、`XUI_LOGIN_MAX_LOCK`、
`XUI_LOGIN_GLOBAL_FAILS`、`XUI_LOGIN_GLOBAL_WINDOW`、`XUI_LOGIN_GLOBAL_LOCK`、
`XUI_TRUST_PROXY`、`XUI_COOKIE_SECURE`、`XUI_HEALTHCHECK_PORT`。

---

## 5. 构建与发布

```bash
make image      # 构建镜像（校验 vendor 内核 SHA-256）
make release    # 生成离线迁移包（镜像 tar.gz + 安装脚本 + 源码）
make vet        # go vet
```

镜像内固定两处版本：`Dockerfile` 的 `XRAY_VERSION` / `XRAY_SHA256`。
**不要使用面板的"切换内核版本"功能**（本分支已移除该入口）：容器重建会回滚，且 26.x 重写了 mKCP 线上格式。

---

## 6. 客户端要求

- VLESS + Vision / REALITY / VLESS Encryption：Xray **≥ 26.3**（建议与服务端同代）。
- Hysteria 2：支持 `hysteria2://` 的客户端（Xray 26.x / sing-box / mihomo / Hysteria 官方客户端）。
- 旧版 mKCP 客户端不受支持（26.x 已改变 mKCP 线上格式）。

---

## 7. 许可证

GPL-3.0，见 [LICENSE](LICENSE)。上游项目：[vaxilu/x-ui](https://github.com/vaxilu/x-ui)。
本仓库的修改点记录在 [docs/HARDENING.md](docs/HARDENING.md)。
