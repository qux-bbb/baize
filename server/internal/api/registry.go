// Agent 注册表 — enrollment token 管理 + Agent 身份密钥（对标 Wazuh authd 注册机制）
//
// 模型：
//   - token（enrollment token）：Dashboard 生成，可吊销；Agent 安装时写入 authd.pass，
//     首次启动凭 token 注册换取通信身份密钥（agent_key）
//   - agent_key：注册时 Server 生成下发，Agent 存 client.key；gRPC 连接时经
//     metadata 携带（baize-agent-key），Server 按 agent_id + key_hash 校验
//   - 持久化：<data-dir>/agents.json（tokens + agents 两张表）
package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrTokenInvalid   = errors.New("注册 token 无效或已吊销")
	ErrAgentIDInvalid = errors.New("agent_id 无效")
)

// AgentRecord 已注册 Agent 的记录
type AgentRecord struct {
	AgentID      string `json:"agent_id"`
	KeyHash      string `json:"key_hash"`       // agent_key 的 SHA256（明文 key 只下发一次）
	Hostname     string `json:"hostname"`       // 注册时的主机名（后续心跳更新）
	TokenID      string `json:"token_id"`       // 注册所用 token（追溯用）
	RegisteredAt string `json:"registered_at"`
	LastSeen     string `json:"last_seen,omitempty"` // 最近连接/心跳时间
}

// TokenRecord enrollment token 的记录
//   - Plain：AES-256-GCM 密文（hex），密钥由 auth.json secret_key 派生；明文只在创建/查看时可见
//   - 兼容旧数据：早期版本只存 sha256（Plain 为空），无法还原明文，可吊销重建
type TokenRecord struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	Revoked   bool   `json:"revoked,omitempty"`
	// UsedCount 记录该 token 已注册过的 Agent 数（展示用）
	UsedCount int `json:"used_count,omitempty"`
	// Plain 明文密文（hex），不直接序列化明文；列表接口不回传，仅按 id 查询时解密
	Plain string `json:"plain,omitempty"`
	// Masked 部分显示（前 6 + 后 4 位），列表展示用，由 ListTokens 解密生成
	Masked string `json:"masked,omitempty"`
}

type registryFile struct {
	Tokens map[string]*TokenRecord `json:"tokens"` // key: sha256(token)
	Agents map[string]*AgentRecord `json:"agents"` // key: agent_id
}

// AgentRegistry 线程安全的注册表
type AgentRegistry struct {
	path   string
	encKey []byte // AES-256 密钥（由 auth.json secret_key HMAC 派生），用于 token 明文加解密
	mu     sync.RWMutex
	data   registryFile
}

// NewAgentRegistry 加载或初始化注册表。
// master：JWT 签名密钥（auth.json secret_key），派生 token 加密密钥（域分离，不与 JWT 直接共用）。
// 首次初始化（无任何 token）时自动生成一个 token 并打印到日志（对标 Wazuh 随机注册密码）。
func NewAgentRegistry(path string, master []byte) *AgentRegistry {
	reg := &AgentRegistry{path: path, encKey: deriveTokenKey(master), data: registryFile{
		Tokens: make(map[string]*TokenRecord),
		Agents: make(map[string]*AgentRecord),
	}}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &reg.data); err == nil {
			log.Printf("[Registry] 注册表已加载: %s (%d token, %d agent)", path, len(reg.data.Tokens), len(reg.data.Agents))
			if reg.data.Tokens == nil {
				reg.data.Tokens = make(map[string]*TokenRecord)
			}
			if reg.data.Agents == nil {
				reg.data.Agents = make(map[string]*AgentRecord)
			}
			return reg
		}
		log.Printf("[Registry] 注册表损坏，重新初始化: %v", err)
	}
	// 首次初始化：自动生成一个默认 token
	token, _ := reg.CreateToken("default")
	log.Println("═══════════════════════════════════════════════")
	log.Println("  Agent 注册 token（安装 Agent 时使用）:")
	log.Printf("  %s", token)
	log.Println("  Dashboard → 下载 Agent → 可查看/生成/吊销")
	log.Println("═══════════════════════════════════════════════")
	return reg
}

func (r *AgentRegistry) save() {
	data, err := json.MarshalIndent(r.data, "", "  ")
	if err != nil {
		log.Printf("[Registry] 序列化失败: %v", err)
		return
	}
	if dir := filepath.Dir(r.path); dir != "" {
		os.MkdirAll(dir, 0755)
	}
	if err := os.WriteFile(r.path, data, 0644); err != nil {
		log.Printf("[Registry] 写入失败: %v", err)
	}
}

// ── Token 管理 ──────────────────────────────────────────

// CreateToken 生成新的 enrollment token，返回 (明文 token, 记录 ID)。
// 明文只在创建时返回一次；落盘存 AES-GCM 密文（可按 id 解密查看/复制）。
func (r *AgentRegistry) CreateToken(name string) (string, string) {
	raw := make([]byte, 18)
	rand.Read(raw)
	token := "baize-" + hex.EncodeToString(raw)

	r.mu.Lock()
	defer r.mu.Unlock()
	id := tokenID()
	if name == "" {
		name = "default"
	}
	r.data.Tokens[hashToken(token)] = &TokenRecord{
		ID:        id,
		Name:      name,
		CreatedAt: time.Now().Format(time.RFC3339),
		Plain:     encryptToken(token, r.encKey),
	}
	r.save()
	return token, id
}

// ListTokens 返回全部 token 记录（不含明文，仅部分掩码），按创建时间排序
func (r *AgentRegistry) ListTokens() []TokenRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]TokenRecord, 0, len(r.data.Tokens))
	for _, t := range r.data.Tokens {
		rec := *t
		// 统计该 token 注册过的 Agent 数
		for _, a := range r.data.Agents {
			if a.TokenID == t.ID {
				rec.UsedCount++
			}
		}
		// 列表只暴露部分掩码（前 6 + 后 4），供辨认
		if rec.Plain != "" {
			if plain, ok := decryptToken(rec.Plain, r.encKey); ok {
				rec.Masked = maskToken(plain)
			}
		}
		rec.Plain = "" // 密文也不回传列表
		list = append(list, rec)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt > list[j].CreatedAt })
	return list
}

// GetTokenPlain 按 id 返回 token 明文（Dashboard 查看/复制用，JWT 保护）。
// 旧数据（仅存 sha256，无密文）返回 false。
func (r *AgentRegistry) GetTokenPlain(id string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, t := range r.data.Tokens {
		if t.ID == id {
			if t.Plain == "" {
				return "", false
			}
			return decryptToken(t.Plain, r.encKey)
		}
	}
	return "", false
}

// RevokeToken 吊销 token（已注册的 Agent 不受影响，仅禁止新的注册）
func (r *AgentRegistry) RevokeToken(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.data.Tokens {
		if t.ID == id {
			t.Revoked = true
			r.save()
			return true
		}
	}
	return false
}

// validateToken 校验 token 有效（存在且未吊销），返回其记录 ID
func (r *AgentRegistry) validateToken(token string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.data.Tokens[hashToken(token)]
	if !ok || t.Revoked {
		return "", false
	}
	return t.ID, true
}

// ── Agent 注册 / 身份校验 ───────────────────────────────

// Register 用 enrollment token 注册 Agent，返回通信身份密钥（明文）。
// 同一 agent_id 重复注册（如 client.key 丢失后重装）会生成新 key，旧 key 失效。
func (r *AgentRegistry) Register(agentID, hostname, token string) (string, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return "", ErrAgentIDInvalid
	}
	tokenID, ok := r.validateToken(token)
	if !ok {
		return "", ErrTokenInvalid
	}

	key := make([]byte, 18)
	rand.Read(key)
	keyHex := hex.EncodeToString(key)

	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().Format(time.RFC3339)
	if rec, exists := r.data.Agents[agentID]; exists {
		// 重新注册：换发新 key（旧 key 立即失效），保留注册时间，更新 hostname
		rec.KeyHash = hashToken(keyHex)
		rec.Hostname = hostname
		rec.TokenID = tokenID
		rec.LastSeen = now
	} else {
		r.data.Agents[agentID] = &AgentRecord{
			AgentID:      agentID,
			KeyHash:      hashToken(keyHex),
			Hostname:     hostname,
			TokenID:      tokenID,
			RegisteredAt: now,
			LastSeen:     now,
		}
	}
	r.save()
	return keyHex, nil
}

// VerifyAgentKey 校验 Agent 连接身份（agent_id + key 匹配且注册记录存在）
func (r *AgentRegistry) VerifyAgentKey(agentID, key string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.data.Agents[agentID]
	if !ok {
		return false
	}
	return rec.KeyHash == hashToken(key)
}

// DeleteAgent 删除 Agent 注册记录（对标 Wazuh remove agent）：
// 旧 key 立即失效（拒绝新连接），在线连接在心跳/事件周期内被断开；
// Agent 可用有效 token 重新注册（想永久阻止接入请吊销 token）。
func (r *AgentRegistry) DeleteAgent(agentID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data.Agents[agentID]; !ok {
		return false
	}
	delete(r.data.Agents, agentID)
	r.save()
	return true
}

// Touch 更新 Agent 最近活跃时间（连接建立 / 心跳时调用）
func (r *AgentRegistry) Touch(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.data.Agents[agentID]; ok {
		rec.LastSeen = time.Now().Format(time.RFC3339)
	}
}

// ListAgents 返回全部已注册 Agent 记录
func (r *AgentRegistry) ListAgents() []AgentRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]AgentRecord, 0, len(r.data.Agents))
	for _, a := range r.data.Agents {
		list = append(list, *a)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].RegisteredAt > list[j].RegisteredAt })
	return list
}

// ── 工具 ────────────────────────────────────────────────

func hashToken(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// deriveTokenKey 由 JWT 主密钥派生 token 加密密钥（HMAC 域分离，避免与 JWT 签名直接共用）
func deriveTokenKey(master []byte) []byte {
	h := hmac.New(sha256.New, master)
	h.Write([]byte("baize-enrollment-token-v1"))
	return h.Sum(nil)
}

// encryptToken AES-256-GCM 加密（nonce 前置拼接），返回 hex
func encryptToken(plain string, key []byte) string {
	block, err := aes.NewCipher(key)
	if err != nil {
		log.Printf("[Registry] AES 初始化失败: %v", err)
		return ""
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		log.Printf("[Registry] GCM 初始化失败: %v", err)
		return ""
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		log.Printf("[Registry] nonce 生成失败: %v", err)
		return ""
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return hex.EncodeToString(sealed)
}

// decryptToken 解密 AES-256-GCM hex 密文（失败返回 false，如密钥不匹配/数据损坏）
func decryptToken(enc string, key []byte) (string, bool) {
	raw, err := hex.DecodeString(enc)
	if err != nil {
		return "", false
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", false
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", false
	}
	if len(raw) < gcm.NonceSize() {
		return "", false
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", false
	}
	return string(plain), true
}

// maskToken 生成部分掩码用于列表展示：保留前 6 位与后 4 位，中间打点，等长 42 字符
// 例如 baize-3f2a91••••••••••••••••••••••••9c1e
func maskToken(plain string) string {
	if len(plain) < 20 {
		return ""
	}
	const dots = "••••••••••••••••••••••••••"
	return plain[:12] + dots + plain[len(plain)-4:]
}

var tokenSeq int64

func tokenID() string {
	tokenSeq++
	return time.Now().Format("20060102") + "-" + hex.EncodeToString([]byte{byte(tokenSeq), byte(time.Now().Unix() % 256)})
}
