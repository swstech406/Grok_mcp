// Package logx 提供可按开关启用的调试日志，避免在生产环境刷屏。
package logx

import (
	"log"
	"sync/atomic"
)

// DebugState 保存可在运行时安全切换的共享调试状态。
type DebugState struct {
	enabled atomic.Bool
}

// NewDebugState 创建共享调试状态。
func NewDebugState(enabled bool) *DebugState {
	debugState := &DebugState{}
	debugState.enabled.Store(enabled)
	return debugState
}

// Enabled 返回当前是否启用调试日志。
func (debugState *DebugState) Enabled() bool {
	return debugState != nil && debugState.enabled.Load()
}

// SetEnabled 原子更新调试日志开关。
func (debugState *DebugState) SetEnabled(enabled bool) {
	if debugState == nil {
		return
	}
	debugState.enabled.Store(enabled)
}

// Logger 在共享调试状态为 false 时忽略所有 Debugf 调用。
type Logger struct {
	debugState *DebugState
	prefix     string
}

// New 创建带 [prefix] 前缀的调试日志器。
func New(prefix string, enabled bool) *Logger {
	return NewWithDebugState(prefix, NewDebugState(enabled))
}

// NewWithDebugState 创建读取共享运行时调试状态的日志器。
func NewWithDebugState(prefix string, debugState *DebugState) *Logger {
	if debugState == nil {
		debugState = NewDebugState(false)
	}
	return &Logger{debugState: debugState, prefix: prefix}
}

// Debugf 等价于 log.Printf，仅在调试模式开启时输出。
func (l *Logger) Debugf(format string, args ...any) {
	if l == nil || !l.debugState.Enabled() {
		return
	}
	log.Printf("["+l.prefix+"] "+format, args...)
}

// Truncate 将字符串截断到 max 字节长度，超出部分以 "..." 结尾（用于日志脱敏/限长）。
func Truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
