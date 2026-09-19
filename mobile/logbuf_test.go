package mobile

import "testing"

func TestSetLogBufferOffSkipsRing(t *testing.T) {
	t.Cleanup(func() { SetLogBuffer(true); ClearLogs() })

	l := &sinkLogger{buf: sharedLogBuf}
	SetLogBuffer(true)
	l.Infof("kept")
	if DumpLogs() == "" {
		t.Fatal("ring must collect lines by default")
	}

	SetLogBuffer(false)
	if DumpLogs() != "" {
		t.Fatal("disabling must drop collected lines")
	}
	l.Infof("dropped")
	if DumpLogs() != "" {
		t.Fatal("disabled ring must stay empty")
	}
}
