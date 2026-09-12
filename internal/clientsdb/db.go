package clientsdb

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"sync"
	"time"
)

type ClientInfo struct {
	Comment string `json:"comment,omitempty"`
}

type Data struct {
	Clients map[string]ClientInfo `json:"clients"`
}

type DB struct {
	mu           sync.RWMutex
	path         string
	data         Data
	lastModified time.Time
}

func New(path string) (*DB, error) {
	db := &DB{
		path: path,
		data: Data{Clients: make(map[string]ClientInfo)},
	}

	if err := db.load(); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
	}

	return db, nil
}

func (db *DB) StartHotReload(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			db.loadIfModified()
		}
	}()
}

func (db *DB) IsAuthorized(clientID string) bool {
	db.mu.RLock()
	defer db.mu.RUnlock()
	_, ok := db.data.Clients[clientID]
	return ok
}

func (db *DB) Add(clientID, comment string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	next := Data{Clients: maps.Clone(db.data.Clients)}
	next.Clients[clientID] = ClientInfo{Comment: comment}
	return db.save(next)
}

func (db *DB) Remove(clientID string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	next := Data{Clients: maps.Clone(db.data.Clients)}
	delete(next.Clients, clientID)
	return db.save(next)
}

func (db *DB) List() map[string]ClientInfo {
	db.mu.RLock()
	defer db.mu.RUnlock()

	res := make(map[string]ClientInfo)
	for k, v := range db.data.Clients {
		res[k] = v
	}
	return res
}

func (db *DB) load() error {
	stat, err := os.Stat(db.path)
	if err != nil {
		return err
	}

	b, err := os.ReadFile(db.path)
	if err != nil {
		return err
	}

	var d Data
	if err := json.Unmarshal(b, &d); err != nil {
		return fmt.Errorf("failed to parse %s: %w", db.path, err)
	}

	if d.Clients == nil {
		d.Clients = make(map[string]ClientInfo)
	}

	db.data = d
	db.lastModified = stat.ModTime()
	return nil
}

func (db *DB) loadIfModified() {
	stat, err := os.Stat(db.path)
	if err != nil {
		return
	}

	db.mu.RLock()
	modTime := db.lastModified
	db.mu.RUnlock()

	if stat.ModTime().After(modTime) {
		db.mu.Lock()
		_ = db.load()
		db.mu.Unlock()
	}
}

func (db *DB) save(next Data) error {
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}

	tmpFile := db.path + ".tmp"
	err = os.WriteFile(tmpFile, b, 0o600) // 0o600: файл содержит Client ID токены авторизации
	if err == nil {
		err = os.Rename(tmpFile, db.path)
	}
	if err == nil {
		// Авторизация меняется только после успешной замены файла.
		db.data = next
		stat, _ := os.Stat(db.path)
		if stat != nil {
			db.lastModified = stat.ModTime()
		}
	}
	return err
}

// Тег режима едет хвостом той же записи: клиент до этого поля его не писал, а читатель
// брал ровно 1+len байт - лишний байт старый сервер молча пропускает.
const (
	ModeUnset byte = 0
	ModeUDP   byte = 1
	ModeTCP   byte = 2
)

// WriteClientID отправляет Client ID (1 байт длины + строка + 1 байт режима).
func WriteClientID(conn net.Conn, clientID string, mode byte) error {
	b := []byte(clientID)
	if len(b) > 255 {
		b = b[:255]
	}
	buf := make([]byte, 1+len(b)+1)
	buf[0] = byte(len(b)) //nolint:gosec // len(b) усечён до ≤255 выше
	copy(buf[1:], b)
	buf[1+len(b)] = mode
	_, err := conn.Write(buf)
	return err
}

// ReadClientID читает Client ID из первой DTLS-записи. Режим ModeUnset - клиент старше
// тега, режим у него всегда udp.
func ReadClientID(conn net.Conn) (string, byte, error) {
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	buf := make([]byte, 257)
	n, err := conn.Read(buf)
	if err != nil {
		return "", ModeUnset, err
	}
	if n == 0 {
		return "", ModeUnset, nil
	}

	l := int(buf[0])
	if n < 1+l {
		return "", ModeUnset, io.ErrUnexpectedEOF
	}
	mode := ModeUnset
	if n > 1+l {
		mode = buf[1+l]
	}
	return string(buf[1 : 1+l]), mode, nil
}
