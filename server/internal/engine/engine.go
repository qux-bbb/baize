// 检测引擎 — Sigma 规则加载 + 事件匹配 + 告警生成
package engine

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/qux-bbb/baize/server/internal/store"
)

// Engine 检测引擎
type Engine struct {
	mu     sync.RWMutex
	rules  []*SigmaRule
	store  *store.Store
	loaded bool
}

// New 创建检测引擎，传入 ES 存储（用于写入告警）
func New(s *store.Store) *Engine {
	return &Engine{store: s}
}

// LoadRules 加载规则目录
func (e *Engine) LoadRules(path string) error {
	rules, err := LoadSigmaDir(path)
	if err != nil {
		return fmt.Errorf("加载规则失败: %w", err)
	}

	e.mu.Lock()
	e.rules = rules
	e.loaded = true
	e.mu.Unlock()

	log.Printf("[Engine] 已加载 %d 条 Sigma 规则", len(rules))
	for _, r := range rules {
		log.Printf("[Engine]   ├─ %s", r.RuleInfo())
	}
	return nil
}

// RuleCount 返回当前规则数
func (e *Engine) RuleCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.rules)
}

// IsLoaded 返回引擎是否已初始化
func (e *Engine) IsLoaded() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.loaded
}

// Eval 对一条事件执行所有规则匹配，返回匹配的规则列表
func (e *Engine) Eval(eventFields map[string]string) []*SigmaRule {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var matches []*SigmaRule
	for _, rule := range e.rules {
		if matchRule(rule, eventFields) {
			matches = append(matches, rule)
		}
	}
	return matches
}

// EvalAndAlert 匹配规则并写入告警到 ES
func (e *Engine) EvalAndAlert(eventFields map[string]string, rawEventJSON string) {
	matches := e.Eval(eventFields)
	if len(matches) == 0 {
		return
	}

	for _, rule := range matches {
		alert := map[string]interface{}{
			"@timestamp":     eventFields["@timestamp"],
			"alert_id":      uuid.New().String(),
			"rule_name":     rule.Title,
			"rule_id":       rule.ID,
			"severity":      rule.Level,
			"description":   rule.Description,
			"hostname":      eventFields["hostname"],
			"agent_id":      eventFields["agent_id"],
			"event_type":    eventFields["event_type"],
			"event_category": eventFields["event_category"],
			"tags":          rule.Tags,
			"source_event":  rawEventJSON,
		}

		if rule.Tags != nil {
			// √ MITRE 战术和技术
			for _, tag := range rule.Tags {
				if strings.HasPrefix(tag, "attack.") {
					parts := strings.SplitN(tag, ".", 3)
					if len(parts) >= 2 {
						alert["mitre_technique"] = parts[1]
					}
				}
			}
		}

		log.Printf("[Alert] %s | %s(%s) %s", rule.Level, rule.Title, rule.ID, eventFields["hostname"])

		// 写入 ES 告警索引
		if e.store != nil {
			if err := e.store.WriteAlert(alert); err != nil {
				log.Printf("[Alert] ES 写入失败: %v", err)
			}
		}
	}
}

// ── 匹配逻辑 ──────────────────────────────────────────────

// matchRule 判断一条规则是否匹配事件
func matchRule(rule *SigmaRule, event map[string]string) bool {
	// 1. logsource 过滤 — 如果规则指定了 category，事件必须匹配
	if rule.LogSource.Category != "" {
		if event["event_category"] != rule.LogSource.Category {
			return false
		}
	}

	// 2. 解析 condition
	cond := strings.TrimSpace(rule.Detection.Condition)

	// 处理 "selection" 和 "all of selection*" 两种常见模式
	if cond == "selection" {
		return evalSelection("selection", rule.Detection.Selections, rule.Detection.Keywords, event)
	}

	if strings.HasPrefix(cond, "all of ") {
		// all of selection1, selection2, ...
		prefix := strings.TrimPrefix(cond, "all of ")
		names := strings.Split(prefix, ",")
		for _, name := range names {
			name = strings.TrimSpace(name)
			if !evalSelection(name, rule.Detection.Selections, rule.Detection.Keywords, event) {
				return false
			}
		}
		return true
	}

	if strings.HasPrefix(cond, "any of ") {
		// any of selection1, selection2, ...
		prefix := strings.TrimPrefix(cond, "any of ")
		names := strings.Split(prefix, ",")
		for _, name := range names {
			name = strings.TrimSpace(name)
			if evalSelection(name, rule.Detection.Selections, rule.Detection.Keywords, event) {
				return true
			}
		}
		return false
	}

	// fallback: 只解析简单的 "selection1 or selection2" / "selection1 and not selection2"
	return evalSimpleCondition(cond, rule.Detection.Selections, rule.Detection.Keywords, event)
}

// evalSelection 评估单个 selection 是否匹配
func evalSelection(name string, selections map[string]map[string]interface{}, keywords map[string]interface{}, event map[string]string) bool {
	// 1. 检查 map 型 selection
	if sel, ok := selections[name]; ok {
		for field, pattern := range sel {
			evVal, exists := event[field]
			if !exists {
				return false
			}
			if !fieldMatch(evVal, pattern) {
				return false
			}
		}
		return true
	}

	// 2. 检查 keyword 型
	if kw, ok := keywords[name]; ok {
		return keywordMatch(event, kw)
	}

	return false
}

// fieldMatch 判断单个字段是否匹配模式
func fieldMatch(eventValue string, pattern interface{}) bool {
	switch p := pattern.(type) {
	case string:
		// 通配符匹配
		return wildcardMatch(eventValue, p)
	case []interface{}:
		// 列表 OR 匹配
		for _, item := range p {
			if itemStr, ok := item.(string); ok {
				if wildcardMatch(eventValue, itemStr) {
					return true
				}
			}
		}
		return false
	}
	return false
}

// keywordMatch 关键词匹配 — 在事件所有字段中搜索
func keywordMatch(event map[string]string, pattern interface{}) bool {
	switch p := pattern.(type) {
	case string:
		for _, v := range event {
			if strings.Contains(strings.ToLower(v), strings.ToLower(p)) {
				return true
			}
		}
	case []interface{}:
		for _, item := range p {
			if itemStr, ok := item.(string); ok {
				for _, v := range event {
					if strings.Contains(strings.ToLower(v), strings.ToLower(itemStr)) {
						return true
					}
				}
			}
		}
	}
	return false
}

// evalSimpleCondition 处理简单逻辑表达式
func evalSimpleCondition(cond string, selections map[string]map[string]interface{}, keywords map[string]interface{}, event map[string]string) bool {
	cond = strings.TrimSpace(cond)

	// and not 子句
	if strings.Contains(cond, " and not ") {
		parts := strings.SplitN(cond, " and not ", 2)
		left := strings.TrimSpace(parts[0])
		right := strings.TrimSpace(parts[1])
		return evalSimpleCondition(left, selections, keywords, event) &&
			!evalSimpleCondition(right, selections, keywords, event)
	}

	// or 子句
	if strings.Contains(cond, " or ") {
		parts := strings.Split(cond, " or ")
		for _, p := range parts {
			if evalSimpleCondition(strings.TrimSpace(p), selections, keywords, event) {
				return true
			}
		}
		return false
	}

	// and 子句
	if strings.Contains(cond, " and ") {
		parts := strings.Split(cond, " and ")
		for _, p := range parts {
			if !evalSimpleCondition(strings.TrimSpace(p), selections, keywords, event) {
				return false
			}
		}
		return true
	}

	// 单个 selection 名称
	return evalSelection(cond, selections, keywords, event)
}

// wildcardMatch 支持 * 通配符的字符串匹配
func wildcardMatch(value, pattern string) bool {
	// 替换 Sigma 通配符为正则
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return strings.EqualFold(value, pattern)
	}

	idx := 0
	for i, part := range parts {
		if part == "" {
			continue
		}
		if i == 0 {
			// 第一个部分必须从开头匹配
			if !strings.HasPrefix(strings.ToLower(value), strings.ToLower(part)) {
				return false
			}
			idx = len(part)
		} else if i == len(parts)-1 {
			// 最后一个部分必须匹配到末尾
			return strings.HasSuffix(strings.ToLower(value), strings.ToLower(part))
		} else {
			// 中间部分在剩余字符串中查找
			lowerVal := strings.ToLower(value[idx:])
			lowerPart := strings.ToLower(part)
			pos := strings.Index(lowerVal, lowerPart)
			if pos == -1 {
				return false
			}
			idx += pos + len(part)
		}
	}
	return true
}
