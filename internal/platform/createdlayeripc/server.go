package createdlayeripc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net"
	"time"
)

// Serve serializes collection. A synchronous kernel call cannot be forcibly
// preempted by context; no detached sampler is created to suggest otherwise.
func (s *Server) Serve(ctx context.Context) error {
	if checkContext(ctx) != nil {
		return errContext
	}
	if s == nil {
		return errInput
	}
	ln, cleanup, e := s.listen(s.placement)
	if e != nil {
		return errNative
	}
	defer cleanup()
	defer ln.Close()
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); ln.Close() })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	for {
		conn, e := ln.Accept()
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errNative
		}
		if checkContext(ctx) != nil {
			conn.Close()
			return nil
		}
		s.handle(ctx, conn)
	}
}
func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	stop := closeOnCancel(ctx, conn)
	defer stop()
	if checkContext(ctx) != nil || conn.SetDeadline(s.now().Add(handshakeWindow)) != nil || s.peer(conn, true) != nil {
		return
	}
	var challenge [32]byte
	if _, e := io.ReadFull(s.entropy, challenge[:]); e != nil {
		return
	}
	greeting := append([]byte(magic), challenge[:]...)
	if writeAll(conn, greeting) != nil {
		return
	}
	raw, e := readFrame(conn, s.key, requestDomain, challenge[:], nil, requestLimit)
	if e != nil {
		return
	}
	var req request
	if canonicalDecode(raw, &req) != nil || req.Version != version || !hex64.MatchString(req.Nonce) || !req.Binding.valid(s.digest) {
		return
	}
	now := s.now().UTC()
	if req.Deadline.Location() != time.UTC || !req.Deadline.After(now) || req.Deadline.Sub(now) > window {
		return
	}
	if conn.SetDeadline(req.Deadline) != nil || checkContext(ctx) != nil {
		return
	}
	collectCtx, cancel := context.WithDeadline(ctx, req.Deadline)
	defer cancel()
	collection := s.collect(collectCtx, req.Binding)
	if checkContext(collectCtx) != nil || s.now().After(req.Deadline) {
		return
	}
	res := response{version, req.Nonce, bodyDigest(raw), req.Binding, collection}
	out, e := json.Marshal(res)
	if e != nil {
		return
	}
	hash := sha256.Sum256(raw)
	_ = writeFrame(conn, s.key, responseDomain, challenge[:], hash[:], out, responseLimit)
}
