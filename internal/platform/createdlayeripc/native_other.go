//go:build !linux || (!amd64 && !arm64)

package createdlayeripc

import (
	"context"
	"net"
)

func nativeKey(string) ([]byte, error)                             { return nil, errUnsupported }
func nativePeer(net.Conn, bool) error                              { return errUnsupported }
func nativeDial(context.Context) (net.Conn, error)                 { return nil, errUnsupported }
func nativeListen(observerPlacement) (net.Listener, func(), error) { return nil, nil, errUnsupported }
