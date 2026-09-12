package logx

import "strings"

type prefixedLogger struct {
	Logger
	prefix string
}

// WithPrefix привязывает источник к логгеру, не к изменяемому глобальному состоянию.
func WithPrefix(logger Logger, prefix string) Logger {
	return &prefixedLogger{Logger: OrNop(logger), prefix: strings.ReplaceAll(prefix, "%", "%%")}
}

func (l *prefixedLogger) Debugf(format string, v ...any) {
	if l.DebugEnabled() {
		l.Logger.Debugf(l.prefix+format, v...)
	}
}

func (l *prefixedLogger) Infof(format string, v ...any)  { l.Logger.Infof(l.prefix+format, v...) }
func (l *prefixedLogger) Warnf(format string, v ...any)  { l.Logger.Warnf(l.prefix+format, v...) }
func (l *prefixedLogger) Errorf(format string, v ...any) { l.Logger.Errorf(l.prefix+format, v...) }
