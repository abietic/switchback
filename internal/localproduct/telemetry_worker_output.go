package localproduct

import (
	"context"
	"encoding/binary"
	"io"
	"sync/atomic"
	"time"

	"github.com/abietic/switchback/internal/platform/telemetry"
)

const (
	workerOutputBuffer         = 16 << 10
	workerOutputMaxFrame       = 1 << 20
	workerOutputBytesPerSecond = 256 << 10
	workerOutputAggregation    = time.Second
)

type workerOutputReason string

const (
	workerOutputEOF        workerOutputReason = "eof"
	workerOutputInvalid    workerOutputReason = "invalid_frame"
	workerOutputCancelled  workerOutputReason = "cancelled"
	workerOutputReadFailed workerOutputReason = "read_failed"
)

// consumeWorkerOutput retains only three numeric counters and a fixed discard
// buffer. Neither Docker errors nor payload bytes can cross the emit boundary.
// The owner must provide a Close that interrupts Read (Moby's HTTP body does).
func consumeWorkerOutput(ctx context.Context, reader io.ReadCloser, tty bool, scope telemetry.Scope, emit func(telemetry.Event)) workerOutputReason {
	stopClose := context.AfterFunc(ctx, func() { _ = reader.Close() })
	defer stopClose()
	defer reader.Close()
	var totals [3]atomic.Int64
	flush := func() {
		for i, source := range []telemetry.OutputSource{telemetry.OutputStdout, telemetry.OutputStderr, telemetry.OutputCombined} {
			if n := totals[i].Swap(0); n > 0 {
				emit(telemetry.Event{TS: time.Now(), Level: telemetry.LevelInfo, Service: telemetry.Service, SchemaVersion: telemetry.SchemaVersion, Component: telemetry.ComponentWorker, Event: telemetry.EventRuntimeOutputSuppressed, Scope: scope, OutputSource: source, Bytes: &n})
			}
		}
	}
	done, flushed := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(flushed)
		ticker := time.NewTicker(workerOutputAggregation)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				flush()
				return
			case <-ticker.C:
				flush()
			}
		}
	}()
	defer func() { close(done); <-flushed }()
	var buffer [workerOutputBuffer]byte
	// Smooth rate accounting includes multiplex headers, preventing zero-frame
	// floods. At most one fixed chunk can burst; every consumed byte incurs debt.
	next := time.Now()
	read := func(p []byte) (int, error) {
		if wait := time.Until(next); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return 0, ctx.Err()
			case <-timer.C:
			}
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		n, err := reader.Read(p)
		next = time.Now().Add(time.Duration(n) * time.Second / workerOutputBytesPerSecond)
		return n, err
	}
	readFull := func(p []byte) (int, error) {
		offset := 0
		for offset < len(p) {
			n, err := read(p[offset:])
			offset += n
			if err != nil {
				if offset == len(p) {
					return offset, nil
				}
				return offset, err
			}
			if n == 0 {
				return offset, io.ErrNoProgress
			}
		}
		return offset, nil
	}
	failed := func(err error, partial bool) workerOutputReason {
		if ctx.Err() != nil {
			return workerOutputCancelled
		}
		if err == io.EOF {
			if partial {
				return workerOutputInvalid
			}
			return workerOutputEOF
		}
		return workerOutputReadFailed
	}
	for {
		if tty {
			n, err := read(buffer[:])
			totals[2].Add(int64(n))
			if err != nil {
				return failed(err, false)
			}
			if n == 0 {
				return workerOutputReadFailed
			}
			continue
		}
		var header [8]byte
		n, err := readFull(header[:])
		if err != nil {
			return failed(err, n > 0)
		}
		length := binary.BigEndian.Uint32(header[4:])
		if (header[0] != 1 && header[0] != 2) || header[1] != 0 || header[2] != 0 || header[3] != 0 || length > workerOutputMaxFrame {
			return workerOutputInvalid
		}
		remaining := int(length)
		for remaining > 0 {
			chunk := min(remaining, len(buffer))
			n, err = readFull(buffer[:chunk])
			totals[int(header[0])-1].Add(int64(n))
			remaining -= n
			if err != nil {
				return failed(err, true)
			}
		}
	}
}
