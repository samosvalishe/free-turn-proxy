package clientsdb

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestClientIDRoundTrip(t *testing.T) {
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
	serverConn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer func() { _ = serverConn.Close() }()

	udpAddr, ok := serverConn.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("LocalAddr is not *net.UDPAddr")
	}
	clientConn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer func() { _ = clientConn.Close() }()

	expectedID := "client-test-uuid-123"

	go func() {
		if werr := WriteClientID(clientConn, expectedID, ModeTCP); werr != nil {
			t.Errorf("WriteClientID failed: %v", werr)
		}
	}()

	readID, mode, err := ReadClientID(serverConn)
	if err != nil {
		t.Fatalf("ReadClientID: %v", err)
	}

	if readID != expectedID {
		t.Errorf("expected %q, got %q", expectedID, readID)
	}
	if mode != ModeTCP {
		t.Errorf("mode = %d, want %d", mode, ModeTCP)
	}
}

// Клиент старше тега режима слал ровно 1+len байт - такую запись читатель обязан принять.
func TestReadClientIDLegacyRecordHasNoMode(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()

	const id = "legacy-client"
	go func() {
		buf := append([]byte{byte(len(id))}, id...) //nolint:gosec // длина константы заведомо ≤255
		_, _ = clientConn.Write(buf)
	}()

	readID, mode, err := ReadClientID(serverConn)
	if err != nil {
		t.Fatalf("ReadClientID: %v", err)
	}
	if readID != id {
		t.Errorf("id = %q, want %q", readID, id)
	}
	if mode != ModeUnset {
		t.Errorf("mode = %d, want ModeUnset", mode)
	}
}
