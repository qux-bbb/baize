package engine

import (
	"encoding/json"
	"os"
	"sync"
)

const configFile = "data/config.json"

// configData 磁盘存储结构
type configData struct {
	Global   []string          `json:"global"`
	PerAgent map[string][]string `json:"per_agent"`
}

// ConfigManager 管理全局和主机级配置
type ConfigManager struct {
	mu       sync.RWMutex
	global   []string
	perAgent map[string][]string
}

func NewConfigManager() *ConfigManager {
	cm := &ConfigManager{
		perAgent: make(map[string][]string),
	}
	cm.load()
	return cm
}

func (c *ConfigManager) load() {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return // 文件不存在，使用默认空配置
	}
	var cd configData
	if err := json.Unmarshal(data, &cd); err != nil {
		return
	}
	c.global = cd.Global
	if cd.PerAgent != nil {
		c.perAgent = cd.PerAgent
	}
}

func (c *ConfigManager) save() {
	cd := configData{
		Global:   c.global,
		PerAgent: c.perAgent,
	}
	data, _ := json.MarshalIndent(cd, "", "  ")
	os.MkdirAll("data", 0755)
	os.WriteFile(configFile, data, 0644)
}

// SetGlobal 设置全局文件监控目录
func (c *ConfigManager) SetGlobal(dirs []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.global = dirs
	c.save()
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
	c.save()
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
