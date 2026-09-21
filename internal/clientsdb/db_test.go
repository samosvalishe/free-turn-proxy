package clientsdb

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cbeuw/connutil"
)

func TestFailedSavePreservesAuthorization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clients.json")
	db, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Add("existing", "original"); err != nil {
		t.Fatal(err)
	}
	// Каталог вместо временного файла воспроизводит отказ записи без прав root.
	if err = os.Mkdir(path+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	operations := []struct {
		name string
		run  func() error
	}{
		{"add", func() error { return db.Add("new", "new") }},
		{"update", func() error { return db.Add("existing", "changed") }},
		{"remove", func() error { return db.Remove("existing") }},
	}
	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			if runErr := op.run(); runErr == nil {
				t.Fatal("save unexpectedly succeeded")
			}
			if !db.IsAuthorized("existing") || db.IsAuthorized("new") {
				t.Fatal("failed save changed authorization")
			}
			if got := db.List(); len(got) != 1 || got["existing"].Comment != "original" {
				t.Fatalf("failed save changed clients: %+v", got)
			}
		})
	}
	loaded, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.List(); len(got) != 1 || got["existing"].Comment != "original" {
		t.Fatalf("disk changed: %+v", got)
	}
}

func TestClientsDB(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "clients.json")

	db, err := New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create db: %v", err)
	}

	if err = db.Add("client-123", "Test 1"); err != nil {
		t.Fatalf("Failed to add client: %v", err)
	}

	if !db.IsAuthorized("client-123") {
		t.Errorf("Expected client-123 to be authorized")
	}

	if db.IsAuthorized("client-456") {
		t.Errorf("Expected client-456 not to be authorized")
	}

	if err = db.Remove("client-123"); err != nil {
		t.Fatalf("Failed to remove client: %v", err)
	}

	if db.IsAuthorized("client-123") {
		t.Errorf("Expected client-123 to be removed")
	}

	_ = db.Add("client-789", "Test Persistence")

	db2, err := New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create db2: %v", err)
	}

	if !db2.IsAuthorized("client-789") {
		t.Errorf("Expected client-789 to be persisted")
	}

	db2.mu.Lock()
	db2.lastModified = db2.lastModified.Add(-1 * time.Second)
	db2.mu.Unlock()

	_ = db.Add("client-999", "Hot reload test")
	db2.loadIfModified()

	if !db2.IsAuthorized("client-999") {
		t.Errorf("Expected client-999 to be loaded via hot reload")
	}
}

// dropWrites теряет первые n записей - модель потерь датаграмм.
type dropWrites struct {
	net.Conn
	n int
}

func (d *dropWrites) Write(b []byte) (int, error) {
	if d.n > 0 {
		d.n--
		return len(b), nil
	}
	return d.Conn.Write(b)
}

const testID = "e8030ba252344e536664d96f4544d64d"

func accept(t *testing.T, conn net.Conn) (string, byte, net.Conn, error) {
	t.Helper()
	var gotMode byte
	id, data, err := AcceptClientID(conn, func(_ string, mode byte) error {
		gotMode = mode
		return nil
	})
	return id, gotMode, data, err
}

func TestClientIDRoundTrip(t *testing.T) {
	client, server := pipe()
	errc := make(chan error, 1)
	go func() { errc <- WriteClientID(t.Context(), client, testID, ModeTCP) }()

	id, mode, _, err := accept(t, server)
	if err != nil || id != testID || mode != ModeTCP {
		t.Fatalf("accept = %q/%d/%v, want %q/%d", id, mode, err, testID, ModeTCP)
	}
	if err := <-errc; err != nil {
		t.Fatalf("WriteClientID: %v", err)
	}
}

// Потеря первой ID-записи: клиент повторяет по таймеру, сессия поднимается.
func TestClientIDSurvivesLostRecord(t *testing.T) {
	client, server := pipe()
	errc := make(chan error, 1)
	go func() { errc <- WriteClientID(t.Context(), &dropWrites{Conn: client, n: 1}, testID, ModeUDP) }()

	if id, _, _, err := accept(t, server); err != nil || id != testID {
		t.Fatalf("accept = %q/%v", id, err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("WriteClientID: %v", err)
	}
}

func TestClientIDSurvivesLostAck(t *testing.T) {
	client, server := pipe()
	errc := make(chan error, 1)
	go func() {
		if err := WriteClientID(t.Context(), client, testID, ModeUDP); err != nil {
			errc <- err
			return
		}
		_, err := client.Write([]byte("payload"))
		errc <- err
	}()

	_, _, data, err := accept(t, &dropWrites{Conn: server, n: 1})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	buf := make([]byte, 64)
	n, err := data.Read(buf)
	if err != nil || string(buf[:n]) != "payload" {
		t.Fatalf("data = %q/%v, want payload", buf[:n], err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("client: %v", err)
	}
}

// Клиенты старше подтверждения: без режима ([len][id]) и с режимом ([len][id][mode]).
// Им сервер не отвечает и отдаёт соединение как есть - поведение до подтверждения.
func TestAcceptLegacyRecordNoAck(t *testing.T) {
	head := append([]byte{byte(len(testID))}, testID...)
	for _, tc := range []struct {
		name string
		rec  []byte
		mode byte
	}{
		{"no mode", head, ModeUnset},
		{"with mode", append(append([]byte{}, head...), ModeUDP), ModeUDP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := pipe()
			if _, err := client.Write(tc.rec); err != nil {
				t.Fatal(err)
			}
			id, mode, data, err := accept(t, server)
			if err != nil || id != testID || mode != tc.mode || data != server {
				t.Fatalf("accept = %q/%d/%v, conn untouched=%v", id, mode, err, data == server)
			}
			_ = client.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			if n, err := client.Read(make([]byte, 4)); err == nil {
				t.Fatalf("legacy client got %d bytes of ack", n)
			}
		})
	}
}

func TestAcceptRejectsWithoutAck(t *testing.T) {
	client, server := pipe()
	if _, err := client.Write(idRecord(testID, ModeUDP)); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("denied")
	if _, _, err := AcceptClientID(server, func(string, byte) error { return denied }); !errors.Is(err, denied) {
		t.Fatalf("err = %v, want denied", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if n, err := client.Read(make([]byte, 4)); err == nil {
		t.Fatalf("rejected client got %d bytes of ack", n)
	}
}

// pipe - датаграммная пара; таймаут отдаётся как у DTLS (net.Error), а не своей ошибкой connutil.
func pipe() (net.Conn, net.Conn) {
	a, b := connutil.AsyncPacketPipe()
	return &netTimeout{a}, &netTimeout{b}
}

type netTimeout struct{ net.Conn }

func (c *netTimeout) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if errors.Is(err, connutil.ErrTimeout) {
		err = os.ErrDeadlineExceeded
	}
	return n, err
}
