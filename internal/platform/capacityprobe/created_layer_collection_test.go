package capacityprobe_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
)

func TestCreatedLayerInvalidTargetDoesNoIO(t *testing.T) {
	reads := 0
	got := capacityprobe.CollectCreatedLayer(context.Background(), capacityprobe.CreatedLayerTarget{ContainerID: strings.Repeat("A", 64)}, capacityprobe.CreatedLayerSources{
		Get:      func(context.Context, string) ([]byte, error) { reads++; return nil, nil },
		Physical: func(context.Context, string) (*capacityprobe.CreatedLayerPhysical, error) { reads++; return nil, nil },
	})
	if reads != 0 || got.ObservationComplete || got.ExecutionEnabled || got.CapacityReady || got.Status != "preparation_blocked" || got.Trust != "none" {
		t.Fatalf("invalid target diagnostic: reads=%d result=%+v", reads, got)
	}
}

const layerCID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const layerUpper = "/mnt/lima-sbquota/docker/overlay2/actual-driver-17/diff"
const layerEngine = "12345678-1234-5678-9abc-123456789abc"
const layerVersion = `{"ApiVersion":"1.56","Version":"29.9.0"}`
const layerInfo = `{"ID":"12345678-1234-5678-9abc-123456789abc","ServerVersion":"29.9.0","DockerRootDir":"/mnt/lima-sbquota/docker","Driver":"overlay2","Labels":["ignored-engine-token"]}`
const layerInspect = `{"Id":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","Image":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","State":{"Status":"created","Pid":0,"Running":false,"Paused":false,"Restarting":false,"Dead":false,"StartedAt":"0001-01-01T00:00:00Z"},"GraphDriver":{"Name":"overlay2","Data":{"UpperDir":"/mnt/lima-sbquota/docker/overlay2/actual-driver-17/diff"}},"Config":{"Env":["API_TOKEN=actual-fixture-secret-123"],"Cmd":["credential-command-456"]},"LogPath":"secret-log-path","Mounts":[{"Source":"secret-mount"}]}`

type layerFixture struct {
	source        capacityprobe.CreatedLayerSources
	requests      []string
	physicalReads int
	physicalPath  string
	ticks         int
	mutate        func(int, string, string) (string, error)
	physical      func(context.Context, string) (*capacityprobe.CreatedLayerPhysical, error)
}

func newLayerFixture(t *testing.T) *layerFixture {
	t.Helper()
	f := &layerFixture{}
	f.source.Now = func() time.Time {
		f.ticks++
		return time.Date(2026, 10, 16, 0, 0, 0, f.ticks*int(time.Millisecond), time.UTC)
	}
	f.source.Get = func(ctx context.Context, path string) ([]byte, error) {
		f.requests = append(f.requests, path)
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 30*time.Second {
			t.Error("missing bounded context")
		}
		raw := ""
		switch path {
		case "/v1.56/version":
			raw = layerVersion
		case "/v1.56/info":
			raw = layerInfo
		case "/v1.56/containers/" + layerCID + "/json":
			raw = layerInspect
		default:
			t.Fatalf("unexpected GET %s", path)
		}
		if f.mutate != nil {
			s, err := f.mutate(len(f.requests), path, raw)
			return []byte(s), err
		}
		return []byte(raw), nil
	}
	f.source.Physical = func(ctx context.Context, path string) (*capacityprobe.CreatedLayerPhysical, error) {
		f.physicalReads++
		f.physicalPath = path
		if f.physical != nil {
			return f.physical(ctx, path)
		}
		return completeLayerPhysical(), nil
	}
	return f
}

func completeLayerPhysical() *capacityprobe.CreatedLayerPhysical {
	dir := func(path string, inode uint64) capacityprobe.CreatedLayerDirectory {
		id := capacityprobe.CreatedLayerIdentity{CanonicalPath: path, Inode: inode, DeviceMajor: 8, DeviceMinor: 1, Mode: 0040755, UID: 1000, GID: 1000, MountID: 19, XFSUUID: [16]byte{1, 2, 3}}
		return capacityprobe.CreatedLayerDirectory{Before: id, After: id, NoFollowWalk: true, ReopenedNoFollow: true, IdentityRechecked: true, FilesystemUUIDSameFD: true}
	}
	return &capacityprobe.CreatedLayerPhysical{Root: dir("/mnt/lima-sbquota", 31), Parent: dir("/mnt/lima-sbquota/docker/overlay2/actual-driver-17", 47), Diff: dir(layerUpper, 48), MountInfoSHA256Before: [32]byte{9}, MountInfoSHA256After: [32]byte{9}, MountInfoRechecked: true, ProjectID: 5129, BootIDBefore: [16]byte{4}, BootIDAfter: [16]byte{4}, MountNamespaceBefore: 55, MountNamespaceAfter: 55, RootXFSVerified: true, SameMountVerified: true, BootIdentityRechecked: true, MountNamespaceRechecked: true, ObservationComplete: true}
}

func (f *layerFixture) collect(ctx context.Context) capacityprobe.CreatedLayerCollection {
	return capacityprobe.CollectCreatedLayer(ctx, capacityprobe.CreatedLayerTarget{ContainerID: layerCID}, f.source)
}

func TestCreatedLayerCompleteFiniteObservationStaysBlocked(t *testing.T) {
	f := newLayerFixture(t)
	got := f.collect(context.Background())
	want := []string{"/v1.56/version", "/v1.56/info", "/v1.56/containers/" + layerCID + "/json", "/v1.56/containers/" + layerCID + "/json", "/v1.56/version", "/v1.56/info"}
	if !reflect.DeepEqual(f.requests, want) || f.physicalReads != 1 || f.physicalPath != layerUpper {
		t.Fatalf("reads: %v physical %d %s", f.requests, f.physicalReads, f.physicalPath)
	}
	if !got.ObservationComplete || got.Status != "preparation_blocked" || got.Trust != "none" || got.ExecutionEnabled || got.CapacityReady || got.Schema != "switchback.capacity.untrusted_created_layer_collection.v1" {
		t.Fatalf("diagnostic: %+v", got)
	}
	if got.Engine.ID != layerEngine || got.Inspect.UpperDir != layerUpper || got.Physical.ProjectID != 5129 {
		t.Fatalf("lost observations: %+v", got)
	}
	if got.Physical.TargetQuota != [112]byte{} || got.Physical.QStatV != [160]byte{} {
		t.Fatal("zero/off raw facts rewritten")
	}
	if len(got.Samples) != 7 {
		t.Fatalf("samples=%d", len(got.Samples))
	}
	previous := got.Started
	for i, s := range got.Samples {
		if s.Sequence != i+1 || s.Started.Before(previous) || !s.Ended.After(s.Started) || s.Source == "" {
			t.Fatalf("unordered sample %+v", s)
		}
		previous = s.Ended
	}
	assertLayerPrivate(t, got)
}

func assertLayerPrivate(t *testing.T, got capacityprobe.CreatedLayerCollection) {
	t.Helper()
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"actual-fixture-secret-123", "API_TOKEN", "credential-command-456", "ignored-engine-token", "secret-log-path", "secret-mount", "sk-credential-shaped-error-789", "Config", "Env", "Cmd", "LogPath", "Mounts"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("output leaked %q", secret)
		}
	}
}

func TestCreatedLayerTargetAndDependencyValidation(t *testing.T) {
	for _, cid := range []string{"", layerCID[:12], strings.ToUpper(layerCID), "../" + layerCID, layerCID + "/json"} {
		t.Run(cid, func(t *testing.T) {
			f := newLayerFixture(t)
			g := capacityprobe.CollectCreatedLayer(context.Background(), capacityprobe.CreatedLayerTarget{ContainerID: cid}, f.source)
			if g.ObservationComplete || len(f.requests) != 0 || f.physicalReads != 0 {
				t.Fatal("invalid CID performed IO")
			}
		})
	}
	for _, kind := range []string{"nil_context", "get", "physical", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			f := newLayerFixture(t)
			ctx := context.Background()
			switch kind {
			case "nil_context":
				ctx = nil
			case "get":
				f.source.Get = nil
			case "physical":
				f.source.Physical = nil
			case "cancelled":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			g := f.collect(ctx)
			if g.ObservationComplete || len(f.requests) != 0 || f.physicalReads != 0 {
				t.Fatalf("IO despite %s: %+v", kind, g)
			}
		})
	}
}

func TestCreatedLayerRejectsInspectAmbiguityStateAndPath(t *testing.T) {
	cases := map[string]string{
		"malformed": "{", "trailing": layerInspect + "{}", "oversize": strings.Repeat(" ", 1<<20) + layerInspect,
		"duplicate":         strings.Replace(layerInspect, `"Pid":0`, `"Pid":7,"Pid":0`, 1),
		"case_alias":        strings.Replace(layerInspect, `"Id":`, `"id":"x","Id":`, 1),
		"nested_duplicate":  strings.Replace(layerInspect, `"Env":[`, `"env":[],"Env":[`, 1),
		"missing_bool":      strings.Replace(layerInspect, `"Dead":false,`, "", 1),
		"missing_pid":       strings.Replace(layerInspect, `"Pid":0,`, "", 1),
		"missing_timestamp": strings.Replace(layerInspect, `,"StartedAt":"0001-01-01T00:00:00Z"`, "", 1),
		"null_bool":         strings.Replace(layerInspect, `"Dead":false`, `"Dead":null`, 1),
		"string_pid":        strings.Replace(layerInspect, `"Pid":0`, `"Pid":"0"`, 1),
		"null_state":        strings.Replace(layerInspect, `"State":{`, `"unused":{`, 1),
		"mismatch":          strings.Replace(layerInspect, layerCID, strings.Repeat("a", 64), 1),
		"running":           strings.Replace(layerInspect, `"Running":false`, `"Running":true`, 1),
		"paused":            strings.Replace(layerInspect, `"Paused":false`, `"Paused":true`, 1),
		"restarting":        strings.Replace(layerInspect, `"Restarting":false`, `"Restarting":true`, 1),
		"dead":              strings.Replace(layerInspect, `"Dead":false`, `"Dead":true`, 1),
		"pid":               strings.Replace(layerInspect, `"Pid":0`, `"Pid":123`, 1),
		"started":           strings.Replace(layerInspect, "0001-01-01T00:00:00Z", "2026-10-16T00:00:00Z", 1),
		"zero_noncanonical": strings.Replace(layerInspect, "0001-01-01T00:00:00Z", "0001-01-01T00:00:00.000Z", 1),
		"status":            strings.Replace(layerInspect, `"Status":"created"`, `"Status":"exited"`, 1),
		"driver":            strings.Replace(layerInspect, `"Name":"overlay2"`, `"Name":"overlayfs"`, 1),
		"outside":           strings.Replace(layerInspect, layerUpper, "/tmp/actual-driver-17/diff", 1),
		"traversal":         strings.Replace(layerInspect, layerUpper, "/mnt/lima-sbquota/docker/overlay2/a/../b/diff", 1),
		"separator":         strings.Replace(layerInspect, layerUpper, "/mnt/lima-sbquota/docker/overlay2/a/b/diff", 1),
		"space":             strings.Replace(layerInspect, layerUpper, "/mnt/lima-sbquota/docker/overlay2/a b/diff", 1),
		"dot":               strings.Replace(layerInspect, layerUpper, "/mnt/lima-sbquota/docker/overlay2/./diff", 1),
		"bad_image":         strings.Replace(layerInspect, "sha256:"+strings.Repeat("a", 64), "mutable:latest", 1),
		"deep":              strings.Replace(layerInspect, `"Config":{`, `"ignored":`+strings.Repeat("[", 34)+"0"+strings.Repeat("]", 34)+`,"Config":{`, 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			f := newLayerFixture(t)
			f.mutate = func(n int, p, s string) (string, error) {
				if n == 3 {
					return raw, nil
				}
				return s, nil
			}
			g := f.collect(context.Background())
			if g.ObservationComplete || f.physicalReads != 0 || len(f.requests) != 3 || g.Inspect != nil {
				t.Fatalf("accepted invalid inspect: %+v", g)
			}
			assertLayerPrivate(t, g)
		})
	}
}

func TestCreatedLayerRejectsEngineIdentityAndEpochDrift(t *testing.T) {
	cases := []struct {
		name     string
		n        int
		old, new string
	}{
		{"api", 1, "1.56", "1.55"}, {"version", 1, "29.9.0", "bad"}, {"uuid", 2, layerEngine, strings.ToUpper(layerEngine)}, {"root", 2, "/mnt/lima-sbquota/docker", "/var/lib/docker"}, {"driver", 2, "overlay2", "overlayfs"}, {"server_version", 2, "29.9.0", "29.8.0"},
		{"uuid_drift", 6, layerEngine, "12345678-1234-5678-9abc-123456789abd"},
		{"api_drift", 5, "1.56", "1.57"}, {"version_drift", 5, "29.9.0", "29.9.1"},
		{"root_drift", 6, "/mnt/lima-sbquota/docker", "/var/lib/docker"}, {"driver_drift", 6, "overlay2", "overlayfs"},
		{"target_drift", 4, layerCID, strings.Repeat("b", 64)}, {"path_drift", 4, "actual-driver-17", "actual-driver-18"},
		{"image_drift", 4, "sha256:" + strings.Repeat("a", 64), "sha256:" + strings.Repeat("b", 64)},
		{"state_drift", 4, `"Running":false`, `"Running":true`},
		{"info_duplicate", 2, `"ID":`, `"ID":"x","ID":`}, {"version_duplicate", 1, `"Version":`, `"Version":"bad","Version":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLayerFixture(t)
			f.mutate = func(n int, p, s string) (string, error) {
				if n == tc.n {
					return strings.Replace(s, tc.old, tc.new, 1), nil
				}
				return s, nil
			}
			g := f.collect(context.Background())
			if g.ObservationComplete || len(f.requests) != tc.n {
				t.Fatalf("accepted drift/read past failure: count %d result %+v", len(f.requests), g)
			}
			assertLayerPrivate(t, g)
		})
	}
}

func TestCreatedLayerReadFailuresAndPartialErrno(t *testing.T) {
	for read := 1; read <= 6; read++ {
		t.Run(fmt.Sprintf("get_%d", read), func(t *testing.T) {
			f := newLayerFixture(t)
			f.mutate = func(n int, p, s string) (string, error) {
				if n == read {
					return "", fmt.Errorf("sk-credential-shaped-error-789: %w", syscall.EACCES)
				}
				return s, nil
			}
			g := f.collect(context.Background())
			if g.ObservationComplete || len(f.requests) != read {
				t.Fatalf("failure did not stop: %+v", g)
			}
			sample := g.Samples[len(g.Samples)-1]
			if sample.Errno == nil || *sample.Errno != uint64(syscall.EACCES) || sample.Outcome != "unavailable" {
				t.Fatalf("errno lost: %+v", sample)
			}
			assertLayerPrivate(t, g)
		})
	}
	t.Run("physical_partial", func(t *testing.T) {
		f := newLayerFixture(t)
		f.physical = func(context.Context, string) (*capacityprobe.CreatedLayerPhysical, error) {
			p := completeLayerPhysical()
			p.TargetQuota[12] = 87
			return p, fmt.Errorf("sk-credential-shaped-error-789: %w", syscall.EIO)
		}
		g := f.collect(context.Background())
		if g.ObservationComplete || g.Physical == nil || g.Physical.TargetQuota[12] != 87 || len(f.requests) != 6 || f.physicalReads != 1 {
			t.Fatalf("partial facts lost: %+v", g)
		}
		s := g.Samples[3]
		if s.Errno == nil || *s.Errno != uint64(syscall.EIO) {
			t.Fatalf("physical errno lost: %+v", s)
		}
		assertLayerPrivate(t, g)
	})
	t.Run("physical_nil", func(t *testing.T) {
		f := newLayerFixture(t)
		f.physical = func(context.Context, string) (*capacityprobe.CreatedLayerPhysical, error) { return nil, nil }
		if g := f.collect(context.Background()); g.ObservationComplete || len(f.requests) != 6 {
			t.Fatal("nil physical accepted")
		}
	})
	t.Run("non_errno", func(t *testing.T) {
		f := newLayerFixture(t)
		f.mutate = func(n int, p, s string) (string, error) { return "", errors.New("sk-credential-shaped-error-789") }
		g := f.collect(context.Background())
		if g.Samples[0].Errno != nil || g.Samples[0].Outcome != "unavailable" {
			t.Fatal("non errno fabricated")
		}
		assertLayerPrivate(t, g)
	})
}

func TestCreatedLayerCancellationBetweenEveryEffect(t *testing.T) {
	for read := 1; read <= 6; read++ {
		t.Run(fmt.Sprintf("get_%d", read), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := newLayerFixture(t)
			f.mutate = func(n int, p, s string) (string, error) {
				if n == read {
					cancel()
				}
				return s, nil
			}
			g := f.collect(ctx)
			if g.ObservationComplete || len(f.requests) != read {
				t.Fatalf("continued after cancellation: %+v", g)
			}
			if read <= 3 && f.physicalReads != 0 {
				t.Fatal("physical read after cancellation")
			}
		})
	}
	t.Run("physical", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f := newLayerFixture(t)
		f.physical = func(context.Context, string) (*capacityprobe.CreatedLayerPhysical, error) {
			cancel()
			return completeLayerPhysical(), nil
		}
		g := f.collect(ctx)
		if g.ObservationComplete || len(f.requests) != 3 || f.physicalReads != 1 {
			t.Fatalf("reinspection after cancellation: %+v", g)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		f := newLayerFixture(t)
		f.source.Get = func(ctx context.Context, p string) ([]byte, error) {
			f.requests = append(f.requests, p)
			<-ctx.Done()
			return []byte(layerVersion), nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()
		g := f.collect(ctx)
		if g.ObservationComplete || len(f.requests) != 1 {
			t.Fatalf("late callback accepted: %+v", g)
		}
	})
}

func TestCreatedLayerTimeOrderingAndWindow(t *testing.T) {
	for _, kind := range []string{"constant", "regress", "expired", "physical_expired", "end_regress"} {
		t.Run(kind, func(t *testing.T) {
			f := newLayerFixture(t)
			tick := 0
			f.source.Now = func() time.Time {
				tick++
				delta := time.Duration(tick) * time.Millisecond
				switch kind {
				case "constant":
					delta = 0
				case "regress":
					if tick >= 3 {
						delta = -time.Second
					}
				case "expired":
					if tick >= 3 {
						delta = 31 * time.Second
					}
				case "physical_expired":
					if tick >= 9 {
						delta = 31 * time.Second
					}
				case "end_regress":
					if tick == 16 {
						delta = -time.Second
					}
				}
				return time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC).Add(delta)
			}
			g := f.collect(context.Background())
			if g.ObservationComplete {
				t.Fatalf("time %s accepted ticks=%d result %+v", kind, tick, g)
			}
			if (kind == "constant" || kind == "regress" || kind == "expired") && len(f.requests) > 1 {
				t.Fatal("extra read after invalid clock")
			}
		})
	}
}

func TestCreatedLayerPhysicalRechecksAndSanitization(t *testing.T) {
	cases := map[string]func(*capacityprobe.CreatedLayerPhysical){
		"geometry_after":      func(p *capacityprobe.CreatedLayerPhysical) { p.Diff.GeometryAfter[255]++ },
		"fsxattr_after":       func(p *capacityprobe.CreatedLayerPhysical) { p.Parent.FSXAttrAfter[27]++ },
		"mountinfo_after":     func(p *capacityprobe.CreatedLayerPhysical) { p.MountInfoSHA256After[31]++ },
		"mountinfo_rechecked": func(p *capacityprobe.CreatedLayerPhysical) { p.MountInfoRechecked = false },
		"incomplete":          func(p *capacityprobe.CreatedLayerPhysical) { p.ObservationComplete = false },
		"walk":                func(p *capacityprobe.CreatedLayerPhysical) { p.Diff.NoFollowWalk = false },
		"reopen":              func(p *capacityprobe.CreatedLayerPhysical) { p.Parent.ReopenedNoFollow = false },
		"metadata":            func(p *capacityprobe.CreatedLayerPhysical) { p.Root.After.Inode++ },
		"mount":               func(p *capacityprobe.CreatedLayerPhysical) { p.Diff.After.MountID++ },
		"boot":                func(p *capacityprobe.CreatedLayerPhysical) { p.BootIDAfter[0]++ },
		"namespace":           func(p *capacityprobe.CreatedLayerPhysical) { p.MountNamespaceAfter++ },
		"samefd":              func(p *capacityprobe.CreatedLayerPhysical) { p.Root.FilesystemUUIDSameFD = false },
		"same_mount":          func(p *capacityprobe.CreatedLayerPhysical) { p.SameMountVerified = false },
		"xfs":                 func(p *capacityprobe.CreatedLayerPhysical) { p.RootXFSVerified = false },
		"path": func(p *capacityprobe.CreatedLayerPhysical) {
			p.Diff.Before.CanonicalPath = "sk-credential-shaped-error-789"
		},
		"sample": func(p *capacityprobe.CreatedLayerPhysical) {
			p.Samples = []capacityprobe.CreatedLayerSample{{Operation: "sk-credential-shaped-error-789", Source: "actual-fixture-secret-123", Outcome: "credential-command-456"}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newLayerFixture(t)
			f.physical = func(context.Context, string) (*capacityprobe.CreatedLayerPhysical, error) {
				p := completeLayerPhysical()
				mutate(p)
				return p, nil
			}
			g := f.collect(context.Background())
			if g.ObservationComplete || g.Physical == nil || g.Physical.ObservationComplete {
				t.Fatalf("physical recheck failure accepted: %+v", g)
			}
			assertLayerPrivate(t, g)
		})
	}
}

func TestCreatedLayerRequiresEveryTypedStateField(t *testing.T) {
	for _, field := range []string{"Status", "Pid", "Running", "Paused", "Restarting", "Dead", "StartedAt"} {
		for _, kind := range []string{"missing", "null", "wrong_type"} {
			t.Run(field+"_"+kind, func(t *testing.T) {
				f := newLayerFixture(t)
				f.mutate = func(n int, p, s string) (string, error) {
					if n != 3 {
						return s, nil
					}
					var m map[string]any
					if err := json.Unmarshal([]byte(s), &m); err != nil {
						t.Fatal(err)
					}
					state := m["State"].(map[string]any)
					switch kind {
					case "missing":
						delete(state, field)
					case "null":
						state[field] = nil
					case "wrong_type":
						state[field] = []any{false}
					}
					raw, err := json.Marshal(m)
					if err != nil {
						t.Fatal(err)
					}
					return string(raw), nil
				}
				g := f.collect(context.Background())
				if g.ObservationComplete || f.physicalReads != 0 || len(f.requests) != 3 {
					t.Fatalf("accepted %s %s", field, kind)
				}
			})
		}
	}
	t.Run("invalid_utf8", func(t *testing.T) {
		f := newLayerFixture(t)
		f.mutate = func(n int, p, s string) (string, error) {
			if n == 3 {
				return strings.Replace(s, "secret-log-path", string([]byte{0xff}), 1), nil
			}
			return s, nil
		}
		g := f.collect(context.Background())
		if g.ObservationComplete || f.physicalReads != 0 {
			t.Fatal("invalid utf8 accepted")
		}
	})
}

func TestCreatedLayerPhysicalSamplesAreFiniteAndDetached(t *testing.T) {
	validSample := func() capacityprobe.CreatedLayerSample {
		base := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
		return capacityprobe.CreatedLayerSample{Operation: "root_geometry", Outcome: "observed", Started: base.Add(8100 * time.Microsecond), Ended: base.Add(8200 * time.Microsecond)}
	}
	for _, kind := range []string{"valid", "mountinfo_before", "mountinfo_after", "before_physical", "after_physical", "regress", "many", "unknown", "errno"} {
		t.Run(kind, func(t *testing.T) {
			f := newLayerFixture(t)
			p := completeLayerPhysical()
			p.Samples = []capacityprobe.CreatedLayerSample{validSample()}
			switch kind {
			case "mountinfo_before", "mountinfo_after":
				p.Samples[0].Operation = kind
			case "before_physical":
				p.Samples[0].Started = p.Samples[0].Started.Add(-time.Second)
			case "after_physical":
				p.Samples[0].Ended = p.Samples[0].Ended.Add(time.Second)
			case "regress":
				p.Samples[0].Ended = p.Samples[0].Started
			case "many":
				p.Samples = make([]capacityprobe.CreatedLayerSample, 65)
			case "unknown":
				p.Samples[0].Operation = "sk-credential-shaped-error-789"
			case "errno":
				n := uint64(syscall.EIO)
				p.Samples[0].Errno = &n
			}
			original, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			f.physical = func(context.Context, string) (*capacityprobe.CreatedLayerPhysical, error) { return p, nil }
			g := f.collect(context.Background())
			if g.ObservationComplete != (kind == "valid" || kind == "mountinfo_before" || kind == "mountinfo_after") || g.Physical == nil || len(g.Physical.Samples) > 64 {
				t.Fatalf("sample bounds %s: %+v", kind, g)
			}
			after, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(original, after) {
				t.Fatal("collector mutated callback-owned facts")
			}
			if kind == "many" && !g.Physical.SamplesTruncated {
				t.Fatal("missing explicit truncation marker")
			}
			if kind == "errno" {
				*p.Samples[0].Errno = 123
				if *g.Physical.Samples[0].Errno != uint64(syscall.EIO) {
					t.Fatal("errno pointer aliases callback")
				}
			}
			assertLayerPrivate(t, g)
		})
	}
}

func TestCreatedLayerRetainsRawBeforeAfterAndQuotaBytes(t *testing.T) {
	f := newLayerFixture(t)
	p := completeLayerPhysical()
	for _, d := range []*capacityprobe.CreatedLayerDirectory{&p.Root, &p.Parent, &p.Diff} {
		d.Geometry[0] = 15
		d.Geometry[255] = 29
		d.GeometryAfter = d.Geometry
		d.FSXAttr[4] = 22
		d.FSXAttr[27] = 33
		d.FSXAttrAfter = d.FSXAttr
	}
	p.QStatV[159] = 47
	p.TargetQuota[111] = 55
	p.DefaultQuota[111] = 71
	f.physical = func(context.Context, string) (*capacityprobe.CreatedLayerPhysical, error) { return p, nil }
	g := f.collect(context.Background())
	if !g.ObservationComplete || !reflect.DeepEqual(g.Physical, p) || g.ExecutionEnabled || g.CapacityReady {
		t.Fatal("raw finite observations changed or granted authority")
	}
}
