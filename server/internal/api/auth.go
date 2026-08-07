// 认证模块 — JWT签发/验证、bcrypt密码管理、随机密码生成
package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ── 上下文键 ─────────────────────────────────────────────

type ctxKey string

const authCtxKey ctxKey = "auth_username"

// ── 配置 ────────────────────────────────────────────────

type AuthConfig struct {
	Username         string `json:"username"`
	PasswordHash     string `json:"password_hash"`
	MustChangePwd    bool   `json:"must_change_password"`
	SecretKey        string `json:"secret_key,omitempty"` // JWT 签名密钥（持久化，重启复用）
	CreatedAt        string `json:"created_at,omitempty"`
}

// AuthManager 管理认证状态
type AuthManager struct {
	configPath string
	secret     []byte
	mu         sync.RWMutex
	cfg        AuthConfig

	revoked   map[string]int64 // token签名部分 → 过期时间(unix)，用于黑名单
	revokedMu sync.RWMutex
}

func NewAuthManager(configPath string) *AuthManager {
	am := &AuthManager{
		configPath: configPath,
		revoked:    make(map[string]int64),
	}

	if data, err := os.ReadFile(configPath); err == nil {
		if err := json.Unmarshal(data, &am.cfg); err == nil {
			log.Printf("[Auth] 认证配置已加载: %s", configPath)
			if am.cfg.SecretKey != "" {
				// 复用持久化的密钥，保证重启后已签发的 token 仍然有效
				key, err := base64.StdEncoding.DecodeString(am.cfg.SecretKey)
				if err != nil {
					log.Printf("[Auth] secret_key 解码失败，重新生成: %v", err)
					am.secret = generateSecret()
					am.cfg.SecretKey = base64.StdEncoding.EncodeToString(am.secret)
					am.save()
				} else {
					am.secret = key
				}
			} else {
				// 老配置没有密钥 → 生成并写回
				am.secret = generateSecret()
				am.cfg.SecretKey = base64.StdEncoding.EncodeToString(am.secret)
				am.save()
				log.Printf("[Auth] 已生成 JWT 密钥并持久化到配置文件")
			}
			return am
		}
		log.Printf("[Auth] 配置文件损坏，将重新初始化: %v", err)
	}

	am.initDefault()
	return am
}

func (am *AuthManager) initDefault() {
	password := generatePassword(16)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("[Auth] bcrypt 失败: %v", err)
	}

	am.secret = generateSecret()
	am.cfg = AuthConfig{
		Username:      "admin",
		PasswordHash:  string(hash),
		MustChangePwd: true,
		SecretKey:     base64.StdEncoding.EncodeToString(am.secret),
		CreatedAt:     time.Now().Format(time.RFC3339),
	}
	am.save()

	log.Println("═══════════════════════════════════════════════")
	log.Println("  Baize (白泽) EDR 首次启动！")
	log.Println("  Dashboard:   https://localhost:8080")
	log.Printf("  用户名:      %s", am.cfg.Username)
	log.Printf("  密码:        %s", password)
	log.Println("  ⚠ 请立即登录并修改密码")
	log.Println("═══════════════════════════════════════════════")
}

func (am *AuthManager) save() {
	data, err := json.MarshalIndent(am.cfg, "", "  ")
	if err != nil {
		log.Printf("[Auth] 序列化失败: %v", err)
		return
	}
	dir := filepath.Dir(am.configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("[Auth] 创建目录失败: %v", err)
		return
	}
	if err := os.WriteFile(am.configPath, data, 0644); err != nil {
		log.Printf("[Auth] 写入失败: %v", err)
	}
}

// ── 密码验证 ─────────────────────────────────────────────

func (am *AuthManager) Verify(username, password string) bool {
	am.mu.RLock()
	defer am.mu.RUnlock()
	if am.cfg.Username != username {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(am.cfg.PasswordHash), []byte(password)) == nil
}

func (am *AuthManager) ChangePassword(oldPwd, newPwd string) error {
	am.mu.Lock()
	defer am.mu.Unlock()

	if bcrypt.CompareHashAndPassword([]byte(am.cfg.PasswordHash), []byte(oldPwd)) != nil {
		return fmt.Errorf("旧密码错误")
	}
	if len(newPwd) < 6 {
		return fmt.Errorf("密码长度不能少于6位")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPwd), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("密码加密失败: %v", err)
	}
	am.cfg.PasswordHash = string(hash)
	am.cfg.MustChangePwd = false
	am.save()
	return nil
}

func (am *AuthManager) MustChangePassword() bool {
	am.mu.RLock()
	defer am.mu.RUnlock()
	return am.cfg.MustChangePwd
}

func (am *AuthManager) Username() string {
	am.mu.RLock()
	defer am.mu.RUnlock()
	return am.cfg.Username
}

// ── JWT ─────────────────────────────────────────────────

const jwtHeader = `{"alg":"HS256","typ":"JWT"}`

type jwtPayload struct {
	Username       string `json:"username"`
	Exp            int64  `json:"exp"`
	MustChangePwd  bool   `json:"must_change_password"`
	JTI            string `json:"jti"`
}

func (am *AuthManager) IssueToken(username string, mustChangePwd bool) string {
	payload := jwtPayload{
		Username:      username,
		Exp:           time.Now().Add(24 * time.Hour).Unix(),
		MustChangePwd: mustChangePwd,
		JTI:           generatePassword(12),
	}

	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(jwtHeader))
	payloadJSON, _ := json.Marshal(payload)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)

	signingInput := headerB64 + "." + payloadB64
	mac := hmac.New(sha256.New, am.secret)
	mac.Write([]byte(signingInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return signingInput + "." + sig
}

// VerifyToken 返回 (username, mustChangePassword, error)
func (am *AuthManager) VerifyToken(token string) (string, bool, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false, fmt.Errorf("无效的 token 格式")
	}

	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, am.secret)
	mac.Write([]byte(signingInput))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(parts[2]), []byte(expectedSig)) {
		return "", false, fmt.Errorf("签名无效")
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false, fmt.Errorf("payload 解码失败: %v", err)
	}

	var payload jwtPayload
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		return "", false, fmt.Errorf("payload 解析失败: %v", err)
	}

	if time.Now().Unix() > payload.Exp {
		return "", false, fmt.Errorf("token 已过期")
	}

	return payload.Username, payload.MustChangePwd, nil
}

// ── 工具 ────────────────────────────────────────────────

func generatePassword(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%"
	buf := make([]byte, length)
	rand.Read(buf)
	for i := range buf {
		buf[i] = charset[int(buf[i])%len(charset)]
	}
	return string(buf)
}

func generateSecret() []byte {
	secret := make([]byte, 32)
	rand.Read(secret)
	return secret
}

// UsernameFromContext 从请求上下文提取用户名
func UsernameFromContext(ctx context.Context) string {
	v, _ := ctx.Value(authCtxKey).(string)
	return v
}

// ── Token 吊销 ─────────────────────────────────────────

// RevokeToken 将 token 加入黑名单
func (am *AuthManager) RevokeToken(token string) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return
	}
	sig := parts[2]

	// 解析 payload 拿过期时间
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return
	}
	var payload jwtPayload
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		return
	}

	am.revokedMu.Lock()
	am.revoked[sig] = payload.Exp
	am.revokedMu.Unlock()

	log.Printf("[Auth] token 已吊销 (user=%s)", payload.Username)
}

// IsRevoked 检查 token 是否已被吊销
func (am *AuthManager) IsRevoked(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	sig := parts[2]

	am.revokedMu.RLock()
	exp, ok := am.revoked[sig]
	am.revokedMu.RUnlock()

	if !ok {
		return false
	}

	// 如果已过期，清除黑名单记录并返回未吊销（反正也过期了）
	if time.Now().Unix() > exp {
		am.revokedMu.Lock()
		delete(am.revoked, sig)
		am.revokedMu.Unlock()
		return false
	}

	return true
}
