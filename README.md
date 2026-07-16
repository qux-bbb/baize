# Baize (白泽) EDR

> 白泽识妖，无所遁形——终端检测与响应系统

## 架构

```
┌─────────────────────┐      gRPC 双向流       ┌──────────────────────┐
│  Rust Agent         │◄──────────────────────►│  Go Server           │
│  ├─ ETW (Windows)   │     实时事件上报        │  ├─ gRPC Handler     │
│  ├─ auditd (Linux)  │       ╱───────╮        │  ├─ 检测引擎         │
│  ├─ YARA 扫描       │      ╱         ╲       │  │  ├─ Sigma 规则    │
│  └─ 远控指令执行    │     ╱           ╲      │  │  ├─ IOC 匹配      │
└─────────────────────┘    ╱             ╲     │  ├─ 响应模块         │
                           ╲             ╱     │  │  ├─ 隔离/杀进程   │
                            ╲           ╱      │  │  └─ 远程脚本      │
                             ╲         ╱       │  └─ HTTP API         │
                              ╲───────╮        └────────┬─────────────┘
                                       ╲                 │
                                        ╲                │
                              ┌──────────────────────────▼────┐
                              │  Elasticsearch               │
                              │  ├─ 遥测索引 (baize-events-*) │
                              │  ├─ 告警索引 (baize-alerts-*) │
                              │  └─ 主机状态 (baize-hosts-*)  │
                              └───────────────────────────────┘
                                          │
                              ┌───────────▼───────────┐
                              │  React Dashboard      │
                              │  ├─ 主机列表/状态     │
                              │  ├─ 告警详情          │
                              │  ├─ 事件时间线/溯源图  │
                              │  └─ 规则管理/狩猎查询  │
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
cd D:\files\projects\Baize
go run ./server/cmd/
```

### 2. 启动 Agent
```bash
# 新开一个终端
cd D:\files\projects\Baize\agent
cargo run
```

Agent 默认连接 `127.0.0.1:50051`，Server 不在本机时用 `--server` 指定：
```bash
cargo run -- --server http://192.168.x.x:50051
```

### 3. 打开 Dashboard
浏览器访问 http://localhost:8080

### 4. 编译 Protobuf（改了 proto 文件后需要重新生成）
```bash
cd proto
protoc --proto_path=. \
  --go_out=gen/go --go_opt=paths=source_relative \
  --go-grpc_out=gen/go --go-grpc_opt=paths=source_relative \
  baize/v1/baize.proto
```

## 当前状态

Phase 1 已完成:
- [x] 架构设计（文档 + 架构图）
- [x] Protobuf 协议定义（18 种事件 + 4 种指令 + gRPC 服务）
- [x] Go Server 骨架（gRPC 服务端，双向流实现）
- [x] 模拟 Agent 端到端测试通过
- [ ] Rust Agent（待开发）
- [ ] 检测引擎 + Storage + Dashboard（待开发）

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
