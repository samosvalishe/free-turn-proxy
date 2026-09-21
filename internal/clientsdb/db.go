package clientsdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
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

func (db *DB) StartHotReload(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				db.loadIfModified()
			}
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
	err = writeSync(tmpFile, b)
	if err == nil {
		err = os.Rename(tmpFile, db.path)
	}
	if err == nil {
		syncDir(filepath.Dir(db.path))
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

func writeSync(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // 0o600: файл содержит Client ID токены авторизации
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

const (
	ModeUnset byte = 0
	ModeUDP   byte = 1
	ModeTCP   byte = 2

	// idVersionAck - клиент ждёт подтверждения ID.
	idVersionAck byte = 2
	idAck        byte = 0x06

	idReadTimeout = 5 * time.Second
)

// ErrNoIDAck - сервер не подтвердил Client ID: он старше протокола подтверждения или
// канал теряет всё подряд.
var ErrNoIDAck = errors.New("clientsdb: server did not acknowledge client ID (server outdated?)")

// idRetransmit - таймеры повтора ID (RFC 6347 §4.2.4: старт 1 с, удвоение).
var idRetransmit = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

func idRecord(clientID string, mode byte) []byte {
	b := []byte(clientID)
	if len(b) > 255 {
		b = b[:255]
	}
	buf := make([]byte, 0, len(b)+3)
	buf = append(buf, byte(len(b))) //nolint:gosec // len(b) усечён до ≤255 выше
	buf = append(buf, b...)
	return append(buf, mode, idVersionAck)
}

func WriteClientID(ctx context.Context, conn net.Conn, clientID string, mode byte) error {
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	rec := idRecord(clientID, mode)
	var buf [16]byte
	for _, wait := range idRetransmit {
		if _, err := conn.Write(rec); err != nil {
			return fmt.Errorf("send client ID: %w", err)
		}
		if err := conn.SetReadDeadline(time.Now().Add(wait)); err != nil {
			return fmt.Errorf("client ID deadline: %w", err)
		}
		for {
			n, err := conn.Read(buf[:])
			if err == nil && n == 1 && buf[0] == idAck {
				return nil
			}
			if err != nil {
				var ne net.Error
				if !errors.As(err, &ne) || !ne.Timeout() {
					return fmt.Errorf("await client ID ack: %w", err)
				}
				break
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return ErrNoIDAck
}

func readClientID(conn net.Conn) (string, byte, byte, error) {
	_ = conn.SetReadDeadline(time.Now().Add(idReadTimeout))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	buf := make([]byte, 258)
	n, err := conn.Read(buf)
	if err != nil {
		return "", ModeUnset, 0, err
	}
	if n == 0 {
		return "", ModeUnset, 0, io.ErrUnexpectedEOF
	}
	l := int(buf[0])
	if n < 1+l {
		return "", ModeUnset, 0, io.ErrUnexpectedEOF
	}
	var mode, ver byte
	if n > 1+l {
		mode = buf[1+l]
	}
	if n > 2+l {
		ver = buf[2+l]
	}
	return string(buf[1 : 1+l]), mode, ver, nil
}

func AcceptClientID(conn net.Conn, authorize func(id string, mode byte) error) (string, net.Conn, error) {
	id, mode, ver, err := readClientID(conn)
	if err != nil {
		return "", nil, err
	}
	if err := authorize(id, mode); err != nil {
		return id, nil, err
	}
	if ver < idVersionAck {
		return id, conn, nil
	}
	if _, err := conn.Write([]byte{idAck}); err != nil {
		return id, nil, fmt.Errorf("ack client ID: %w", err)
	}
	return id, &ackedConn{Conn: conn, rec: idRecord(id, mode)}, nil
}

type ackedConn struct {
	net.Conn
	rec  []byte
	data bool
}

func (c *ackedConn) Read(b []byte) (int, error) {
	for {
		n, err := c.Conn.Read(b)
		if err != nil || c.data || !bytes.Equal(b[:n], c.rec) {
			c.data = c.data || err == nil
			return n, err
		}
		if _, err := c.Write([]byte{idAck}); err != nil {
			return 0, err
		}
	}
}
