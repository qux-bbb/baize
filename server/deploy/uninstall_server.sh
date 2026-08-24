#!/usr/bin/env bash
# ============================================================
# Baize (白泽) Server 卸载脚本（Linux + systemd）
#
# 用法:
#   sudo ./uninstall_server.sh             # 交互确认，保留数据目录
#   sudo ./uninstall_server.sh --purge     # 彻底卸载（连同数据目录、运行用户）
#   sudo ./uninstall_server.sh --yes       # 跳过交互确认（自动化/脚本化）
#
# 参数:
#   --purge        同时删除数据目录与运行用户 baize（不可恢复）
#   --yes, -y      跳过交互确认（对标 Agent 卸载的 /S 静默模式）
#   --data-dir     数据目录（默认 /opt/baize/data，须与安装时一致）
#   --agent-dir    Agent 分发目录（默认 /opt/baize/agent-files）
#
# 幂等: 重复执行无副作用；未安装时提示后退出
# 前提: 需要 root 权限
# ============================================================
set -euo pipefail

INSTALL_DIR=/opt/baize
DATA_DIR=/opt/baize/data
AGENT_DIR=/opt/baize/agent-files
SERVICE_NAME=baize-server
PURGE=0
YES=0
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT_SELF="$SCRIPT_DIR/$(basename "${BASH_SOURCE[0]}")"
# 脚本是否随装落盘在 INSTALL_DIR 内（卸载入口随安装常驻 /opt/baize）：
# 决定 --purge 时是否需避免 rm -rf 把运行中的脚本一并删除。
if [[ "$SCRIPT_DIR" == "$INSTALL_DIR" ]]; then
  INSIDE_INSTALL=1
else
  INSIDE_INSTALL=0
fi

print_help() {
  sed -n '2,18p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --purge) PURGE=1; shift ;;
    --yes|-y) YES=1; shift ;;
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

# ---- 已安装检测（幂等：未安装直接退出，不报错）----
INSTALLED=0
if [[ -f "/etc/systemd/system/$SERVICE_NAME.service" ]] \
  || [[ -d "$INSTALL_DIR" ]] || [[ -d "$AGENT_DIR" ]] || [[ -d "$DATA_DIR" ]]; then
  INSTALLED=1
fi
if [[ "$INSTALLED" == "0" ]]; then
  echo "[提示] 未检测到 Baize Server（$SERVICE_NAME 服务 / $INSTALL_DIR），无需卸载"
  exit 0
fi

echo "=================================================="
echo " Baize (白泽) Server 卸载"
echo "=================================================="
echo "本操作将:"
echo "  1. 停止并禁用 $SERVICE_NAME 服务"
echo "  2. 删除 systemd 单元与程序文件（$INSTALL_DIR）"
if [[ "$PURGE" == "1" ]]; then
  echo "  3. 删除数据目录 $DATA_DIR 与运行用户 baize（⚠ 不可恢复）"
else
  echo "  3. 保留数据目录 $DATA_DIR（如需清理请用 --purge）"
fi
echo ""
if [[ "$YES" == "0" ]]; then
  read -r -p "确认卸载？(y/N): " CONFIRM
  if [[ "$CONFIRM" != "y" && "$CONFIRM" != "Y" ]]; then
    echo "已取消。"
    exit 0
  fi
fi

# ---- [1] 停止并禁用服务（存在才停；停后轮询确认，最多 30 秒）----
if [[ -f "/etc/systemd/system/$SERVICE_NAME.service" ]]; then
  echo "[1/4] 停止并禁用服务..."
  systemctl disable --now "$SERVICE_NAME" >/dev/null 2>&1 || true
  WAIT=0
  while systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null && (( WAIT < 15 )); do
    sleep 2
    WAIT=$((WAIT+1))
  done
  if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
    echo "      [警告] 服务 30 秒内未停止（可能有进程残留），继续后续步骤"
  else
    echo "      [OK] 服务已停止"
  fi
else
  echo "[1/4] 停止并禁用服务..."
  echo "      [提示] 服务未安装，跳过"
fi

# ---- [2] 删除 systemd 单元 ----
echo "[2/4] 删除 systemd 单元..."
if [[ -f "/etc/systemd/system/$SERVICE_NAME.service" ]]; then
  rm -f "/etc/systemd/system/$SERVICE_NAME.service"
  systemctl daemon-reload
  echo "      [OK] 已删除 /etc/systemd/system/$SERVICE_NAME.service"
else
  echo "      [提示] 单元文件不存在，跳过"
fi

# ---- [3] 删除程序文件（数据目录按 purge 决定去留）----
echo "[3/4] 删除程序文件..."
if [[ "$PURGE" == "1" ]]; then
  if [[ "$INSIDE_INSTALL" == "1" ]]; then
    # 脚本自身在 $INSTALL_DIR 内：先删内容与数据，最后单独删脚本自身、
    # 用 rmdir 兜底删空目录——避免 rm -rf 整目录把运行中的脚本一并删除。
    rm -f "$INSTALL_DIR/baize-server"
    rm -rf "$AGENT_DIR" "$DATA_DIR"
    rm -f "$SCRIPT_SELF"
    rmdir "$INSTALL_DIR" 2>/dev/null || true
    echo "      [OK] 已删除 $INSTALL_DIR（含数据目录与卸载脚本）"
  else
    rm -rf "$INSTALL_DIR"
    echo "      [OK] 已删除 $INSTALL_DIR（含数据目录）"
  fi
else
  rm -f "$INSTALL_DIR/baize-server"
  rm -rf "$AGENT_DIR"
  echo "      [OK] 已删除程序文件与 Agent 分发目录 $AGENT_DIR"
  # 数据目录保留；若 INSTALL_DIR 只剩空壳（数据目录外置时）则一并删除
  rmdir "$INSTALL_DIR" 2>/dev/null || true
  echo "      [保留] 数据目录 $DATA_DIR（如需删除: sudo ./uninstall_server.sh --purge）"
fi

# ---- [4] 运行用户（仅 --purge）----
if [[ "$PURGE" == "1" ]]; then
  echo "[4/4] 删除运行用户 baize..."
  if id -u baize >/dev/null 2>&1; then
    if userdel baize 2>/dev/null; then
      echo "      [OK] 运行用户 baize 已删除"
    else
      echo "      [警告] 删除用户 baize 失败（可能有进程占用），可稍后手动执行: userdel baize"
    fi
  else
    echo "      [提示] 用户 baize 不存在，跳过"
  fi
fi

echo ""
echo "══════════════════════════════════════════════════"
echo "  Baize (白泽) Server 卸载完成"
if [[ "$PURGE" == "1" ]]; then
  echo "  已彻底移除（服务 / 单元 / 程序 / 数据 / 用户）"
else
  echo "  服务与程序已移除，数据保留: $DATA_DIR"
fi
echo "──────────────────────────────────────────────────"
echo "  验证: systemctl status $SERVICE_NAME（应显示 not-found）"
echo "  防火墙（若安装时手动放行过端口，按实际端口删除规则）:"
echo "    firewall-cmd --permanent --remove-port=50051/tcp --remove-port=8080/tcp && firewall-cmd --reload"
echo "    或 iptables 删除对应入站规则"
echo "══════════════════════════════════════════════════"
