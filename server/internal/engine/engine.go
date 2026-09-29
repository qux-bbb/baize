// 检测引擎 — Sigma 规则加载 + 事件匹配 + 告警生成
package engine

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/qux-bbb/baize/server/internal/store"
)

// Engine 检测引擎
type Engine struct {
	mu     sync.RWMutex
	rules  []*SigmaRule // 已启用规则（匹配热路径只遍历这个）
	all    []*SigmaRule // 目录中全部规则（含已禁用，供规则页展示）
	errs   []RuleLoadError
	store  *store.Store
	loaded bool

	ruleDir string     // 规则目录（真源：*.yml 文件）
	state   *RulesState // 启用状态（rules.json）
}

// New 创建检测引擎，传入存储（用于写入告警）
func New(s *store.Store) *Engine {
	return &Engine{store: s}
}

// InitRules 初始化规则目录：播种内置规则 → 加载状态 → 加载规则。
// ruleDir 为规则真源目录（如 data/rules），stateFile 存启停状态（如 data/rules.json）。
// 播种只做一次（按 rules.json 的 seeded 名单增量补），绝不覆盖用户已存在/改过的文件。
func (e *Engine) InitRules(ruleDir, stateFile string) error {
	if err := os.MkdirAll(ruleDir, 0755); err != nil {
		return fmt.Errorf("创建规则目录失败: %w", err)
	}

	e.mu.Lock()
	e.ruleDir = ruleDir
	e.state = NewRulesState(stateFile)
	e.mu.Unlock()

	seeded := e.state.Seeded()
	names, err := BuiltInRuleNames()
	if err != nil {
		return fmt.Errorf("读取内嵌规则失败: %w", err)
	}

	var newly []string
	for _, name := range names {
		if seeded[name] {
			continue // 已播种过：用户改过、删过都不再动它
		}
		dest := filepath.Join(ruleDir, name)
		if _, err := os.Stat(dest); err == nil {
			newly = append(newly, name) // 目录里已有同名文件 → 只登记，不覆盖
			continue
		}
		data, err := ReadBuiltInRule(name)
		if err != nil {
			log.Printf("[Engine] 读取内嵌规则 %s 失败: %v", name, err)
			continue
		}
		if err := os.WriteFile(dest, data, 0644); err != nil {
			log.Printf("[Engine] 播种规则 %s 失败: %v", dest, err)
			continue
		}
		log.Printf("[Engine] 播种内置规则: %s", name)
		newly = append(newly, name)
	}
	e.state.MarkSeeded(newly)

	return e.Reload()
}

// Reload 重新从规则目录加载规则，并按启用状态重建匹配列表（热生效，无需重启）
func (e *Engine) Reload() error {
	e.mu.RLock()
	dir := e.ruleDir
	e.mu.RUnlock()
	if dir == "" {
		return fmt.Errorf("规则目录未初始化")
	}

	rules, errs, err := LoadSigmaDirDetailed(dir)
	if err != nil {
		return fmt.Errorf("加载规则失败: %w", err)
	}

	e.mu.Lock()
	e.all = rules
	e.errs = errs
	enabled := make([]*SigmaRule, 0, len(rules))
	for _, r := range rules {
		if e.state.IsEnabled(r.FileName) {
			enabled = append(enabled, r)
		}
	}
	e.rules = enabled
	e.loaded = true
	e.mu.Unlock()

	for _, le := range errs {
		log.Printf("[Engine] 规则解析失败 %s: %s", le.File, le.Error)
	}
	log.Printf("[Engine] 已加载 %d 条规则（启用 %d）", len(rules), len(enabled))
	for _, r := range enabled {
		log.Printf("[Engine]   ├─ %s", r.RuleInfo())
	}
	return nil
}

// RulesInfo 返回全部规则元数据（含禁用）+ 解析失败清单
func (e *Engine) RulesInfo() ([]RuleMeta, []RuleLoadError) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	metas := make([]RuleMeta, 0, len(e.all))
	for _, r := range e.all {
		metas = append(metas, RuleMeta{
			File:        r.FileName,
			Title:       r.Title,
			ID:          r.ID,
			Level:       r.Level,
			Category:    r.LogSource.Category,
			Description: r.Description,
			Tags:        r.Tags,
			Builtin:     e.state.IsBuiltin(r.FileName),
			Enabled:     e.state.IsEnabled(r.FileName),
		})
	}
	errs := append([]RuleLoadError(nil), e.errs...)
	return metas, errs
}

// SetEnabled 启用/禁用一条规则（立即生效）
func (e *Engine) SetEnabled(file string, on bool) error {
	name, err := SafeRuleFileName(file)
	if err != nil {
		return err
	}
	if !e.ruleExists(name) {
		return fmt.Errorf("规则不存在: %s", name)
	}
	e.state.SetEnabled(name, on)
	return e.Reload()
}

// SaveRule 新建/覆盖一条规则（先解析校验，通过才落盘）
func (e *Engine) SaveRule(file, content string) error {
	name, err := SafeRuleFileName(file)
	if err != nil {
		return err
	}
	if _, err := ParseSigmaBytes([]byte(content)); err != nil {
		return err
	}

	e.mu.RLock()
	dir := e.ruleDir
	e.mu.RUnlock()
	if dir == "" {
		return fmt.Errorf("规则目录未初始化")
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		return fmt.Errorf("写入规则失败: %w", err)
	}
	log.Printf("[Engine] 已保存规则: %s", name)
	return e.Reload()
}

// DeleteRule 删除一条自定义规则（内置规则只允许禁用，不允许删除）
func (e *Engine) DeleteRule(file string) error {
	name, err := SafeRuleFileName(file)
	if err != nil {
		return err
	}
	if e.state.IsBuiltin(name) {
		return fmt.Errorf("内置规则不允许删除，请停用")
	}
	if !e.ruleExists(name) {
		return fmt.Errorf("规则不存在: %s", name)
	}

	e.mu.RLock()
	dir := e.ruleDir
	e.mu.RUnlock()

	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("删除规则失败: %w", err)
	}
	e.state.Forget(name)
	log.Printf("[Engine] 已删除规则: %s", name)
	return e.Reload()
}

// RuleYAML 读取规则原文（规则页查看/编辑用）
func (e *Engine) RuleYAML(file string) (string, error) {
	name, err := SafeRuleFileName(file)
	if err != nil {
		return "", err
	}
	e.mu.RLock()
	dir := e.ruleDir
	e.mu.RUnlock()
	if dir == "" {
		return "", fmt.Errorf("规则目录未初始化")
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", fmt.Errorf("规则文件不存在: %s", name)
	}
	return string(data), nil
}

func (e *Engine) ruleExists(name string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, r := range e.all {
		if r.FileName == name {
			return true
		}
	}
	return false
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
			if s, ok := patternString(item); ok && wildcardMatch(eventValue, s) {
				return true
			}
		}
		return false
	default:
		// YAML 标量（如 remote_port: 4444 写成整数）→ 统一转字符串比较。
		// 事件字段一律是字符串，规则里写整数曾被静默跳过 → 规则永不出告警。
		if s, ok := patternString(pattern); ok {
			return wildcardMatch(eventValue, s)
		}
		return false
	}
}

// patternString 把规则里的 YAML 标量统一转成字符串（string / int / float / bool）
func patternString(v interface{}) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case int:
		return strconv.Itoa(t), true
	case int64:
		return strconv.FormatInt(t, 10), true
	case uint64:
		return strconv.FormatUint(t, 10), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	}
	return "", false
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
