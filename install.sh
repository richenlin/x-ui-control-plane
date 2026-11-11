#!/usr/bin/env bash
# x-ui 一键部署 / 迁移
#
#   有域名（Caddy 自动申请 Let's Encrypt 证书）：
#     ./install.sh --domain p.example.com --email you@example.com
#
#   已有证书 / 已有站点占用 80,443：
#     ./install.sh --domain p.example.com --cert ./fullchain.pem --key ./privkey.pem
#
#   仅现代协议（不要 TLS 站点）：
#     ./install.sh --domain p.example.com --no-caddy --cert ... --key ...
#
# 入站布局：
#   TCP 8443  VLESS + TLS + XTLS Vision   （最新协议，证书由 Caddy 申请/续期）
#   UDP 8443  Hysteria 2                  （最新协议，需安全组放行 UDP 8443）
#   TCP 3389  VLESS + REALITY + Vision    （无需证书，伪装成自己的站点）
#   UDP 3389  VMess + mKCP（25.x 老内核） （旧客户端配置原样可用）
#   TCP 4433  x-ui 面板（随机管理员密码）
set -Eeuo pipefail

IMAGE="${XUI_IMAGE:-x-ui:hardened-20261109}"
CADDY_IMAGE="${CADDY_IMAGE:-caddy:2-alpine}"
DIR="${XUI_DIR:-/opt/x-ui}"
DATA_DIR="${XUI_DATA_DIR:-/etc/x-ui}"
CONTAINER="${XUI_CONTAINER:-x-ui}"

PANEL_PORT="${PANEL_PORT:-4433}"
VISION_PORT="${VISION_PORT:-8443}"
HY2_PORT="${HY2_PORT:-3389}"
REALITY_PORT="${REALITY_PORT:-3389}"


DOMAIN=""; EMAIL="${ACME_EMAIL:-}"; ENDPOINT=""
CERT=""; KEY=""; IMAGE_TAR=""; CADDY_TAR=""
REALITY_DEST=""; NO_CADDY=0; NO_TLS=0; NO_PQ=0; NO_HY2=0; SKIP_START=0

log()  { printf '\033[1;32m[+]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

usage() { sed -n '2,18p' "$0" | sed -e 's/^# \{0,1\}//' -e '/^$/{x;/./d;x}' ; cat <<'EOF'

参数:
  --domain <域名>        必填；TLS 与 REALITY 的 SNI
  --email <邮箱>         ACME 注册邮箱（Caddy 申请证书用，建议填）
  --cert <路径> --key <路径>   使用已有证书（跳过 Caddy）
  --endpoint <地址>      客户端链接里的服务器地址（默认 = --domain）
  --image-tar <tar.gz>   离线导入 x-ui 镜像
  --caddy-tar <tar.gz>   离线导入 caddy 镜像
  --panel-port/--vision-port/--hy2-port/--reality-port
  --reality-dest <host:port>   REALITY 回落目标（默认 127.0.0.1:443，即 Caddy 站点）
  --no-caddy             不部署 Caddy（80/443 已被占用时）
  --no-tls               不建 TLS 入站（只保留 REALITY）；此时 --domain 仅作 REALITY SNI
  --no-hy2               不建 Hysteria2 入站（无证书 / 纯 REALITY 场景）
  --no-pq                关闭 VLESS Encryption（客户端 < 26.3 时使用）
  --skip-start           只准备数据，不启动容器
环境变量: XUI_IMAGE XUI_DIR XUI_DATA_DIR XUI_CONTAINER
EOF
  exit 0; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain) DOMAIN="${2:?}"; shift 2;;
    --email) EMAIL="${2:?}"; shift 2;;
    --cert) CERT="${2:?}"; shift 2;;
    --key) KEY="${2:?}"; shift 2;;
    --endpoint) ENDPOINT="${2:?}"; shift 2;;
    --image-tar) IMAGE_TAR="${2:?}"; shift 2;;
    --caddy-tar) CADDY_TAR="${2:?}"; shift 2;;
    --image) IMAGE="${2:?}"; shift 2;;
    --panel-port) PANEL_PORT="${2:?}"; shift 2;;
    --vision-port) VISION_PORT="${2:?}"; shift 2;;
    --hy2-port) HY2_PORT="${2:?}"; shift 2;;
    --reality-port) REALITY_PORT="${2:?}"; shift 2;;
    --reality-dest) REALITY_DEST="${2:?}"; shift 2;;
    --no-caddy) NO_CADDY=1; shift;;
    --no-tls) NO_TLS=1; shift;;
    --no-hy2) NO_HY2=1; shift;;
    --no-pq) NO_PQ=1; shift;;
    --skip-start) SKIP_START=1; shift;;
    -h|--help) usage;;
    *) die "未知参数: $1（--help 查看用法）";;
  esac
done

[[ $EUID -eq 0 ]] || die "请用 root 运行"
command -v docker >/dev/null || die "未安装 docker"
docker compose version >/dev/null 2>&1 || die "缺少 docker compose 插件"
command -v openssl >/dev/null || die "缺少 openssl"
[[ -n "$DOMAIN" ]] || die "需要 --domain（--help 查看用法）"
[[ -n "$ENDPOINT" ]] || ENDPOINT="$DOMAIN"
[[ -n "$REALITY_DEST" ]] || REALITY_DEST="127.0.0.1:443"

# ---------- 1. 镜像 ----------
load_image() { # $1=tag $2=tar(optional) $3=build dir(optional)
  if docker image inspect "$1" >/dev/null 2>&1; then log "镜像已存在: $1"; return; fi
  if [[ -n "$2" ]]; then
    log "导入镜像 $2"; case "$2" in *.gz) gunzip -c "$2" | docker load;; *) docker load -i "$2";; esac
  elif [[ -n "$3" && -f "$3/Dockerfile" ]]; then
    log "本地构建 $1"; ( cd "$3" && docker build --network host -t "$1" . )
  else
    die "没有镜像 $1：请提供 tar 或在含 Dockerfile 的目录执行"
  fi
}
SELF_DIR="$(cd "$(dirname "$0")" && pwd)"
load_image "$IMAGE" "$IMAGE_TAR" "$SELF_DIR"

MANAGE_CADDY=0
if [[ $NO_CADDY -eq 0 && -z "$CERT" && $NO_TLS -eq 0 ]]; then
  MANAGE_CADDY=1
  if ! docker image inspect "$CADDY_IMAGE" >/dev/null 2>&1; then
    [[ -n "$CADDY_TAR" ]] && load_image "$CADDY_IMAGE" "$CADDY_TAR"
    [[ -n "$CADDY_TAR" ]] || { log "拉取 $CADDY_IMAGE"; docker pull -q "$CADDY_IMAGE" >/dev/null; }
  fi
  # 80/443 必须空闲
  for p in 80 443; do
    if ss -lntp 2>/dev/null | awk '{print $4}' | grep -qE "[:.]$p$"; then
      die "端口 $p 已被占用，无法用 Caddy 申请证书；请用 --cert/--key 或 --no-caddy"
    fi
  done
fi

# ---------- 2. 证书 ----------
install -d -m 700 "$DATA_DIR" "$DATA_DIR/certs"
INSECURE=0
if [[ $NO_TLS -eq 0 ]]; then
  if [[ -n "$CERT" && -n "$KEY" ]]; then
    install -m 644 "$CERT" "$DATA_DIR/certs/fullchain.pem"
    install -m 600 "$KEY"  "$DATA_DIR/certs/privkey.pem"
    log "使用已有证书: $CERT"
  elif [[ $MANAGE_CADDY -eq 1 ]]; then
    log "由 Caddy 申请证书: $DOMAIN"
    install -d -m 700 "$DATA_DIR/caddy/data" "$DATA_DIR/caddy/config" "$DATA_DIR/www"
    [[ -f "$DATA_DIR/www/index.html" ]] || cat > "$DATA_DIR/www/index.html" <<'HTML'
<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Welcome</title></head>
<body><h1>It works!</h1><p>This site is served by Caddy.</p></body></html>
HTML
    cat > "$DATA_DIR/Caddyfile" <<EOF
{
	email ${EMAIL:-admin@$DOMAIN}
	admin off
}
$DOMAIN {
	root * /srv
	encode gzip
	file_server
	header {
		-Server
		X-Content-Type-Options nosniff
	}
}
EOF
  elif [[ -f "$DATA_DIR/certs/fullchain.pem" && -f "$DATA_DIR/certs/privkey.pem" ]]; then
    log "沿用 $DATA_DIR/certs 中已有证书"
  else
    die "需要 --cert/--key，或让脚本用 Caddy 申请证书"
  fi
fi

# ---------- 3. 面板数据库 ----------
in_img() { docker run --rm -v "$DATA_DIR:/etc/x-ui" -v "$DATA_DIR/certs:/certs:ro" "$IMAGE" "$@"; }
[[ -f "$DATA_DIR/x-ui.db" ]] || { log "初始化面板数据库"; in_img ./x-ui setting -show true >/dev/null; }
ADMIN_PASS="$(openssl rand -base64 18 | tr -d '/+=' | cut -c1-20)"
log "面板端口 $PANEL_PORT，管理员密码稍后打印"
in_img ./x-ui setting -port "$PANEL_PORT" >/dev/null
in_img ./x-ui setting -username admin -password "$ADMIN_PASS" >/dev/null

# ---------- 4. compose / caddy 启动 ----------
install -d -m 755 "$DIR"

CADDY_BLOCK=""
[[ $MANAGE_CADDY -eq 1 ]] && CADDY_BLOCK=$(cat <<EOF
  caddy:
    image: $CADDY_IMAGE
    container_name: ${CONTAINER}-caddy
    network_mode: host
    restart: unless-stopped
    volumes:
      - $DATA_DIR/Caddyfile:/etc/caddy/Caddyfile:ro
      - $DATA_DIR/www:/srv:ro
      - $DATA_DIR/caddy/data:/data
      - $DATA_DIR/caddy/config:/config
    logging:
      driver: json-file
      options: { max-size: "10m", max-file: "2" }
EOF
)


cat > "$DIR/compose.yaml" <<EOF
name: $CONTAINER
services:
$CADDY_BLOCK
  $CONTAINER:
    image: $IMAGE
    container_name: $CONTAINER
    network_mode: host
    restart: unless-stopped
    stop_grace_period: 30s
    volumes:
      - $DATA_DIR:/etc/x-ui
      - $DATA_DIR/certs:/certs:ro
    environment:
      TZ: Asia/Shanghai
      XUI_LOG_LEVEL: info
      XUI_LOGIN_MAX_FAILS: "5"
      XUI_LOGIN_WINDOW: 10m
      XUI_LOGIN_LOCK: 15m
      XUI_LOGIN_MAX_LOCK: 24h
      XUI_LOGIN_GLOBAL_FAILS: "100"
      XUI_LOGIN_GLOBAL_WINDOW: 10m
      XUI_LOGIN_GLOBAL_LOCK: 15m
      XUI_HEALTHCHECK_PORT: "$PANEL_PORT"
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    pids_limit: 512
    mem_limit: 256m
    memswap_limit: 384m
    logging:
      driver: json-file
      options: { max-size: "10m", max-file: "3" }
EOF

if [[ $MANAGE_CADDY -eq 1 && $SKIP_START -eq 0 ]]; then
  log "启动 Caddy 申请证书（等待 ACME，最多 3 分钟）"
  ( cd "$DIR" && docker compose up -d caddy >/dev/null )
  CERT_PATH="$DATA_DIR/caddy/data/caddy/certificates/acme-v02.api.letsencrypt.org-directory/$DOMAIN/$DOMAIN.crt"
  for i in $(seq 1 60); do
    [[ -f "$CERT_PATH" ]] && break; sleep 3
  done
  [[ -f "$CERT_PATH" ]] || die "证书申请失败，请检查 DNS 是否解析到本机、80/443 是否可达、域名是否备案"
  install -m 644 "$CERT_PATH" "$DATA_DIR/certs/fullchain.pem"
  install -m 600 "${CERT_PATH%.crt}.key" "$DATA_DIR/certs/privkey.pem"
  log "证书就绪: $DOMAIN"
fi

# ---------- 5. 入站 ----------
log "生成入站（Vision / REALITY+VLESS Encryption / Hysteria2）"
# DOMAIN/REALITY_SNI are always passed: in --no-tls mode they only feed REALITY serverNames
# (provision.py gates the TLS inbound on the cert files actually existing).
H2=$((1-NO_HY2))
ENVS=(-e "VISION_PORT=$VISION_PORT" -e "HY2_PORT=$HY2_PORT" -e "REALITY_PORT=$REALITY_PORT"
      -e "REALITY_DEST=$REALITY_DEST" -e "ENDPOINT=$ENDPOINT" -e "INSECURE=$INSECURE"
      -e "PQ=$((1-NO_PQ))" -e "HY2_ENABLE=$H2"
      -e "DOMAIN=$DOMAIN" -e "REALITY_SNI=${REALITY_SNI:-$DOMAIN}")
[[ $NO_TLS -eq 0 ]] && ENVS+=(-e "CERT_FILE=/certs/fullchain.pem" -e "KEY_FILE=/certs/privkey.pem")
SUMMARY="$(docker run --rm -v "$DATA_DIR:/etc/x-ui" -v "$DATA_DIR/certs:/certs:ro" "${ENVS[@]}" "$IMAGE" ./provision.py)"

# ---------- 6. 启动 & 证书续期 ----------
if [[ $SKIP_START -eq 0 ]]; then
  log "启动全部服务"
  ( cd "$DIR" && docker compose up -d >/dev/null )
  sleep 8
  docker ps --filter "name=$CONTAINER" --format '    {{.Names}}\t{{.Status}}'
fi

if [[ $MANAGE_CADDY -eq 1 ]]; then
  cat > "$DATA_DIR/sync-cert.sh" <<EOF
#!/bin/sh
# Caddy 续期后同步证书并重启面板（cron 每天 04:20 执行）
set -e
SRC="$DATA_DIR/caddy/data/caddy/certificates/acme-v02.api.letsencrypt.org-directory/$DOMAIN"
DST="$DATA_DIR/certs"
[ -f "\$SRC/$DOMAIN.crt" ] || exit 0
if ! cmp -s "\$SRC/$DOMAIN.crt" "\$DST/fullchain.pem"; then
  cp "\$SRC/$DOMAIN.crt" "\$DST/fullchain.pem"
  cp "\$SRC/$DOMAIN.key" "\$DST/privkey.pem"
  chmod 644 "\$DST/fullchain.pem"; chmod 600 "\$DST/privkey.pem"
  docker restart "$CONTAINER" >/dev/null 2>&1 || true
fi
EOF
  chmod 700 "$DATA_DIR/sync-cert.sh"
  echo "20 4 * * * root $DATA_DIR/sync-cert.sh" > /etc/cron.d/x-ui-cert-sync
  chmod 644 /etc/cron.d/x-ui-cert-sync
  log "已安装证书续期同步任务 /etc/cron.d/x-ui-cert-sync"
fi

printf '\n============ 部署完成 ============\n'
printf '面板:   http://%s:%s/\n账号:   admin\n密码:   %s\n\n' "$ENDPOINT" "$PANEL_PORT" "$ADMIN_PASS"
printf '入站与客户端链接（保存在 %s/inbounds.json）:\n' "$DATA_DIR"
printf '%s' "$SUMMARY" | python3 - <<'PY' 2>/dev/null || printf '%s\n' "$SUMMARY"
import json, sys
try: d = json.load(sys.stdin)
except Exception: sys.exit(1)
for i in d.get("inbounds", []):
    print(f"  - {i['protocol']:<22} port {i['port']}")
    print(f"    {i['link']}")
PY
cat <<EOF

提示:
  * 安全组需放行: TCP $PANEL_PORT / TCP $VISION_PORT / UDP $HY2_PORT / TCP $REALITY_PORT
  * Hysteria2 走 UDP $HY2_PORT，很多云厂商默认只放行 TCP，请确认 UDP 规则（本机需有 TLS 证书）
  * REALITY 回落目标 $REALITY_DEST 应是本机真实 TLS 站点（Caddy 站点即满足）
  * 面板改密: docker exec $CONTAINER ./x-ui setting -username admin -password '<新密码>'
  * REALITY 入站启用了 VLESS Encryption（ML-KEM-768 后量子），客户端需 Xray ≥ 26.3；旧客户端可用 --no-pq 关闭
=================================
EOF
