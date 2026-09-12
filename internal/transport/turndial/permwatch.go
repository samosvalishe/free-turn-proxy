package turndial

import (
	"strings"
	"sync"

	"github.com/pion/logging"
)

const (
	permFailMarker = "Failed to bind channel"
	permOKMarker   = "Channel binding successful"
	// Refresh сообщает об отказе раньше очередного ChannelBind.
	allocFailMarker = "Failed to refresh allocation"
	turncScope      = "turnc"
	// permFailThreshold - число последовательных провалов ChannelBind refresh до срабатывания onDead.
	permFailThreshold = 2
)

// permWatchFactory перехватывает логи pion-turnc для отслеживания сбоев ChannelBind.
type permWatchFactory struct {
	inner     logging.LoggerFactory
	onDead    func()
	threshold int
}

func (f *permWatchFactory) NewLogger(scope string) logging.LeveledLogger {
	inner := f.inner.NewLogger(scope)
	if scope != turncScope {
		return inner
	}
	return &permWatchLogger{LeveledLogger: inner, f: f}
}

type permWatchLogger struct {
	logging.LeveledLogger
	f     *permWatchFactory
	mu    sync.Mutex
	fails int
	fired bool
}

func (l *permWatchLogger) note(msg string) {
	switch {
	case strings.Contains(msg, allocFailMarker):
		l.mu.Lock()
		fire := !l.fired
		l.fired = true
		l.mu.Unlock()
		if fire && l.f.onDead != nil {
			l.f.onDead()
		}
	case strings.Contains(msg, permFailMarker):
		l.mu.Lock()
		l.fails++
		fire := !l.fired && l.fails >= l.f.threshold
		if fire {
			l.fired = true
		}
		l.mu.Unlock()
		if fire && l.f.onDead != nil {
			l.f.onDead()
		}
	case strings.Contains(msg, permOKMarker):
		l.mu.Lock()
		l.fails = 0
		l.mu.Unlock()
	}
}

func (l *permWatchLogger) Warn(msg string) {
	l.note(msg)
	l.LeveledLogger.Warn(msg)
}

func (l *permWatchLogger) Warnf(format string, args ...any) {
	l.note(format)
	l.LeveledLogger.Warnf(format, args...)
}

func (l *permWatchLogger) Debug(msg string) {
	l.note(msg)
	l.LeveledLogger.Debug(msg)
}

func (l *permWatchLogger) Debugf(format string, args ...any) {
	l.note(format)
	l.LeveledLogger.Debugf(format, args...)
}
