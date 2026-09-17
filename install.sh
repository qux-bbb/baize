#!/usr/bin/env bash
# ============================================================
# Baize (白泽) Server 远程安装脚本 — GitHub Release 源
#
# 用法:
#   纯 URL（未指定地址；脚本走到 install_server.sh 自动检测/交互选 IP）:
#     curl -fsSL https://raw.githubusercontent.com/qux-bbb/baize/main/install.sh | sudo bash
#   带地址/端口（bash -s -- 位置参数，免交互）:
#     curl -fsSL https://raw.githubusercontent.com/qux-bbb/baize/main/install.sh | sudo bash -s -- --public-addr <IP> [--port 50051] [--http-port 8080]
#
# 可选环境变量（仅"非 sudo 已 export"场景生效；sudo 默认 env_reset 会清掉，故优先用 bash -s --）:
#   BAIZE_PUBLIC_ADDR      Server 局域网地址（证书 SAN + Agent 连接地址）。
#                          给了免交互；不给则交给 install_server.sh 自动检测选 IP
#   BAIZE_VERSION          版本覆盖（默认从 GitHub latest release 自动探测）
#   BAIZE_OFFLINE_TARBALL  本地已有 tar.gz 时跳过下载（离线/内网现场）
#
# 流程: 探测版本 → 下载 tar.gz + SHA256SUMS → 校验 → mktemp 解压
#       → 调解压目录里的 install_server.sh → 安装完成
#
# 前提: 目标机 Linux + root + curl + tar + sha256sum
# ============================================================
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "[错误] Baize 需要 root 权限，请用 sudo 执行（curl ... | sudo bash）"
  exit 1
fi

# ---- 解析命令行位置参数（curl | sudo bash -s -- --public-addr ...）----
# 关键：sudo 默认 env_reset 会清空自定义环境变量（BAIZE_* 在 sudo 下传不进去），
#       而位置参数 $@ 不受其影响。因此地址/端口优先走 bash -s -- 的实参；
#       BAIZE_* 环境变量仅作为"非 sudo / 已 export"场景的替代。
declare -a EXTRA_ARGS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --public-addr) BAIZE_PUBLIC_ADDR="${2:-}"; shift 2 ;;
    --port)        EXTRA_ARGS+=(--port "${2:-}"); shift 2 ;;
    --http-port)   EXTRA_ARGS+=(--http-port "${2:-}"); shift 2 ;;
    *) echo "[错误] 未知参数: $1（支持: --public-addr --port --http-port）"; exit 1 ;;
  esac
done

# ---- 常量（建 repo / 发布时确认真实 owner/repo 后更新默认值）----
REPO="${BAIZE_REPO:-qux-bbb/baize}"
VERSION="${BAIZE_VERSION:-}"
GH_API="https://api.github.com/repos/$REPO"
GH_DL="https://github.com/$REPO/releases/download"

# ---- 版本解析：BAIZE_VERSION 优先，否则探测 latest release ----
if [[ -z "$VERSION" ]]; then
  echo "❯ 探测最新 release 版本..."
  TAG=""
  # 首选 api.github.com（拿 tag_name）；失败回退到 releases/latest 的 Location 跳转
  # （后者不占 api 配额，但同样可靠）
  TAG="$(curl -fsSL "$GH_API/releases/latest" 2>/dev/null \
      | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1 || true)"
  if [[ -z "$TAG" || "$TAG" == *'/releases/tag/'* ]]; then
    TAG="$(curl -fsLI -o /dev/null -w '%{url_effective}' "$GH_API/releases/latest" 2>/dev/null \
      | sed -n 's|.*/releases/tag/||p' || true)"
  fi
  [[ -n "$TAG" ]] || { echo "[错误] 无法探测 release 版本（网络 / repo 可见性 / BAIZE_REPO 是否正确）"; exit 1; }
  VERSION="${TAG#v}"
fi
echo "  版本: $VERSION"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

ASSET="baize-server-$VERSION-linux-amd64.tar.gz"
TARBALL="$TMP/$ASSET"

# ---- 获取发布包（离线包 或 下载+校验）----
if [[ -n "${BAIZE_OFFLINE_TARBALL:-}" ]]; then
  [[ -f "$BAIZE_OFFLINE_TARBALL" ]] || { echo "[错误] 离线包不存在: $BAIZE_OFFLINE_TARBALL"; exit 1; }
  cp "$BAIZE_OFFLINE_TARBALL" "$TARBALL"
  echo "  使用离线包: $BAIZE_OFFLINE_TARBALL"
else
  echo "❯ 下载 $GH_DL/v$VERSION/$ASSET"
  curl -fsSL -o "$TARBALL" "$GH_DL/v$VERSION/$ASSET" \
    || { echo "[错误] 下载失败（检查版本 / 网络 / BAIZE_REPO）"; exit 1; }
  if curl -fsSL -o "$TMP/SHA256SUMS.txt" "$GH_DL/v$VERSION/SHA256SUMS.txt" 2>/dev/null; then
    (cd "$TMP" && sha256sum -c --ignore-missing SHA256SUMS.txt) \
      || { echo "[错误] SHA256 校验失败，发布包疑似损坏或被篡改"; exit 1; }
    echo "  SHA256 校验通过 ✔"
  else
    echo "[警告] SHA256SUMS 不可用，跳过校验"
  fi
fi

# ---- 解压 ----
echo "❯ 解压..."
tar xzf "$TARBALL" -C "$TMP"
PKG_DIR="$(find "$TMP" -maxdepth 1 -type d -name 'baize-server-*' | head -1)"
[[ -n "$PKG_DIR" ]] || { echo "[错误] 解压后未找到 baize-server 包目录"; exit 1; }

# ---- 调 install_server.sh（地址/端口透传）----
SERVER_ARGS=()
[[ -n "${BAIZE_PUBLIC_ADDR:-}" ]] && SERVER_ARGS+=(--public-addr "$BAIZE_PUBLIC_ADDR")
SERVER_ARGS+=( "${EXTRA_ARGS[@]}" )
if [[ ${#SERVER_ARGS[@]} -eq 0 ]]; then
  echo "❯ 执行安装（未指定地址→交给 install_server.sh 自动检测）..."
else
  echo "❯ 执行安装（${SERVER_ARGS[*]}）..."
fi
"$PKG_DIR/install_server.sh" "${SERVER_ARGS[@]}"

echo ""
echo "═══════════════════════════════════════"
echo " Baize (白泽) Server 由 GitHub Release 安装完成"
echo " 后续版本升级: 重新执行同一条 curl | sudo bash 命令即可"
echo "═══════════════════════════════════════"