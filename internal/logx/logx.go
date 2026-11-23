// Package logx 提供带文件轮转和内存环缓冲的简单日志器。
package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	maxFileSize = 5 << 20 // 5MB
	ringSize    = 400
)

// Logger 同时写文件与内存环缓冲。
type Logger struct {
	mu   sync.Mutex
	file *os.File
	path string
	ring []string
}

// Open 打开（或创建）日志文件。
func Open(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	rotate(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Logger{file: f, path: path, ring: make([]string, 0, ringSize)}, nil
}

func rotate(path string) {
	st, err := os.Stat(path)
	if err != nil || st.Size() < maxFileSize {
		return
	}
	_ = os.Remove(path + ".1")
	_ = os.Rename(path, path+".1")
}

// Path 返回日志文件路径。
func (l *Logger) Path() string { return l.path }

func (l *Logger) write(level, format string, args ...any) {
	if l == nil {
		return
	}
	line := time.Now().Format("2006-01-02 15:04:05.000") + " [" + level + "] " + fmt.Sprintf(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.ring) >= ringSize {
		l.ring = append(l.ring[:0], l.ring[1:]...)
	}
	l.ring = append(l.ring, line)
	if l.file != nil {
		_, _ = l.file.WriteString(line + "\r\n")
	}
}

// Printf 记录普通事件。
func (l *Logger) Printf(format string, args ...any) { l.write("INFO", format, args...) }

// Errorf 记录错误。
func (l *Logger) Errorf(format string, args ...any) { l.write("ERROR", format, args...) }

// Lines 返回内存中最近若干行日志（旧→新）。
func (l *Logger) Lines() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.ring))
	copy(out, l.ring)
	return out
}

// Close 关闭日志文件。
func (l *Logger) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}
