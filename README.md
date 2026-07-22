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

## 技术栈

| 层     | 语言     | 关键依赖                              |
|--------|---------|---------------------------------------|
| Agent  | Rust    | tonic (gRPC), windows-rs (ETW), yara  |
| Server | Go      | gRPC, Elasticsearch Go client, embed   |
| 存储   | Elasticsearch | 全文检索 + 聚合                     |
| 前端   | React   | Material UI, 时间线/图可视化           |

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
└── docs/               # 文档
```

## 快速开始

### 1. 启动 Server
```bash
cd server
run.bat              # 自动编译前端 + 启动 Server（推荐）
# 或分开执行:
# cd web && npm run build && cd .. && go run ./cmd/
```

Server 启动后:
- gRPC 端口: `50051`
- Dashboard + API: `http://localhost:8080`

### 2. 启动 Agent（Windows，需管理员权限）
```cmd
REM 先启用进程创建审计（只需执行一次）
auditpol /set /subcategory:{0CCE922B-69AE-11D9-BED3-505054503030} /success:enable

REM 启动 Agent
cd D:\files\projects\Baize\agent\target\debug
baize-agent.exe
```

### 3. 编译 Agent（改代码后）

在 **cmd.exe** 中执行（需要设置 PROTOC 环境变量）：

```cmd
cd D:\files\projects\Baize\agent
set PROTOC=C:\Users\q\protoc\bin\protoc.exe
cargo build
```

或在 **PowerShell** 中：

```powershell
cd D:\files\projects\Baize\agent
$env:PROTOC="C:\Users\q\protoc\bin\protoc.exe"
cargo build
```

> 注意：Agent 编译时需要 protoc 生成 gRPC 桩代码。`PROTOC` 环境变量指向 protoc.exe 路径。如果已设置到系统 PATH 则可省略。

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
