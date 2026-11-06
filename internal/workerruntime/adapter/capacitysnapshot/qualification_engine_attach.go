package capacitysnapshot

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/abietic/switchback/internal/platform/qualificationio"
)

const qualificationAttachHeaderLimit = 16 << 10

// Moby's hijack path bypasses the bounded ordinary response transport. This
// fixed Unix upgrade installs the common deadline/cancel hook before writing
// any request and reads no more than one finite HTTP header. The same reader
// survives the upgrade so prefetched first-frame bytes are never discarded.
func qualificationAttachWithDial(ctx context.Context, cid, version string, control qualificationio.Control,
	dial func(context.Context) (net.Conn, error)) (*qualificationAttachedHelper, error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > qualificationBudget ||
		!containerIDPattern.MatchString(cid) || !qualificationEngineAPIVersion(version) || dial == nil || ctx.Err() != nil {
		return nil, errQualification
	}
	conn, err := dial(ctx)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, errQualification
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	keep := false
	defer func() {
		if !keep {
			stop()
			_ = conn.Close()
		}
	}()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	url := "http://qualification-engine/v" + version + "/containers/" + cid + "/attach?stderr=1&stdin=1&stdout=1&stream=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	if err := req.Write(conn); err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(conn, 4096)
	header := make([]byte, 0, 512)
	for len(header) < qualificationAttachHeaderLimit {
		b, err := reader.ReadByte()
		if err != nil {
			return nil, err
		}
		header = append(header, b)
		if len(header) >= 4 && bytes.Equal(header[len(header)-4:], []byte("\r\n\r\n")) {
			break
		}
	}
	if len(header) < 4 || !bytes.Equal(header[len(header)-4:], []byte("\r\n\r\n")) {
		return nil, fmt.Errorf("qualification attach response header oversized")
	}
	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(header)), req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	upgrade := false
	for _, token := range strings.Split(response.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(token), "Upgrade") {
			upgrade = true
		}
	}
	if response.StatusCode != http.StatusSwitchingProtocols || !upgrade || !strings.EqualFold(response.Header.Get("Upgrade"), "tcp") || ctx.Err() != nil {
		return nil, errQualification
	}
	h := &qualificationAttachedHelper{CID: cid, Control: control, Connection: conn, Reader: &qualificationDockerStdout{input: reader}, stopCancel: stop}
	keep = true
	return h, nil
}
