package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/samosvalishe/free-turn-proxy/internal/config"
	"github.com/samosvalishe/free-turn-proxy/internal/logx"
)

type diagnosticLog struct {
	logx.Logger
	mu    sync.Mutex
	lines []string
}

func (l *diagnosticLog) Infof(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func TestRelayDiagnosticsKeepSessionIdentity(t *testing.T) {
	log := &diagnosticLog{Logger: logx.Nop()}
	first, err := New(&config.Client{}, Deps{Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(&config.Client{}, Deps{Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("test stop")
	for _, s := range []*Session{first, second} {
		if err := s.relayLoop(t.Context(), func(context.Context) error { return failure }); !errors.Is(err, failure) {
			t.Fatalf("relay result = %v", err)
		}
	}
	first.deps.Logger.Infof("late event")
	if len(log.lines) != 5 {
		t.Fatalf("events = %v", log.lines)
	}
	prefix := func(line string) string { return strings.SplitN(line, "]", 2)[0] }
	if prefix(log.lines[0]) == prefix(log.lines[2]) || prefix(log.lines[0]) != prefix(log.lines[4]) {
		t.Fatalf("session identity lost: %v", log.lines)
	}
	for _, i := range []int{1, 3} {
		if !strings.Contains(log.lines[i], "generation=1 reason=finished elapsed=") {
			t.Fatalf("missing attempt result: %s", log.lines[i])
		}
	}
}
