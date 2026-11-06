package capacityprobe

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func manifestFixture() Manifest {
	return Manifest{"switchback.capacity.collection_manifest.v1", "switchback-core1011", Socket, Root}
}
func TestManifestRejectsAuthorityExpansion(t *testing.T) {
	for _, raw := range []string{`{}`, `{"schema":"switchback.capacity.collection_manifest.v1","profile":"switchback-core1011","socket":"tcp://other","root":"/mnt/lima-sbquota"}`, `{"schema":"switchback.capacity.collection_manifest.v1","profile":"switchback-core1011","socket":"/var/run/docker.sock","root":"/mnt/lima-sbquota","approved":true}`} {
		if _, err := ParseManifest([]byte(raw)); err == nil {
			t.Fatal("accepted expanded or incomplete manifest")
		}
	}
}
func TestCollectorCanaryAndInventory(t *testing.T) {
	const secret = "S3_CANARY_SECRET_NOT_FOR_EVIDENCE"
	id := strings.Repeat("a", 64)
	id2 := strings.Repeat("b", 64)
	engineID := "550e8400-e29b-41d4-a716-446655440000"
	info := `{"ID":"` + engineID + `","ServerVersion":"29.9.0","DockerRootDir":"/mnt/lima-sbquota/docker","Driver":"overlay2","Labels":["` + secret + `"]}`
	states := map[string]string{id: "created", id2: "exited"}
	calls := []string{}
	s := Sources{Get: func(_ context.Context, path string) ([]byte, error) {
		calls = append(calls, path)
		if path == "/v1.55/info" {
			return []byte(info), nil
		}
		if path == "/v1.55/containers/json?all=1" {
			return []byte(`[{"Id":"` + id + `","Command":"` + secret + `"},{"Id":"` + id2 + `"}]`), nil
		}
		for key, state := range states {
			if path == "/v1.55/containers/"+key+"/json" {
				return []byte(`{"Id":"` + key + `","Image":"sha256:` + id + `","Config":{"Env":["TOKEN=` + secret + `"],"Labels":{"owner":"` + secret + `"}},"Args":["` + secret + `"],"State":{"Status":"` + state + `"},"HostConfig":{"NanoCPUs":2000000000,"Memory":4294967296,"MemorySwap":4294967296,"PidsLimit":512,"CgroupParent":"switchback-workers.slice"},"Mounts":[{"Source":"` + secret + `","Type":"volume"}]}`), nil
			}
		}
		return nil, errors.New(secret)
	}, Linux: func(*Collection) (*LinuxFacts, error) {
		return &LinuxFacts{BootID: "synthetic", MountNamespace: "synthetic"}, nil
	}}
	c := Collect(context.Background(), manifestFixture(), s)
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), secret) {
		t.Fatal("secret leaked")
	}
	if c.Status != "preparation_blocked" || c.Trust != "none" || c.Engine == nil || c.Engine.ID != engineID || len(c.Engine.Containers) != 2 || len(calls) != 4 {
		t.Fatalf("incomplete bounded inventory: %+v", c)
	}
	if c.Engine.Containers[0].State != "created" || c.Engine.Containers[1].State != "exited" {
		t.Fatal("non-running inventory omitted")
	}
	if c.Engine.Containers[0].UnclassifiedMounts != 1 {
		t.Fatal("unknown mount not diagnosed")
	}
}
func TestCollectorRejectsMalformedOversizeAndSecretErrors(t *testing.T) {
	secret := "ERROR_CANARY"
	for _, value := range []string{`{"Id":"` + secret + `"`, strings.Repeat(secret, MaxResponse/len(secret)+1)} {
		c := Collect(context.Background(), manifestFixture(), Sources{Get: func(context.Context, string) ([]byte, error) { return []byte(value), nil }, Linux: func(*Collection) (*LinuxFacts, error) { return nil, errors.New(secret) }})
		raw, _ := json.Marshal(c)
		if strings.Contains(string(raw), secret) || c.Engine != nil || c.Status != "preparation_blocked" {
			t.Fatal("unsafe parsing")
		}
	}
	reader := NewEngineReader()
	if _, err := reader(context.Background(), "/v1.55/containers/x/start"); err == nil {
		t.Fatal("mutation path accepted")
	}
	if _, err := reader(context.Background(), "https://elsewhere/info"); err == nil {
		t.Fatal("endpoint expansion accepted")
	}
}
func TestQuotaUnitsIdentityAndOverflow(t *testing.T) {
	raw := make([]byte, 112)
	raw[0] = 1
	raw[1] = 2
	binary.LittleEndian.PutUint32(raw[4:8], 100)
	binary.LittleEndian.PutUint64(raw[8:16], 1000)
	binary.LittleEndian.PutUint64(raw[40:48], 7)
	binary.LittleEndian.PutUint64(raw[24:32], 99)
	q, err := parseQuota(raw, 100)
	if err != nil || q.UnitBytes != 512 || *q.UsedBlocks != 7 || *q.HardInodes != 99 || len(q.RawHex) != 224 {
		t.Fatal("lost raw quota facts")
	}
	if _, err := parseQuota(raw, 101); err == nil {
		t.Fatal("wrong quota project accepted")
	}
	binary.LittleEndian.PutUint64(raw[40:48], math.MaxUint64)
	if _, err := parseQuota(raw, 100); err == nil {
		t.Fatal("overflow accepted")
	}
	if n, err := Scale(7, 4096); err != nil || n != 28672 {
		t.Fatal("statfs unit conversion")
	}
	if _, err := Scale(1, 0); err == nil {
		t.Fatal("zero unit accepted")
	}
}

func TestBoundedInventoryAndIdentityRejectInspectExpansion(t *testing.T) {
	id := strings.Repeat("c", 64)
	for _, inventory := range []string{`[{"Id":"../secret"}]`, `[{"Id":"` + id + `"},{"Id":"` + id + `"}]`, "[" + strings.TrimSuffix(strings.Repeat(`{"Id":"`+id+`"},`, MaxContainers+1), ",") + "]"} {
		inspections := 0
		c := Collect(context.Background(), manifestFixture(), Sources{Get: func(_ context.Context, path string) ([]byte, error) {
			if path == "/v1.55/info" {
				return []byte(`{}`), nil
			}
			if path == "/v1.55/containers/json?all=1" {
				return []byte(inventory), nil
			}
			inspections++
			return nil, errors.New("synthetic failure")
		}, Linux: func(*Collection) (*LinuxFacts, error) { return nil, errors.New("synthetic missing Linux") }})
		if inspections > 1 || c.Status != "preparation_blocked" {
			t.Fatal("inventory expanded requests")
		}
	}
}

func TestEngineUUIDSeparatedFromContainerIdentity(t *testing.T) {
	containerID := strings.Repeat("d", 64)
	for _, engineID := range []string{"550e8400-e29b-41d4-a716-446655440000", containerID, "not-an-engine", "550E8400-E29B-41D4-A716-446655440000"} {
		t.Run(engineID, func(t *testing.T) {
			c := Collect(context.Background(), manifestFixture(), Sources{Get: func(_ context.Context, path string) ([]byte, error) {
				if path == "/v1.55/info" {
					return []byte(`{"ID":"` + engineID + `","ServerVersion":"29.9.0","DockerRootDir":"/mnt/lima-sbquota/docker","Driver":"overlay2"}`), nil
				}
				if path == "/v1.55/containers/json?all=1" {
					return []byte(`[{"Id":"` + containerID + `"}]`), nil
				}
				return []byte(`{"Id":"` + containerID + `","Image":"sha256:` + containerID + `","State":{"Status":"created"}}`), nil
			}, Linux: func(*Collection) (*LinuxFacts, error) { return nil, errors.New("synthetic missing Linux") }})
			if engineID == "550e8400-e29b-41d4-a716-446655440000" {
				if c.Engine == nil || c.Engine.ID != engineID || len(c.Engine.Containers) != 1 || c.Engine.Containers[0].ID != containerID {
					t.Fatal("canonical Engine UUID and distinct inspected container lost")
				}
			} else if c.Engine != nil {
				t.Fatal("invalid Engine identity retained")
			}
			if c.Status != "preparation_blocked" || c.Trust != "none" {
				t.Fatal("identity observation granted authority")
			}
		})
	}
}

func TestControlledErrnoNeverRetainsPathOrCauseCanary(t *testing.T) {
	const canary = "ERRNO_CANARY_PRIVATE_PATH_OR_BODY"
	for _, errno := range []syscall.Errno{syscall.ENOENT, syscall.EACCES, syscall.EIO} {
		err := kernelReadError("fixed kernel read failed", &os.PathError{Op: canary, Path: canary, Err: errno})
		var actual syscall.Errno
		if !errors.As(err, &actual) || actual != errno {
			t.Fatal("low-level errno lost")
		}
		if strings.Contains(err.Error(), canary) {
			t.Fatal("canary retained in controlled error")
		}
		c := Collection{}
		c.sample("fixed_proc_read", time.Unix(0, 0), err)
		raw, _ := json.Marshal(c)
		if len(c.Samples) != 1 || c.Samples[0].Errno == nil || *c.Samples[0].Errno != uint64(errno) || c.Samples[0].Operation != "fixed_proc_read" || c.Samples[0].Outcome != "unavailable" || strings.Contains(string(raw), canary) {
			t.Fatal("errno diagnostic missing or unsafe")
		}
	}
	err := kernelReadError("fixed kernel semantic failure", errors.New(canary))
	var errno syscall.Errno
	if errors.As(err, &errno) || strings.Contains(err.Error(), canary) {
		t.Fatal("semantic failure retained unsafe cause")
	}
}
