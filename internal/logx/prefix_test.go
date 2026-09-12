package logx

import (
	"fmt"
	"testing"
)

type captureLogger struct {
	nopLogger
	lines []string
}

func (l *captureLogger) Infof(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func TestPrefixRemainsBoundToLogger(t *testing.T) {
	sink := &captureLogger{}
	old := WithPrefix(sink, "[old 100%] ")
	next := WithPrefix(sink, "[next] ")
	next.Infof("ready %d", 2)
	old.Infof("closed %d", 1)
	if sink.lines[0] != "[next] ready 2" || sink.lines[1] != "[old 100%] closed 1" {
		t.Fatalf("messages = %q", sink.lines)
	}
}

func TestPrefixNilLogger(t *testing.T) {
	l := WithPrefix(nil, "test ")
	l.Debugf("%d", 1)
	l.Infof("%d", 1)
	l.Warnf("%d", 1)
	l.Errorf("%d", 1)
	if l.DebugEnabled() {
		t.Fatal("nil logger enabled debug")
	}
}
