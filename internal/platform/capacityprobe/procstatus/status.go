// Package procstatus replays the fixed bounded Linux proc status fields.
package procstatus

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

var ErrInvalid = errors.New("process status invalid")

type Status struct {
	PID               uint64    `json:"pid"`
	TGID              uint64    `json:"tgid"`
	TID               uint64    `json:"tid"`
	StartTimeTicks    uint64    `json:"start_time_ticks"`
	UIDs              [4]uint32 `json:"uids"`
	GIDs              [4]uint32 `json:"gids"`
	CapInh            string    `json:"cap_inh"`
	CapPrm            string    `json:"cap_prm"`
	CapEff            string    `json:"cap_eff"`
	CapBnd            string    `json:"cap_bnd"`
	CapAmb            string    `json:"cap_amb"`
	NoNewPrivs        uint32    `json:"no_new_privs"`
	Seccomp           uint32    `json:"seccomp"`
	UnifiedCgroupPath string    `json:"unified_cgroup_path"`
}

func Parse(raw []byte) (Status, error) {
	var out Status
	if len(raw) == 0 || len(raw) > 65536 || raw[len(raw)-1] != '\n' || bytes.IndexByte(raw, 0) >= 0 {
		return out, ErrInvalid
	}
	selected := map[string]bool{"Pid": true, "Tgid": true, "Uid": true, "Gid": true, "CapInh": true, "CapPrm": true, "CapEff": true, "CapBnd": true, "CapAmb": true, "NoNewPrivs": true, "Seccomp": true}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || !selected[key] {
			continue
		}
		if seen[key] {
			return Status{}, ErrInvalid
		}
		seen[key] = true
		fields := strings.Fields(value)
		switch key {
		case "Uid", "Gid":
			if len(fields) != 4 {
				return Status{}, ErrInvalid
			}
			var tuple [4]uint32
			for i, v := range fields {
				n, e := decimal(v, 32)
				if e != nil {
					return Status{}, ErrInvalid
				}
				tuple[i] = uint32(n)
			}
			if key == "Uid" {
				out.UIDs = tuple
			} else {
				out.GIDs = tuple
			}
		case "CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb":
			if len(fields) != 1 || len(fields[0]) != 16 {
				return Status{}, ErrInvalid
			}
			if _, e := hex.DecodeString(fields[0]); e != nil {
				return Status{}, ErrInvalid
			}
			v := strings.ToLower(fields[0])
			switch key {
			case "CapInh":
				out.CapInh = v
			case "CapPrm":
				out.CapPrm = v
			case "CapEff":
				out.CapEff = v
			case "CapBnd":
				out.CapBnd = v
			case "CapAmb":
				out.CapAmb = v
			}
		default:
			if len(fields) != 1 {
				return Status{}, ErrInvalid
			}
			n, e := decimal(fields[0], 32)
			if e != nil {
				return Status{}, ErrInvalid
			}
			switch key {
			case "Pid":
				if n == 0 {
					return Status{}, ErrInvalid
				}
				out.TID = n
			case "Tgid":
				if n == 0 {
					return Status{}, ErrInvalid
				}
				out.TGID = n
				out.PID = n
			case "NoNewPrivs":
				if n > 1 {
					return Status{}, ErrInvalid
				}
				out.NoNewPrivs = uint32(n)
			case "Seccomp":
				if n > 2 {
					return Status{}, ErrInvalid
				}
				out.Seccomp = uint32(n)
			}
		}
	}
	if len(seen) != len(selected) {
		return Status{}, ErrInvalid
	}
	return out, nil
}

func decimal(s string, bits int) (uint64, error) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, ErrInvalid
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, ErrInvalid
		}
	}
	return strconv.ParseUint(s, 10, bits)
}
