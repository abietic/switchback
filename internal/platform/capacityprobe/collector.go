// Package capacityprobe collects untrusted, bounded diagnostics. It cannot issue admission evidence.
package capacityprobe

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"regexp"
	"sort"
	"syscall"
	"time"
)

const Root = "/mnt/lima-sbquota"
const Socket = "/var/run/docker.sock"
const MaxResponse = 1 << 20
const MaxContainers = 32

type Manifest struct {
	Schema  string `json:"schema"`
	Profile string `json:"profile"`
	Socket  string `json:"socket"`
	Root    string `json:"root"`
}

func ParseManifest(raw []byte) (Manifest, error) {
	var m Manifest
	if len(raw) > 4096 {
		return m, errors.New("manifest oversized")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF || m.Schema != "switchback.capacity.collection_manifest.v1" || m.Profile != "switchback-core1011" || m.Socket != Socket || m.Root != Root {
		return Manifest{}, errors.New("deployment manifest rejected")
	}
	return m, nil
}
func Scale(value, unit uint64) (uint64, error) {
	if unit == 0 || value > math.MaxUint64/unit {
		return 0, errors.New("unit overflow or zero")
	}
	return value * unit, nil
}

type Sample struct {
	Errno     *uint64   `json:"errno,omitempty"`
	Sequence  int       `json:"sequence"`
	Operation string    `json:"operation"`
	Started   time.Time `json:"started"`
	Ended     time.Time `json:"ended"`
	Outcome   string    `json:"outcome"`
}
type Container struct {
	ID                 string   `json:"id"`
	Image              string   `json:"image"`
	State              string   `json:"state"`
	NanoCPUs           int64    `json:"nano_cpus"`
	Memory             int64    `json:"memory"`
	MemorySwap         int64    `json:"memory_swap"`
	PIDs               *int64   `json:"pids"`
	CgroupParent       string   `json:"cgroup_parent"`
	FixedMounts        []string `json:"fixed_mounts"`
	UnclassifiedMounts int      `json:"unclassified_mounts"`
}
type Engine struct {
	ID         string      `json:"id"`
	Version    string      `json:"version"`
	Root       string      `json:"root"`
	Driver     string      `json:"driver"`
	Containers []Container `json:"containers"`
}
type Quota struct {
	RawHex     string  `json:"raw_hex,omitempty"`
	Status     string  `json:"status"`
	ABI        string  `json:"abi"`
	UnitBytes  uint64  `json:"unit_bytes"`
	HardBlocks *uint64 `json:"hard_blocks"`
	UsedBlocks *uint64 `json:"used_blocks"`
	HardInodes *uint64 `json:"hard_inodes"`
	UsedInodes *uint64 `json:"used_inodes"`
}
type Filesystem struct {
	Path         string   `json:"path"`
	Type         int64    `json:"type"`
	BlockSize    int64    `json:"block_size"`
	FragmentSize int64    `json:"fragment_size"`
	Blocks       uint64   `json:"blocks"`
	Free         uint64   `json:"free"`
	Available    uint64   `json:"available"`
	Files        uint64   `json:"files"`
	FreeFiles    uint64   `json:"free_files"`
	FSID         [2]int32 `json:"fsid"`
	ProjectID    *uint32  `json:"project_id"`
	XFlags       *uint32  `json:"xflags"`
	Quota        Quota    `json:"quota"`
}
type LinuxFacts struct {
	BootID         string            `json:"boot_id"`
	MountNamespace string            `json:"mount_namespace"`
	MountID        string            `json:"mount_id"`
	Device         string            `json:"device_major_minor"`
	FilesystemType string            `json:"filesystem_type"`
	Filesystems    []Filesystem      `json:"filesystems"`
	Cgroups        map[string]string `json:"cgroups"`
}
type Collection struct {
	Schema   string      `json:"schema"`
	Status   string      `json:"status"`
	Trust    string      `json:"trust"`
	Started  time.Time   `json:"started"`
	Ended    time.Time   `json:"ended"`
	Engine   *Engine     `json:"engine"`
	Linux    *LinuxFacts `json:"linux"`
	Samples  []Sample    `json:"samples"`
	Blockers []string    `json:"blockers"`
}

// Sources is an internal deployment/test seam, never Task-controlled endpoint selection.
type Sources struct {
	Get   func(context.Context, string) ([]byte, error)
	Linux func(*Collection) (*LinuxFacts, error)
	Now   func() time.Time
}

func (c *Collection) sample(op string, start time.Time, err error) {
	outcome := "observed"
	if err != nil {
		outcome = "unavailable"
	}
	var number *uint64
	var errno syscall.Errno
	if errors.As(err, &errno) {
		n := uint64(errno)
		number = &n
	}
	c.Samples = append(c.Samples, Sample{Sequence: len(c.Samples) + 1, Operation: op, Started: start, Ended: time.Now().UTC(), Outcome: outcome, Errno: number})
}

// Engine identity is a canonical UUID, distinct from 64-hex container IDs.
// Source: moby docker-v29.1.3 daemon/id.go LoadOrCreateID -> uuid.New().String().
// Exact candidate 29.9.0 package and actual Engine identity remain unverified.
var engineID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var hexID = regexp.MustCompile(`^[0-9a-f]{64}$`)
var version = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func decode(raw []byte, out any) error {
	if len(raw) > MaxResponse {
		return errors.New("engine response oversized")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("engine response invalid")
	}
	return nil
}
func Collect(ctx context.Context, m Manifest, s Sources) Collection {
	c := Collection{Schema: "switchback.capacity.untrusted_collection.v1", Status: "preparation_blocked", Trust: "none",
		Blockers: []string{"physical_enforcement_unproven", "all_time_Mmax_and_Delta_inode_unknown", "exact_SRU_unknown", "host_backing_unknown", "writer_coverage_unknown", "provisioner_cgroup_swap_and_create_before_start_gate_missing"}}
	if s.Now == nil {
		s.Now = time.Now
	}
	c.Started = s.Now().UTC()
	raw, _ := json.Marshal(m)
	if _, err := ParseManifest(raw); err != nil {
		c.Blockers = append(c.Blockers, "manifest_invalid")
		c.Ended = s.Now().UTC()
		return c
	}
	if s.Get == nil || s.Linux == nil {
		c.Blockers = append(c.Blockers, "sources_unconfigured")
		c.Ended = s.Now().UTC()
		return c
	}
	get := func(path string, out any) error {
		start := s.Now().UTC()
		raw, err := s.Get(ctx, path)
		if err == nil {
			err = decode(raw, out)
		}
		c.sample(path, start, err)
		return err
	}
	var info struct{ ID, ServerVersion, DockerRootDir, Driver string }
	if err := get("/v1.55/info", &info); err != nil || !engineID.MatchString(info.ID) || !version.MatchString(info.ServerVersion) || info.DockerRootDir != Root+"/docker" || info.Driver != "overlay2" {
		c.Blockers = append(c.Blockers, "engine_identity_unavailable_or_unexpected")
	} else {
		c.Engine = &Engine{ID: info.ID, Version: info.ServerVersion, Root: info.DockerRootDir, Driver: info.Driver}
	}
	var inventory []struct {
		ID string `json:"Id"`
	}
	if err := get("/v1.55/containers/json?all=1", &inventory); err != nil || len(inventory) > MaxContainers {
		c.Blockers = append(c.Blockers, "inventory_unavailable_or_excessive")
	} else {
		seen := map[string]bool{}
		for _, item := range inventory {
			if !hexID.MatchString(item.ID) || seen[item.ID] {
				c.Blockers = append(c.Blockers, "inventory_identity_invalid")
				continue
			}
			seen[item.ID] = true
			var v struct {
				ID         string `json:"Id"`
				Image      string
				State      struct{ Status string }
				HostConfig struct {
					NanoCPUs, Memory, MemorySwap int64
					PidsLimit                    *int64
					CgroupParent                 string
					Privileged                   bool
					NetworkMode                  string
				}
				Mounts []struct {
					Source, Destination, Type string
					RW                        bool
				}
			}
			if err := get("/v1.55/containers/"+item.ID+"/json", &v); err != nil || v.ID != item.ID || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(v.Image) {
				c.Blockers = append(c.Blockers, "inspect_identity_unavailable")
				continue
			}
			stateOK := false
			for _, state := range []string{"created", "running", "paused", "restarting", "removing", "exited", "dead"} {
				if v.State.Status == state {
					stateOK = true
				}
			}
			if !stateOK {
				c.Blockers = append(c.Blockers, "container_state_invalid")
				continue
			}
			x := Container{ID: v.ID, Image: v.Image, State: v.State.Status, NanoCPUs: v.HostConfig.NanoCPUs, Memory: v.HostConfig.Memory, MemorySwap: v.HostConfig.MemorySwap, PIDs: v.HostConfig.PidsLimit}
			if v.HostConfig.CgroupParent == "switchback-core.slice" || v.HostConfig.CgroupParent == "switchback-workers.slice" {
				x.CgroupParent = v.HostConfig.CgroupParent
			} else {
				c.Blockers = append(c.Blockers, "cgroup_parent_unknown")
			}
			if v.HostConfig.Privileged || v.HostConfig.NetworkMode == "host" || x.Memory <= 0 || x.NanoCPUs <= 0 || x.MemorySwap != x.Memory || x.PIDs == nil || *x.PIDs <= 0 {
				c.Blockers = append(c.Blockers, "resource_or_isolation_unproven")
			}
			for _, mount := range v.Mounts {
				matched := false
				for _, fixed := range []string{Root + "/core/source", Root + "/core/blocked-status", Root + "/core/postgres/data", Root + "/core/postgres/wal", Root + "/product", Socket} {
					if mount.Source == fixed && mount.Type == "bind" {
						x.FixedMounts = append(x.FixedMounts, fixed)
						matched = true
						break
					}
				}
				if !matched {
					x.UnclassifiedMounts++
				}
			}
			if x.UnclassifiedMounts > 0 {
				c.Blockers = append(c.Blockers, "unclassified_mount")
			}
			// Identity observations do not confer deployment ownership, even when labels look plausible.
			c.Blockers = append(c.Blockers, "container_owner_and_layer_quota_unproven")
			// Without a valid Engine identity, discard inventory facts rather than bind them to an unknown owner.
			if c.Engine != nil {
				c.Engine.Containers = append(c.Engine.Containers, x)
			}
		}
	}
	start := s.Now().UTC()
	facts, err := s.Linux(&c)
	c.sample("linux", start, err)
	if err != nil {
		c.Blockers = append(c.Blockers, "linux_facts_unavailable")
	} else {
		c.Linux = facts
	}
	sort.Strings(c.Blockers)
	c.Ended = s.Now().UTC()
	return c
}
func NewEngineReader() func(context.Context, string) ([]byte, error) {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", Socket)
	}}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect forbidden") }}
	return func(ctx context.Context, path string) ([]byte, error) {
		if path != "/v1.55/info" && path != "/v1.55/containers/json?all=1" && !regexp.MustCompile(`^/v1.55/containers/[0-9a-f]{64}/json$`).MatchString(path) {
			return nil, errors.New("engine path forbidden")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://engine"+path, nil)
		if err != nil {
			return nil, errors.New("engine request invalid")
		}
		response, err := client.Do(req)
		if err != nil {
			return nil, errors.New("engine unavailable")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, errors.New("engine response rejected")
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, MaxResponse+1))
		if err != nil || len(raw) > MaxResponse {
			return nil, errors.New("engine response invalid or oversized")
		}
		return raw, nil
	}
}

// parseQuota preserves the complete UAPI payload and validates identity/units before exposure.
func parseQuota(raw []byte, project uint32) (Quota, error) {
	q := Quota{Status: "unavailable", ABI: "linux_v6.8_fs_disk_quota_112_le", UnitBytes: 512}
	if len(raw) != 112 || raw[0] != 1 || raw[1] != 2 || binary.LittleEndian.Uint32(raw[4:8]) != project {
		return q, errors.New("quota ABI or identity invalid")
	}
	hard, used := binary.LittleEndian.Uint64(raw[8:16]), binary.LittleEndian.Uint64(raw[40:48])
	if _, err := Scale(hard, 512); err != nil {
		return q, err
	}
	if _, err := Scale(used, 512); err != nil {
		return q, err
	}
	ih, iu := binary.LittleEndian.Uint64(raw[24:32]), binary.LittleEndian.Uint64(raw[48:56])
	q.Status = "observed"
	q.RawHex = hex.EncodeToString(raw)
	q.HardBlocks = &hard
	q.UsedBlocks = &used
	q.HardInodes = &ih
	q.UsedInodes = &iu
	return q, nil
}

// kernelReadError retains only an errno cause, never PathError paths or arbitrary error text.
// All callers supply a fixed operation description; semantic failures carry no errno.
func kernelReadError(operation string, cause error) error {
	var errno syscall.Errno
	if errors.As(cause, &errno) {
		return fmt.Errorf("%s: %w", operation, errno)
	}
	return errors.New(operation)
}
