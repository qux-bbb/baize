package main
import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"google.golang.org/grpc"

	pb "github.com/qux-bbb/baize/proto/gen/go/baize/v1"
	"github.com/qux-bbb/baize/server/internal/api"
	"github.com/qux-bbb/baize/server/internal/engine"
	"github.com/qux-bbb/baize/server/internal/store"
)

//go:embed web/* web/assets/*
var webFS embed.FS
type baizeServer struct {
	pb.UnimplementedBaizeServiceServer
	es       *store.Store
	engine   *engine.Engine
	cmdBus   *engine.CommandBus
	cfg      *engine.ConfigManager
}

func (s *baizeServer) AgentStream(stream pb.BaizeService_AgentStreamServer) error {
	log.Println("[Connect] 新的 Agent 连接已建立")

	// 等待第一个事件获取 agent_id
	first, err := stream.Recv()
	if err != nil {
		log.Printf("[Connect] 接收首个事件失败: %v", err)
		return err
	}

	agentID := first.GetAgentInfo().GetAgentId()
	hostname := first.GetAgentInfo().GetHostname()
	log.Printf("[Connect] Agent %s (%s) 已认证", agentID, hostname)

	// 注册指令通道
	cmdChan := make(chan *pb.Command, 64)
	s.cmdBus.Register(agentID, func(cmd *pb.Command) error {
		select {
		case cmdChan <- cmd:
			return nil
		default:
			log.Printf("[CmdBus] Agent %s 指令队列满，丢弃", agentID)
			return nil
		}
	})
	defer s.cmdBus.Unregister(agentID)

	// 下发文件监控配置（主机级优先，无则用全局）
	dirs := s.cfg.GetWatchDirs(agentID)
	cmdChan <- &pb.Command{
		CommandId: fmt.Sprintf("filewatch-init-%d", time.Now().Unix()),
		IssuedAtNs: uint64(time.Now().UnixNano()),
		CommandType: &pb.Command_ConfigureFileWatch{
			ConfigureFileWatch: &pb.ConfigureFileWatchCommand{
				WatchDirs: dirs,
				Reason:    "server config",
			},
		},
	}

	// 下发事件类型配置
	effectiveET := s.cfg.GetEffectiveEventTypes(agentID)
	cmdChan <- engine.BuildConfigureEventTypesCmd(effectiveET)

	// 后台协程
	go func() {
		for cmd := range cmdChan {
			if err := stream.Send(cmd); err != nil {
				log.Printf("[CmdBus] Agent %s 发送指令失败: %v", agentID, err)
				return
			}
			log.Printf("[CmdBus] Agent %s 已下发指令: %s", agentID, cmd.GetCommandId())
		}
	}()

	// 处理首个事件
	eventCount := 1
	processEvent(first, s.es, s.engine)

	for {
		event, err := stream.Recv()
		if err != nil {
			// 检查是否是客户端主动断开
			if ctxErr := stream.Context().Err(); ctxErr != nil {
				log.Printf("[Connect] %s 已断开连接 (%d 事件)", agentID, eventCount)
			} else {
				log.Printf("[Connect] %s 接收结束 (%d 事件): %v", agentID, eventCount, err)
			}
			close(cmdChan)
			return err
		}
		eventCount++
		processEvent(event, s.es, s.engine)
	}
}

func processEvent(event *pb.Event, es *store.Store, eng *engine.Engine) {
	agentID := event.GetAgentInfo().GetAgentId()
	hostname := event.GetAgentInfo().GetHostname()
	seq := event.GetSequenceId()

	// 1. 写入 ES
	if es != nil {
		if err := es.WriteEvent(event); err != nil {
			log.Printf("[ES] 写入失败: %v", err)
		}
		// 1b. 伴随更新主机实体（节流 10s）
		if err := es.UpsertHostFromEvent(event); err != nil {
			log.Printf("[Host] 主机文档更新失败: %v", err)
		}
	}

	// 2. 检测引擎
	if eng != nil && eng.IsLoaded() {
		fields := engine.EventToFieldMap(event)
		rawJSON, _ := json.Marshal(fields)
		eng.EvalAndAlert(fields, string(rawJSON))
	}

	// 3. 控制台
	printEvent(event, agentID, hostname, seq)
}

func printEvent(event *pb.Event, agentID, hostname string, seq uint64) {
	switch e := event.GetEventType().(type) {
	case *pb.Event_ProcessCreate:
		log.Printf("[Event] Agent=%s (%s) seq=%d | 进程创建 PID=%d %s",
			agentID, hostname, seq, e.ProcessCreate.GetPid(), e.ProcessCreate.GetImagePath())
	case *pb.Event_NetworkConnection:
		log.Printf("[Event] Agent=%s (%s) seq=%d | 网络连接 %s:%d → %s:%d",
			agentID, hostname, seq,
			e.NetworkConnection.GetLocalIp(), e.NetworkConnection.GetLocalPort(),
			e.NetworkConnection.GetRemoteIp(), e.NetworkConnection.GetRemotePort())
	case *pb.Event_FileCreate:
		log.Printf("[Event] Agent=%s (%s) seq=%d | 文件创建 %s", agentID, hostname, seq, e.FileCreate.GetFilePath())
	case *pb.Event_FileModify:
		log.Printf("[Event] Agent=%s (%s) seq=%d | 文件修改 %s", agentID, hostname, seq, e.FileModify.GetFilePath())
	case *pb.Event_FileDelete:
		log.Printf("[Event] Agent=%s (%s) seq=%d | 文件删除 %s", agentID, hostname, seq, e.FileDelete.GetFilePath())
	case *pb.Event_ProcessTerminate:
		log.Printf("[Event] Agent=%s (%s) seq=%d | 进程终止 PID=%d", agentID, hostname, seq, e.ProcessTerminate.GetPid())
	case *pb.Event_RegistryChange:
		log.Printf("[Event] Agent=%s (%s) seq=%d | 注册表变更 %s", agentID, hostname, seq, e.RegistryChange.GetKeyPath())
	case *pb.Event_ScheduledTask:
		log.Printf("[Event] Agent=%s (%s) seq=%d | 计划任务变更 %s", agentID, hostname, seq, e.ScheduledTask.GetTaskName())
	case *pb.Event_YaraMatch:
		log.Printf("[Event] Agent=%s (%s) seq=%d | YARA 命中 规则=%s 目标=%s",
			agentID, hostname, seq, e.YaraMatch.GetRuleName(), e.YaraMatch.GetTargetPath())
	case *pb.Event_DnsQuery:
		log.Printf("[Event] Agent=%s (%s) seq=%d | DNS 查询 %s → %s",
			agentID, hostname, seq, e.DnsQuery.GetProcessName(), e.DnsQuery.GetQueryName())
	default:
		log.Printf("[Event] Agent=%s (%s) seq=%d | 未知事件", agentID, hostname, seq)
	}
}

func (s *baizeServer) Heartbeat(ctx context.Context, info *pb.AgentInfo) (*pb.Empty, error) {
	log.Printf("[Heartbeat] Agent=%s (%s) OS=%s v%s",
		info.GetAgentId(), info.GetHostname(), info.GetOsType(), info.GetOsVersion())
	// 注册/心跳：写入（upsert）主机实体文档
	if s.es != nil {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if err := s.es.WriteHost(info, now); err != nil {
			log.Printf("[Heartbeat] 主机文档写入失败: %v", err)
		}
	}
	return &pb.Empty{}, nil
}

func (s *baizeServer) ReportCommandResult(ctx context.Context, result *pb.CommandResult) (*pb.Empty, error) {
	status := "done"
	if !result.GetSuccess() {
		status = "err: " + result.GetErrorMessage()
	}
	log.Printf("[CommandResult] Command=%s %s", result.GetCommandId(), status)
	if s.cmdBus != nil {
		s.cmdBus.HandleResult(result)
	}
	return &pb.Empty{}, nil
}

func initEngine(esStore *store.Store) *engine.Engine {
	eng := engine.New(esStore)

	// 将内嵌规则写入临时目录
	tmpDir, err := os.MkdirTemp("", "baize-rules-*")
	if err != nil {
		log.Printf("[Engine] 创建临时目录失败: %v", err)
		return eng
	}

	ruleNames, err := engine.BuiltInRuleNames()
	if err != nil {
		log.Printf("[Engine] 读取内嵌规则失败: %v", err)
		return eng
	}

	for _, name := range ruleNames {
		data, err := engine.ReadBuiltInRule(name)
		if err != nil {
			log.Printf("[Engine] 读取规则 %s 失败: %v", name, err)
			continue
		}
		dest := filepath.Join(tmpDir, name)
		if err := os.WriteFile(dest, data, 0644); err != nil {
			log.Printf("[Engine] 写入规则文件 %s 失败: %v", dest, err)
			continue
		}
	}

	if err := eng.LoadRules(tmpDir); err != nil {
		log.Printf("[Engine] 加载规则失败: %v", err)
	}
	return eng
}

func main() {
	port := flag.Int("port", 50051, "gRPC 端口")
	dataDir := flag.String("data-dir", "./data", "数据目录（Bleve 索引、日志、auth.json）")
	httpPort := flag.Int("http-port", 8080, "HTTP API / Dashboard 端口")
	publicAddr := flag.String("public-addr", "", "Server 对外 gRPC 地址（注入 agent.conf），如 http://10.0.0.1:50051；留空则用请求 Host 推断")
	agentBinary := flag.String("agent-binary", "", "Agent 可执行文件路径（下载打包用），如 C:\\Baize\\baize-agent.exe")
	agentInstaller := flag.String("agent-installer", "", "预构建的 Agent MSI 安装包路径（下载用），如 C:\\Baize\\baize-agent.msi")
	flag.Parse()

	// 日志写入文件（与 stderr 同时输出）
	logDir := *dataDir
	os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, "server.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, logFile))
		log.Printf("[Log] 日志已写入: %s", logPath)
		defer logFile.Close()
	}

	// 初始化 Bleve 存储
	log.Printf("[Store] 初始化 Bleve 索引...")
	storePath := filepath.Join(*dataDir, "baize.bleve")
	bleveStore, err := store.New(storePath)
	if err != nil {
		log.Printf("[Store] 初始化失败: %v（不影响 Server 启动）", err)
	} else {
		log.Printf("[Store] Bleve 索引就绪: %s", storePath)
		defer bleveStore.Close()
	}

	// 初始化检测引擎
	eng := initEngine(bleveStore)

	// 后台重建主机实体（从事件索引恢复存量主机，幂等）
	if bleveStore != nil {
		go func() {
			n, err := bleveStore.RebuildHosts()
			if err != nil {
				log.Printf("[Host] 主机重建失败: %v", err)
			} else {
				log.Printf("[Host] 主机重建完成: %d 台", n)
			}
		}()
	}

	// 初始化认证
	authPath := filepath.Join(*dataDir, "auth.json")
	authManager := api.NewAuthManager(authPath)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatalf("监听端口 %d 失败: %v", *port, err)
	}

	s := grpc.NewServer()
	cmdBus := engine.NewCommandBus()
	cfg := engine.NewConfigManager()
	pb.RegisterBaizeServiceServer(s, &baizeServer{es: bleveStore, engine: eng, cmdBus: cmdBus, cfg: cfg})

	// 启动 HTTP API + Dashboard Server
	{
		mux := http.NewServeMux()
		if bleveStore != nil {
			apiHandler := api.New(bleveStore, cmdBus, cfg, authManager, *agentBinary, *publicAddr, *agentInstaller)
			mux.HandleFunc("POST /api/login", apiHandler.Login)
			mux.HandleFunc("POST /api/change-password", apiHandler.ChangePassword)
			mux.HandleFunc("POST /api/logout", apiHandler.Logout)
			mux.HandleFunc("GET /api/hosts", apiHandler.Hosts)
			mux.HandleFunc("GET /api/alerts", apiHandler.Alerts)
			mux.HandleFunc("GET /api/alert", apiHandler.AlertDetail)
			mux.HandleFunc("GET /api/agent/info", apiHandler.AgentInfo)
			mux.HandleFunc("GET /api/agent/package", apiHandler.AgentPackage)
			mux.HandleFunc("GET /api/agent/installer", apiHandler.AgentInstaller)
			mux.HandleFunc("GET /api/config/file-watch", apiHandler.ConfigFileWatch)
			mux.HandleFunc("POST /api/config/file-watch", apiHandler.ConfigFileWatch)
			mux.HandleFunc("GET /api/config/event-types", apiHandler.ConfigEventTypes)
			mux.HandleFunc("POST /api/config/event-types", apiHandler.ConfigEventTypes)
			mux.HandleFunc("GET /api/events", apiHandler.Events)
			mux.HandleFunc("GET /api/export/events", apiHandler.ExportEvents)
			mux.HandleFunc("GET /api/export/alerts", apiHandler.ExportAlerts)
			mux.HandleFunc("GET /api/systeminfo", apiHandler.SystemInfo)
		}
		mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		})
		// 手动验证指令下发
		mux.HandleFunc("GET /api/cmd/isolate", func(w http.ResponseWriter, r *http.Request) {
			agentID := r.URL.Query().Get("agent_id")
			if agentID == "" {
				http.Error(w, "missing agent_id", 400)
				return
			}
			cmdBus.IsolateAgent(agentID)
			log.Printf("[API] 手动隔离: %s", agentID)
			json.NewEncoder(w).Encode(map[string]string{"status": "sent", "agent": agentID})
		})
		// 内嵌 Dashboard 静态文件
		webSub, _ := fs.Sub(webFS, "web")
		fileSrv := http.FileServer(http.FS(webSub))
		mux.Handle("GET /assets/", fileSrv)
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			data, _ := webFS.ReadFile("web/index.html")
			w.Header().Set("Content-Type", "text/html")
			w.Write(data)
		})
		httpSrv := &http.Server{
			Addr:    fmt.Sprintf(":%d", *httpPort),
			Handler: api.CORSMiddleware(api.AuthMiddleware(authManager)(mux)),
		}
		go func() {
			log.Printf("[HTTP] Dashboard + API: http://localhost:%d", *httpPort)
			if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("[HTTP] 错误: %v", err)
			}
		}()
		defer httpSrv.Shutdown(context.Background())
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("收到退出信号，正在关闭...")
		s.Stop()
	}()

	log.Printf("═══════════════════════════════════════════")
	log.Printf("  Baize (白泽) EDR Server")
	log.Printf("  gRPC 端口: %d", *port)
	log.Printf("  存储: %s", storePath)
	log.Printf("  检测引擎:  %d 条规则已加载", eng.RuleCount())
	log.Printf("  等待 Agent 连接...")
	log.Printf("═══════════════════════════════════════════")

	if err := s.Serve(lis); err != nil {
		log.Fatalf("gRPC 服务启动失败: %v", err)
	}
}
