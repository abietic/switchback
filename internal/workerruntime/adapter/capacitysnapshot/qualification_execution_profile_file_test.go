package capacitysnapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func executionProfileFixtureStat(ctx context.Context, fd int) (unix.Stat_t, error) {
	stat, err := qualificationStat(ctx, fd)
	// The fixture controls root identity only; bytes, flags, links, modes and
	// replacement/rename behavior still come from the real private file seam.
	stat.Uid, stat.Gid = 0, 0
	return stat, err
}

func TestQualificationExecutionProfilePrivateFile(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
		mode string
	}{
		{name: "missing", mode: "disabled"},
		{name: "disabled", data: `{"schema":"switchback.qualification.execution_profile.v1","mode":"disabled"}`, mode: "disabled"},
		{name: "worker_delivery", data: `{"schema":"switchback.qualification.execution_profile.v1","mode":"worker_delivery"}`, mode: "worker_delivery"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if test.data != "" {
				if err := os.WriteFile(filepath.Join(root, "execution-profile.json"), []byte(test.data), 0400); err != nil {
					t.Fatal(err)
				}
			}
			dir, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer dir.Close()
			profile, err := qualificationExecutionProfileAt(context.Background(), dir, executionProfileFixtureStat)
			if err != nil || profile.Schema != "switchback.qualification.execution_profile.v1" || profile.Mode != test.mode {
				t.Fatalf("profile=%+v error=%v", profile, err)
			}
		})
	}
}

func TestQualificationExecutionProfilePrivateFileRejectsUnsafeOrChangingLeaf(t *testing.T) {
	for _, failure := range []string{"uid", "gid", "mode0440", "mode0600", "special_mode", "symlink", "hardlink", "directory", "fifo", "empty", "oversize", "fd_changed", "replaced", "removed", "stat_failure", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "execution-profile.json")
			raw := []byte(`{"schema":"switchback.qualification.execution_profile.v1","mode":"worker_delivery"}`)
			if failure == "empty" {
				raw = nil
			}
			if failure == "oversize" {
				raw = []byte(strings.Repeat(" ", 4097))
			}
			switch failure {
			case "symlink":
				if err := os.Symlink("elsewhere", path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0400); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(path, 0400); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(path, raw, 0400); err != nil {
					t.Fatal(err)
				}
				if failure == "hardlink" {
					if err := os.Link(path, filepath.Join(root, "another-link")); err != nil {
						t.Fatal(err)
					}
				}
			}
			dir, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer dir.Close()
			calls := 0
			stat := func(ctx context.Context, fd int) (unix.Stat_t, error) {
				value, err := executionProfileFixtureStat(ctx, fd)
				calls++
				switch failure {
				case "uid":
					value.Uid = 501
				case "gid":
					value.Gid = 20
				case "mode0440":
					value.Mode = unix.S_IFREG | 0440
				case "mode0600":
					value.Mode = unix.S_IFREG | 0600
				case "special_mode":
					value.Mode = unix.S_IFREG | 04400
				case "fd_changed":
					if calls > 1 {
						value.Ino++
					}
				case "replaced", "removed":
					if calls == 1 {
						if renameErr := os.Rename(path, path+".old"); renameErr != nil {
							t.Fatal(renameErr)
						}
						if failure == "replaced" {
							if writeErr := os.WriteFile(path, raw, 0400); writeErr != nil {
								t.Fatal(writeErr)
							}
						}
					}
				case "stat_failure":
					err = errors.New("fixture stat failure")
				}
				return value, err
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "cancelled" {
				cancel()
			}
			if profile, err := qualificationExecutionProfileAt(ctx, dir, stat); err == nil {
				t.Fatalf("unsafe or changed leaf became accepted profile: %+v", profile)
			}
		})
	}
}
