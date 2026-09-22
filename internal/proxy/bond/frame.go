// Package bond объединяет smux-потоки разных сессий в одно TCP-соединение.
package bond

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	MaxLanes          = 256
	SetupTimeout      = 10 * time.Second
	maxChunk          = 16 * 1024
	pendingCap        = 256
	frameData    byte = 1
	frameFIN     byte = 2
)

var ErrProtocol = errors.New("bond: invalid protocol")

// Сохранён формат VLB1: magic(4), version(1), connection(8), index(2), count(2); big endian.
type Hello struct {
	ID    uint64
	Index uint16
	Count uint16
}

func WriteHello(w io.Writer, h Hello) error {
	var b [17]byte
	copy(b[:], "VLB1")
	b[4] = 1
	binary.BigEndian.PutUint64(b[5:13], h.ID)
	binary.BigEndian.PutUint16(b[13:15], h.Index)
	binary.BigEndian.PutUint16(b[15:17], h.Count)
	return writeFull(w, b[:])
}

func ReadHello(r io.Reader) (Hello, error) {
	var b [17]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return Hello{}, fmt.Errorf("bond hello: %w", err)
	}
	h := Hello{ID: binary.BigEndian.Uint64(b[5:13]), Index: binary.BigEndian.Uint16(b[13:15]), Count: binary.BigEndian.Uint16(b[15:17])}
	if string(b[:4]) != "VLB1" || b[4] != 1 || h.Count == 0 || h.Count > MaxLanes || h.Index >= h.Count {
		return Hello{}, fmt.Errorf("%w: hello", ErrProtocol)
	}
	return h, nil
}

// Кадр VLB1: type(1), sequence(8), size(4), payload(size); FIN обозначает следующий sequence.
type frame struct {
	typ  byte
	seq  uint64
	data []byte
}

func writeFrame(w io.Writer, typ byte, seq uint64, data []byte) error {
	var b [13]byte
	b[0] = typ
	binary.BigEndian.PutUint64(b[1:9], seq)
	binary.BigEndian.PutUint32(b[9:13], uint32(len(data))) //nolint:gosec // payload ограничен maxChunk
	if err := writeFull(w, b[:]); err != nil {
		return err
	}
	return writeFull(w, data)
}

func readFrame(r io.Reader) (frame, error) {
	var b [13]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return frame{}, fmt.Errorf("bond frame: %w", err)
	}
	size := binary.BigEndian.Uint32(b[9:13])
	f := frame{typ: b[0], seq: binary.BigEndian.Uint64(b[1:9])}
	if (f.typ != frameData && f.typ != frameFIN) || size > maxChunk || (f.typ == frameFIN && size != 0) {
		return frame{}, fmt.Errorf("%w: frame", ErrProtocol)
	}
	f.data = make([]byte, size)
	if _, err := io.ReadFull(r, f.data); err != nil {
		return frame{}, fmt.Errorf("bond payload: %w", err)
	}
	return f, nil
}

func writeFull(w io.Writer, b []byte) error {
	if len(b) == 0 {
		return nil
	}
	n, err := w.Write(b)
	if err != nil {
		return fmt.Errorf("bond write: %w", err)
	}
	if n != len(b) {
		return fmt.Errorf("bond write: %w", io.ErrShortWrite)
	}
	return nil
}
