// Package logx is a minimal leveled logger over stdlib log. Callers receive a
// Logger and call Debugf/Infof/Warnf/Errorf directly; Debugf is gated by a
// debug flag, the other levels always print via stdlib log.
package logx

import "log"

// Logger is the leveled-log interface used across the proxy. Debugf is gated by
// the constructor's debug flag; the other levels always print via stdlib log.
type Logger interface {
	Debugf(format string, v ...any)
	Infof(format string, v ...any)
	Warnf(format string, v ...any)
	Errorf(format string, v ...any)
	// DebugEnabled reports whether Debugf will produce output. Hot paths
	// (stats counters, condition-gated branches) use this to skip work the
	// logger would discard anyway.
	DebugEnabled() bool
}

type stdLogger struct {
	debug bool
}

// New returns a Logger that prints via stdlib log. If debug is false, Debugf is
// a no-op.
func New(debug bool) Logger {
	return &stdLogger{debug: debug}
}

// Nop returns a Logger whose every method discards its input. Useful in tests.
func Nop() Logger { return nopLogger{} }

func (l *stdLogger) Debugf(format string, v ...any) {
	if l.debug {
		log.Printf(format, v...)
	}
}
func (l *stdLogger) Infof(format string, v ...any)  { log.Printf(format, v...) }
func (l *stdLogger) Warnf(format string, v ...any)  { log.Printf(format, v...) }
func (l *stdLogger) Errorf(format string, v ...any) { log.Printf(format, v...) }
func (l *stdLogger) DebugEnabled() bool             { return l.debug }

type nopLogger struct{}

func (nopLogger) Debugf(string, ...any) {}
func (nopLogger) Infof(string, ...any)  {}
func (nopLogger) Warnf(string, ...any)  {}
func (nopLogger) Errorf(string, ...any) {}
func (nopLogger) DebugEnabled() bool    { return false }
