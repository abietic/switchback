//go:build linux && (amd64 || arm64)

package capacitysnapshot

import (
	"context"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Image export is a separate, fixed streaming endpoint. It does not relax the
// 512KiB ordinary Engine JSON transport. No image pull/build or Create occurs.
func qualificationImageArtifact(ctx context.Context, api, id, target, role string, d qualificationCandidateDirectory) (qualificationArtifact, error) {
	if !digestPattern.MatchString(id) || !strings.HasPrefix(target, "/") || !label(role) {
		return qualificationArtifact{}, errQualification
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dial net.Dialer
		return dial.DialContext(ctx, "unix", "/run/switchback-engine/docker.sock")
	}, DisableKeepAlives: true, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16384}
	defer tr.CloseIdleConnections()
	endpoint, e := qualificationImageArchiveURL(api, id)
	if e != nil {
		return qualificationArtifact{}, e
	}
	req, e := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if e != nil {
		return qualificationArtifact{}, e
	}
	r, e := (&http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return errQualification }}).Do(req)
	if e != nil {
		return qualificationArtifact{}, e
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return qualificationArtifact{}, fmt.Errorf("image export failed")
	}
	raw, e := qualificationExtractImageArtifact(ctx, io.LimitReader(r.Body, (2<<30)+1), id, strings.TrimPrefix(target, "/"))
	if e != nil {
		return qualificationArtifact{}, e
	}
	name := "image-artifact-" + role
	fd, e := unix.Openat(int(d.file.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0400)
	if e != nil {
		return qualificationArtifact{}, e
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	for off := 0; off < len(raw); {
		if e = qualificationContext(ctx); e != nil {
			return qualificationArtifact{}, e
		}
		end := min(off+32768, len(raw))
		n, e := f.Write(raw[off:end])
		if e != nil || n != end-off {
			return qualificationArtifact{}, io.ErrShortWrite
		}
		off += n
	}
	if e = f.Sync(); e != nil {
		return qualificationArtifact{}, e
	}
	if e = f.Close(); e != nil {
		return qualificationArtifact{}, e
	}
	if e = d.file.Sync(); e != nil {
		return qualificationArtifact{}, e
	}
	return qualificationArtifact{Role: role, Path: qualificationGuestCandidateRoot + "/" + name, Digest: Digest(raw)}, nil
}
