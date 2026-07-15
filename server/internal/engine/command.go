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
	mu   sync.RWMutex
	conn map[string]CommandSender
}

func NewCommandBus() *CommandBus {
	return &CommandBus{conn: make(map[string]CommandSender)}
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

// IsolateAgent 隔离指定 Agent 主机（自动化响应）
func (b *CommandBus) IsolateAgent(agentID string) {
	cmd := BuildIsolateCmd(true)
	_ = b.SendToAgent(agentID, cmd)
}
