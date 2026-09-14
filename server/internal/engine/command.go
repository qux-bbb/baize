// 指令总线 — 规则命中 / 手动操作 → Agent 指令下发
package engine

import (
	"fmt"
	"log"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/qux-bbb/baize/proto/gen/go/baize/v1"
)

// CommandSender 是推送指令到 Agent 流的函数
type CommandSender func(cmd *pb.Command) error

// CommandBus 管理所有在线 Agent 的指令通道
type CommandBus struct {
	mu      sync.RWMutex
	conn    map[string]CommandSender
	results map[string]*pb.CommandResult // command_id → 执行结果
}

func NewCommandBus() *CommandBus {
	return &CommandBus{
		conn:    make(map[string]CommandSender),
		results: make(map[string]*pb.CommandResult),
	}
}

func (b *CommandBus) Register(agentID string, sender CommandSender) {
	b.mu.Lock()
	b.conn[agentID] = sender
	b.mu.Unlock()
	log.Printf("[CommandBus] Agent %s 已注册", agentID)
}

func (b *CommandBus) Unregister(agentID string) {
	b.mu.Lock()
	delete(b.conn, agentID)
	b.mu.Unlock()
	log.Printf("[CommandBus] Agent %s 已注销", agentID)
}

func (b *CommandBus) AgentCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.conn)
}

func (b *CommandBus) AgentIDs() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	ids := make([]string, 0, len(b.conn))
	for id := range b.conn {
		ids = append(ids, id)
	}
	return ids
}

// SendToAgent 向指定 Agent 下发一个指令
func (b *CommandBus) SendToAgent(agentID string, cmd *pb.Command) error {
	b.mu.RLock()
	sender, ok := b.conn[agentID]
	b.mu.RUnlock()
	if !ok {
		return fmt.Errorf("Agent %s 不在线", agentID)
	}
	return sender(cmd)
}

// Broadcast 向所有 Agent 广播指令
func (b *CommandBus) Broadcast(cmd *pb.Command) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for id, sender := range b.conn {
		if err := sender(proto.Clone(cmd).(*pb.Command)); err != nil {
			log.Printf("[CommandBus] 下发到 %s 失败: %v", id, err)
		}
	}
}

// BuildQuerySystemInfoCmd 组装系统信息查询指令
func BuildQuerySystemInfoCmd() *pb.Command {
	return &pb.Command{
		CommandId:  fmt.Sprintf("si-%d", time.Now().UnixNano()),
		IssuedAtNs: uint64(time.Now().UnixNano()),
		CommandType: &pb.Command_QuerySystemInfo{
			QuerySystemInfo: &pb.QuerySystemInfoCommand{Reason: "dashboard"},
		},
	}
}

// HandleResult 存储指令执行结果（由 ReportCommandResult 调用）
func (b *CommandBus) HandleResult(result *pb.CommandResult) {
	b.mu.Lock()
	defer b.mu.Unlock()
	log.Printf("[CommandBus] 结果: Command=%s success=%v", result.GetCommandId(), result.GetSuccess())
	b.results[result.GetCommandId()] = result
}

// QuerySystemInfo 向 Agent 查询系统信息，超时 10 秒
func (b *CommandBus) QuerySystemInfo(agentID string) (string, error) {
	cmd := BuildQuerySystemInfoCmd()
	if err := b.SendToAgent(agentID, cmd); err != nil {
		return "", err
	}
	// 等待结果，超时 10 秒
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r, ok := b.getPendingResult(cmd.GetCommandId()); ok {
			if !r.GetSuccess() {
				return "", fmt.Errorf("agent error: %s", r.GetErrorMessage())
			}
			return r.GetOutput(), nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("查询超时")
}

func (b *CommandBus) getPendingResult(id string) (*pb.CommandResult, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	r, ok := b.results[id]
	return r, ok
}

// BuildIsolateCmd 组装隔离/解除隔离指令
func BuildIsolateCmd(isolate bool) *pb.Command {
	return &pb.Command{
		CommandId:  fmt.Sprintf("cmd-%d", time.Now().UnixNano()),
		IssuedAtNs: uint64(time.Now().UnixNano()),
		CommandType: &pb.Command_Isolate{
			Isolate: &pb.IsolateCommand{Isolate: isolate},
		},
	}
}

// BuildConfigureEventTypesCmd 组装事件类型配置指令
func BuildConfigureEventTypesCmd(categories map[string]bool) *pb.Command {
	return &pb.Command{
		CommandId:  fmt.Sprintf("et-%d", time.Now().UnixNano()),
		IssuedAtNs: uint64(time.Now().UnixNano()),
		CommandType: &pb.Command_ConfigureEventTypes{
			ConfigureEventTypes: &pb.ConfigureEventTypesCommand{
				Categories: categories,
				Reason:     "server config",
			},
		},
	}
}

// BuildKillCmd 组装杀进程指令
func BuildKillCmd(pid uint64) *pb.Command {
	return &pb.Command{
		CommandId:  fmt.Sprintf("cmd-%d", time.Now().UnixNano()),
		IssuedAtNs: uint64(time.Now().UnixNano()),
		CommandType: &pb.Command_KillProcess{
			KillProcess: &pb.KillProcessCommand{Pid: pid},
		},
	}
}

// BuildExecCmd 组装远程执行命令/脚本指令
// interpreter 需为 powershell / cmd / bash / python（调用方已做白名单校验）
// timeoutSecs = 0 表示由 Agent 按默认值（30 秒）处理
func BuildExecCmd(script, interpreter string, timeoutSecs uint32, reason string) *pb.Command {
	if reason == "" {
		reason = "dashboard"
	}
	return &pb.Command{
		CommandId:  fmt.Sprintf("cmd-%d", time.Now().UnixNano()),
		IssuedAtNs: uint64(time.Now().UnixNano()),
		CommandType: &pb.Command_ExecuteScript{
			ExecuteScript: &pb.ExecuteScriptCommand{
				ScriptContent: script,
				Interpreter:   interpreter,
				TimeoutSecs:   timeoutSecs,
				Reason:        reason,
			},
		},
	}
}

// IsolateAgent 隔离指定 Agent 主机（自动化响应）
func (b *CommandBus) IsolateAgent(agentID string) {
	cmd := BuildIsolateCmd(true)
	_ = b.SendToAgent(agentID, cmd)
}

// ExecuteSync 下发指令并同步等待 Agent 执行结果。
// 调用方区分三种情况：
//   - err != nil：下发失败 / Agent 离线 / 超时（HTTP 应回 4xx）
//   - res.Success == false：Agent 上报执行失败（用 res.ErrorMessage）
//   - res.Success == true：执行成功
func (b *CommandBus) ExecuteSync(agentID string, cmd *pb.Command, timeout time.Duration) (*pb.CommandResult, error) {
	if err := b.SendToAgent(agentID, cmd); err != nil {
		return nil, err
	}
	return b.WaitResult(cmd.GetCommandId(), timeout)
}

// WaitResult 等待指定指令的结果（用于"先下发再造流/喂数据"的场景，如文件上传）。
func (b *CommandBus) WaitResult(commandID string, timeout time.Duration) (*pb.CommandResult, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if r, ok := b.getPendingResult(commandID); ok {
			return r, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("指令超时（Agent 未在 %v 内回报结果）", timeout)
}

// BuildListDirCmd 组装列目录指令
func BuildListDirCmd(dirPath string) *pb.Command {
	return &pb.Command{
		CommandId:  fmt.Sprintf("cmd-%d", time.Now().UnixNano()),
		IssuedAtNs: uint64(time.Now().UnixNano()),
		CommandType: &pb.Command_ListDir{
			ListDir: &pb.ListDirCommand{DirPath: dirPath, Reason: "dashboard"},
		},
	}
}

// BuildDeletePathCmd 组装删除文件/目录指令
func BuildDeletePathCmd(path string, recursive bool) *pb.Command {
	return &pb.Command{
		CommandId:  fmt.Sprintf("cmd-%d", time.Now().UnixNano()),
		IssuedAtNs: uint64(time.Now().UnixNano()),
		CommandType: &pb.Command_DeletePath{
			DeletePath: &pb.DeletePathCommand{Path: path, Recursive: recursive, Reason: "dashboard"},
		},
	}
}

// BuildFileDownloadCmd 组装远程下载传输任务指令
func BuildFileDownloadCmd(transferID, filePath string) *pb.Command {
	return &pb.Command{
		CommandId:  fmt.Sprintf("cmd-%d", time.Now().UnixNano()),
		IssuedAtNs: uint64(time.Now().UnixNano()),
		CommandType: &pb.Command_FileDownload{
			FileDownload: &pb.FileDownloadCommand{TransferId: transferID, FilePath: filePath, Reason: "dashboard"},
		},
	}
}

// BuildFileUploadCmd 组装远程上传传输任务指令
func BuildFileUploadCmd(transferID, destPath string, size uint64, overwrite bool) *pb.Command {
	return &pb.Command{
		CommandId:  fmt.Sprintf("cmd-%d", time.Now().UnixNano()),
		IssuedAtNs: uint64(time.Now().UnixNano()),
		CommandType: &pb.Command_FileUpload{
			FileUpload: &pb.FileUploadCommand{TransferId: transferID, DestPath: destPath, Size: size, Overwrite: overwrite, Reason: "dashboard"},
		},
	}
}
