package createdlayeripc

import "net"

// listenAt is native-only. The parent FD remains pinned until cleanup; callbacks
// cannot select a public address or change the fixed filesystem leaf.
type endpointOps struct {
	files   fileOps
	pid     int
	missing func(error) bool
	bind    func(int) (net.Listener, error)
	chmod   func(int) error
	unlink  func(int) error
}

func listenFixed(ops endpointOps) (net.Listener, func(), error) {
	return listenFixedFor(ops, legacyObserverPlacement)
}

func listenFixedFor(ops endpointOps, policy observerPlacement) (net.Listener, func(), error) {
	fs := ops.files
	if placementFor(fs, ops.pid, policy) != nil {
		return nil, nil, errNative
	}
	p, e := walk(fs, "/run/switchback-created-layer")
	if e != nil {
		return nil, nil, errNative
	}
	release := true
	defer func() {
		if release {
			fs.close(p)
		}
	}()
	st, e := fs.stat(p)
	if e != nil || !secureDirectory(st, true) {
		return nil, nil, errNative
	}
	if _, e = fs.leaf(p, "observer.sock"); !ops.missing(e) {
		return nil, nil, errNative
	}
	ln, e := ops.bind(p)
	if e != nil {
		return nil, nil, errNative
	}
	bound, e := fs.leaf(p, "observer.sock")
	if e != nil || bound.mode&modeType != modeSocket || bound.uid != 0 || bound.gid != 0 || bound.nlink != 1 {
		ln.Close()
		return nil, nil, errNative
	}
	removeBound := func() {
		leaf, e := fs.leaf(p, "observer.sock")
		if e == nil && leaf == bound {
			_ = ops.unlink(p)
		}
	}
	// The private root-only directory protects the brief bind-to-chmod interval.
	if ops.chmod(p) != nil {
		ln.Close()
		removeBound()
		return nil, nil, errNative
	}
	own, e := socketIdentity(fs, p)
	if e != nil || own.dev != bound.dev || own.ino != bound.ino || !sameSocket(fs, p, own) {
		ln.Close()
		return nil, nil, errNative
	}
	release = false
	cleanup := func() {
		if sameSocket(fs, p, own) {
			_ = ops.unlink(p)
		}
		fs.close(p)
	}
	return ln, cleanup, nil
}
