#!/usr/bin/env bash
# ============================================================
# Baize (白泽) Server 发布包构建脚本（本地打包先行版）
#
# 用法:
#   ./build_release.sh            # 版本号默认 0.1.0
#   ./build_release.sh 0.2.0      # 指定版本
#
# 产出（server/release/）:
#   baize-server-<版本>-linux-amd64.tar.gz   # 二进制 + install_server.sh + README.md
#   baize-server-<版本>-windows-amd64.zip    # 二进制 + README.md（Windows 安装脚本二期）
#   SHA256SUMS.txt
#
# 前提: server/ 目录内执行（脚本位于 server/deploy/），Go 1.22+ 可用
# ============================================================
set -euo pipefail

VERSION="${1:-0.1.0}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
RELEASE="$ROOT/release"
mkdir -p "$RELEASE"

echo "=================================================="
echo " Baize Server 发布包构建 (v$VERSION)"
echo "=================================================="

# [1/4] 交叉编译
echo "[1/4] 交叉编译 (linux-amd64 / windows-amd64)..."
GOOS=linux GOARCH=amd64 go build -o "$RELEASE/baize-server" ./cmd/
GOOS=windows GOARCH=amd64 go build -o "$RELEASE/baize-server.exe" ./cmd/

# [2/4] Linux tar.gz
echo "[2/4] 打包 Linux..."
PKG_LINUX="$RELEASE/pkg-linux/baize-server-$VERSION-linux-amd64"
mkdir -p "$PKG_LINUX"
cp "$RELEASE/baize-server" "$PKG_LINUX/"
cp "$ROOT/deploy/install_server.sh" "$PKG_LINUX/"
cp "$ROOT/deploy/README.md" "$PKG_LINUX/"
# 附带 baize-agent.exe：install_server.sh [3/7] 自动拷到 agent-files → Server zip 下载即用（零手动）
# 注意：Windows git-bash 下 `[[ -f "D:/..." ]]` 不认盘符路径，用相对路径判断（基于 $ROOT）
cd "$ROOT"
AGENT_EXE="../agent/target/debug/baize-agent.exe"
if [[ -f "$AGENT_EXE" ]]; then
  cp "$AGENT_EXE" "$PKG_LINUX/"
  echo "      已附带 baize-agent.exe → 解压后 install_server.sh 自动部署为 zip 下载源"
else
  echo "      [警告] 未找到 baize-agent.exe（$AGENT_EXE），发布包不含 Agent（下载页 zip 功能不可用）"
fi
chmod +x "$PKG_LINUX/install_server.sh"
# --mode=755 必须加：Windows 交叉编译产物在 NTFS 上无 POSIX 执行位，
# MSYS 的 chmod +x 对无扩展名文件（baize-server）无效，tar --mode 直接设归档权限
tar czf "$RELEASE/baize-server-$VERSION-linux-amd64.tar.gz" --mode=755 -C "$RELEASE/pkg-linux" .

# [3/4] Windows zip
echo "[3/4] 打包 Windows..."
PKG_WIN="$RELEASE/pkg-win/baize-server-$VERSION-windows-amd64"
mkdir -p "$PKG_WIN"
cp "$RELEASE/baize-server.exe" "$PKG_WIN/"
cp "$ROOT/deploy/README.md" "$PKG_WIN/"
if [[ -f "$AGENT_EXE" ]]; then
  cp "$AGENT_EXE" "$PKG_WIN/"
fi
# python3/python 任一可用（zip 打包用标准库，避免依赖 zip 命令）。
# 注意：Windows 上 command -v python3 可能命中 WindowsApps 的 stub（执行即失败），
#       必须用 "python -c import zipfile" 验证真实可用，而不是只看命令存在。
PYTHON_CMD=""
for c in python3 python; do
  if command -v "$c" >/dev/null 2>&1 && "$c" -c "import zipfile" >/dev/null 2>&1; then
    PYTHON_CMD="$c"
    break
  fi
done
if [[ -z "$PYTHON_CMD" ]]; then
  echo "[错误] 需要可用的 python3 或 python（zip 打包）"; exit 1
fi
# 相对路径打包（Windows git-bash 下 MSYS 对 python 的路径转换不可靠，cd + 相对路径最稳）
(cd "$RELEASE/pkg-win" \
  && "$PYTHON_CMD" -m zipfile -c "../baize-server-$VERSION-windows-amd64.zip" \
       "baize-server-$VERSION-windows-amd64") \
  || { echo "[错误] python zipfile 打包失败"; exit 1; }

# [4/4] 校验 + SHA256
echo "[4/4] 校验与校验和..."
tar tzf "$RELEASE/baize-server-$VERSION-linux-amd64.tar.gz" \
  | grep -q "baize-server-$VERSION-linux-amd64/install_server.sh" \
  || { echo "[错误] tar.gz 缺 install_server.sh"; exit 1; }
(cd "$RELEASE" && "$PYTHON_CMD" -m zipfile -l "baize-server-$VERSION-windows-amd64.zip") \
  | grep -q "baize-server.exe" \
  || { echo "[错误] zip 缺 baize-server.exe"; exit 1; }
(cd "$RELEASE" && sha256sum baize-server-$VERSION-* > SHA256SUMS.txt)
rm -rf "$RELEASE/pkg-linux" "$RELEASE/pkg-win"

echo ""
echo "=== 产物 (server/release/) ==="
ls -la "$RELEASE"/baize-server-$VERSION-* "$RELEASE/SHA256SUMS.txt" | awk '{print "  " $5 " B  " $9}'
echo ""
echo "=== SHA256 ==="
cat "$RELEASE/SHA256SUMS.txt"
echo ""
echo "=== Linux 部署 ==="
echo "  tar xzf baize-server-$VERSION-linux-amd64.tar.gz && cd baize-server-$VERSION-linux-amd64"
echo "  sudo ./install_server.sh --public-addr <Server的IP>"
