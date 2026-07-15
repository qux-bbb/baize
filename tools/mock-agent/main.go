// 测试用模拟 Agent — 连接到 Baize Server 并发送模拟事件
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/qux-bbb/baize/proto/gen/go/baize/v1"
)

var (
	serverAddr = flag.String("server", "localhost:50051", "Baize Server 地址")
	agentID    = flag.String("agent-id", "test-agent-001", "Agent ID")
	hostname   = flag.String("hostname", "WIN-TEST-01", "主机名")
	interval   = flag.Duration("interval", 3*time.Second, "事件发送间隔")
)

func main() {
	flag.Parse()

	conn, err := grpc.NewClient(*serverAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("连接 Server 失败: %v", err)
	}
	defer conn.Close()

	client := pb.NewBaizeServiceClient(conn)
	ctx := context.Background()

	stream, err := client.AgentStream(ctx)
	if err != nil {
		log.Fatalf("建立 Connect 流失败: %v", err)
	}

	log.Printf("=== 模拟 Agent %s (%s) 已连接 ===", *agentID, *hostname)

	// 后台接收 Server 下发的指令
	go func() {
		for {
			cmd, err := stream.Recv()
			if err != nil {
				log.Printf("[Recv] 流关闭: %v", err)
				return
			}
			log.Printf("[Recv] 收到指令: %s (ID=%s)", cmd.String(), cmd.GetCommandId())
		}
	}()

	seq := uint64(0)
	for {
		seq++
		event := generateEvent(seq)
		if err := stream.Send(event); err != nil {
			log.Printf("[Send] 发送失败: %v", err)
			return
		}
		log.Printf("[Send] seq=%d %s", seq, eventSummary(event))
		time.Sleep(*interval)
	}
}

func generateEvent(seq uint64) *pb.Event {
	now := uint64(time.Now().UnixNano())
	event := &pb.Event{
		AgentInfo: &pb.AgentInfo{
			AgentId:      *agentID,
			Hostname:     *hostname,
			OsType:       "windows",
			OsVersion:    "10.0.19045",
			AgentVersion: "0.0.1",
			IpAddresses:  []string{"192.168.1.100"},
		},
		SequenceId: seq,
	}

	pid := uint64(1000 + seq)

	switch seq % 8 {
	case 0:
		event.EventType = &pb.Event_ProcessCreate{
			ProcessCreate: &pb.ProcessCreateEvent{
				Pid: pid, ParentPid: 888,
				CommandLine: fmt.Sprintf("C:\\Windows\\System32\\notepad.exe C:\\temp\\doc%d.txt", seq),
				ImagePath:   "C:\\Windows\\System32\\notepad.exe",
				HashSha256:  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				User:        "NT AUTHORITY\\SYSTEM",
				TimestampNs: now,
				IsElevated:  true,
			},
		}
	case 1:
		event.EventType = &pb.Event_NetworkConnection{
			NetworkConnection: &pb.NetworkConnectionEvent{
				Pid: pid, ProcessName: "chrome.exe",
				LocalIp: "192.168.1.100", LocalPort: 54321,
				RemoteIp: fmt.Sprintf("203.0.113.%d", rand.Intn(100)), RemotePort: 443,
				Protocol: "tcp", Direction: "outbound",
				TimestampNs: now,
			},
		}
	case 2:
		event.EventType = &pb.Event_FileCreate{
			FileCreate: &pb.FileCreateEvent{
				FilePath:    fmt.Sprintf("C:\\Users\\admin\\Downloads\\malware%d.exe", seq),
				FileSize:    uint64(rand.Intn(500000) + 1000),
				HashSha256:  "d7a8fbb307d7809469ca9abcb0082e4f8d5651e46d3cdb762d02d0bf37c9e592",
				Pid:         pid,
				ProcessName: "svchost.exe",
				TimestampNs: now,
			},
		}
	case 3:
		event.EventType = &pb.Event_FileModify{
			FileModify: &pb.FileModifyEvent{
				FilePath: "C:\\Windows\\System32\\drivers\\etc\\hosts",
				FileSizeBefore: 824, FileSizeAfter: 942,
				Pid: pid, ProcessName: "notepad.exe",
				TimestampNs: now,
			},
		}
	case 4:
		event.EventType = &pb.Event_FileDelete{
			FileDelete: &pb.FileDeleteEvent{
				FilePath:      fmt.Sprintf("C:\\temp\\dump%d.bin", seq),
				OriginalSize:  1024 * 1024,
				Pid:           pid,
				ProcessName:   "cleanup.exe",
				HashSha256:    "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				TimestampNs:   now,
			},
		}
	case 5:
		event.EventType = &pb.Event_RegistryChange{
			RegistryChange: &pb.RegistryChangeEvent{
				KeyPath:   "HKLM\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Run",
				ValueName: "MaliciousService",
				ValueData: "C:\\Users\\admin\\malware.exe",
				Operation: "create",
				Pid:       pid, ProcessName: "regedit.exe",
				TimestampNs: now,
			},
		}
	case 6:
		event.EventType = &pb.Event_ScheduledTask{
			ScheduledTask: &pb.ScheduledTaskEvent{
				TaskName:    "WindowsUpdateChecker",
				TaskPath:    "C:\\Users\\admin\\update.exe",
				TriggerType: "logon",
				Operation:   "create",
				Pid:         pid, ProcessName: "schtasks.exe",
				TimestampNs: now,
			},
		}
	case 7:
		event.EventType = &pb.Event_YaraMatch{
			YaraMatch: &pb.YaraMatchEvent{
				RuleName:      "Mimikatz_Detect",
				RuleId:        "MAL-001",
				TargetPath:    fmt.Sprintf("C:\\Users\\admin\\proc_%d.exe", seq),
				ScanType:      "file",
				MatchedString: "mimikatz",
				TimestampNs:   now,
			},
		}
	}

	return event
}

func eventSummary(e *pb.Event) string {
	switch t := e.GetEventType().(type) {
	case *pb.Event_ProcessCreate:
		return fmt.Sprintf("[进程创建] %s", t.ProcessCreate.GetImagePath())
	case *pb.Event_NetworkConnection:
		return fmt.Sprintf("[网络连接] %s:%d → %s:%d",
			t.NetworkConnection.GetLocalIp(), t.NetworkConnection.GetLocalPort(),
			t.NetworkConnection.GetRemoteIp(), t.NetworkConnection.GetRemotePort())
	case *pb.Event_FileCreate:
		return fmt.Sprintf("[文件创建] %s", t.FileCreate.GetFilePath())
	case *pb.Event_FileModify:
		return fmt.Sprintf("[文件修改] %s", t.FileModify.GetFilePath())
	case *pb.Event_FileDelete:
		return fmt.Sprintf("[文件删除] %s", t.FileDelete.GetFilePath())
	case *pb.Event_RegistryChange:
		return fmt.Sprintf("[注册表] %s %s", t.RegistryChange.GetOperation(), t.RegistryChange.GetKeyPath())
	case *pb.Event_ScheduledTask:
		return fmt.Sprintf("[计划任务] %s %s", t.ScheduledTask.GetOperation(), t.ScheduledTask.GetTaskName())
	case *pb.Event_YaraMatch:
		return fmt.Sprintf("[YARA] %s 命中 %s", t.YaraMatch.GetRuleName(), t.YaraMatch.GetTargetPath())
	}
	return "[未知]"
}
