package engine

import "sync"

// ConfigManager 管理全局和主机级配置
type ConfigManager struct {
	mu       sync.RWMutex
	global   []string        // 全局文件监控目录
	perAgent map[string][]string // agent_id → 目录列表
}

func NewConfigManager() *ConfigManager {
	return &ConfigManager{
		perAgent: make(map[string][]string),
	}
}

// SetGlobal 设置全局文件监控目录
func (c *ConfigManager) SetGlobal(dirs []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.global = dirs
}

// GetGlobal 获取全局文件监控目录
func (c *ConfigManager) GetGlobal() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.global
}

// SetAgent 设置指定主机的文件监控目录
func (c *ConfigManager) SetAgent(agentID string, dirs []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.perAgent[agentID] = dirs
}

// GetWatchDirs 获取指定主机的监控目录（主机级优先，无则用全局）
func (c *ConfigManager) GetWatchDirs(agentID string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if dirs, ok := c.perAgent[agentID]; ok {
		return dirs
	}
	return c.global
}

// GetAll 返回所有配置（用于 API 展示）
func (c *ConfigManager) GetAll() map[string][]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make(map[string][]string)
	result["_global"] = c.global
	for k, v := range c.perAgent {
		result[k] = v
	}
	return result
}
