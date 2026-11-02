package capacitysnapshot

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/qualificationio"
)

func qualificationPipeAttach(t *testing.T, serve func(net.Conn)) func(context.Context) (net.Conn, error) {
	t.Helper()
	return func(context.Context) (net.Conn, error) {
		client, server := net.Pipe()
		go func() { defer server.Close(); serve(server) }()
		return client, nil
	}
}

func qualificationReadAttachRequest(c net.Conn) error {
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return err
		}
		if line == "\r\n" {
			return nil
		}
	}
}

func TestQualificationAttachHandshakeBoundedAndBuffered(t *testing.T) {
	cid := strings.Repeat("a", 64)
	good := "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Type: application/vnd.docker.multiplexed-stream\r\n\r\n"
	t.Run("stalled deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		dial := qualificationPipeAttach(t, func(c net.Conn) {
			if qualificationReadAttachRequest(c) == nil {
				io.Copy(io.Discard, c)
			}
		})
		started := time.Now()
		if h, err := qualificationAttachWithDial(ctx, cid, "1.56", qualificationio.Control{}, dial); err == nil {
			h.close()
			t.Fatal("stalled upgrade accepted")
		}
		if time.Since(started) > time.Second {
			t.Fatal("handshake did not obey deadline")
		}
	})
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		dial := qualificationPipeAttach(t, func(c net.Conn) {
			if qualificationReadAttachRequest(c) == nil {
				io.Copy(io.Discard, c)
			}
		})
		go func() { time.Sleep(20 * time.Millisecond); cancel() }()
		if h, err := qualificationAttachWithDial(ctx, cid, "1.56", qualificationio.Control{}, dial); err == nil {
			h.close()
			t.Fatal("cancelled upgrade accepted")
		}
	})
	for name, reply := range map[string]string{
		"oversized header": "HTTP/1.1 101 Switching Protocols\r\nX-Too-Large: " + strings.Repeat("x", 20<<10) + "\r\n\r\n",
		"truncated header": "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\n",
		"error response":   "HTTP/1.1 404 Not Found\r\nContent-Length: 999999\r\n\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			dial := qualificationPipeAttach(t, func(c net.Conn) {
				if qualificationReadAttachRequest(c) == nil {
					c.Write([]byte(reply))
				}
			})
			if h, err := qualificationAttachWithDial(ctx, cid, "1.56", qualificationio.Control{}, dial); err == nil {
				h.close()
				t.Fatal("invalid upgrade accepted")
			}
		})
	}
	t.Run("upgrade buffered first frame", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var frame [11]byte
		frame[0] = 1
		binary.BigEndian.PutUint32(frame[4:8], 3)
		copy(frame[8:], "abc")
		dial := qualificationPipeAttach(t, func(c net.Conn) {
			if qualificationReadAttachRequest(c) == nil {
				c.Write(append([]byte(good), frame[:]...))
				io.Copy(io.Discard, c)
			}
		})
		h, err := qualificationAttachWithDial(ctx, cid, "1.56", qualificationio.Control{}, dial)
		if err != nil {
			t.Fatal(err)
		}
		defer h.close()
		var got [3]byte
		if _, err := io.ReadFull(h.Reader, got[:]); err != nil || string(got[:]) != "abc" {
			t.Fatalf("first buffered frame lost: %q %v", got, err)
		}
	})
}
