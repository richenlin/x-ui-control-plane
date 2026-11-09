# x-ui 安全加固与部署说明

本仓库是 `vaxilu/x-ui` 的加固分支。起因：面板（旧版本为 0.3.2，端口 47821，现改为 4433）长期被
登录爆破，且密码以明文存库、会话 Cookie 中携带明文密码、登录接口无限流。

## 1. 修复内容

| 位置 | 问题 | 修复 |
| --- | --- | --- |
| `web/guard/guard.go`（新增） | 登录接口没有任何限流，可无限尝试 | 每 IP 滑动窗口失败计数（默认 10 分钟内 5 次）→ 锁定 15 分钟，复发时锁定时间指数增长（上限 24h）；全局熔断（10 分钟内 100 次失败 → 暂停登录 15 分钟）防分布式爆破；状态持久化到 `/etc/x-ui/.login_guard.json`，重启不清零；loopback 永不限流（可 SSH 隧道急救） |
| `web/controller/index.go` | 失败日志把用户输入的**明文密码**打进日志；无 429 | 去掉密码日志；被限流时返回 `429 + Retry-After` 与中文提示 |
| `web/service/user.go` | 密码**明文**存储、明文 SQL 比对 | 使用 bcrypt（cost 12）校验；旧明文密码首次登录成功后**自动原地升级**为 hash；用户不存在时执行一次 dummy bcrypt 以消除时间侧信道 |
| `web/session/session.go` | 整条 `model.User`（含明文密码）写进客户端 Cookie；无 HttpOnly/SameSite；改密后旧 Cookie 仍有效 | Cookie 只保存 `id/username` + 凭据签名；`HttpOnly` + `SameSite=Lax`；每次鉴权比对签名，改密/删用户后旧会话立即失效 |
| `web/controller/util.go` | 无条件信任 `X-Forwarded-For`，攻击者可伪造 IP 绕过限流 | 默认取 `RemoteAddr`；只有 `XUI_TRUST_PROXY=true` 时才采信代理头 |
| `web/web.go` | 无安全响应头；gin 信任代理头 | `X-Content-Type-Options`/`X-Frame-Options`/`Referrer-Policy`、面板响应 `Cache-Control: no-store`；`engine.TrustedProxies` 置空 |
| `web/job/stats_notify_job.go` | 每次登录/失败都无条件请求 Telegram API（即使未启用），且 `bot.Debug=true` 会把 token 打进日志 | 未启用/未配置直接返回；失败通知 5 分钟节流；关闭 Debug |
| `web/assets/js/util/utils.js` | axios 对 429 只显示 `Request failed with status code 429` | 优先展示服务端 JSON 中的中文提示 |
| `database/db.go`、`main.go` | 初始账号 `admin/admin` 明文入库；`-show` 把明文密码当密码打印 | 初始化即 bcrypt；`-show` 明确提示 hash 不可逆 |
| `Dockerfile` / `compose.yaml` | 无容器化方案 | 多阶段构建，host 网络运行，仅挂载 `/etc/x-ui`，`cap_drop: ALL` |

限流参数均可用环境变量覆盖（见 `compose.yaml`）：
`XUI_LOGIN_MAX_FAILS`、`XUI_LOGIN_WINDOW`、`XUI_LOGIN_LOCK`、`XUI_LOGIN_MAX_LOCK`、
`XUI_LOGIN_GLOBAL_FAILS`、`XUI_LOGIN_GLOBAL_WINDOW`、`XUI_LOGIN_GLOBAL_LOCK`。

## 2. 部署（Docker host 网络）

```bash
cd /root/x-ui-src
docker compose build
docker compose up -d
docker compose logs -f          # 面板日志（xray 的 stdout 也会汇总到这里）
```

- `network_mode: host`：面板与 xray 的入站/出站直接使用宿主机网络栈，无需端口映射。
- 唯一持久化卷：`/etc/x-ui`（面板数据库 + 限流状态）。
- 旧的原生 systemd 单元 `xray.service` 已 `disable --now`，避免与容器抢 3389 端口/空转重启。

## 3. 常用运维

```bash
# 解锁某个 IP / 清空限流状态（保留数据库）
docker compose restart x-ui && cat /etc/x-ui/.login_guard.json   # 查看被锁 IP
# 彻底清零：先停容器，再删状态文件
docker compose stop x-ui && rm -f /etc/x-ui/.login_guard.json && docker compose start x-ui

# 重置面板账号（bcrypt 入库）
docker exec -it x-ui ./x-ui setting -username <user> -password <pass>
docker compose restart x-ui

# 查看面板设置
docker exec -it x-ui ./x-ui setting -show true
```

安全建议：
- 若不需要公网直接访问面板，把面板监听改为 `127.0.0.1`（面板设置里修改监听 IP），
  再用 `ssh -L 4433:127.0.0.1:4433 root@<host>` 访问；loopback 不受限流影响。
- 面板若置于 Caddy/Nginx 之后，需要设置 `XUI_TRUST_PROXY=true`，否则所有客户端会被当成代理 IP。
- 面板启用自身 HTTPS（cert/key）时 Cookie 自动带 `Secure`；也可用 `XUI_COOKIE_SECURE=true` 强制。

## 4. 主机资源治理（2026-11-09）

| 项 | 做法 | 效果 |
| --- | --- | --- |
| journald | `/etc/systemd/journald.conf.d/limits.conf`：`SystemMaxUse=200M`、`SystemMaxFileSize=50M`、`SystemMaxFiles=10` | 日志 2.3 G → 184 M |
| docker | `docker builder prune -f` + `docker image prune -f` | 构建缓存 1.66 G → 264 M，磁盘 15 G → 11 G |
| 旧日志 | 删除 `auth.log.[2-4].gz`、`btmp.1`、`syslog.[2-4].gz` + `apt clean` | /var/log 2.4 G → 195 M |
| SSH 爆破 | `fail2ban` + `/etc/fail2ban/jail.d/sshd.local`（5 次/10 分钟封 1h，复发翻倍至 1 周；`ignoreip` 含本机公网 IP 防止自锁） | 攻击源被直接封禁，日志不再刷屏 |
| x-ui 容器 | `mem_limit: 256m` / `memswap_limit: 384m` | 实测占用 ~21–32 MiB，超限仅重启容器 |

注意：`fail2ban` 的 `sshd` jail 必须覆盖 `journalmatch = _SYSTEMD_UNIT=ssh.service + _COMM=sshd`，
因为 Ubuntu 的 unit 名是 `ssh.service`，上游默认的 `sshd.service` 匹配不到任何日志。

面板限流状态文件 `/etc/x-ui/.login_guard.json` 会记录被锁 IP；若自己被锁（例如通过本机代理访问
面板，源 IP 会变成服务器公网 IP），停容器 → 删该文件 → 启动容器即可解锁。

## 5. 切勿在面板里随意切换 Xray 内核版本（mKCP 兼容性）

实测（vmess + mKCP，srtp header，同 seed/UUID）：

| 客户端 | 服务端 | 结果 |
| --- | --- | --- |
| 1.4.2 | 1.4.2 | ✅ 204 |
| 1.4.2 | 1.8.24 | ✅ 204 |
| 1.4.2 | 25.6.8 | ✅ 204 |
| 1.4.2 | **26.9.30** | ❌ 超时（UDP 握手不通） |
| 26.9.30 | 26.9.30 | ✅ 204 |
| 1.4.2 ↔ 26.9.30（**TCP** 传输） | | ✅ 204（说明只是 mKCP 层不兼容） |

结论：Xray **26.x 改了 mKCP 线上协议**，与 1.4.2 时代的客户端不兼容；VMess 本身跨版本没问题。
旧客户端请把服务端内核固定在 **≤25.x**，否则必须同时升级客户端到 26.x。

另一个坑：面板的"切换版本"是把新内核下载进**容器可写层**（`/usr/local/x-ui/bin/`），
任何 `docker compose up -d` 重建容器都会静默回滚到镜像里自带的 1.4.2，导致"改了又坏/坏了又好"。
需要长期使用某个内核版本时，请在 `Dockerfile` 里下载并固化，而不是用面板切换。

### 5.1 实测兼容矩阵（vmess，2026-11 复测）

| 客户端 | 服务端 | 传输 | 结果 |
| --- | --- | --- | --- |
| 1.4.2 | 1.4.2 | mKCP（legacy header/seed） | ✅ 204 |
| 1.4.2 | 25.6.8 | mKCP（legacy） | ✅ 204 |
| 1.4.2 | 26.9.30 | mKCP（legacy） | ❌ 超时 |
| 26.9.30 | 26.9.30 | mKCP（legacy） | ✅ 204 |
| 26.3.27 | 26.3.27 | mKCP（无 header/seed） | ✅ 204 |
| 26.3.27 | 26.9.30 | mKCP（无 header/seed） | ✅ 204 |
| 26.3.27 | 1.4.2 | mKCP（无 header/seed） | ❌ 超时 |
| 26.3.27 | 1.4.2 / 26.9.30 | **TCP** | ✅ 204 / ✅ 204 |
| 1.4.2 | 26.9.30 | **TCP** | ✅ 204 |

结论：
1. **mKCP 的线上格式在 26.x 重写了**（Finalmask 迁移），1.4.x/25.x 与 26.x 之间双向不兼容。
2. 26.x 里 mKCP 配置也变了：26.3.27 **直接拒绝** `kcpSettings.header` / `seed`；26.9.30 虽接受但会映射到新模式，
   于是"面板生成的老配置 + 26.x 客户端"也连不上。
3. **vmess+TCP 全版本互通**，是跨版本最稳的选择。
4. 因此 26.x 客户端时代，mKCP 必须"客户端与服务端内核同代 + 配置模式一致"，否则必然挂。

应对：`/etc/x-ui/x-ui.db` 中已新增一条 vmess+**tcp** 入站（id=2, port **8443**, tag `inbound-8443`），
与原 mKCP(UDP 3389) 入站并存，老客户端继续走 3389，新客户端走 8443/tcp。

## 6. 最终入站布局（2026-11-09）

| 端口 | 协议 | 内核 | 客户端要求 | 实测 |
| --- | --- | --- | --- | --- |
| TCP 4433 | x-ui 面板 | - | 浏览器 | 200 |
| TCP 8443 | VLESS + TLS + XTLS **Vision** | 26.3.27 | Xray ≥1.8 | 204 |
| UDP 8443 | **Hysteria 2**（TLS/h3） | 26.3.27 | 支持 hysteria2 | 204 |
| TCP 3389 | VLESS + **REALITY** + Vision | 26.3.27 | Xray ≥1.8 | 204 |
| UDP 3389 | VMess + mKCP（旧配置原样） | **25.6.8**（独立容器） | 任意老版本 | 204 + 现网真实流量 |

架构要点：
1. **双内核并存**：老客户端只认 25.x 的 mKCP 线上格式，而 26.x 重写了 mKCP，因此 legacy 入站
   单独跑在 `x-ui-legacy` 容器（同一镜像内的 `bin/xray-legacy-linux-amd64`），与 26.x 入站完全隔离。
2. **证书**：`install.sh --domain --email` 会拉起 Caddy 用 ACME 申请证书，成功后拷贝到
   `/etc/x-ui/certs/{fullchain,privkey}.pem`；`/etc/cron.d/x-ui-cert-sync` 每天同步续期后的证书并重启面板。
3. **REALITY 回落**指向本机 443（Caddy 站点），流量特征与真实站点一致。
4. 内核版本固化在镜像：`XRAY_VERSION=26.3.27`、legacy `25.6.8`，均带 SHA256 校验；
   `inbounds.port` 的 UNIQUE 约束已移除（TLS 与 Hysteria2 共用 8443、REALITY 与 legacy 共用 3389，
   TCP/UDP 各自独立）。注意 gorm AutoMigrate 不会重新加回该约束（已实测）。

## 7. 控制台改造（2026-11-09）

后端：
- **删除版本切换**：移除 `/server/getXrayVersion`、`/server/installXray/:version` 路由及
  `GetXrayVersions/downloadXRay/UpdateXray` 实现（消除"面板下载内核"的任意版本写入风险，内核随镜像固定）。
- **新增分享链接能力**：`web/service/inbound_link.go`
  - `POST /xui/inbound/links` 返回全部入站的 `{protocol,port,network,security,features,up,down,link}`；
  - `POST /xui/inbound/link/:id` 返回单个入站链接。
  - 支持 vless（TLS / REALITY / Vision / **VLESS Encryption**，REALITY 公钥由私钥用 X25519 现场推导）、
    vmess（tcp/ws/grpc/kcp/h2）、trojan、shadowsocks、**hysteria2**。
  - VLESS Encryption 的客户端 encryption 串取自面板设置表 `vlessEncryption`（provision 写入）。

前端：
- 仪表盘重做：资源环 + 4 张状态卡（内核状态/运行时间/负载/连接数）+ 网络卡（实时速率、累计流量、入站汇总）
  + **"协议入站与客户端链接"表**（协议徽标、端口、传输/安全特性标签、流量、链接一键复制）。
- 入站列表：协议按类型着色；新增"复制客户端链接"；传输/安全列直接解析原始 JSON，未知协议（hysteria）
  不再破坏表格；无前端表单模型的协议点"详细信息"展示原始 JSON，避免渲染崩溃。
- 二维码与"详细信息"里的复制均改用后端生成的链接，确保 REALITY/VLESS Encryption 参数完整。

### 7.1 VLESS Encryption 密钥管理（设置页 → 协议密钥）

- 后端 `web/service/vlessenc.go` + 3 个接口：
  - `POST /xui/setting/vlessEnc` 查看状态（算法、服务端 decryption、客户端 encryption 长度、生效入站）
  - `POST /xui/setting/vlessEnc/regenerate` 轮换密钥对（调 `xray vlessenc` 取 ML-KEM-768 那一组，
    同步更新入站 `settings.decryption` 与面板设置表 `vlessEncryption`，随后重启 Xray）
  - `POST /xui/setting/vlessEnc/disable` 关闭（decryption=none，清空客户端串）
- 目标入站固定为 tag `inbound-reality`（REALITY 通道），关闭后仍可通过"重新生成"再次开启。
- 前端「设置 → 协议密钥」：状态徽标、算法、入站、decryption 显示/隐藏/复制、
  encryption 复制、刷新/重新生成/关闭（均带二次确认与风险提示）。
- 已验证闭环：重新生成 → 新链接 204、旧链接失效；关闭 → encryption=none 链接 204；再次生成 → 恢复 PQ。

## 8. 控制台 v3：深色 Control Plane 主题（合并用户改版）

用户提供的改版（`/root/x-ui-release/x-ui-src`）是一套完整的深色控制台：
新增 `assets/css/theme.css`（设计变量 + 自托管字体 + antd 1.7.x 重绘）、`assets/css/custom.css`（45KB 页面级样式）、
`assets/fonts/`（IBM Plex Sans / Saira Semi Condensed）、`assets/img/favicon.svg`、`moment/locale/zh-cn.js`，
并把 `head.html`/`js.html` 改为引主题样式 + zh-CN 语言包；`index/inbounds/setting/login/common_sider` 全部重构为
`page-head / stat-card / ib-card / settings-card` 的卡片式布局。

合并方式（保留对方视觉、保留本分支功能）：
- 资源与公共模板直接采用对方版本（`theme.css`、`custom.css`、`fonts/`、`img/`、`locale/`、`head.html`、`js.html`、`login.html`、`common_sider.html`、`component/setting.html`、`form/inbound.html`）。
- `index.html`：删除"切换版本"按钮与 `version-modal`/`versionModal`/两个切换方法；在 Inbound Overview 行内增加
  **协议特性标签**（tcp/tls/vision/reality/vless-encryption）与**复制链接按钮**，数据来自 `/xui/inbound/links`。
- `inbounds.html`：卡片布局保留；新增"复制客户端链接"，编辑按钮仅对前端有表单模型的协议显示；
  `protoLabel` 增加 `hysteria → HY2`；`streamOf` 改为解析原始 JSON（含 vision/pq），未知协议不再报错；
  二维码与详情改用后端链接；hysteria 详情降级为原始 JSON 展示。
- `setting.html`：保留对方 tabs/settings-card 结构，新增「协议密钥」分区（VLESS Encryption 状态/密钥/轮换/关闭）。
- 后端新增 `POST /xui/inbound/resetTraffic/:id`（hysteria 等无表单模型的入站也能重置流量，前端不再调 update）。
- 补充少量主题配套 CSS：`.ib-line-feat`、`.key-grid/.key-row/.key-text/.key-actions`（含移动端断点）。
- 校验：`go vet ./...`、四页内联脚本 `node --check`、页面 200、主题资源 200、`/server/getXrayVersion` 仍为 404。

### 8.1 入站表单：协议列表与新特性（2026-11-09）

原表单的协议下拉只有 vmess/vless/trojan/shadowsocks/dokodemo-door/http/socks，且用的是弹窗内局部的
`protocols` 映射（不是全局 `Protocols`），因此新协议不可见。修正内容：

- `xray.js`：新增 `Protocols.HYSTERIA`；新增 `Inbound.HysteriaSettings`（clients[].auth / email）与
  `HysteriaStreamSettings`（udpIdleTimeout、masquerade），并接入 `StreamSettings.fromJson/toJson`
  与 `Inbound.Settings.getSettings/fromJson`；切换到 hysteria 时自动置 `network=hysteria, security=tls,
  alpn=h3, 证书=/certs/fullchain.pem|privkey.pem`；`TlsStreamSettings.toJson` 对字符串 alpn 做数组归一化。
- `inbound_modal.html`：协议列表加入 `HYSTERIA`；新增 `toggleVlessEnc/generateVlessEnc`。
- 新增 `form/protocol/hysteria.html`（认证密码、email、UDP 空闲超时、伪装类型/内容）。
- `form/protocol/vless.html`：新增 **VLESS Encryption** 开关 + 只读 decryption + "重新生成密钥"。
- 后端：`POST /xui/setting/vlessEnc/pair` 生成后量子密钥对（只暴露服务端 decryption，客户端串写入设置表）；
  `checkPortExist` 改为按 **TCP/UDP 同族** 判重（同号可共存）；新增 `NextTag` 生成唯一 tag（避免
  `inbound-<port>` 在同号双协议时与 UNIQUE(tag) 冲突）。

实测：hysteria 入站表单提交后 xray 正常下发配置且 e2e 204；同族端口冲突正确拒绝（3389/udp、8443/tcp），
同号异族允许（24443 tcp vless + udp hysteria 同时监听）。

### 8.2 入站表单支持 REALITY（2026-11-09）

- `xray.js`：新增 `RealityStreamSettings`（show/dest/xver/serverNames/privateKey/shortIds/maxTimeDiff，
  逗号串↔数组归一化）、`StreamSettings` 接入 `realitySettings`（仅 security=reality 时输出）、
  `Inbound.reality` getter/setter（开启时强制 network=tcp）与 `canEnableReality()`（vless/trojan + tcp）。
- `form/tls_settings.html`：新增 REALITY 开关与参数区（dest、serverNames、xver、maxTimeDiff、
  privateKey、shortIds、一键生成密钥），并提示 dest 必须是真实 TLS1.3 站点（本机填 127.0.0.1:443）。
- `inbound_modal.html`：`generateRealityKeys()` 调后端生成 X25519 密钥对与 shortId，并自动补
  dest/serverNames 默认值。
- `form/protocol/vless.html`：REALITY 下也显示 flow 选择；`FLOW_CONTROL` 收敛为
  `xtls-rprx-vision`（26.x 已移除 origin/direct，避免生成非法配置）。
- 后端：`POST /xui/setting/realityKeyPair`（新 `service/reality.go`）返回 privateKey/publicKey/shortId；
  链接生成沿用 `inbound_link.go` 里由私钥现场推导公钥的逻辑。

实测：用表单同构 payload 新建 vless+reality+vision 入站 → xray 正常监听 24444 → 面板生成链接
（含 pbk/sid/sni/fp）→ 客户端 e2e 204；测试入站已删除。

### 8.3 协议命名与全协议回归（2026-11-09）

Xray 内核只认 `protocol: "hysteria"`（26.3.27 实测 `hysteria2` 会报 `unknown config id`），
但用户在面板里期望看到 `hysteria2`。处理方式：

- 面板/DB 统一使用 **`hysteria2`**（`Protocols.HYSTERIA = 'hysteria2'`，表单下拉、列表徽标 HY2）。
- `database/model/model.go` 的 `GenXrayInboundConfig()` 在生成内核配置时把 `hysteria2` 映射为 `hysteria`；
  旧的 `hysteria` 值仍然兼容（`isUdpInbound`、链接生成、统计均两者都认）。
- `deploy/provision.py` 改为写入 `hysteria2`；生产库已把原 `hysteria` 行迁移为 `hysteria2`。

全协议回归（用面板 API 以表单同构 payload 逐个新建 → 校验内核配置 → 真实连通性 → 删除）：

| 协议 | 端口 | 内核配置 | 连通性 |
| --- | --- | --- | --- |
| vmess (tcp) | 25001 | ✅ | 204 |
| vless (tcp) | 25002 | ✅ | 204 |
| trojan (tcp+tls) | 25004 | ✅ | 204 |
| shadowsocks (aes-256-gcm) | 25005 | ✅ | 204 |
| socks (noauth) | 25006 | ✅ | 204（直接 socks5 拉取） |
| http (带认证) | 25007 | ✅ | 204（http 代理拉取） |
| dokodemo-door | 25008 | ✅ | 204（转发到 www.gstatic.com:80） |
| hysteria2 (QUIC/tls) | 25009 | ✅（配置里为 `hysteria`） | 204 |

测试入站已全部删除，生产保持 3 条：8443 vless+vision / 3389 vless+reality+PQ / 3389 hysteria2。

### 8.4 REALITY 密钥管理进面板 + 前端缓存修复（2026-11-09）

**REALITY 密钥面板化**（与 VLESS Encryption 并排，脱离 provision 脚本）：
- `service/reality.go` 扩展：`Status()` 列出所有 REALITY 入站（dest/serverNames/公钥/shortIds），
  `Rotate(id)` 轮换（id<=0 表示全部），重新生成 X25519 私钥与等量 shortId，写回 `streamSettings.realitySettings`
  并重启 Xray；链接里的 `pbk/sid` 由私钥现场推导，因此轮换后重新复制即可。
- 接口：`POST /xui/setting/reality`、`POST /xui/setting/reality/rotate/:id`。
- 设置页「协议密钥」新增 REALITY 卡片：逐条列出入站、显示/复制公钥与 shortId、单条或全部轮换（二次确认 + 立即失效提示）。

**"添加入站/编辑点击无效"根因与修复**：
- 根因：静态资源 URL 的版本号是常量 `0.3.2`，而 `assets/*` 带 `max-age=31536000`，浏览器一直用旧
  `xray.js`（不含 `canEnableReality`/`reality`），新 HTML 模板渲染时抛错 → 弹窗静默失败。
- 修复：`web/web.go` 启动时对嵌入资源做 SHA-256 指纹，注入 `cur_ver`（`0.3.2-<hash>`），
  使每次构建的 JS/CSS URL 都变化；HTML 仍为 `no-store`。已用 jsdom 回归：添加/编辑弹窗均可正常打开、
  无控制台错误、REALITY 开关可见。

轮换实测：临时 REALITY 入站轮换后 pbk/sid 变化，重新生成的链接 e2e 204；测试入站已删除。
