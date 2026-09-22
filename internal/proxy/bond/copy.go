package bond

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/samosvalishe/free-turn-proxy/internal/stats"
)

// Copy владеет local и lanes; ошибка любого направления закрывает всё соединение.
func Copy(ctx context.Context, local net.Conn, lanes []net.Conn, traffic *stats.Stats) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() {
		_ = local.Close()
		for _, lane := range lanes {
			_ = lane.Close()
		}
	})
	defer stop()
	defer func() {
		_ = local.Close()
		for _, lane := range lanes {
			_ = lane.Close()
		}
	}()
	if len(lanes) == 0 || len(lanes) > MaxLanes {
		return fmt.Errorf("%w: lane count", ErrProtocol)
	}
	results := make(chan error, 2)
	go func() { results <- send(local, lanes, traffic) }()
	var receiver sync.WaitGroup
	receiver.Go(func() {
		err := receive(ctx, local, lanes, traffic, cancel)
		results <- err
		if err == nil {
			watchClosure(lanes, cancel)
		}
	})
	defer func() { cancel(); receiver.Wait() }()
	err := <-results
	if err != nil {
		cancel()
	}
	return errors.Join(err, <-results)
}

// После FIN чтение EOF/ошибки всё ещё должно прерывать заблокированный local.Read.
func watchClosure(lanes []net.Conn, cancel context.CancelFunc) {
	var wg sync.WaitGroup
	for _, lane := range lanes {
		wg.Go(func() {
			var b [1]byte
			_, _ = lane.Read(b[:])
			cancel()
		})
	}
	wg.Wait()
}

func send(local net.Conn, lanes []net.Conn, traffic *stats.Stats) error {
	buf := make([]byte, maxChunk)
	var seq uint64
	for {
		n, err := local.Read(buf)
		if n > 0 {
			if werr := writeFrame(lanes[seq%uint64(len(lanes))], frameData, seq, buf[:n]); werr != nil {
				return werr
			}
			seq++
			if traffic != nil {
				traffic.AddTx(n)
			}
		}
		if errors.Is(err, io.EOF) {
			for _, lane := range lanes {
				if werr := writeFrame(lane, frameFIN, seq, nil); werr != nil {
					return werr
				}
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("bond local read: %w", err)
		}
	}
}

type received struct {
	frame
	err error
}

func receive(ctx context.Context, local net.Conn, lanes []net.Conn, traffic *stats.Stats, abort context.CancelFunc) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	frames := make(chan received, len(lanes))
	var readers sync.WaitGroup
	for _, lane := range lanes {
		readers.Go(func() { readLane(ctx, lane, frames, abort) })
	}
	err := reorder(ctx, local, frames, len(lanes), traffic)
	cancel()
	// При ошибке чтения другие lanes могут ждать данных бесконечно.
	if err != nil {
		for _, lane := range lanes {
			_ = lane.Close()
		}
	}
	readers.Wait()
	return err
}

func readLane(ctx context.Context, lane net.Conn, frames chan<- received, abort context.CancelFunc) {
	for {
		f, err := readFrame(lane)
		if err != nil {
			// Получатель может стоять в local.Write, не читая frames.
			abort()
		}
		select {
		case frames <- received{frame: f, err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil || f.typ == frameFIN {
			return
		}
	}
}

func reorder(ctx context.Context, local net.Conn, frames <-chan received, count int, traffic *stats.Stats) error {
	pending := make(map[uint64][]byte)
	var expect, end uint64
	var fins int
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("bond receive: %w", ctx.Err())
		case f := <-frames:
			if f.err != nil {
				return f.err
			}
			if f.typ == frameFIN {
				if f.seq < expect || (fins > 0 && end != f.seq) {
					return fmt.Errorf("%w: conflicting FIN", ErrProtocol)
				}
				end = f.seq
				fins++
			} else {
				if err := queueFrame(pending, f.frame, expect, end, fins); err != nil {
					return err
				}
			}
			if err := drain(local, pending, &expect, traffic); err != nil {
				return err
			}
			if fins == count {
				if expect != end || len(pending) != 0 {
					return fmt.Errorf("%w: missing data before FIN", ErrProtocol)
				}
				return closeWrite(local)
			}
		}
	}
}

func closeWrite(local net.Conn) error {
	if cw, ok := local.(interface{ CloseWrite() error }); ok {
		if err := cw.CloseWrite(); err != nil {
			return fmt.Errorf("bond half-close: %w", err)
		}
	}
	return nil
}

func queueFrame(pending map[uint64][]byte, f frame, expect, end uint64, fins int) error {
	_, duplicate := pending[f.seq]
	if f.seq < expect || duplicate || f.seq-expect > pendingCap || (fins > 0 && f.seq >= end) {
		return fmt.Errorf("%w: data sequence", ErrProtocol)
	}
	pending[f.seq] = f.data
	return nil
}

func drain(local net.Conn, pending map[uint64][]byte, expect *uint64, traffic *stats.Stats) error {
	for {
		data, ok := pending[*expect]
		if !ok {
			return nil
		}
		if err := writeFull(local, data); err != nil {
			return err
		}
		if traffic != nil {
			traffic.AddRx(len(data))
		}
		delete(pending, *expect)
		*expect++
	}
}
