package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// rotatedNameRe 校验轮转文件名符合约定的 server.log.<yyyyMMdd-HHmmss>（同秒冲突时追加 -N）
var rotatedNameRe = regexp.MustCompile(`^server\.log\.\d{8}-\d{6}(-\d+)?$`)

func listLogFiles(t *testing.T, dir string) (active string, rotated []string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	for _, e := range entries {
		if e.Name() == "server.log" {
			active = filepath.Join(dir, e.Name())
			continue
		}
		if strings.HasPrefix(e.Name(), "server.log.") {
			if !rotatedNameRe.MatchString(e.Name()) {
				t.Errorf("轮转文件名不符合约定: %s", e.Name())
			}
			rotated = append(rotated, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(rotated)
	return active, rotated
}

// 未达到上限时不得轮转，且内容完整
func TestRotatingWriterNoRotationBelowLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.log")

	w, err := newRotatingWriter(path, 4096)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	const lines = 5
	for i := 0; i < lines; i++ {
		if _, err := w.Write([]byte("hello\n")); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	active, rotated := listLogFiles(t, dir)
	if len(rotated) != 0 {
		t.Fatalf("未超限不应轮转，实际产生 %d 个轮转文件", len(rotated))
	}
	data, err := os.ReadFile(active)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got, want := string(data), strings.Repeat("hello\n", lines); got != want {
		t.Fatalf("内容不一致\n got=%q\nwant=%q", got, want)
	}
}

// 超过上限时按大小轮转，且所有日志行一条不丢
func TestRotatingWriterRotatesAtLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.log")

	// 每行 11 字节，上限 50 字节 → 约每 5 行轮转一次
	w, err := newRotatingWriter(path, 50)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	const lines = 40
	var want []string
	for i := 0; i < lines; i++ {
		line := "0123456789\n"
		want = append(want, line)
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("写入第 %d 行失败: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	active, rotated := listLogFiles(t, dir)
	if len(rotated) == 0 {
		t.Fatal("超过上限却没有轮转")
	}

	// 按时间顺序（文件名排序）把所有文件拼起来，行序与内容必须与写入一致
	var gotLines []string
	for _, f := range append(rotated, active) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", f, err)
		}
		for _, l := range strings.SplitAfter(string(data), "\n") {
			if l != "" {
				gotLines = append(gotLines, l)
			}
		}
	}
	if len(gotLines) != len(want) {
		t.Fatalf("日志行数不一致: got=%d want=%d（轮转 %d 个文件）", len(gotLines), len(want), len(rotated))
	}
	for i := range want {
		if gotLines[i] != want[i] {
			t.Fatalf("第 %d 行不一致: got=%q want=%q", i, gotLines[i], want[i])
		}
	}

	// 每个文件都不应显著超过上限（允许最后一条日志超出）
	for _, f := range append(rotated, active) {
		fi, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Size() > 50+11 {
			t.Errorf("%s 体积 %d 超过上限过多", filepath.Base(f), fi.Size())
		}
	}
	t.Logf("轮转 %d 个文件，共 %d 行，无丢失", len(rotated), len(gotLines))
}

// 启动时若已有日志已超限，应立即轮转，而不是继续往巨型文件里追加
func TestRotatingWriterRotatesOversizedFileOnStartup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.log")

	if err := os.WriteFile(path, []byte(strings.Repeat("old\n", 100)), 0o644); err != nil {
		t.Fatalf("预置文件失败: %v", err)
	}

	w, err := newRotatingWriter(path, 64)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if _, err := w.Write([]byte("new\n")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	active, rotated := listLogFiles(t, dir)
	if len(rotated) != 1 {
		t.Fatalf("启动时应立即轮转一次，实际 %d 个轮转文件", len(rotated))
	}
	if data, _ := os.ReadFile(active); string(data) != "new\n" {
		t.Fatalf("新文件内容应为 new，实际 %q", string(data))
	}
	if data, _ := os.ReadFile(rotated[0]); !strings.Contains(string(data), "old") {
		t.Fatalf("旧内容应在轮转文件里保留，实际 %q", string(data))
	}
}

// 同一秒内连续轮转不得互相覆盖（补 -N 后缀）
func TestRotatingWriterUniqueNameOnCollision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.log")

	w, err := newRotatingWriter(path, 4)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	const lines = 12
	for i := 0; i < lines; i++ {
		if _, err := w.Write([]byte("ab\n")); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	active, rotated := listLogFiles(t, dir)
	if len(rotated) < 2 {
		t.Fatalf("应产生多个轮转文件，实际 %d", len(rotated))
	}
	total := 0
	for _, f := range append(rotated, active) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		total += strings.Count(string(data), "ab\n")
	}
	if total != lines {
		t.Fatalf("轮转文件互相覆盖导致丢日志: got=%d want=%d", total, lines)
	}
}
