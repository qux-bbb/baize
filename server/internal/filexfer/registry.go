// 文件传输注册表 — 管理 Agent ↔ Server 的流式文件传输任务
//
// 传输方向：
//
//	download (A→S): Server 下发 FileDownloadCommand, Agent 建 FileTransferStream 逐块推送,
//	                gRPC handler 将块写入 Transfer.chunks, HTTP 下载 handler 消费并写出。
//	upload   (S→A): Server 下发 FileUploadCommand, Agent 建 FileTransferStream,
//	                gRPC handler 把 Send 函数挂到 Transfer, HTTP 上传 handler 边读请求体边写入流, Agent 落盘。
package filexfer

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	pb "github.com/qux-bbb/baize/proto/gen/go/baize/v1"
)

const (
	// ChunkSize 单个分块字节数（gRPC 默认 4MB 消息上限内，留余量）。
	ChunkSize = 512 * 1024

	// StreamReadyTimeout 等待 Agent 建立传输流的超时。
	StreamReadyTimeout = 10 * time.Second

	// IdleTimeout 传输过程中长时间无数据的超时（防止 Agent 僵死挂起 HTTP 请求）。
	IdleTimeout = 60 * time.Second

	// DownloadChunkBuf 下载方向块通道缓冲（背压缓冲）。
	DownloadChunkBuf = 16
)

// Transfer 表示一次进行中的文件传输任务。
type Transfer struct {
	ID        string
	AgentID   string
	Direction string // "download" / "upload"
	created   time.Time

	// download: HTTP 下载 handler 消费的块流
	chunks   chan *pb.FileChunk
	done     chan struct{}
	doneOnce sync.Once
	err      error

	// upload: Server → Agent 的 Send 函数（Agent 流就绪后由 gRPC handler 挂载）
	sendMu    sync.Mutex
	send      func(*pb.FileChunk) error
	ready     chan struct{}
	readyOnce sync.Once
}

// Chunks 返回下载方向的块流（仅 download 使用）。
func (t *Transfer) Chunks() <-chan *pb.FileChunk { return t.chunks }

// PushChunk 写入一个块（download 方向, gRPC handler 调用）。
// 返回 true 表示写入成功; false 表示传输已结束（done 关闭）。
func (t *Transfer) PushChunk(chunk *pb.FileChunk) bool {
	select {
	case t.chunks <- chunk:
		return true
	case <-t.done:
		return false
	}
}

// Done 返回传输结束信号（成功或失败都会关闭）。
func (t *Transfer) Done() <-chan struct{} { return t.done }

// Err 返回传输失败原因。
func (t *Transfer) Err() error { return t.err }

// SetErr 标记传输失败并结束（用于断线清理 / 取消）。
func (t *Transfer) SetErr(err error) {
	t.doneOnce.Do(func() {
		t.err = err
		close(t.done)
	})
}

// Ready 返回上传方向"Agent 流已就绪"信号。
func (t *Transfer) Ready() <-chan struct{} { return t.ready }

// SetSend 挂载 Server → Agent 的发送函数（upload, Agent 流就绪后调用）。
func (t *Transfer) SetSend(fn func(*pb.FileChunk) error) {
	t.sendMu.Lock()
	already := t.send != nil
	t.send = fn
	t.sendMu.Unlock()
	if !already {
		t.readyOnce.Do(func() { close(t.ready) })
	}
}

// Send 向 Agent 发送一个块（upload）。流未就绪返回错误。
func (t *Transfer) Send(chunk *pb.FileChunk) error {
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	if t.send == nil {
		return fmt.Errorf("传输流未就绪")
	}
	return t.send(chunk)
}

// Registry 管理全部进行中的传输任务。
type Registry struct {
	mu      sync.Mutex
	byID    map[string]*Transfer
	byAgent map[string]map[string]struct{} // agentID → transferID 集合
}

func NewRegistry() *Registry {
	return &Registry{
		byID:    make(map[string]*Transfer),
		byAgent: make(map[string]map[string]struct{}),
	}
}

// NewTransferID 生成全局唯一的传输 ID。
func NewTransferID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "ft-" + hex.EncodeToString(b)
}

func (r *Registry) add(t *Transfer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[t.ID] = t
	if r.byAgent[t.AgentID] == nil {
		r.byAgent[t.AgentID] = make(map[string]struct{})
	}
	r.byAgent[t.AgentID][t.ID] = struct{}{}
}

// NewDownload 注册一个下载传输（A→S），HTTP 下载 handler 从 Chunks() 消费。
func (r *Registry) NewDownload(agentID, transferID string) *Transfer {
	t := &Transfer{
		ID:        transferID,
		AgentID:   agentID,
		Direction: "download",
		created:   time.Now(),
		chunks:    make(chan *pb.FileChunk, DownloadChunkBuf),
		done:      make(chan struct{}),
		ready:     make(chan struct{}),
	}
	r.add(t)
	return t
}

// NewUpload 注册一个上传传输（S→A），HTTP 上传 handler 等 Ready() 后 Send()。
func (r *Registry) NewUpload(agentID, transferID string) *Transfer {
	t := &Transfer{
		ID:        transferID,
		AgentID:   agentID,
		Direction: "upload",
		created:   time.Now(),
		chunks:    make(chan *pb.FileChunk, DownloadChunkBuf),
		done:      make(chan struct{}),
		ready:     make(chan struct{}),
	}
	r.add(t)
	return t
}

// Get 按 transfer_id 查找传输任务。
func (r *Registry) Get(transferID string) (*Transfer, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byID[transferID]
	return t, ok
}

// Remove 移除传输任务并结束其生命周期（正常完成后调用，会关闭 Done 唤醒等待方）。
func (r *Registry) Remove(transferID string) {
	r.mu.Lock()
	t, ok := r.byID[transferID]
	if !ok {
		r.mu.Unlock()
		return
	}
	delete(r.byID, transferID)
	if set, ok := r.byAgent[t.AgentID]; ok {
		delete(set, t.ID)
		if len(set) == 0 {
			delete(r.byAgent, t.AgentID)
		}
	}
	r.mu.Unlock()
	// 唤醒等待中的传输（gRPC handler 阻塞在 Done 上）
	t.doneOnce.Do(func() { close(t.done) })
}

// CloseAgent 在该 Agent 断线时清理其全部传输，使挂起的 HTTP 请求立即结束并报错。
func (r *Registry) CloseAgent(agentID string) {
	r.mu.Lock()
	set := r.byAgent[agentID]
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		if t, ok := r.Get(id); ok {
			t.SetErr(fmt.Errorf("Agent %s 已断开连接，传输中断", agentID))
			r.Remove(id)
		}
	}
}
