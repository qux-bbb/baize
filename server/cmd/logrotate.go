package main

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// logRotateMaxBytes 单个日志文件的大小上限，超过后把当前文件改名为
// <path>.<yyyyMMdd-HHmmss>，再新建同名文件继续写。
const logRotateMaxBytes int64 = 50 << 20 // 50 MB

// rotatingWriter 是按大小自动轮转的日志写入器。
//
// 设计取舍：
//   - 用「时间戳后缀」而不是 .1/.2 数字后缀：轮转只需一次改名，不必逐级搬动
//     N 个历史文件，失败路径更少，且文件名天然按时间排序。
//   - 不限制保留份数：只改名、从不删除历史日志，空间由使用者自行清理。
//   - 改名失败时继续写原文件，绝不因为轮转出错而丢日志。
//
// 并发：从 log.SetOutput 使用（log.Logger 自身已串行化），这里仍加锁兜底。
type rotatingWriter struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	size    int64
	f       *os.File
	closed  bool
}

func newRotatingWriter(path string, maxSize int64) (*rotatingWriter, error) {
	if maxSize <= 0 {
		return nil, fmt.Errorf("maxSize 必须 > 0")
	}
	w := &rotatingWriter{path: path, maxSize: maxSize}
	if err := w.open(); err != nil {
		return nil, err
	}
	// 启动时若已有文件已超限（例如升级前累积的老日志），先轮转一次，
	// 否则要再写满一个 maxSize 才会触发第一次轮转。
	w.rotateIfOversized()
	return w, nil
}

func (w *rotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w.f = f
	if fi, err := f.Stat(); err == nil {
		w.size = fi.Size()
	} else {
		w.size = 0
	}
	return nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.f == nil {
		return 0, os.ErrClosed
	}
	// size > 0 保证「单条日志本身就比上限还大」时不会反复轮转
	if w.size > 0 && w.size+int64(len(p)) > w.maxSize {
		w.rotate()
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.f == nil {
		return nil
	}
	return w.f.Close()
}

// rotateIfOversized 在启动时调用：已有的日志文件若已超限就先轮转。
func (w *rotatingWriter) rotateIfOversized() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size > w.maxSize {
		w.rotate()
	}
}

// rotate 关闭当前文件、改名、再新建同名文件。
// 调用方必须已持有 w.mu。任何一步失败都尽量把写入能力恢复回来。
func (w *rotatingWriter) rotate() {
	if w.f == nil {
		return
	}
	if err := w.f.Close(); err != nil {
		// 关不掉就恢复写入，避免把日志丢掉
		_ = w.open()
		return
	}
	w.f = nil

	base := fmt.Sprintf("%s.%s", w.path, time.Now().Format("20060102-150405"))
	target := w.uniqueName(base)
	if err := os.Rename(w.path, target); err != nil {
		// 改名失败（权限、被占用等）：重新打开原文件继续写
		_ = w.open()
		return
	}
	// 重新打开：open() 会重建文件并按实际大小重置 size
	if err := w.open(); err != nil {
		w.f = nil
	}
}

// uniqueName 在 base 已被占用时追加 -1、-2 … 直到拿到空闲名字。
// 同一秒内连续轮转（或手工留下了同名文件）时避免覆盖历史日志。
func (w *rotatingWriter) uniqueName(base string) string {
	name := base
	for i := 1; i <= 1000; i++ {
		if _, err := os.Stat(name); os.IsNotExist(err) {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return fmt.Sprintf("%s-%d", base, time.Now().UnixNano())
}
