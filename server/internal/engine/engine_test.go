package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── 字段匹配 ──────────────────────────────────────────────

// 规则里的 YAML 标量可能是字符串也可能是整数（如 remote_port: 4444），
// 事件侧字段一律是字符串；整数模式曾被静默跳过 → 规则永不出告警。
func TestFieldMatchPatternTypes(t *testing.T) {
	cases := []struct {
		value   string
		pattern interface{}
		want    bool
	}{
		{"4444", "4444", true},
		{"4444", 4444, true},
		{"4444", []interface{}{4444, 5555}, true},
		{"4444", []interface{}{6666, 7777}, false},
		{"true", true, true},
		{"mimikatz.exe", "*mimikatz*", true},
		{`C:\Windows\evil.exe`, `C:\Windows\*`, true},
		{"notepad.exe", "*mimikatz*", false},
		{"powershell.exe", "*powershell*", true},
	}
	for _, c := range cases {
		if got := fieldMatch(c.value, c.pattern); got != c.want {
			t.Errorf("fieldMatch(%q, %#v) = %v, 期望 %v", c.value, c.pattern, got, c.want)
		}
	}
}

// ── 内置规则：播种 + 命中 ─────────────────────────────────

func netEvent(remotePort, processName string) map[string]string {
	return map[string]string{
		"event_type":     "network_connection",
		"event_category": "network",
		"remote_ip":      "10.10.10.10",
		"remote_port":    remotePort,
		"process_name":   processName,
		"hostname":       "WIN-TEST",
		"@timestamp":     "2026-09-24T10:00:00Z",
	}
}

func procEvent(image, cmdline string) map[string]string {
	return map[string]string{
		"event_type":     "process_create",
		"event_category": "process",
		"image_path":     image,
		"command_line":   cmdline,
		"hostname":       "WIN-TEST",
		"@timestamp":     "2026-09-24T10:00:01Z",
	}
}

func hasRule(rules []*SigmaRule, title string) bool {
	for _, r := range rules {
		if r.Title == title {
			return true
		}
	}
	return false
}

func newTestEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	dir := t.TempDir()
	eng := New(nil)
	if err := eng.InitRules(dir, filepath.Join(dir, "rules.json")); err != nil {
		t.Fatalf("InitRules 失败: %v", err)
	}
	return eng, dir
}

func TestInitRulesSeedsBuiltinRules(t *testing.T) {
	eng, dir := newTestEngine(t)

	names, _ := BuiltInRuleNames()
	metas, errs := eng.RulesInfo()
	if len(errs) != 0 {
		t.Fatalf("内置规则不应解析失败: %v", errs)
	}
	if len(metas) != len(names) {
		t.Fatalf("规则数 = %d, 期望 %d", len(metas), len(names))
	}
	for _, m := range metas {
		if !m.Builtin {
			t.Errorf("%s 应标记为内置", m.File)
		}
		if !m.Enabled {
			t.Errorf("%s 默认应启用", m.File)
		}
		if _, err := os.Stat(filepath.Join(dir, m.File)); err != nil {
			t.Errorf("规则文件未播种: %v", err)
		}
	}
	if eng.RuleCount() != len(names) {
		t.Errorf("启用规则数 = %d, 期望 %d", eng.RuleCount(), len(names))
	}
}

// 反向 Shell 规则：remote_port 曾是整数列表 → 规则永不命中（本次修复点）
func TestReverseShellRuleActuallyFires(t *testing.T) {
	eng, _ := newTestEngine(t)

	if got := eng.Eval(netEvent("4444", "powershell.exe")); !hasRule(got, "反向 Shell 检测") {
		t.Fatalf("反向 Shell 规则未命中，命中规则: %v", ruleTitles(got))
	}
	// 端口对但进程无害 → 不命中
	if got := eng.Eval(netEvent("4444", "chrome.exe")); hasRule(got, "反向 Shell 检测") {
		t.Error("非可疑进程不应命中反向 Shell 规则")
	}
	// 进程可疑但端口不匹配 → 不命中
	if got := eng.Eval(netEvent("80", "powershell.exe")); hasRule(got, "反向 Shell 检测") {
		t.Error("普通端口不应命中反向 Shell 规则")
	}
}

func TestProcessRuleMatches(t *testing.T) {
	eng, _ := newTestEngine(t)
	got := eng.Eval(procEvent(`C:\tools\mimikatz.exe`, `mimikatz.exe "sekurlsa::logonpasswords"`))
	if !hasRule(got, "Mimikatz 检测") {
		t.Fatalf("Mimikatz 规则未命中，命中规则: %v", ruleTitles(got))
	}
	if got := eng.Eval(procEvent(`C:\Windows\notepad.exe`, "notepad.exe a.txt")); len(got) != 0 {
		t.Errorf("正常进程不应命中任何规则，实际: %v", ruleTitles(got))
	}
}

func ruleTitles(rules []*SigmaRule) []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.Title)
	}
	return out
}

// ── 启停（热生效 + 持久化）────────────────────────────────

func TestSetEnabledTakesEffectAndPersists(t *testing.T) {
	eng, dir := newTestEngine(t)
	ev := netEvent("4444", "powershell.exe")

	if err := eng.SetEnabled("reverse-shell.yml", false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if got := eng.Eval(ev); hasRule(got, "反向 Shell 检测") {
		t.Fatal("停用后规则仍在生效")
	}

	// 重启（新建引擎读同一目录/状态文件）→ 停用状态保持
	eng2 := New(nil)
	if err := eng2.InitRules(dir, filepath.Join(dir, "rules.json")); err != nil {
		t.Fatalf("重启加载失败: %v", err)
	}
	if got := eng2.Eval(ev); hasRule(got, "反向 Shell 检测") {
		t.Fatal("停用状态未持久化")
	}

	// 重新启用 → 立即生效
	if err := eng2.SetEnabled("reverse-shell.yml", true); err != nil {
		t.Fatalf("启用失败: %v", err)
	}
	if got := eng2.Eval(ev); !hasRule(got, "反向 Shell 检测") {
		t.Fatal("重新启用后规则未生效")
	}
}

// ── 增删改 ────────────────────────────────────────────────

const customRule = `title: 测试规则 — notepad
id: 11111111-2222-3333-4444-555555555555
description: 单测用自定义规则
level: low
logsource:
  category: process
detection:
  selection:
    image_path: '*notepad.exe'
  condition: selection
`

func TestSaveDeleteCustomRule(t *testing.T) {
	eng, dir := newTestEngine(t)

	// 上传自定义规则 → 落盘 + 立即生效
	if err := eng.SaveRule("test-custom.yml", customRule); err != nil {
		t.Fatalf("保存规则失败: %v", err)
	}
	if got := eng.Eval(procEvent(`C:\Windows\notepad.exe`, "notepad.exe")); !hasRule(got, "测试规则 — notepad") {
		t.Fatalf("自定义规则未生效，命中: %v", ruleTitles(got))
	}
	metas, _ := eng.RulesInfo()
	found := false
	for _, m := range metas {
		if m.File == "test-custom.yml" {
			found = true
			if m.Builtin {
				t.Error("自定义规则不应标记为内置")
			}
		}
	}
	if !found {
		t.Fatal("规则列表中没有自定义规则")
	}

	// 删除自定义规则 → 立即失效
	if err := eng.DeleteRule("test-custom.yml"); err != nil {
		t.Fatalf("删除规则失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "test-custom.yml")); !os.IsNotExist(err) {
		t.Error("规则文件未被删除")
	}
	if got := eng.Eval(procEvent(`C:\Windows\notepad.exe`, "notepad.exe")); hasRule(got, "测试规则 — notepad") {
		t.Error("删除后规则仍在生效")
	}
}

func TestSaveRuleValidation(t *testing.T) {
	eng, dir := newTestEngine(t)

	bad := []struct{ file, yaml, wantErr string }{
		{"no-title.yml", "id: x\ndetection:\n  selection:\n    image_path: '*a*'\n  condition: selection\n", "title"},
		{"no-condition.yml", "title: 缺条件\ndetection:\n  selection:\n    image_path: '*a*'\n", "condition"},
		{"no-selection.yml", "title: 缺选择\ndetection:\n  condition: selection\n", "selection"},
		{"broken.yml", "title: [未闭合\n", "YAML"},
		{"../evil.yml", customRule, "路径分隔符"},
		{"bad-ext.txt", customRule, "后缀"},
	}
	for _, c := range bad {
		err := eng.SaveRule(c.file, c.yaml)
		if err == nil {
			t.Errorf("SaveRule(%q) 应失败", c.file)
			continue
		}
		if !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("SaveRule(%q) 错误 = %q, 期望包含 %q", c.file, err.Error(), c.wantErr)
		}
	}

	// 校验失败不得落盘（含目录穿越的 ../evil.yml）
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.yml")); !os.IsNotExist(err) {
		t.Error("非法规则文件被写出到规则目录之外")
	}
	names, _ := BuiltInRuleNames()
	builtin := make(map[string]bool, len(names))
	for _, n := range names {
		builtin[n] = true
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".yml") && !builtin[e.Name()] {
			t.Errorf("校验失败的规则被落盘: %s", e.Name())
		}
	}
}

func TestBuiltinRuleCannotBeDeleted(t *testing.T) {
	eng, dir := newTestEngine(t)
	if err := eng.DeleteRule("mimikatz.yml"); err == nil {
		t.Fatal("内置规则应拒绝删除")
	}
	if _, err := os.Stat(filepath.Join(dir, "mimikatz.yml")); err != nil {
		t.Fatalf("内置规则文件不应被删除: %v", err)
	}
}

// 播种只做一次：用户改过的内置规则在重启后不被覆盖
func TestSeedingDoesNotOverwriteUserEdits(t *testing.T) {
	eng, dir := newTestEngine(t)
	_ = eng

	edited := "title: 用户改过的规则\nid: user-edit\ndetection:\n  selection:\n    image_path: '*custom*'\n  condition: selection\n"
	path := filepath.Join(dir, "mimikatz.yml")
	if err := os.WriteFile(path, []byte(edited), 0644); err != nil {
		t.Fatal(err)
	}

	eng2 := New(nil)
	if err := eng2.InitRules(dir, filepath.Join(dir, "rules.json")); err != nil {
		t.Fatalf("重启失败: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != edited {
		t.Fatal("重启后被播种覆盖了用户修改")
	}
	// 删除内置规则文件后重启，也不应被重新播种（seeded 名单已记录）
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	eng3 := New(nil)
	if err := eng3.InitRules(dir, filepath.Join(dir, "rules.json")); err != nil {
		t.Fatalf("重启失败: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("用户删除的内置规则被重新播种复活")
	}
}
