package capacitysnapshot

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/platform/qualificationio"
	"github.com/abietic/switchback/internal/workerruntime/adapter/docker"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

const qualificationEngineSocket = "unix:///run/switchback-engine/docker.sock"
const qualificationEngineNetwork = "switchback-m1-worker-control"
const qualificationHelperOutputLimit = 16 << 20

type qualificationEngine interface {
	ClientVersion() string
	ContainerCreate(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerAttach(context.Context, string, client.ContainerAttachOptions) (client.ContainerAttachResult, error)
	ContainerStart(context.Context, string, client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerWait(context.Context, string, client.ContainerWaitOptions) client.ContainerWaitResult
}

type qualificationHelperSpec struct {
	Case        string
	Candidate   qualificationCandidateRecord
	Window      qualificationWindowOpen
	TestRoot    string
	ControlRoot string
}

func qualificationNewEngine() (*client.Client, error) {
	return qualificationFixedEngineClient()
}

func qualificationRandomToken() (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", token), nil
}

func qualificationHelperOptions(s qualificationHelperSpec, seccomp string) (client.ContainerCreateOptions, error) {
	if !qualificationHelperCase(s.Case) || s.Candidate.Schema != candidateSchema || !digestPattern.MatchString(s.Candidate.Fingerprint) || !digestPattern.MatchString(s.Candidate.StaticIntentDigest) ||
		s.Window.Schema != windowOpenSchema || !qualificationWindowLabel(s.Window.ID) ||
		!containerIDPattern.MatchString(s.Window.Nonce) || !absolute(s.TestRoot) || !absolute(s.ControlRoot) || seccomp == "" {
		return client.ContainerCreateOptions{}, errQualification
	}
	profile, ok := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	if !ok || profile.ValidateDigest() != nil {
		return client.ContainerCreateOptions{}, errQualification
	}
	imageRole, binaryPath, command, user := "backend", "/usr/local/bin/switchback-backend", "qualify-backend", "0:0"
	capabilities := []string{"CHOWN", "DAC_OVERRIDE", "FOWNER"}
	if s.Case == "worker" {
		imageRole, binaryPath, command, user, capabilities = "worker", "/usr/local/bin/worker-shim", "qualify-worker", "10002:10002", nil
	} else {
		switch s.Case {
		case "without-chown":
			capabilities = []string{"DAC_OVERRIDE", "FOWNER"}
		case "without-dac-override":
			capabilities = []string{"CHOWN", "FOWNER"}
		case "without-fowner":
			capabilities = []string{"CHOWN", "DAC_OVERRIDE"}
		}
	}
	image := ""
	for _, b := range s.Candidate.Source.Builds {
		if b.Role == imageRole {
			image = b.ImageDigest
		}
	}
	if !digestPattern.MatchString(image) {
		return client.ContainerCreateOptions{}, errQualification
	}
	entrypoint := []string{binaryPath}
	if s.Case == "worker" {
		entrypoint = []string{"/usr/bin/tini", "--", binaryPath}
	}
	labels := map[string]string{
		"com.switchback.qualification-window":        s.Window.Nonce,
		"com.switchback.qualification-static-intent": s.Candidate.StaticIntentDigest,
		"com.switchback.qualification-case":          s.Case,
	}
	pids := profile.PIDsLimit
	name := "switchback-qualification-" + s.Window.ID + "-" + s.Case
	return client.ContainerCreateOptions{
		Name: name, Image: image,
		Config: &container.Config{User: user, Labels: labels, WorkingDir: "/qualification-data", Tty: false,
			OpenStdin: true, StdinOnce: true, AttachStdin: true, AttachStdout: true, AttachStderr: true,
			Entrypoint: entrypoint, Cmd: []string{command}},
		HostConfig: &container.HostConfig{
			NetworkMode: container.NetworkMode(qualificationEngineNetwork), ExtraHosts: []string{"host.docker.internal:host-gateway"},
			Privileged: false, PublishAllPorts: false, CapDrop: []string{"ALL"}, CapAdd: capabilities,
			SecurityOpt:  []string{"no-new-privileges", "seccomp=" + seccomp},
			CgroupnsMode: container.CgroupnsModePrivate, IpcMode: container.IPCModePrivate,
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
			StorageOpt:    map[string]string{"size": strconv.FormatInt(profile.WritableLayerBytes, 10)},
			Resources: container.Resources{CgroupParent: "switchback-workers.slice", NanoCPUs: profile.CPUUnits * 1_000_000_000,
				Memory: profile.MemoryBytes, MemorySwap: profile.MemoryBytes, PidsLimit: &pids},
			Mounts: []mount.Mount{
				{Type: mount.TypeBind, Source: s.ControlRoot, Target: "/qualification", ReadOnly: true},
				{Type: mount.TypeBind, Source: s.TestRoot, Target: "/qualification-data"},
			},
		},
	}, nil
}

func qualificationVerifyCreatedHelper(ctx context.Context, engine qualificationEngine, cid string, expected client.ContainerCreateOptions) (string, string, error) {
	if !containerIDPattern.MatchString(cid) {
		return "", "", errQualification
	}
	inspected, err := engine.ContainerInspect(ctx, cid, client.ContainerInspectOptions{})
	if err != nil {
		return "", "", err
	}
	i := inspected.Container
	if i.ID != cid || i.Image != expected.Image || i.State == nil || i.State.Status != container.StateCreated || i.State.Pid != 0 ||
		i.Config == nil || i.HostConfig == nil || !reflect.DeepEqual(i.Config.Labels, expected.Config.Labels) ||
		i.Config.User != expected.Config.User || !reflect.DeepEqual(i.Config.Entrypoint, expected.Config.Entrypoint) ||
		!reflect.DeepEqual(i.Config.Cmd, expected.Config.Cmd) || i.HostConfig.Privileged || i.HostConfig.PublishAllPorts ||
		!reflect.DeepEqual(i.HostConfig.CapDrop, expected.HostConfig.CapDrop) || !reflect.DeepEqual(i.HostConfig.CapAdd, expected.HostConfig.CapAdd) ||
		!reflect.DeepEqual(i.HostConfig.SecurityOpt, expected.HostConfig.SecurityOpt) ||
		string(i.HostConfig.NetworkMode) != qualificationEngineNetwork || i.HostConfig.Memory != expected.HostConfig.Memory ||
		i.HostConfig.MemorySwap != expected.HostConfig.MemorySwap || i.HostConfig.NanoCPUs != expected.HostConfig.NanoCPUs ||
		!reflect.DeepEqual(i.HostConfig.PidsLimit, expected.HostConfig.PidsLimit) {
		return "", "", errQualification
	}
	if len(i.Mounts) != 2 {
		return "", "", errQualification
	}
	for _, expectedMount := range expected.HostConfig.Mounts {
		found := false
		for _, actual := range i.Mounts {
			if actual.Destination == expectedMount.Target && actual.Source == expectedMount.Source && actual.RW == !expectedMount.ReadOnly && actual.Type == expectedMount.Type {
				found = true
			}
		}
		if !found {
			return "", "", errQualification
		}
	}
	// The observer is intentionally not serving during qualification. Read the
	// created upper layer directly through the already-authorized native probe,
	// before this exact CID receives its one Start.
	collection := capacityprobe.CollectCreatedLayer(ctx, capacityprobe.CreatedLayerTarget{ContainerID: cid}, capacityprobe.CreatedLayerSources{
		Get: capacityprobe.NewCreatedLayerEngineReader(), Physical: capacityprobe.ReadCreatedLayerPhysical,
	})
	if err := capacityprobe.ValidateCreatedLayerObservation(cid, collection); err != nil {
		return "", "", err
	}
	if collection.Inspect == nil || collection.Engine == nil || collection.Inspect.Image != expected.Image {
		return "", "", errQualification
	}
	return collection.Engine.ID, collection.Engine.DockerRoot, qualificationContext(ctx)
}

type qualificationAttachedHelper struct {
	CID        string
	Control    qualificationio.Control
	Connection net.Conn
	Reader     *qualificationDockerStdout
	stopCancel func() bool
}

func (h *qualificationAttachedHelper) close() {
	if h.stopCancel != nil {
		h.stopCancel()
	}
	if h.Connection != nil {
		_ = h.Connection.Close()
	}
}

func qualificationAttachHelper(ctx context.Context, engine qualificationEngine, cid string, control qualificationio.Control) (*qualificationAttachedHelper, error) {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	return qualificationAttachWithDial(ctx, cid, engine.ClientVersion(), control, func(ctx context.Context) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", "/run/switchback-engine/docker.sock")
	})
}

func (h *qualificationAttachedHelper) receive(seq uint64, phase, testCase string) (qualificationio.Frame, error) {
	frame, err := qualificationio.ReadFrame(h.Reader)
	if err != nil {
		return frame, err
	}
	if err := frame.Match(h.Control, seq, phase, testCase); err != nil {
		return frame, err
	}
	return frame, nil
}
func (h *qualificationAttachedHelper) send(seq uint64, phase, testCase string) error {
	return qualificationio.WriteFrame(h.Connection, qualificationio.NewCommand(h.Control, seq, phase, testCase))
}

// Docker attach without TTY multiplexes stdout/stderr. Stderr is a failure;
// no partial or oversized packet can be truncated into a successful frame.
type qualificationDockerStdout struct {
	input     *bufio.Reader
	remaining uint32
	total     int
}

func (r *qualificationDockerStdout) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		var header [8]byte
		if _, err := io.ReadFull(r.input, header[:]); err != nil {
			return 0, err
		}
		size := binary.BigEndian.Uint32(header[4:])
		if header[0] != 1 || header[1] != 0 || header[2] != 0 || header[3] != 0 || size == 0 || size > qualificationio.MaxFrameBytes || r.total+int(size) > qualificationHelperOutputLimit {
			return 0, errQualification
		}
		r.remaining = size
		r.total += int(size)
	}
	if uint32(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.input.Read(p)
	r.remaining -= uint32(n)
	return n, err
}

func qualificationWaitHelper(ctx context.Context, engine qualificationEngine, h *qualificationAttachedHelper) error {
	wait := engine.ContainerWait(ctx, h.CID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-wait.Error:
		return fmt.Errorf("qualification helper exit unknown: %w", err)
	case result := <-wait.Result:
		if result.StatusCode != 0 || result.Error != nil {
			return errQualification
		}
	}
	var extra [1]byte
	n, err := h.Reader.Read(extra[:])
	if n != 0 || err != io.EOF {
		return errQualification
	}
	return qualificationContext(ctx)
}

func qualificationRemoveHelper(ctx context.Context, engine qualificationEngine, cid string, labels map[string]string) (qualificationHelperCleanup, error) {
	var result qualificationHelperCleanup
	if !containerIDPattern.MatchString(cid) || len(labels) != 3 {
		return result, errQualification
	}
	i, err := engine.ContainerInspect(ctx, cid, client.ContainerInspectOptions{})
	if err != nil || i.Container.ID != cid || i.Container.Config == nil || !reflect.DeepEqual(i.Container.Config.Labels, labels) || i.Container.State == nil || i.Container.State.Running || i.Container.State.ExitCode != 0 {
		return result, errQualification
	}
	version := engine.ClientVersion()
	deleted, err := qualificationEngineStatus(ctx, http.MethodDelete, cid, version)
	if err != nil || deleted != http.StatusNoContent {
		return result, errQualification
	}
	absent, err := qualificationEngineStatus(ctx, http.MethodGet, cid, version)
	if err != nil || absent != http.StatusNotFound {
		return result, errQualification
	}
	_, err = engine.ContainerInspect(ctx, cid, client.ContainerInspectOptions{})
	if !cerrdefs.IsNotFound(err) {
		return result, errQualification
	}
	result = qualificationHelperCleanup{Case: labels["com.switchback.qualification-case"], CID: cid, Exit: int64(i.Container.State.ExitCode), DeleteStatus: uint64(deleted), InspectStatus: uint64(absent)}
	return result, qualificationContext(ctx)
}

// The high-level client erases successful HTTP status codes. Cleanup needs
// actual 204/404 evidence, so use the same fixed private Unix Engine socket
// for these two exact requests and never infer their status from nil errors.
func qualificationEngineStatus(ctx context.Context, method, cid, version string) (int, error) {
	if (method != http.MethodDelete && method != http.MethodGet) || !containerIDPattern.MatchString(cid) ||
		!qualificationEngineAPIVersion(version) || qualificationContext(ctx) != nil {
		return 0, errQualification
	}
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 3 * time.Second,
		MaxResponseHeaderBytes: 4096, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", "/run/switchback-engine/docker.sock")
		}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errQualification }}
	req, err := http.NewRequestWithContext(ctx, method, "http://qualification-engine/v"+version+"/containers/"+cid+"/json", nil)
	if err != nil {
		return 0, err
	}
	if method == http.MethodDelete {
		req.URL.Path = "/v" + version + "/containers/" + cid
	}
	response, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	n, err := io.Copy(io.Discard, io.LimitReader(response.Body, 4097))
	if err != nil || n > 4096 || qualificationContext(ctx) != nil {
		return 0, errQualification
	}
	return response.StatusCode, nil
}

func qualificationEngineAPIVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 2 || parts[0] != "1" || len(parts[1]) < 2 || len(parts[1]) > 3 {
		return false
	}
	for _, digit := range parts[1] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func qualificationControlDir(root, testCase string) string {
	return filepath.Join(root, "control-"+testCase)
}

func qualificationSeccomp() (string, error) { return docker.QualificationWorkerSeccomp() }
