# Baize (白泽) EDR

> 白泽识妖，无所遁形——终端检测与响应系统

## 架构

```
┌─────────────────────┐      gRPC 双向流       ┌──────────────────────┐
│  Rust Agent         │◄──────────────────────►│  Go Server           │
│  ├─ EvtSubscribe    │     实时事件上报        │  ├─ gRPC Handler     │
│  │  (Windows 4688)  │       ╱───────╮        │  ├─ 检测引擎         │
│  ├─ auditd (Linux)  │      ╱         ╲       │  │  ├─ Sigma 规则    │
│  ├─ 文件监控        │     ╱           ╲      │  │  ├─ IOC 匹配      │
│  └─ 远控指令执行    │    ╱             ╲     │  ├─ 响应模块         │
└─────────────────────┘    ╲             ╱     │  │  ├─ 隔离/杀进程   │
                           ╲           ╱      │  │  └─ 远程脚本      │
                            ╲         ╱       │  └─ HTTP API         │
                             ╲───────╮        └────────┬─────────────┘
                                      ╲                 │
                                       ╲                │
                             ┌──────────────────────────▼────┐
                             │  Bleve 内嵌索引               │
                             │  ├─ 事件存储                  │
                             │  ├─ 告警存储                  │
                             │  └─ 主机聚合                  │
                             └───────────────────────────────┘
                                         │
                             ┌───────────▼───────────┐
                             │  React Dashboard      │
                             │  ├─ 主机列表 (在线/离线)│
                             │  ├─ 告警详情          │
                             │  ├─ 事件时间线         │
                             │  └─ 主机详情          │
                             └───────────────────────┘
```

> ⚠️ 图中 `auditd (Linux)` 为规划中（当前仅 Windows 进程采集）；文件/网络/DNS 采集已实现但需在设置页开启事件类型

## 技术栈

| 层     | 语言     | 关键依赖                              |
|--------|---------|---------------------------------------|
| Agent  | Rust    | tonic (gRPC), windows-rs (ETW)         |
| Server | Go      | gRPC, Bleve 内嵌索引, embed            |
| 存储   | Bleve   | 内嵌全文索引 + 聚合                    |
| 前端   | React   | Vite + Dashboard                       |

## 目录结构

```
Baize/
├── proto/              # Protocol Buffers 协议定义
│   ├── baize/v1/baize.proto
│   └── gen/            # 生成的 Go/Rust 代码
├── server/             # Go Server
│   ├── cmd/            # 入口
│   ├── internal/       # 内部包
│   └── web/            # React Dashboard (内嵌)
├── agent/              # Rust Agent
│   └── src/
├── scripts/            # 辅助脚本 (部署/测试)
```

## 快速开始

> **📦 正式部署（Linux/Windows Server 一键安装、Agent 下载分发、验证闭环）：见 [`server/deploy/README.md`](server/deploy/README.md)**
> 以下为开发模式（源码编译运行）。

### 1. 启动 Server（开发模式，Windows）
```bash
cd server
run.bat              # 自动编译前端 + 启动 Server
```

Server 启动后（**首次启动自动生成 TLS 证书 + 随机登录密码**，看控制台输出）：
- gRPC 端口: `50051`
- Dashboard + API: `https://localhost:8080`（自签证书，浏览器提示时选择"继续访问"）
- Agent 连接需要证书：把 `server/data/ca.crt` 拷到 Agent 的 exe 同目录，agent.conf 的 `server` 用 `https://` 且 `ca` 指向它

### 2. 启动 Agent（Windows，需管理员权限）
```cmd
REM 先启用进程创建审计（只需执行一次）
auditpol /set /subcategory:{0CCE922B-69AE-11D9-BED3-505054503030} /success:enable

REM 在 agent 编译产物目录创建 agent.conf（与 baize-agent.exe 同目录）：
REM {"server":"https://127.0.0.1:50051","ca":"ca.crt","watch_dirs":[]}
REM 并把 Server 的 data/ca.crt 拷到同目录

REM 启动 Agent（编译产物位置）
agent\target\debug\baize-agent.exe
```

### 3. 编译 Agent（改代码后）

需要 protoc（生成 gRPC 桩代码）：设置 `PROTOC` 环境变量指向你的 protoc.exe（若已加入系统 PATH 则省略）：

```cmd
cd agent
set PROTOC=<你的 protoc.exe 路径>
cargo build          # 产物: agent\target\debug\baize-agent.exe
```

或在 PowerShell 中：

```powershell
cd agent
$env:PROTOC="<你的 protoc.exe 路径>"
cargo build
```

### 4. 编译 Protobuf（改了 proto 文件后需要重新生成）
```bash
cd proto
protoc --proto_path=. \
  --go_out=gen/go --go_opt=paths=source_relative \
  --go-grpc_out=gen/go --go-grpc_opt=paths=source_relative \
  baize/v1/baize.proto
cd agent && cargo build  # Agent 侧自动重新生成
```

## 当前状态

Phase 1 & 2 已完成:
- [x] 架构设计（文档 + 架构图）
- [x] Protobuf 协议定义（18 种事件 + 4 种指令 + gRPC 服务）
- [x] Go Server（gRPC + Bleve 内嵌存储 + HTTP API）
- [x] Rust Agent（EvtSubscribe 实时进程采集 + 文件监控 + 指令响应）
- [x] React Dashboard（主机列表/在线状态 + 事件时间线 + 告警）
- [ ] ETW 进程监控（待后续研究）
- [ ] 注册表/计划任务采集
- [ ] YARA 扫描
- [ ] 溯源图

## 协议

详见 [proto/baize/v1/baize.proto](proto/baize/v1/baize.proto)

支持的事件类型:
- `ProcessCreate` — 进程创建
- `ProcessTerminate` — 进程终止
- `FileCreate / FileModify / FileDelete` — 文件变更
- `NetworkConnection` — 网络连接
- `RegistryChange` — 注册表变更 (Windows)
- `ScheduledTask` — 计划任务/自启项
- `YaraMatch` — YARA 规则命中
