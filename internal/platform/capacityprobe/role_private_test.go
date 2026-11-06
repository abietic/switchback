package capacityprobe

import (
	"context"
	"errors"
	"testing"
)

type rolePrivateFixture struct {
	next, stats, reads int
	live               map[int]string
	replace            bool
	drift              bool
	fail               int
	calls              int
}

func (f *rolePrivateFixture) step() error {
	f.calls++
	if f.calls == f.fail {
		return errors.New("private read fault")
	}
	return nil
}
func (f *rolePrivateFixture) open(_ context.Context, p string) (int, error) {
	if e := f.step(); e != nil {
		return -1, e
	}
	f.next++
	f.live[f.next] = p
	return f.next, nil
}
func (f *rolePrivateFixture) close(fd int) { delete(f.live, fd) }
func (f *rolePrivateFixture) stat(_ context.Context, fd int) (createdLayerMetadata, error) {
	if e := f.step(); e != nil {
		return createdLayerMetadata{}, e
	}
	f.stats++
	inode := uint64(100)
	if f.live[fd] == "/mnt/lima-sbquota/core/capacity-publisher-keys" {
		inode++
	}
	if f.replace && f.stats == 2 {
		inode += 100
	}
	return createdLayerMetadata{Mode: 0040700, Inode: inode, Major: 8, Minor: 7, MountID: 73, FSType: 0x58465342}, nil
}
func (f *rolePrivateFixture) mountinfo(_ context.Context) ([]byte, error) {
	if e := f.step(); e != nil {
		return nil, e
	}
	f.reads++
	raw := "1 1 0:1 / / rw - ext4 /dev/root rw\n73 1 8:7 / /mnt/lima-sbquota rw,prjquota - xfs /dev/test rw,prjquota\n"
	if f.drift && f.reads == 2 {
		raw += "74 1 0:2 / /other rw - tmpfs tmpfs rw\n"
	}
	return []byte(raw), nil
}
func TestRolePrivateDirectoryReaderIdentityAndCleanup(t *testing.T) {
	makeFixture := func() *rolePrivateFixture { return &rolePrivateFixture{live: map[int]string{}} }
	f := makeFixture()
	v, e := observeRolePrivateDirectories(context.Background(), f)
	if e != nil || len(v) != 2 || v[0].Root != "/core/capacity-publication-private" || v[0].Inode != 100 || len(f.live) != 0 {
		t.Fatalf("positive: %+v %v open=%v", v, e, f.live)
	}
	for _, name := range []string{"reopened-replacement", "mount-drift"} {
		t.Run(name, func(t *testing.T) {
			g := makeFixture()
			g.replace = name == "reopened-replacement"
			g.drift = name == "mount-drift"
			if _, e := observeRolePrivateDirectories(context.Background(), g); e == nil {
				t.Fatal("identity drift accepted")
			}
			if len(g.live) != 0 {
				t.Fatal("descriptors leaked")
			}
		})
	}
	for j := 1; j <= f.calls; j++ {
		g := makeFixture()
		g.fail = j
		if _, e := observeRolePrivateDirectories(context.Background(), g); e == nil {
			t.Fatalf("fault %d accepted", j)
		}
		if len(g.live) != 0 {
			t.Fatalf("fault %d leaked descriptors", j)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := makeFixture()
	if _, e := observeRolePrivateDirectories(ctx, g); e == nil || g.calls != 0 {
		t.Fatal("cancelled read performed I/O")
	}
}
