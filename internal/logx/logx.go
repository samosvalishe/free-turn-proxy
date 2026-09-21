// Package logx предоставляет уровневый логгер поверх stdlib log.
package logx

import (
	"log"
	"sync/atomic"
)

// Logger - интерфейс уровневого логирования.
type Logger interface {
	Debugf(format string, v ...any)
	Infof(format string, v ...any)
	Warnf(format string, v ...any)
	Errorf(format string, v ...any)
	// DebugEnabled сообщает, включен ли уровень отладки.
	DebugEnabled() bool
}

type stdLogger struct {
	debug bool
}

func New(debug bool) Logger {
	return &stdLogger{debug: debug}
}

func Nop() Logger { return nopLogger{} }

func (l *stdLogger) Debugf(format string, v ...any) {
	if l.debug {
		log.Printf("[DEBUG] "+format, v...)
	}
}
func (*stdLogger) Infof(format string, v ...any)  { log.Printf("[INFO] "+format, v...) }
func (*stdLogger) Warnf(format string, v ...any)  { log.Printf("[WARN] "+format, v...) }
func (*stdLogger) Errorf(format string, v ...any) { log.Printf("[ERROR] "+format, v...) }
func (l *stdLogger) DebugEnabled() bool           { return l.debug }

// OrNop возвращает l или Nop, если l == nil.
func OrNop(l Logger) Logger {
	if l == nil {
		return Nop()
	}
	return l
}

type nopLogger struct{}

func (nopLogger) Debugf(string, ...any) {}
func (nopLogger) Infof(string, ...any)  {}
func (nopLogger) Warnf(string, ...any)  {}
func (nopLogger) Errorf(string, ...any) {}
func (nopLogger) DebugEnabled() bool    { return false }

// Holder хранит пакетный логгер, подменяемый на лету: ядро перезапускается в том же
// процессе (mobile), поэтому Set гонится с пишущими горутинами.
type Holder struct {
	ptr atomic.Pointer[Logger]
}

func (h *Holder) Set(l Logger) {
	v := OrNop(l)
	h.ptr.Store(&v)
}

func (h *Holder) Get() Logger {
	if p := h.ptr.Load(); p != nil {
		return *p
	}
	return Nop()
}
