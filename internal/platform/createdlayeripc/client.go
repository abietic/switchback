package createdlayeripc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"io"
	"net"
	"time"
)

// closeOnCancel only owns the connection. It never closes collector filesystem FDs.
func closeOnCancel(ctx context.Context, conn net.Conn) func() {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); conn.Close() })
	return func() {
		if !stop() {
			<-done
		}
	}
}
func (c *Client) Observe(ctx context.Context, b Binding) (capacityprobe.CreatedLayerCollection, error) {
	var zero capacityprobe.CreatedLayerCollection
	if checkContext(ctx) != nil {
		return zero, errContext
	}
	if c == nil || !b.valid(c.digest) {
		return zero, errInput
	}
	start := c.now().UTC()
	deadline := start.Add(window)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d.UTC()
	}
	if !deadline.After(start) {
		return zero, errContext
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	conn, e := c.dial(ctx)
	if e != nil {
		return zero, errNative
	}
	defer conn.Close()
	stop := closeOnCancel(ctx, conn)
	defer stop()
	if checkContext(ctx) != nil {
		return zero, errContext
	}
	if conn.SetDeadline(deadline) != nil || c.peer(conn, false) != nil {
		return zero, errNative
	}
	var greeting [40]byte
	if _, e = io.ReadFull(conn, greeting[:]); e != nil || string(greeting[:8]) != magic {
		return zero, errWire
	}
	var nonce [32]byte
	if _, e = io.ReadFull(c.entropy, nonce[:]); e != nil {
		return zero, errNative
	}
	req := request{version, hex.EncodeToString(nonce[:]), deadline, b}
	raw, e := json.Marshal(req)
	if e != nil {
		return zero, errWire
	}
	if writeFrame(conn, c.key, requestDomain, greeting[8:], nil, raw, requestLimit) != nil {
		return zero, errWire
	}
	hash := sha256.Sum256(raw)
	rawResponse, e := readFrame(conn, c.key, responseDomain, greeting[8:], hash[:], responseLimit)
	if e != nil {
		return zero, errWire
	}
	var res response
	if canonicalDecode(rawResponse, &res) != nil || res.Version != version || !hmac.Equal([]byte(res.Nonce), []byte(req.Nonce)) || !hmac.Equal([]byte(res.RequestDigest), []byte(bodyDigest(raw))) || res.Binding != b {
		return zero, errWire
	}
	end := c.now().UTC()
	collection := res.Collection
	if checkContext(ctx) != nil || end.After(deadline) || end.Before(start) || collection.Started.IsZero() || collection.Started.Before(start.Add(-2*time.Second)) || collection.Ended.Before(collection.Started) || collection.Ended.After(end.Add(2*time.Second)) || collection.Ended.After(deadline.Add(2*time.Second)) || collection.Ended.Sub(collection.Started) > window {
		return zero, errWire
	}
	return collection, nil
}
