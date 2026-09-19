package mobile

import (
	"strings"
	"sync"
	"sync/atomic"
)

// logBufMax - размер кольцевого буфера логов для DumpLogs.
const logBufMax = 500

type logBuffer struct {
	mu    sync.Mutex
	lines []string
}

func (b *logBuffer) append(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, line)
	if len(b.lines) > logBufMax {
		b.lines = b.lines[len(b.lines)-logBufMax:]
	}
}

func (b *logBuffer) get() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.lines, "\n")
}

func (b *logBuffer) clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = b.lines[:0]
}

var sharedLogBuf = &logBuffer{}

var logBufOff atomic.Bool

func SetLogBuffer(enabled bool) {
	logBufOff.Store(!enabled)
	if !enabled {
		sharedLogBuf.clear()
	}
}

func DumpLogs() string { return sharedLogBuf.get() }

func ClearLogs() { sharedLogBuf.clear() }
