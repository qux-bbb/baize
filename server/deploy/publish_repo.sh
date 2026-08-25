#!/usr/bin/env bash
# ============================================================
# Baize 发布仓库 GitHub 同步脚本 (publish_repo.sh)
#
# 职责: 把发布仓库 <REPO> 的对外内容推到 GitHub main 分支:
#       install.sh（单一源: deploy/install.sh）
#       README.md（单一源: deploy/publish/README.md）
#       description（单一源: deploy/publish/description.txt）
#       三者均有单一来源，避免"两边各写各、文案漂移"。
#
# 用法:
#   ./publish_repo.sh            # 显示 diff 后询问确认再 push
#   ./publish_repo.sh --yes      # 跳过交互确认（自动化场景）
#
# 前置:
#   - 本机 git-bash 运行；gh CLI（默认全路径，可用 BAIZE_GH 覆盖）
#     且已 gh auth login 登录发布账号（token 需 repo 权限）
#   - GitHub 直连被墙，需代理（默认 socks5h://127.0.0.1:10808，
#     可用 BAIZE_PROXY 覆盖；空串表示不走代理）
#
# 说明:
#   - 资产上传（gh release create）由 build_release.sh [7/4] 负责，
#     本脚本只处理仓库 main 分支内容，两脚本相互独立、可单独运行。
#   - 发布流程: ./build_release.sh <版本> 构建出包/传资产  →  ./publish_repo.sh
#   - Linux 下无 LOCALAPPDATA 时回退到 mktemp -d。
# ============================================================
set -euo pipefail

REPO="${BAIZE_REPO:-qux-bbb/baize}"
GH="${BAIZE_GH:-$LOCALAPPDATA/Programs/GitHubCLI/bin/gh.exe}"
command -v "$GH" >/dev/null 2>&1 || GH="gh"
PROXY="${BAIZE_PROXY:-socks5h://127.0.0.1:10808}"
[ -n "$PROXY" ] && { export HTTPS_PROXY="$PROXY"; export HTTP_PROXY="$PROXY"; }

AUTO=0
if [ "${1:-}" == "--yes" ]; then AUTO=1; fi

DEPLOY="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(dirname "$DEPLOY")"
PUB="$DEPLOY/publish"
README_SRC="$PUB/README.md"
DESC_SRC="$PUB/description.txt"
INSTALL_SRC="$DEPLOY/install.sh"

# 临时工作区（用真实 Windows 相对路径规避 git 不认 MSYS /tmp 的坑）
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
REPO_DIR="$WORK/repo"

# 在发布仓库工作树内执行命令（cd + 相对名，让 Windows git 拿到真实 cwd）
in_repo() { ( cd "$REPO_DIR" && "$@"; ); }

echo "=================================================="
echo " 发布仓库同步 -> $REPO (main)"
echo "=================================================="

# [0/5] 单一源必须齐备
for f in "$README_SRC" "$DESC_SRC" "$INSTALL_SRC"; do
  [ -f "$f" ] || { echo "[错误] 缺少单一源: $f"; exit 1; }
done

# [1/5] 行尾校验: install.sh / README 须 LF（CRLF 会破坏 shell 与渲染）
for f in "$INSTALL_SRC" "$README_SRC"; do
  if grep -q $'\r' "$f"; then
    echo "[错误] $f 含 CR（行尾应为 LF）。以 LF 保存后再发布。"
    exit 1
  fi
done

# [2/5] 克隆发布仓库
echo "[1/5] 克隆 $REPO ..."
( cd "$WORK" && git clone --depth 1 "https://github.com/$REPO.git" "./repo" >/dev/null 2>&1 )
[ -d "$REPO_DIR/.git" ] || { echo "[错误] clone 失败（检查网络/代理）"; exit 1; }

# [3/5] 应用单一源
echo "[2/5] 应用单一源到发布仓库 ..."
cp "$INSTALL_SRC" "$REPO_DIR/install.sh"
cp "$README_SRC"  "$REPO_DIR/README.md"
if grep -q $'\r' "$REPO_DIR/install.sh" "$REPO_DIR/README.md" 2>/dev/null; then
  echo "[错误] 应用后仍检出 CRLF（git autocrlf 干扰），请检查 .gitattributes。"
  exit 1
fi

# [4/5] 展示 diff，请求确认
if ! in_repo git diff --quiet -- install.sh README.md; then
  echo "[3/5] 待推送变更:"
  in_repo git --no-pager diff --stat
  if [ "$AUTO" != "1" ]; then
    in_repo git --no-pager diff
    read -r -n1 -p "  确认推送到 $REPO main? (y/N) " ans < /dev/tty || ans="n"
    echo
    [[ "$ans" == "y" || "$ans" == "Y" ]] || { echo "已取消。"; exit 0; }
  fi
else
  echo "[3/5] install.sh / README 与线上一致，无需推送。"
fi

# commit + push
if ! in_repo git diff --quiet -- install.sh README.md; then
  echo "[4/5] 提交并推送 ..."
  in_repo git add install.sh README.md
  in_repo git commit -m "chore: 同步发布仓库 install.sh + README（单一源）" --quiet
  TOKEN="$("$GH" auth token)"
  if in_repo git \
      -c "http.extraheader=Authorization: Basic $(printf 'x-access-token:%s' "$TOKEN" | tr -d '\r\n' | base64 -w0)" \
      push origin main >/dev/null 2>&1; then
    echo "      已推送 main。"
  else
    echo "[错误] push 失败（检查凭据/代理）"
    exit 1
  fi
else
  echo "      无文件变更，跳过 push。"
fi

# description（幂等）
echo "      更新仓库 description ..."
DESC="$(tr -d '\r\n' < "$DESC_SRC")"
"$GH" api -X PATCH "repos/$REPO" -f description="$DESC" >/dev/null
echo "      description -> $DESC"

# [5/5] 验证回读
echo "[5/4] 验证线上 main README 首行为单一源首行 ..."
if curl -fsSL --proxy "$PROXY" "https://raw.githubusercontent.com/$REPO/main/README.md" \
     | grep -qF "$(head -n1 "$README_SRC")"; then
  echo "      ✅ 线上 README 已同步"
else
  echo "      ⚠️  未确认线上最新（可能存在延迟），请手动核对。"
fi

echo ""
echo "=== 完成: https://github.com/$REPO ==="