// Package bondframe contains the wire format used by the VLESS bond multi-lane
// transport (hello + framed data/FIN). Both the client (initiator) and the
// server (acceptor) speak the same encoding.
package bondframe

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	Version uint8 = 1
	Magic         = "VLB1"

	FrameData byte = 1
	FrameFIN  byte = 2

	MaxChunk = 16 * 1024

	LaneAttachTimeout = 300 * time.Millisecond
)

// Hello is the per-lane handshake header sent right after a smux stream opens.
type Hello struct {
	ConnID    uint64
	LaneIndex uint16
	LaneCount uint16
}

// Frame is a single bonded data or FIN unit, identified by Seq within a ConnID.
type Frame struct {
	Type byte
	Seq  uint64
	Data []byte
}

// WriteHello encodes and writes a Hello to w using the bond wire format.
func WriteHello(w io.Writer, connID uint64, laneIndex, laneCount uint16) error {
	var hdr [17]byte
	copy(hdr[0:4], Magic)
	hdr[4] = Version
	binary.BigEndian.PutUint64(hdr[5:13], connID)
	binary.BigEndian.PutUint16(hdr[13:15], laneIndex)
	binary.BigEndian.PutUint16(hdr[15:17], laneCount)
	_, err := w.Write(hdr[:])
	return err
}

// ReadHelloAfterMagic finishes reading a Hello whose first 4 magic bytes have
// already been consumed (server pre-peeks the magic to multiplex protocols).
func ReadHelloAfterMagic(r io.Reader, magic [4]byte) (Hello, error) {
	var hdr [17]byte
	copy(hdr[0:4], magic[:])
	if _, err := io.ReadFull(r, hdr[4:]); err != nil {
		return Hello{}, err
	}
	return ParseHelloHeader(hdr[:])
}

// ParseHelloHeader decodes a 17-byte Hello header from hdr.
func ParseHelloHeader(hdr []byte) (Hello, error) {
	if len(hdr) != 17 {
		return Hello{}, fmt.Errorf("bad bond hello size: %d", len(hdr))
	}
	if string(hdr[0:4]) != Magic {
		return Hello{}, fmt.Errorf("bad bond magic")
	}
	if hdr[4] != Version {
		return Hello{}, fmt.Errorf("unsupported bond version: %d", hdr[4])
	}
	return Hello{
		ConnID:    binary.BigEndian.Uint64(hdr[5:13]),
		LaneIndex: binary.BigEndian.Uint16(hdr[13:15]),
		LaneCount: binary.BigEndian.Uint16(hdr[15:17]),
	}, nil
}

// WriteFrame writes a single Frame to w (header + payload).
func WriteFrame(w io.Writer, typ byte, seq uint64, data []byte) error {
	var hdr [13]byte
	hdr[0] = typ
	binary.BigEndian.PutUint64(hdr[1:9], seq)
	binary.BigEndian.PutUint32(hdr[9:13], uint32(len(data)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	_, err := w.Write(data)
	return err
}

// ReadFrame reads one Frame from r. Payloads over 4 MiB are rejected.
func ReadFrame(r io.Reader) (Frame, error) {
	var hdr [13]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	size := binary.BigEndian.Uint32(hdr[9:13])
	if size > MaxChunk {
		return Frame{}, fmt.Errorf("bond frame too large: %d", size)
	}
	f := Frame{
		Type: hdr[0],
		Seq:  binary.BigEndian.Uint64(hdr[1:9]),
	}
	if size > 0 {
		f.Data = make([]byte, size)
		if _, err := io.ReadFull(r, f.Data); err != nil {
			return Frame{}, err
		}
	}
	return f, nil
}

// PendingCap bounds the per-bond reorder buffer. A peer that emits seq with
// permanent gaps cannot grow it past this many frames.
const PendingCap = 1024

// ReorderHooks plugs caller-specific logging into Reorder. All fields may be nil.
type ReorderHooks struct {
	OnOverflow    func(have int)
	OnUnknownType func(typ byte)
	OnWriteError  func(err error)
	OnCloseWrite  func(format string, v ...any)
}

// Reorder consumes Frames from recv, writes payloads to dst in Seq order, and
// returns the number of fully delivered chunks. It returns when:
//   - the FIN seq has been reached (CloseWrite is called on dst);
//   - recv is closed;
//   - ctx is cancelled;
//   - an unknown frame type, pending overflow, or write error fires.
func Reorder(ctx context.Context, dst net.Conn, recv <-chan Frame, h ReorderHooks) uint64 {
	pending := make(map[uint64][]byte)
	var expect uint64
	var finSeq *uint64

	for {
		if finSeq != nil && expect == *finSeq {
			CloseWrite(dst, h.OnCloseWrite)
			return expect
		}
		select {
		case <-ctx.Done():
			return expect
		case f, ok := <-recv:
			if !ok {
				return expect
			}
			switch f.Type {
			case FrameData:
				if len(pending) >= PendingCap {
					if h.OnOverflow != nil {
						h.OnOverflow(len(pending))
					}
					return expect
				}
				pending[f.Seq] = f.Data
			case FrameFIN:
				v := f.Seq
				if finSeq == nil || v < *finSeq {
					finSeq = &v
				}
			default:
				if h.OnUnknownType != nil {
					h.OnUnknownType(f.Type)
				}
				return expect
			}
			for {
				data, ok := pending[expect]
				if !ok {
					break
				}
				delete(pending, expect)
				if len(data) > 0 {
					if _, err := dst.Write(data); err != nil {
						if h.OnWriteError != nil {
							h.OnWriteError(err)
						}
						return expect
					}
				}
				expect++
			}
		}
	}
}

// CloseWrite half-closes the write side of conn if the underlying type
// supports it (TCPConn, smux.Stream, …); otherwise it is a no-op. errf is
// invoked with the error if CloseWrite fails; callers typically pass a
// debug-gated log func.
func CloseWrite(conn net.Conn, errf func(format string, v ...any)) {
	type closeWriter interface {
		CloseWrite() error
	}
	if cw, ok := conn.(closeWriter); ok {
		if err := cw.CloseWrite(); err != nil && errf != nil {
			errf("CloseWrite failed: %v", err)
		}
	}
}
