#!/usr/bin/env bash
# 首次把本仓库推送到 GitHub（无需 gh CLI）
#
#   GITHUB_TOKEN=ghp_xxx GITHUB_OWNER=<user|org> REPO=<name> ./scripts/push.sh
#
# 可选: PRIVATE=1（默认建私有仓库）, DESCRIPTION="..."
set -Eeuo pipefail

: "${GITHUB_TOKEN:?需要 GITHUB_TOKEN（repo 权限的 PAT）}"
: "${GITHUB_OWNER:?需要 GITHUB_OWNER（用户名或组织）}"
REPO="${REPO:-x-ui-control-plane}"
PRIVATE="${PRIVATE:-1}"
DESCRIPTION="${DESCRIPTION:-x-ui control plane: hardened panel + VLESS Vision/REALITY/Hysteria2, dockerized}"
BRANCH="${BRANCH:-main}"

api() { curl -sS -H "Authorization: Bearer ${GITHUB_TOKEN}" \
             -H "Accept: application/vnd.github+json" "$@"; }

# 1) 仓库不存在则创建（用户 or 组织）
if ! api "https://api.github.com/repos/${GITHUB_OWNER}/${REPO}" | grep -q '"full_name"'; then
  if api -X GET "https://api.github.com/orgs/${GITHUB_OWNER}" | grep -q '"login"'; then
    endpoint="https://api.github.com/orgs/${GITHUB_OWNER}/repos"
  else
    endpoint="https://api.github.com/user/repos"
  fi
  echo "[+] 创建仓库 ${GITHUB_OWNER}/${REPO}"
  api -X POST "$endpoint" \
      -d "{\"name\":\"${REPO}\",\"description\":\"${DESCRIPTION}\",\"private\":$([ "$PRIVATE" = 1 ] && echo true || echo false)}" \
      | grep -E '"(full_name|html_url|private)"' || true
fi

# 2) 推送当前目录（默认分支 main）
git rev-parse --git-dir >/dev/null 2>&1 || { git init -q; git checkout -q -b "$BRANCH"; }
git add -A && { git diff --cached --quiet || git -c user.name="${GIT_AUTHOR_NAME:-x-ui}" \
    -c user.email="${GIT_AUTHOR_EMAIL:-x-ui@users.noreply.github.com}" commit -qm "chore: sync"; }
echo "[+] 推送到 ${GITHUB_OWNER}/${REPO}:${BRANCH}"
git push --quiet "https://x-access-token:${GITHUB_TOKEN}@github.com/${GITHUB_OWNER}/${REPO}.git" "HEAD:${BRANCH}"
git remote remove origin 2>/dev/null || true
git remote add origin "https://github.com/${GITHUB_OWNER}/${REPO}.git"
echo "[✓] https://github.com/${GITHUB_OWNER}/${REPO}"
