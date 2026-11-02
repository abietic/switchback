package capacitysnapshot

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

type qualificationTransfer struct {
	Schema string `json:"schema"`
	Leaf   string `json:"leaf"`
	Digest string `json:"digest"`
	Data   []byte `json:"data"`
}

func qualificationTransferLeaf(s string) bool {
	switch s {
	case "host-material.json", "staged.json", "window-open.json", "candidate.json", "host-before.json", "native-observations.json", "host-observations.json", "common-window.json", "static-intent.json", "created.json":
		return true
	}
	return false
}
func qualificationTransferDirectory(ctx context.Context) (qualificationCandidateDirectory, error) {
	if runtime.GOOS == "darwin" {
		h, e := os.UserHomeDir()
		if e != nil {
			return qualificationCandidateDirectory{}, e
		}
		return openQualificationCandidate(ctx, filepath.Join(h, ".switchback", "qualification-candidate"), uint32(os.Getuid()))
	}
	return openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
}
func qualificationTransferReceive(ctx context.Context) error {
	raw, e := io.ReadAll(io.LimitReader(os.Stdin, 2*MaxBytes+1))
	if e != nil || len(raw) > 2*MaxBytes {
		return errQualification
	}
	var v qualificationTransfer
	// The transport wrapper allows base64 expansion; its payload still uses the
	// unchanged 1MiB strict production decoder/reader cap.
	if e = qualificationDecodeTransfer(raw, &v); e != nil {
		return e
	}
	d, e := qualificationTransferDirectory(ctx)
	if e != nil {
		return e
	}
	defer d.close()
	if e = qualificationTransferBind(ctx, d, v); e != nil {
		return e
	}
	return d.write(ctx, v.Leaf, v.Data)
}
func qualificationTransferEmit(ctx context.Context, leaf string) error {
	if !qualificationTransferLeaf(leaf) {
		return errQualification
	}
	d, e := qualificationTransferDirectory(ctx)
	if e != nil {
		return e
	}
	defer d.close()
	raw, e := d.read(ctx, leaf)
	if e != nil {
		return e
	}
	return qualificationWriteTransfer(os.Stdout, qualificationTransfer{"switchback.qualification.transfer.v1", leaf, Digest(raw), raw})
}
func qualificationTransferBind(ctx context.Context, d qualificationCandidateDirectory, v qualificationTransfer) error {
	if v.Schema != "switchback.qualification.transfer.v1" || !qualificationTransferLeaf(v.Leaf) || len(v.Data) == 0 || len(v.Data) > MaxBytes || Digest(v.Data) != v.Digest {
		return errQualification
	}
	if v.Leaf == "host-material.json" {
		var h qualificationHostMaterial
		if strictJSON(v.Data, &h) != nil || h.Schema != "switchback.qualification.host_material.v1" || validateQualificationHostTarget(h.Host) != nil {
			return errQualification
		}
		return nil
	}
	if v.Leaf == "staged.json" {
		var s qualificationStaged
		if strictJSON(v.Data, &s) != nil || qualificationValidateStaged(s) != nil || qualificationValidateCreateBoundStaged(s) != nil {
			return errQualification
		}
		return nil
	}
	var s qualificationStaged
	if e := d.decode(ctx, "staged.json", &s); e != nil {
		return e
	}
	switch v.Leaf {
	case "window-open.json":
		var w qualificationWindowOpen
		if strictJSON(v.Data, &w) != nil || w.StagedDigest != qualificationDigest(s) || w.Nonce != s.Nonce || w.ID != s.WindowID {
			return errQualification
		}
	case "candidate.json":
		var c qualificationCandidateRecord
		var w qualificationWindowOpen
		if d.decode(ctx, "window-open.json", &w) != nil || strictJSON(v.Data, &c) != nil || c.OpenDigest != qualificationDigest(w) || c.StagedDigest != qualificationDigest(s) || c.StaticIntentDigest != qualificationStaticIntentDigest(s) {
			return errQualification
		}
	default:
		var c qualificationCandidateRecord
		if e := d.decode(ctx, "candidate.json", &c); e != nil {
			return e
		}
		switch v.Leaf {
		case "host-before.json":
			var h qualificationHostBefore
			if strictJSON(v.Data, &h) != nil || h.Candidate != c.Fingerprint || h.OpenDigest != c.OpenDigest {
				return errQualification
			}
		case "native-observations.json":
			var n qualificationNativeObservation
			if strictJSON(v.Data, &n) != nil || n.Candidate != c.Fingerprint || n.OpenDigest != c.OpenDigest || validateQualificationCleanup(n.Cleanup, c.TestProject) != nil {
				return errQualification
			}
		case "host-observations.json":
			var h qualificationHostReceipt
			if strictJSON(v.Data, &h) != nil || h.Candidate != c.Fingerprint {
				return errQualification
			}
		case "common-window.json":
			var common qualificationCommonEnd
			if strictJSON(v.Data, &common) != nil || common.Candidate != c.Fingerprint {
				return errQualification
			}
		default:
			return fmt.Errorf("transfer is not a runtime input")
		}
	}
	return qualificationContext(ctx)
}
