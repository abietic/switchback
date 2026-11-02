package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// Connected-but-unanswered fault: retain the TCP request until its caller cancels.
// The only forwarding destination is this fixture's fixed Langfuse service.
func serveFaultProxy(listen, block string) error {
	if block != "ingestion" && block != "query" {
		return fmt.Errorf("explicit ingestion or query fault required")
	}
	target, _ := url.Parse("http://langfuse-web:3000")
	proxy := httputil.NewSingleHostReverseProxy(target)
	var sequence atomic.Uint64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		matched := block == "ingestion" && r.Method == http.MethodPost && r.URL.Path == "/api/public/ingestion" || block == "query" && r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/public/")
		if matched {
			id := sequence.Add(1)
			started := time.Now()
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"event": "connected_unanswered", "request_sequence": id, "method": r.Method, "path": r.URL.Path, "at": started.UTC()})
			<-r.Context().Done()
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"event": "caller_cancelled", "request_sequence": id, "method": r.Method, "path": r.URL.Path, "at": time.Now().UTC(), "unanswered_ms": float64(time.Since(started)) / float64(time.Millisecond)})
			return
		}
		proxy.ServeHTTP(w, r)
	})
	return (&http.Server{Addr: listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe()
}
