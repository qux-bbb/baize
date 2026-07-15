package main

import (
	"context"
	"fmt"
	"log"
	"net"

	"google.golang.org/grpc"

	pb "github.com/qux-bbb/baize/proto/gen/go/baize/v1"
)

type baizeServer struct {
	pb.UnimplementedBaizeServiceServer
}

// Connect 双向流 RPC — Agent 持续上传事件，Server 下发指令
func (s *baizeServer) Connect(stream pb.BaizeService_ConnectServer) error {
	log.Println("[Connect] 新的 Agent 连接已建立")

	for {
		event, err := stream.Recv()
		if err != nil {
			log.Printf("[Connect] 接收结束: %v", err)
			return err
		}

		agentID := event.GetAgentInfo().GetAgentId()
		hostname := event.GetAgentInfo().GetHostname()
		seq := event.GetSequenceId()

		switch e := event.GetEventType().(type) {
		case *pb.Event_ProcessCreate:
			log.Printf("[Event] Agent=%s (%s) seq=%d | 进程创建 PID=%d %s",
				agentID, hostname, seq,
				e.ProcessCreate.GetPid(), e.ProcessCreate.GetImagePath())

		case *pb.Event_NetworkConnection:
			log.Printf("[Event] Agent=%s (%s) seq=%d | 网络连接 %s:%d → %s:%d",
				agentID, hostname, seq,
				e.NetworkConnection.GetLocalIp(), e.NetworkConnection.GetLocalPort(),
				e.NetworkConnection.GetRemoteIp(), e.NetworkConnection.GetRemotePort())

		case *pb.Event_FileCreate:
			log.Printf("[Event] Agent=%s (%s) seq=%d | 文件创建 %s",
				agentID, hostname, seq,
				e.FileCreate.GetFilePath())

		case *pb.Event_FileModify:
			log.Printf("[Event] Agent=%s (%s) seq=%d | 文件修改 %s",
				agentID, hostname, seq,
				e.FileModify.GetFilePath())

		case *pb.Event_FileDelete:
			log.Printf("[Event] Agent=%s (%s) seq=%d | 文件删除 %s",
				agentID, hostname, seq,
				e.FileDelete.GetFilePath())

		case *pb.Event_ProcessTerminate:
			log.Printf("[Event] Agent=%s (%s) seq=%d | 进程终止 PID=%d",
				agentID, hostname, seq,
				e.ProcessTerminate.GetPid())

		case *pb.Event_RegistryChange:
			log.Printf("[Event] Agent=%s (%s) seq=%d | 注册表变更 %s",
				agentID, hostname, seq,
				e.RegistryChange.GetKeyPath())

		case *pb.Event_ScheduledTask:
			log.Printf("[Event] Agent=%s (%s) seq=%d | 计划任务变更 %s",
				agentID, hostname, seq,
				e.ScheduledTask.GetTaskName())

		case *pb.Event_YaraMatch:
			log.Printf("[Event] Agent=%s (%s) seq=%d | YARA 命中 规则=%s 目标=%s",
				agentID, hostname, seq,
				e.YaraMatch.GetRuleName(), e.YaraMatch.GetTargetPath())

		default:
			log.Printf("[Event] Agent=%s (%s) seq=%d | 未知事件类型", agentID, hostname, seq)
		}
	}
}

func (s *baizeServer) Heartbeat(ctx context.Context, info *pb.AgentInfo) (*pb.Empty, error) {
	log.Printf("[Heartbeat] Agent=%s (%s) OS=%s v%s",
		info.GetAgentId(), info.GetHostname(),
		info.GetOsType(), info.GetOsVersion())
	return &pb.Empty{}, nil
}

func (s *baizeServer) ReportCommandResult(ctx context.Context, result *pb.CommandResult) (*pb.Empty, error) {
	status := "成功"
	if !result.GetSuccess() {
		status = "失败: " + result.GetErrorMessage()
	}
	log.Printf("[CommandResult] Command=%s %s", result.GetCommandId(), status)
	return &pb.Empty{}, nil
}

func main() {
	port := 50051
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Fatalf("监听端口 %d 失败: %v", port, err)
	}

	s := grpc.NewServer()
	pb.RegisterBaizeServiceServer(s, &baizeServer{})

	log.Printf("═══════════════════════════════════════")
	log.Printf("  Baize (白泽) EDR Server")
	log.Printf("  gRPC 端口: %d", port)
	log.Printf("  等待 Agent 连接...")
	log.Printf("═══════════════════════════════════════")

	if err := s.Serve(lis); err != nil {
		log.Fatalf("gRPC 服务启动失败: %v", err)
	}
}
