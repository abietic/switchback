// Command networkprofile serves host-side TCP and UDP echo endpoints for the
// trusted Worker network acceptance. It never logs or persists probe payloads.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
)

type endpointState struct {
	TCPPort int `json:"tcp_port"`
	UDPPort int `json:"udp_port"`
}

func main() {
	statePath := flag.String("state", "", "path for the ready endpoint state")
	flag.Parse()
	if *statePath == "" {
		fatal(errors.New("state path is required"))
	}
	tcpListener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		fatal(err)
	}
	defer tcpListener.Close()
	udpListener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		fatal(err)
	}
	defer udpListener.Close()
	state := endpointState{
		TCPPort: tcpListener.Addr().(*net.TCPAddr).Port,
		UDPPort: udpListener.LocalAddr().(*net.UDPAddr).Port,
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*statePath, append(encoded, '\n'), 0o600); err != nil {
		fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go serveTCP(ctx, tcpListener)
	go serveUDP(ctx, udpListener)
	<-ctx.Done()
}

func serveTCP(ctx context.Context, listener net.Listener) {
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer connection.Close()
			buffer := make([]byte, 256)
			count, err := connection.Read(buffer)
			if err == nil && count > 0 {
				_, _ = connection.Write(buffer[:count])
			}
		}()
	}
}

func serveUDP(ctx context.Context, listener *net.UDPConn) {
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	buffer := make([]byte, 256)
	for {
		count, sender, err := listener.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		if count > 0 {
			_, _ = listener.WriteToUDP(buffer[:count], sender)
		}
	}
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "network profile acceptance server failed")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, strconv.Quote(err.Error()))
	}
	os.Exit(1)
}
