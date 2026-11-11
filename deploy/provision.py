#!/usr/bin/env python3
"""Provision x-ui inbounds:

  1. VLESS + TLS + XTLS Vision                (TCP  VISION_PORT,  default 8443)
  2. VLESS + REALITY + Vision + VLESS Encryption (TCP REALITY_PORT, default 3389)
  3. Hysteria 2                               (UDP  HY2_PORT,     default 3389)

Idempotent: rows are matched by tag or by (port, protocol) and replaced.
Runs against an existing panel database while the panel is stopped.

Environment:
  VISION_PORT / REALITY_PORT / HY2_PORT      ports (8443 / 3389 / 3389)
  DOMAIN                                      TLS SNI and REALITY serverName (required)
  CERT_FILE / KEY_FILE                        certificate paths inside the container
  REALITY_DEST                                REALITY fallback target (127.0.0.1:443)
  REALITY_SNI                                 REALITY serverNames, comma separated ($DOMAIN)
  UUID                                        VLESS UUID for the Vision inbound
  REALITY_UUID / REALITY_PRIVATE_KEY / REALITY_SHORTID
  HY2_AUTH                                    Hysteria 2 password
  PQ                                          1 = enable VLESS Encryption (default), 0 = off
  ENDPOINT                                    host used in the client links
  INSECURE                                    1 = links use allowInsecure/insecure
  XUI_DB                                      database path (/etc/x-ui/x-ui.db)
"""

import base64
import json
import os
import re
import secrets
import sqlite3
import subprocess
import sys
import uuid as uuidlib

DB = os.environ.get("XUI_DB", "/etc/x-ui/x-ui.db")
XRAY = os.environ.get("XRAY_BIN", "/usr/local/x-ui/bin/xray-linux-amd64")
SNIFFING = json.dumps({"enabled": True, "destOverride": ["http", "tls"]}, indent=2)


def env(name, default=""):
    v = os.environ.get(name, "")
    return v if v != "" else default


def gen_uuid():
    return str(uuidlib.uuid4())


def gen_short_id():
    return secrets.token_hex(8)


def gen_secret(n=24):
    return "".join(secrets.choice("abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789")
                   for _ in range(n))


def x25519():
    out = subprocess.run([XRAY, "x25519"], capture_output=True, text=True, check=True).stdout
    priv = re.search(r"PrivateKey:\s*(\S+)", out)
    pub = re.search(r"(?:Password|PublicKey)(?: \(PublicKey\))?:\s*(\S+)", out)
    if not priv or not pub:
        sys.exit("cannot parse x25519 output:\n" + out)
    return priv.group(1), pub.group(1)


def vlessenc():
    """ML-KEM-768 + X25519 hybrid pair for VLESS Encryption (post-quantum)."""
    out = subprocess.run([XRAY, "vlessenc"], capture_output=True, text=True, check=True).stdout
    block = out.split("Authentication: ML-KEM-768", 1)
    if len(block) != 2:
        sys.exit("unexpected vlessenc output:\n" + out)
    dec = re.search(r'"decryption":\s*"([^"]+)"', block[1])
    enc = re.search(r'"encryption":\s*"([^"]+)"', block[1])
    if not dec or not enc:
        sys.exit("cannot parse vlessenc output:\n" + out)
    return dec.group(1), enc.group(1)


def allow_shared_port(con):
    """Drop UNIQUE(port) so TCP and UDP can share a port number."""
    ddl = con.execute(
        "select sql from sqlite_master where type='table' and name='inbounds'").fetchone()[0]
    if "`port` integer UNIQUE" not in ddl:
        return
    cols = ("`id` integer,`user_id` integer,`up` integer,`down` integer,`total` integer,"
            "`remark` text,`enable` numeric,`expiry_time` integer,`listen` text,"
            "`port` integer,`protocol` text,`settings` text,`stream_settings` text,"
            "`tag` text UNIQUE,`sniffing` text,PRIMARY KEY (`id`)")
    con.executescript(f"""
        CREATE TABLE inbounds_new ({cols});
        INSERT INTO inbounds_new SELECT * FROM inbounds;
        DROP TABLE inbounds;
        ALTER TABLE inbounds_new RENAME TO inbounds;
    """)


def upsert(con, tag, port, protocol, remark, settings, stream, sniffing):
    # idempotent even if the panel user renamed the tag: match tag OR port+protocol
    con.execute("delete from inbounds where tag = ? or (port = ? and protocol = ?)",
                (tag, port, protocol))
    con.execute("""insert into inbounds
        (user_id, up, down, total, remark, enable, expiry_time, listen, port,
         protocol, settings, stream_settings, tag, sniffing)
        values (1,0,0,0,?,1,0,'',?,?,?,?,?,?)""",
                (remark, port, protocol,
                 json.dumps(settings, indent=2), json.dumps(stream, indent=2),
                 tag, sniffing))


def main():
    if not os.path.exists(DB):
        sys.exit(f"database not found: {DB} (start the panel once first)")

    domain = env("DOMAIN")
    cert = env("CERT_FILE", "/certs/fullchain.pem")
    key = env("KEY_FILE", "/certs/privkey.pem")
    vision_port = int(env("VISION_PORT", "8443"))
    reality_port = int(env("REALITY_PORT", "3389"))
    hy2_port = int(env("HY2_PORT", "3389"))
    reality_dest = env("REALITY_DEST", "127.0.0.1:443")
    reality_sni = env("REALITY_SNI", domain)
    endpoint = env("ENDPOINT", domain)
    tls = bool(domain) and os.path.exists(cert) and os.path.exists(key)
    insecure = env("INSECURE", "0") == "1" or not tls
    pq = env("PQ", "1") == "1"
    # Hysteria2 terminates TLS itself and therefore needs a certificate; on an
    # IP-only / REALITY-only host set HY2_ENABLE=0 to skip the inbound entirely.
    hy2_enable = env("HY2_ENABLE", "1") == "1" and tls

    con = sqlite3.connect(DB)
    allow_shared_port(con)
    summary = {"tls": tls, "pq": pq, "domain": domain, "inbounds": []}

    # ---------- 1. VLESS + TLS + XTLS Vision ----------
    if tls:
        uuid_v = env("UUID", gen_uuid())
        upsert(con, "inbound-vision", vision_port, "vless", f"vless-vision-{vision_port}",
               {"clients": [{"id": uuid_v, "flow": "xtls-rprx-vision"}], "decryption": "none"},
               {"network": "tcp", "security": "tls", "tlsSettings": {
                   "serverName": domain, "alpn": ["http/1.1"],
                   "certificates": [{"certificateFile": cert, "keyFile": key}]}},
               SNIFFING)
        summary["inbounds"].append({
            "tag": "inbound-vision", "protocol": "vless+vision", "port": vision_port,
            "uuid": uuid_v,
            "link": (f"vless://{uuid_v}@{endpoint}:{vision_port}"
                     f"?encryption=none&security=tls&sni={domain}&alpn=http%2F1.1"
                     f"&flow=xtls-rprx-vision&type=tcp"
                     + ("&allowInsecure=1" if insecure else "") + "#x-ui-vision")})

    # ---------- 2. VLESS + REALITY (+ Vision) (+ VLESS Encryption) ----------
    priv = env("REALITY_PRIVATE_KEY")
    pub = env("REALITY_PUBLIC_KEY")
    if priv and not pub:
        out = subprocess.run([XRAY, "x25519", "-i", priv], capture_output=True, text=True).stdout
        m = re.search(r"(?:Password|PublicKey)(?: \(PublicKey\))?:\s*(\S+)", out)
        pub = m.group(1) if m else ""
    if not priv:
        priv, pub = x25519()
    sid = env("REALITY_SHORTID", gen_short_id())
    uuid_r = env("REALITY_UUID", gen_uuid())
    dec = er = "none"
    if pq:
        dec = env("VLESSENC_DECRYPTION")
        er = env("VLESSENC_ENCRYPTION")
        if not dec or not er:
            dec, er = vlessenc()
    upsert(con, "inbound-reality", reality_port, "vless", f"vless-pq-reality-{reality_port}",
           {"decryption": dec,
            "clients": [{"id": uuid_r, "flow": "xtls-rprx-vision"}]},
           {"network": "tcp", "security": "reality", "realitySettings": {
               "show": False, "dest": reality_dest, "xver": 0,
               "serverNames": [s for s in reality_sni.split(",") if s],
               "privateKey": priv, "shortIds": [sid]}},
           SNIFFING)
    sni0 = [s for s in reality_sni.split(",") if s][0]
    summary["inbounds"].append({
        "tag": "inbound-reality",
        "protocol": "vless+reality+vision" + ("+pq" if pq else ""),
        "port": reality_port, "uuid": uuid_r, "publicKey": pub, "shortId": sid, "sni": sni0,
        "link": (f"vless://{uuid_r}@{endpoint}:{reality_port}"
                 f"?encryption={er if pq else 'none'}&security=reality&sni={sni0}"
                 f"&fp=chrome&pbk={pub}&sid={sid}&flow=xtls-rprx-vision&type=tcp#x-ui-reality"
                 + ("-pq" if pq else ""))})
    if pq:
        summary["vlessenc"] = {"decryption": dec, "encryption": er}
        # The panel only stores the server side string in the inbound settings; the
        # client side string lives in the settings table so that "copy link" can
        # emit encryption=... (see web/service/inbound_link.go: vlessEncryptionFor).
        # Without this row the panel answers "启用了 VLESS Encryption，但面板缺少客户端
        # encryption 串" for every link of this inbound.
        con.execute("delete from settings where key = 'vlessEncryption'")
        con.execute("insert into settings (key, value) values ('vlessEncryption', ?)", (er,))

    # ---------- 3. Hysteria 2 ----------
    if hy2_enable:
        auth = env("HY2_AUTH", gen_secret())
        tls_cfg = {"serverName": domain, "alpn": ["h3"],
                   "certificates": [{"certificateFile": cert, "keyFile": key}]}
        upsert(con, "inbound-hysteria2", hy2_port, "hysteria2", f"hysteria2-{hy2_port}udp",
               {"version": 2, "clients": [{"auth": auth, "email": "h2"}]},
               {"network": "hysteria", "security": "tls", "tlsSettings": tls_cfg,
                "hysteriaSettings": {"version": 2, "auth": auth, "udpIdleTimeout": 60,
                                     "masquerade": {"type": "string", "statusCode": 200,
                                                    "content": "<!doctype html><html><body>It works!</body></html>"}}},
               SNIFFING)
        summary["inbounds"].append({
            "tag": "inbound-hysteria2", "protocol": "hysteria2", "port": hy2_port, "auth": auth,
            "link": (f"hysteria2://{auth}@{endpoint}:{hy2_port}"
                     f"?sni={domain or sni0}&insecure={'1' if insecure else '0'}#x-ui-hysteria2")})

    con.commit()

    path = os.path.join(os.path.dirname(DB), "inbounds.json")
    with open(path, "w") as f:
        json.dump(summary, f, indent=2)
    os.chmod(path, 0o600)
    print(json.dumps(summary, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
