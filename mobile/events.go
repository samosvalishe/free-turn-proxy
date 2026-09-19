package mobile

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/session"
)

const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// EventSink - приёмник событий сессии, реализуемый хостом (Java/ObjC).
// OnLog может вызываться конкурентно; методы не должны блокировать исполнение.
type EventSink interface {
	OnState(state string, streams, total int, errMsg string)
	OnLog(level, msg string, unixMillis int64)
	// OnCaptcha передаёт URL для решения капчи вручную (пустой url - закрыть окно).
	OnCaptcha(url string)
}

var sink atomic.Pointer[EventSink]

// SetEventSink регистрирует приёмник событий (nil отключает push-канал).
func SetEventSink(s EventSink) {
	if s == nil {
		sink.Store(nil)
		return
	}
	sink.Store(&s)
}

func currentSink() EventSink {
	if p := sink.Load(); p != nil {
		return *p
	}
	return nil
}

func emitCaptcha(url string) {
	if s := currentSink(); s != nil {
		s.OnCaptcha(url)
	}
}

type observer struct{}

func (observer) OnPhase(phase session.Phase, streams, total int, errMsg string) {
	if s := currentSink(); s != nil {
		s.OnState(string(phase), streams, total, errMsg)
	}
}

func coreLog() logx.Logger { return &sinkLogger{buf: sharedLogBuf} }

// sinkLogger дублирует логи в кольцевой буфер и EventSink.
type sinkLogger struct {
	debug bool
	buf   *logBuffer
}

func (l *sinkLogger) write(level, format string, v ...any) {
	msg := fmt.Sprintf(format, v...)
	now := time.Now()
	if !logBufOff.Load() {
		l.buf.append(now.Format("15:04:05") + " [" + level + "] " + msg)
	}
	if s := currentSink(); s != nil {
		s.OnLog(level, msg, now.UnixMilli())
	}
}

func (l *sinkLogger) Debugf(format string, v ...any) {
	if l.debug {
		l.write(LevelDebug, format, v...)
	}
}
func (l *sinkLogger) Infof(format string, v ...any)  { l.write(LevelInfo, format, v...) }
func (l *sinkLogger) Warnf(format string, v ...any)  { l.write(LevelWarn, format, v...) }
func (l *sinkLogger) Errorf(format string, v ...any) { l.write(LevelError, format, v...) }
func (l *sinkLogger) DebugEnabled() bool             { return l.debug }
