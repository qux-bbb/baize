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

	EventTypesGlobal   map[string]bool              `json:"event_types_global"`
	EventTypesPerAgent map[string]map[string]bool    `json:"event_types_per_agent"`
}

// ConfigManager 管理全局和主机级配置
type ConfigManager struct {
	mu       sync.RWMutex
	global   []string
	perAgent map[string][]string

	eventTypesGlobal   map[string]bool
	eventTypesPerAgent map[string]map[string]bool
}

func NewConfigManager() *ConfigManager {
	cm := &ConfigManager{
		perAgent:           make(map[string][]string),
		eventTypesGlobal:   make(map[string]bool),
		eventTypesPerAgent: make(map[string]map[string]bool),
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
	if cd.EventTypesGlobal != nil {
		c.eventTypesGlobal = cd.EventTypesGlobal
	}
	if cd.EventTypesPerAgent != nil {
		c.eventTypesPerAgent = cd.EventTypesPerAgent
	}
}

func (c *ConfigManager) save() {
	cd := configData{
		Global:   c.global,
		PerAgent: c.perAgent,
	}
	// 不存储空 map，保持 JSON 整洁
	if len(c.eventTypesGlobal) > 0 {
		cd.EventTypesGlobal = c.eventTypesGlobal
	}
	if len(c.eventTypesPerAgent) > 0 {
		cd.EventTypesPerAgent = c.eventTypesPerAgent
	}
	data, _ := json.MarshalIndent(cd, "", "  ")
	os.MkdirAll("data", 0755)
	os.WriteFile(configFile, data, 0644)
}

// ── 文件监控目录 ──────────────────────────────────────────

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

// ── 事件类型开关 ──────────────────────────────────────────

// DefaultEventTypes 所有事件类型及其默认值（全部关闭）
func DefaultEventTypes() map[string]bool {
	return map[string]bool{
		"process":  false,
		"file":     false,
		"network":  false,
		"dns":      false,
		"registry": false,
		"task":     false,
		"yara":     false,
	}
}

// GetEventTypesGlobal 获取全局事件类型配置
func (c *ConfigManager) GetEventTypesGlobal() map[string]bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.eventTypesGlobal) == 0 {
		return DefaultEventTypes()
	}
	result := DefaultEventTypes()
	for k, v := range c.eventTypesGlobal {
		result[k] = v
	}
	return result
}

// SetEventTypesGlobal 设置全局事件类型配置
func (c *ConfigManager) SetEventTypesGlobal(types map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.eventTypesGlobal = types
	c.save()
}

// GetEventTypesAgent 获取指定 Agent 的事件类型配置
func (c *ConfigManager) GetEventTypesAgent(agentID string) map[string]bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if cfg, ok := c.eventTypesPerAgent[agentID]; ok && len(cfg) > 0 {
		result := DefaultEventTypes()
		for k, v := range cfg {
			result[k] = v
		}
		return result
	}
	return nil // 未设置 Agent 级配置
}

// SetEventTypesAgent 设置指定 Agent 的事件类型配置
func (c *ConfigManager) SetEventTypesAgent(agentID string, types map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.eventTypesPerAgent[agentID] = types
	c.save()
}

// GetEffectiveEventTypes 获取对指定 Agent 生效的事件类型配置
// Agent 级优先，无则用全局；全局无配置 = 全部关闭
func (c *ConfigManager) GetEffectiveEventTypes(agentID string) map[string]bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Agent 级
	if cfg, ok := c.eventTypesPerAgent[agentID]; ok && len(cfg) > 0 {
		result := DefaultEventTypes()
		for k, v := range cfg {
			result[k] = v
		}
		return result
	}

	// 全局
	if len(c.eventTypesGlobal) > 0 {
		result := DefaultEventTypes()
		for k, v := range c.eventTypesGlobal {
			result[k] = v
		}
		return result
	}

	// 默认全部关闭
	return DefaultEventTypes()
}

// GetEventTypesAll 返回所有事件类型配置（用于 API 展示）
func (c *ConfigManager) GetEventTypesAll() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make(map[string]interface{})
	result["_global"] = c.getEventTypesGlobalCopy()
	perAgent := make(map[string]map[string]bool)
	for id, cfg := range c.eventTypesPerAgent {
		perAgent[id] = cfg
	}
	result["per_agent"] = perAgent
	return result
}

func (c *ConfigManager) getEventTypesGlobalCopy() map[string]bool {
	if len(c.eventTypesGlobal) == 0 {
		return DefaultEventTypes()
	}
	result := DefaultEventTypes()
	for k, v := range c.eventTypesGlobal {
		result[k] = v
	}
	return result
}
