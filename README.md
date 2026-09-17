# Baize (白泽)

> 白泽识妖，无所遁形

Baize 是一个轻量的 Agent–Server 采集与分析平台：Rust Agent 采集 Windows 进程事件，经 TLS gRPC 上报 Go Server（Bleve 内嵌存储 + 分析引擎），React Dashboard 展示主机/事件/告警。

## 快速安装

> 前置条件：Server 机器（Linux）+ Agent 机器（Windows 10+ x64），两机同一局域网可互通。

### ① 部署 Server（Linux 一键安装，约 5 分钟）

> 发布包在 `server/release/`（`build_release.sh` 构建，内含 Server 二进制 + Agent 分发文件 + 安装脚本）。
>
> 已发布到 GitHub 时也可一条命令装：`curl -fsSL https://raw.githubusercontent.com/qux-bbb/baize/main/install.sh | sudo bash`，脚本自动拉 latest + 校验 + 完整安装。

```bash
tar xzf baize-server-<版本>-linux-amd64.tar.gz
cd baize-server-<版本>-linux-amd64
sudo ./install_server.sh
```

- 不指定 `--public-addr` 时，脚本自动检测本机 IP 供选择（也可手动输入）；可选参数 `--port`（默认 50051）、`--http-port`（默认 8080）
- 自动完成：创建目录/运行用户 → 部署 Agent 分发文件 → 生成 systemd 服务并启动 → 打印**首次登录密码**
- ✅ 验证：`systemctl status baize-server` 为 active；`curl -sk https://127.0.0.1:8080/api/health`

### ② 安装 Agent（zip）

浏览器打开 `https://<Server IP>:8080`（自签证书警告 → 高级 → 继续访问）→ 用首次密码登录 → 改密 → **下载页生成注册 token**，复制安装命令到 Agent 机器 PowerShell 执行（脚本自动检测管理员权限并提权）。Agent 自动注册并连接 Server，状态在 Dashboard 主机页可见。

- ✅ 验证：Dashboard 主机页看到 Agent 在线
- 卸载：设置 → 应用 → Baize Agent

### ③ 验证闭环（约 2 分钟）

1. Server 配置页（⚙ 配置）开启事件类型开关（如 process）
2. Agent 机器开一个 notepad
3. Dashboard 主机页应看到 Agent 在线
4. 事件页出现 notepad 的进程创建事件

> 📦 **完整部署指南**（防火墙细节、常见问题排查、安全说明、卸载）：见 [`server/deploy/README.md`](server/deploy/README.md)

## 网络隔离（隔离主机）

主机页行尾 `⋯` → **隔离主机**（或主机详情页按钮），选择自动解除时间、填写原因后确认。隔离立即生效，主机状态列变为「已隔离」，菜单随之变为「解除隔离」。

- **隔离期间只保留**：Agent → Server 的管理通道、DNS(53)、本地回环；其余出站（含内网横向）全部阻断
- 状态由 Agent 上报（心跳 30 秒一轮校正）；隔离在 **Agent 崩溃 / 系统重启后依然保持**
- **解除**：菜单「解除隔离」一键恢复；自动解除可选 1 小时 / 24 小时 / 7 天 / 不自动解除
- ⚠️ **本地救援**（Agent 失联、Dashboard 解除不生效时，在目标机管理员终端执行）：

  ```
  "C:\Program Files\Baize\baize-agent.exe" --isolate-off
  ```

- 卸载 Agent 会先自动解除隔离，不留残留规则
- 仅支持 Windows Agent

> 运维与排障细节（残留检查等）：见 [`server/deploy/README.md`](server/deploy/README.md)

## 开发模式

- **前置：协议变更后重新生成** — 新增/修改事件字段、事件类型、指令或 RPC 时执行（生成码入库，run.bat / go build 不会自动重生成）

  ```
  cd proto && protoc --proto_path=. --go_out=gen/go --go_opt=paths=source_relative --go-grpc_out=gen/go --go-grpc_opt=paths=source_relative baize/v1/baize.proto
  ```

- **一键构建并启动**（默认入口，自动编译 Agent + 前端 + Server，Dashboard `https://localhost:8080`）

  ```
  cd server && run.bat
  ```

- **Agent 源码联调**（改 agent 代码时；cargo 自动生成 Rust 协议码，无需手动 protoc）

  ```
  cd agent && cargo build
  ```

  debug 产物连本机 `https://127.0.0.1:50051`；联调前把 `server/data/ca.crt` 拷到 exe 同目录并写 agent.conf（需 protoc）

## 技术栈

| 层     | 语言     | 关键依赖                              |
|--------|---------|---------------------------------------|
| Agent  | Rust    | tonic (gRPC), windows-rs (ETW)         |
| Server | Go      | gRPC, Bleve 内嵌索引, embed            |
| 存储   | Bleve   | 内嵌全文索引 + 聚合                    |
| 前端   | React   | Vite + Dashboard                       |

## 当前状态

Phase 1 & 2 已完成：Agent 进程采集（EvtSubscribe）+ Server 检测/响应 + Dashboard 全链路可用。

- [x] Go Server（gRPC + Bleve 存储 + HTTP API + 认证）
- [x] Rust Agent（进程采集 + 文件监控 + 指令响应 + 注册模型 + 网络隔离）
- [x] React Dashboard（主机/事件/告警 + 下载分发）
- [ ] ETW 进程监控 / 注册表与计划任务采集 / YARA 扫描 / 溯源图

## 协议

详见 [proto/baize/v1/baize.proto](proto/baize/v1/baize.proto)
