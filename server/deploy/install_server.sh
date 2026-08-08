#!/usr/bin/env bash
# ============================================================
# Baize (白泽) Server 一键安装脚本（Linux + systemd）
#
# 用法:
#   sudo ./install_server.sh --public-addr 192.168.1.10
#   sudo ./install_server.sh --public-addr 192.168.1.10 --port 50051 --http-port 8080
#
# 参数:
#   --public-addr <IP/域名>  必填。Server 局域网地址（证书 SAN + Agent 连接地址，不带 http:// 和端口）
#   --port <端口>            gRPC 端口（默认 50051）
#   --http-port <端口>       Dashboard HTTPS 端口（默认 8080）
#   --data-dir <路径>        数据目录（默认 /opt/baize/data）
#   --agent-dir <路径>       Agent 分发文件目录 .exe/.msi（默认 /opt/baize/agent-files）
#
# 幂等: 重复执行 = 覆盖 systemd 配置并重启服务（以最新参数为准）
# 前提: 脚本与 baize-server 二进制同目录；可选同目录放置 baize-agent.exe/.msi 供下载分发
# ============================================================
set -euo pipefail

PORT=50051
HTTP_PORT=8080
INSTALL_DIR=/opt/baize
DATA_DIR=/opt/baize/data
AGENT_DIR=/opt/baize/agent-files
SERVICE_NAME=baize-server
PUBLIC_ADDR=""

print_help() {
  sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --public-addr) PUBLIC_ADDR="${2:-}"; shift 2 ;;
    --port) PORT="${2:-}"; shift 2 ;;
    --http-port) HTTP_PORT="${2:-}"; shift 2 ;;
    --data-dir) DATA_DIR="${2:-}"; shift 2 ;;
    --agent-dir) AGENT_DIR="${2:-}"; shift 2 ;;
    -h|--help) print_help; exit 0 ;;
    *) echo "未知参数: $1（用 --help 查看用法）"; exit 1 ;;
  esac
done

# ---- 前置校验 ----
if [[ $EUID -ne 0 ]]; then
  echo "[错误] 需要 root 权限，请用 sudo 执行"; exit 1
fi
if [[ -z "$PUBLIC_ADDR" ]]; then
  echo "[错误] --public-addr 必填，例如: sudo ./install_server.sh --public-addr 192.168.1.10"; exit 1
fi
if [[ "$PUBLIC_ADDR" == http* || "$PUBLIC_ADDR" == *:* ]]; then
  echo "[错误] --public-addr 只填 IP 或域名（不带 http:// 和端口），如 192.168.1.10"; exit 1
fi
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BINARY="$SCRIPT_DIR/baize-server"
if [[ ! -f "$BINARY" ]]; then
  echo "[错误] 未找到 $BINARY（脚本需与 baize-server 二进制同目录）"; exit 1
fi

echo "=================================================="
echo " Baize (白泽) Server 安装"
echo " public-addr: $PUBLIC_ADDR | gRPC: $PORT | HTTPS: $HTTP_PORT"
echo " 数据目录: $DATA_DIR | Agent 分发目录: $AGENT_DIR"
echo "=================================================="

# [1/7] 创建目录
echo "[1/7] 创建目录..."
mkdir -p "$INSTALL_DIR" "$DATA_DIR" "$AGENT_DIR"

# [2/7] 创建运行用户
echo "[2/7] 创建运行用户 baize..."
if ! id -u baize >/dev/null 2>&1; then
  useradd --system --no-create-home --home-dir "$INSTALL_DIR" baize
  echo "      用户 baize 已创建"
else
  echo "      用户 baize 已存在，跳过"
fi

# [3/7] 拷贝文件
echo "[3/7] 拷贝文件..."
cp -f "$BINARY" "$INSTALL_DIR/baize-server"
if [[ -f "$SCRIPT_DIR/baize-agent.exe" ]]; then
  cp -f "$SCRIPT_DIR/baize-agent.exe" "$AGENT_DIR/"
  echo "      baize-agent.exe → $AGENT_DIR/"
fi
if [[ -f "$SCRIPT_DIR/baize-agent.msi" ]]; then
  cp -f "$SCRIPT_DIR/baize-agent.msi" "$AGENT_DIR/"
  echo "      baize-agent.msi → $AGENT_DIR/"
fi

# [4/7] 生成 systemd 单元（参数已填充，无需手改）
echo "[4/7] 生成 systemd 单元..."
mkdir -p "/etc/systemd/system"
cat > "/etc/systemd/system/$SERVICE_NAME.service" <<EOF
[Unit]
Description=Baize (白泽) Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=baize
Group=baize
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/baize-server \\
  --data-dir $DATA_DIR \\
  --public-addr http://$PUBLIC_ADDR:$PORT \\
  --agent-binary $AGENT_DIR/baize-agent.exe \\
  --agent-installer $AGENT_DIR/baize-agent.msi
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
ProtectHome=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=$DATA_DIR

[Install]
WantedBy=multi-user.target
EOF
echo "      /etc/systemd/system/$SERVICE_NAME.service"

# [5/7] 权限
echo "[5/7] 设置权限..."
chown -R baize:baize "$INSTALL_DIR"
chmod 755 "$INSTALL_DIR/baize-server"

# [6/7] 注册并启动
echo "[6/7] 注册并启动服务..."
systemctl daemon-reload
systemctl enable --now "$SERVICE_NAME"

# [7/7] 健康检查 + 提取首次密码
echo "[7/7] 等待服务就绪..."
READY=0
for _ in $(seq 1 15); do
  if curl -skf "https://127.0.0.1:$HTTP_PORT/api/health" >/dev/null 2>&1; then
    READY=1
    break
  fi
  sleep 1
done

if [[ "$READY" == "1" ]]; then
  echo "      服务已就绪 ✔"
else
  echo "      [警告] 服务未在 15 秒内就绪，请检查: journalctl -u $SERVICE_NAME -n 50"
fi

PASSWORD=$(journalctl -u "$SERVICE_NAME" --since "3 min ago" --no-pager 2>/dev/null \
  | grep -oP '密码:\s*\K\S+' | tail -1 || true)

echo ""
echo "══════════════════════════════════════════════════"
echo "  Baize (白泽) Server 安装完成"
echo "  Dashboard:  https://$PUBLIC_ADDR:$HTTP_PORT"
echo "  gRPC:       $PUBLIC_ADDR:$PORT"
if [[ -n "$PASSWORD" ]]; then
  echo "  首次密码:   $PASSWORD"
else
  echo "  首次密码:   （未提取到，运行: journalctl -u $SERVICE_NAME -n 30 | grep 密码）"
fi
echo "  数据目录:   $DATA_DIR"
echo "──────────────────────────────────────────────────"
echo "  状态检查:   systemctl status $SERVICE_NAME"
echo "  日志跟踪:   journalctl -u $SERVICE_NAME -f"
echo "  防火墙（若启用）:"
echo "    firewall-cmd --permanent --add-port=$PORT/tcp --add-port=$HTTP_PORT/tcp && firewall-cmd --reload"
echo "    或 iptables 放行 $PORT/$HTTP_PORT 入站"
echo "  卸载:       systemctl disable --now $SERVICE_NAME && rm -rf $INSTALL_DIR /etc/systemd/system/$SERVICE_NAME.service && systemctl daemon-reload"
echo "══════════════════════════════════════════════════"
