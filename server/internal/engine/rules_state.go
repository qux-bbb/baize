// 规则状态存储 — rules.json（播种名单 / 内置标记 / 启用覆盖）
//
// 规则真源是 rulesDir 下的 *.yml/.yaml 文件，本文件只存"状态"，不存规则内容：
//   seeded  — 已播种过的内置规则文件名（只播种一次；用户改过/删过的文件不会被覆盖或复活）
//   builtin — 内置规则文件名（Dashboard 标徽 + 禁止删除）
//   enabled — 只记录与默认值不同的项（默认启用；显式禁用写入 false）
package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type rulesStateData struct {
	Seeded  []string        `json:"seeded,omitempty"`
	Builtin []string        `json:"builtin,omitempty"`
	Enabled map[string]bool `json:"enabled,omitempty"`
}

// RulesState 规则状态（并发安全）
type RulesState struct {
	mu      sync.RWMutex
	path    string
	seeded  map[string]bool
	builtin map[string]bool
	enabled map[string]bool
}

func NewRulesState(path string) *RulesState {
	st := &RulesState{
		path:    path,
		seeded:  make(map[string]bool),
		builtin: make(map[string]bool),
		enabled: make(map[string]bool),
	}
	st.load()
	return st
}

func (s *RulesState) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return // 文件不存在 = 首次运行，用空状态
	}
	var d rulesStateData
	if err := json.Unmarshal(data, &d); err != nil {
		return
	}
	for _, n := range d.Seeded {
		s.seeded[n] = true
	}
	for _, n := range d.Builtin {
		s.builtin[n] = true
	}
	for k, v := range d.Enabled {
		s.enabled[k] = v
	}
}

// save 落盘（调用方须持有写锁）
func (s *RulesState) save() {
	d := rulesStateData{
		Seeded:  sortedKeys(s.seeded),
		Builtin: sortedKeys(s.builtin),
		Enabled: make(map[string]bool),
	}
	for k, v := range s.enabled {
		// 默认启用：只存禁用项，JSON 保持简洁
		if !v {
			d.Enabled[k] = false
		}
	}
	if len(d.Enabled) == 0 {
		d.Enabled = nil
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return
	}
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		os.MkdirAll(dir, 0755)
	}
	os.WriteFile(s.path, data, 0644)
}

// Seeded 返回已播种文件名集合的副本
func (s *RulesState) Seeded() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]bool, len(s.seeded))
	for k := range s.seeded {
		out[k] = true
	}
	return out
}

// MarkSeeded 记录本次播种的内置规则文件名（同时登记为内置）
func (s *RulesState) MarkSeeded(names []string) {
	if len(names) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range names {
		s.seeded[n] = true
		s.builtin[n] = true
	}
	s.save()
}

// IsBuiltin 是否为内置规则（UI 标徽 + 禁止删除）
func (s *RulesState) IsBuiltin(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.builtin[name]
}

// IsEnabled 规则是否启用（无记录 = 默认启用）
func (s *RulesState) IsEnabled(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.enabled[name]; ok {
		return v
	}
	return true
}

// SetEnabled 设置规则启用状态并落盘
func (s *RulesState) SetEnabled(name string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on {
		delete(s.enabled, name) // 默认启用 → 不留记录
	} else {
		s.enabled[name] = false
	}
	s.save()
}

// Forget 规则被删除时清理其状态记录
func (s *RulesState) Forget(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.enabled, name)
	s.save()
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ { // 插入排序：名单只有几条，避免引入 sort 依赖以外的开销
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
