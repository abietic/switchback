package capacitysnapshot

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"
)

type qualificationEngineFDTuple struct{ flags, mount, inode uint64 }

// qualificationEngineDecodeFDTuple decodes the source or pin fdinfo fields.
// Flags belong to the source descriptor; the pin's flags are not a substitute.
func qualificationEngineDecodeFDTuple(raw []byte) (qualificationEngineFDTuple, error) {
	var result qualificationEngineFDTuple
	if len(raw) == 0 || len(raw) > 16384 {
		return result, errQualification
	}
	for _, b := range raw {
		if b > 127 {
			return result, errQualification
		}
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		name, value, hasColon := strings.Cut(line, ":")
		if name != "flags" && name != "mnt_id" && name != "ino" {
			continue
		}
		if !hasColon || seen[name] {
			return result, errQualification
		}
		seen[name] = true
		value = strings.TrimSpace(value)
		digits, base, bits := "0123456789", 10, 64
		if name == "flags" {
			digits, base, bits = "01234567", 8, 32
		}
		if value == "" || strings.Trim(value, digits) != "" {
			return result, errQualification
		}
		n, err := strconv.ParseUint(value, base, bits)
		if err != nil {
			return result, errQualification
		}
		switch name {
		case "flags":
			result.flags = n
		case "mnt_id":
			result.mount = n
		case "ino":
			result.inode = n
		}
	}
	if len(seen) != 3 {
		return result, errQualification
	}
	return result, nil
}

// These seams expose metadata only. An O_PATH pin binds a filesystem object,
// not its open-file-description; source flags must always come from fdinfo.
type qualificationEngineFDObject struct {
	Target string `json:"target"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Mode   uint32 `json:"mode"`
}
type qualificationEngineFDOps struct {
	openPin    func(context.Context, string) (int, error)
	pinStat    func(context.Context, int) (qualificationEngineFDObject, error)
	pinLink    func(context.Context, int) (string, error)
	pinInfo    func(context.Context, int) ([]byte, error)
	sourceLink func(context.Context, string) (string, error)
	sourceStat func(context.Context, string) (qualificationEngineFDObject, error)
	sourceInfo func(context.Context, string) ([]byte, error)
	closePin   func(context.Context, int) error
}
type qualificationEngineSourceVanished struct {
	Primitive string
	Cause     error
}

func (e *qualificationEngineSourceVanished) Error() string {
	return fmt.Sprintf("%s: %v", e.Primitive, e.Cause)
}
func (e *qualificationEngineSourceVanished) Unwrap() error { return e.Cause }

// Call only immediately around the source syscall, never around observation,
// decoding or cleanup. Do not search an arbitrary error tree for ENOENT.
func qualificationEngineSourceError(primitive string, e error) error {
	if e == syscall.ENOENT {
		return &qualificationEngineSourceVanished{primitive, e}
	}
	return e
}

type qualificationEngineFDRace struct {
	PID, FD       uint64
	Primitive     string
	Cause         error
	ProcessStable bool
}

func (e *qualificationEngineFDRace) Error() string {
	return fmt.Sprintf("Engine FD sampling race PID %d FD %d %s: %v", e.PID, e.FD, e.Primitive, e.Cause)
}
func (e *qualificationEngineFDRace) Unwrap() error { return e.Cause }

type qualificationEngineCleanupFailure struct {
	Primary  error
	Failures []error
}

func (e *qualificationEngineCleanupFailure) Error() string {
	return fmt.Sprintf("Engine cleanup failed: primary=%v cleanup=%v", e.Primary, e.Failures)
}
func (e *qualificationEngineCleanupFailure) Unwrap() []error {
	out := append([]error{}, e.Failures...)
	if e.Primary != nil {
		out = append(out, e.Primary)
	}
	return out
}
func qualificationEngineCleanup(primary, closeErr error) error {
	if closeErr == nil {
		return primary
	}
	return &qualificationEngineCleanupFailure{Primary: primary, Failures: []error{closeErr}}
}
func qualificationEngineFDTarget(s string) error {
	if s == "" || len(s) > 4096 || !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return fmt.Errorf("invalid Engine FD target")
	}
	return nil
}
func qualificationEngineSampleFD(ctx context.Context, pid, fd uint64, ops qualificationEngineFDOps) (result qualificationEngineProcessFD, err error) {
	name := strconv.FormatUint(fd, 10)
	if err = qualificationContext(ctx); err != nil {
		return
	}
	pin, e := ops.openPin(ctx, name)
	if e != nil {
		if v, ok := e.(*qualificationEngineSourceVanished); ok {
			e = &qualificationEngineFDRace{PID: pid, FD: fd, Primitive: v.Primitive, Cause: v}
		}
		return result, e
	}
	defer func() {
		// Closing must still release the handle after cancellation; context failure
		// poisons success/retry but does not excuse cleanup.
		err = qualificationEngineCleanup(err, ops.closePin(ctx, pin))
		if ce := qualificationContext(ctx); ce != nil {
			err = qualificationEngineCleanup(err, ce)
		}
		if err != nil {
			result = qualificationEngineProcessFD{}
		}
	}()
	defer func() {
		if v, ok := err.(*qualificationEngineSourceVanished); ok {
			err = &qualificationEngineFDRace{PID: pid, FD: fd, Primitive: v.Primitive, Cause: v}
		}
	}()
	check := func() error { return qualificationContext(ctx) }
	if err = check(); err != nil {
		return
	}
	pinned, e := ops.pinStat(ctx, pin)
	if e != nil {
		return result, e
	}
	if err = check(); err != nil {
		return
	}
	target, e := ops.pinLink(ctx, pin)
	if e != nil {
		return result, e
	}
	if err = qualificationEngineFDTarget(target); err != nil {
		return
	}
	if err = check(); err != nil {
		return
	}
	raw, e := ops.pinInfo(ctx, pin)
	if e != nil {
		return result, e
	}
	pi, e := qualificationEngineDecodeFDTuple(raw)
	if e != nil {
		return result, e
	}
	if pi.inode != pinned.Inode {
		return result, fmt.Errorf("invalid pin inode")
	}
	var objects [2]qualificationEngineFDObject
	var infos [2]qualificationEngineFDTuple
	for i := range objects {
		if err = check(); err != nil {
			return
		}
		link, e := ops.sourceLink(ctx, name)
		if e != nil {
			return result, e
		}
		if e = qualificationEngineFDTarget(link); e != nil {
			return result, e
		}
		if err = check(); err != nil {
			return
		}
		obj, e := ops.sourceStat(ctx, name)
		if e != nil {
			return result, e
		}
		obj.Target = link
		objects[i] = obj
		if err = check(); err != nil {
			return
		}
		raw, e := ops.sourceInfo(ctx, name)
		if e != nil {
			return result, e
		}
		infos[i], e = qualificationEngineDecodeFDTuple(raw)
		if e != nil {
			return result, e
		}
	}
	// Flags drift is a hard failure even if object metadata also drifted.
	if infos[0].flags != infos[1].flags {
		return result, fmt.Errorf("source FD flags drift")
	}
	for i, obj := range objects {
		if obj.Target != target || obj.Device != pinned.Device || obj.Inode != pinned.Inode || obj.Mode&0170000 != pinned.Mode&0170000 || infos[i].mount != pi.mount || infos[i].inode != pi.inode {
			return result, &qualificationEngineFDRace{PID: pid, FD: fd, Primitive: "object-drift", Cause: fmt.Errorf("FD object changed")}
		}
	}
	if strings.HasPrefix(target, "socket:") {
		ino, e := qualificationEngineSocketFDInode(target)
		if e != nil || pinned.Mode&0170000 != 0140000 || ino != pinned.Inode {
			return result, errQualification
		}
	}
	return qualificationEngineProcessFD{FD: fd, Target: target, Flags: infos[0].flags, Device: pinned.Device, Inode: pinned.Inode, Mount: pi.mount}, check()
}

type qualificationEngineFDDirectory interface {
	names(context.Context, int) ([]string, error)
	metadata() qualificationEngineFDOps
	close(context.Context) error
}
type qualificationEngineProcessOps struct {
	pids    func(context.Context) ([]uint64, error)
	read    func(context.Context, uint64, string, int64) ([]byte, error)
	exe     func(context.Context, uint64) (string, error)
	openFDs func(context.Context, uint64) (qualificationEngineFDDirectory, error)
}

// The same complete process loop runs on Linux and in portable primitive tests.
// Partial facts are returned on failure for hard-negative policy only.
func qualificationEngineProcessesWith(ctx context.Context, ops qualificationEngineProcessOps) ([]qualificationEngineProcess, error) {
	if e := qualificationContext(ctx); e != nil {
		return nil, e
	}
	pids, e := ops.pids(ctx)
	if e != nil {
		return nil, e
	}
	if len(pids) > 65536 {
		return nil, errQualification
	}
	out := []qualificationEngineProcess{}
	count := 0
	seen := map[uint64]bool{}
	for _, pid := range pids {
		if e := qualificationContext(ctx); e != nil {
			return out, e
		}
		if pid == 0 || pid > 1<<31-1 || seen[pid] {
			return out, errQualification
		}
		seen[pid] = true
		raw, e := ops.read(ctx, pid, "stat", 4096)
		if e != nil {
			return out, e
		}
		before, e := qualificationEngineParseProcessStat(raw)
		if e != nil || before.PID != pid {
			return out, errQualification
		}
		group, e := ops.read(ctx, pid, "cgroup", 4096)
		if e != nil {
			return out, e
		}
		if !strings.HasPrefix(string(group), "0::/") || !strings.HasSuffix(string(group), "\n") || strings.Count(string(group), "\n") != 1 {
			return out, errQualification
		}
		exe, e := ops.exe(ctx, pid)
		if e != nil {
			return out, e
		}
		p := qualificationEngineProcess{PID: pid, Start: before.Start, Exe: exe, Cgroup: strings.TrimSuffix(strings.TrimPrefix(string(group), "0::"), "\n"), FDs: []qualificationEngineProcessFD{}}
		// Add even an empty process before scanning so a foreign daemon cannot be
		// hidden by its first descriptor racing.
		out = append(out, p)
		at := len(out) - 1
		dir, e := ops.openFDs(ctx, pid)
		if e != nil {
			return out, e
		}
		enumeratedCount := 0
		scanErr := func() (err error) {
			defer func() {
				err = qualificationEngineCleanup(err, dir.close(ctx))
				if ce := qualificationContext(ctx); ce != nil {
					err = qualificationEngineCleanup(err, ce)
				}
			}()
			if e := qualificationContext(ctx); e != nil {
				return e
			}
			names, e := dir.names(ctx, 131073-count)
			if e != nil {
				return e
			}
			enumeratedCount = len(names)
			count += len(names)
			if count > 131072 {
				return errQualification
			}
			// Complete enumeration/validation precedes any pins (especially /proc/self).
			fds := make([]uint64, len(names))
			used := map[uint64]bool{}
			for i, name := range names {
				fd, e := strconv.ParseUint(name, 10, 20)
				if e != nil || name == "" || strings.Trim(name, "0123456789") != "" || used[fd] {
					return errQualification
				}
				used[fd] = true
				fds[i] = fd
			}
			for _, fd := range fds {
				row, e := qualificationEngineSampleFD(ctx, pid, fd, dir.metadata())
				if e != nil {
					return e
				}
				out[at].FDs = append(out[at].FDs, row)
			}
			return nil
		}()
		race, isRace := scanErr.(*qualificationEngineFDRace)
		if scanErr != nil && !isRace {
			return out, scanErr
		}
		// Never authorize a race before fresh process identity, exe and cgroup.
		if e := qualificationContext(ctx); e != nil {
			return out, e
		}
		raw, e = ops.read(ctx, pid, "stat", 4096)
		if e != nil {
			return out, e
		}
		after, e := qualificationEngineParseProcessStat(raw)
		if e != nil || qualificationEngineProcessStatStable(before, after, exe, enumeratedCount) != nil {
			return out, fmt.Errorf("Engine process identity changed")
		}
		currentExe, e := ops.exe(ctx, pid)
		if e != nil {
			return out, e
		}
		currentGroup, e := ops.read(ctx, pid, "cgroup", 4096)
		if e != nil {
			return out, e
		}
		if currentExe != exe || string(currentGroup) != string(group) {
			return out, fmt.Errorf("Engine process exe/cgroup changed")
		}
		if isRace {
			race.ProcessStable = true
			return out, race
		}
	}
	if e := qualificationContext(ctx); e != nil {
		return out, e
	}
	again, e := ops.pids(ctx)
	if e != nil {
		return out, e
	}
	if !reflect.DeepEqual(pids, again) {
		return out, fmt.Errorf("Engine complete process inventory changed")
	}
	return out, qualificationContext(ctx)
}

type qualificationEngineSocketSample struct {
	Parents     []qualificationEngineSocketParent
	Sockets     []qualificationEngineSocketIdentity
	ParentLinks []uint64
}
type qualificationEngineNativeOps struct {
	boot       func(context.Context) (string, error)
	filesystem func(context.Context) (qualificationEngineFilesystem, qualificationEngineRuntimeParent, error)
	daemon     func(context.Context, int, string) (qualificationEngineDaemon, error)
	tables     func(context.Context) ([]qualificationEngineListener, error)
	sockets    func(context.Context, []string, []qualificationEngineDaemon) (qualificationEngineSocketSample, error)
	processes  func(context.Context) ([]qualificationEngineProcess, error)
}
type qualificationEngineAttempt struct {
	Number   int
	Complete bool
	Cause    error
}
type qualificationEngineCollectionFailure struct {
	Attempts []qualificationEngineAttempt `json:"-"`
	Cause    error
}

func (e *qualificationEngineCollectionFailure) Error() string {
	return fmt.Sprintf("Engine native collection failed after %d attempt(s): %v", len(e.Attempts), e.Cause)
}
func (e *qualificationEngineCollectionFailure) Unwrap() error { return e.Cause }
func qualificationEngineAnchor(n qualificationEngineNative) qualificationEngineNative {
	n.Complete = false
	n.Listeners = nil
	n.Attempts = nil
	n.Daemons = append([]qualificationEngineDaemon{}, n.Daemons...)
	n.Sockets = append([]qualificationEngineSocketIdentity{}, n.Sockets...)
	n.SocketParents = append([]qualificationEngineSocketParent{}, n.SocketParents...)
	n.socketParentLinks = append([]uint64{}, n.socketParentLinks...)
	return n
}
func qualificationEngineNativePolicy(n qualificationEngineNative, rows []qualificationEngineListener, ps []qualificationEngineProcess, partial bool) ([]qualificationEngineListener, error) {
	if e := qualificationEngineProcessPolicy(ps, n.Daemons); e != nil {
		return nil, e
	}
	attached, e := qualificationEngineAttachOwners(rows, ps)
	if e != nil {
		return nil, e
	}
	return attached, qualificationEngineListenerPolicy(n.Daemons, n.Sockets, attached, partial)
}

// Every observed raw table must retain known negatives. Each process observation
// is checked independently and only as partial evidence: never merge snapshots
// into fictitious complete coverage, or discard an earlier owner because a later
// partial scan has not reached it. At most four bounded snapshots exist across
// the collection's two attempts; fresh complete scans alone establish success.
func qualificationEngineKnownNegatives(n qualificationEngineNative, rows []qualificationEngineListener, observed [][]qualificationEngineProcess) error {
	err := qualificationEngineListenerPolicy(n.Daemons, n.Sockets, rows, true)
	for _, ps := range observed {
		if _, pe := qualificationEngineNativePolicy(n, rows, ps, true); pe != nil {
			err = errors.Join(err, pe)
		}
	}
	return err
}

// Exactly one retry boundary, enclosing only metadata acquisition. API/service
// calls are outside this function. Retry requires a direct typed race; errors.As
// must not promote a race nested under cleanup or observation failure.
func qualificationEngineCollectNativeWith(ctx context.Context, r qualificationEngineReceiptV2, ops qualificationEngineNativeOps) (qualificationEngineNative, error) {
	var anchor *qualificationEngineNative
	var attempts []qualificationEngineAttempt
	var observed [][]qualificationEngineProcess
	var n qualificationEngineNative
	if _, e := qualificationEngineReceiptCheck(r.qualificationEngineReceipt, 2, r.SocketParents); e != nil {
		return n, e
	}
	for attempt := 1; attempt <= 2; attempt++ {
		var e error
		if e = qualificationContext(ctx); e == nil {
			n, e = qualificationEngineCollectNativeOnce(ctx, r, ops, &anchor, &observed)
		}
		attempts = append(attempts, qualificationEngineAttempt{Number: attempt, Complete: e == nil, Cause: e})
		n.Attempts = append([]qualificationEngineAttempt{}, attempts...)
		if e == nil {
			return n, nil
		}
		n.Complete = false
		race, ok := e.(*qualificationEngineFDRace)
		if !ok || !race.ProcessStable || anchor == nil || attempt == 2 {
			return n, &qualificationEngineCollectionFailure{Attempts: attempts, Cause: e}
		}
	}
	panic("unreachable bounded Engine collection")
}
func qualificationEngineCollectNativeOnce(ctx context.Context, r qualificationEngineReceiptV2, ops qualificationEngineNativeOps, anchor **qualificationEngineNative, observed *[][]qualificationEngineProcess) (n qualificationEngineNative, err error) {
	if err = qualificationContext(ctx); err != nil {
		return
	}
	n.Boot, err = ops.boot(ctx)
	if err != nil {
		return
	}
	n.Filesystem, n.RuntimeParent, err = ops.filesystem(ctx)
	if err != nil {
		return
	}
	for i := range qualificationEngineUnits {
		d, e := ops.daemon(ctx, i, n.Boot)
		if e != nil {
			return n, e
		}
		n.Daemons = append(n.Daemons, d)
	}
	tables, e := ops.tables(ctx)
	if e != nil {
		// Acquisition failed before current socket metadata was available. The
		// receipt supplies only rejection context; it cannot authorize retry or
		// completion, and both acquisition/cleanup and known policy errors stay.
		failed := qualificationEngineNative{Daemons: n.Daemons, Sockets: r.Sockets}
		return n, errors.Join(e, qualificationEngineKnownNegatives(failed, tables, *observed))
	}
	paths, e := qualificationEngineDiscoverSocketPaths(tables)
	if e != nil {
		return n, e
	}
	first, e := ops.sockets(ctx, paths, n.Daemons)
	if e != nil {
		return n, e
	}
	if e = qualificationEnginePairSocketInodes(first.Sockets, tables); e != nil {
		return n, e
	}
	n.Sockets = first.Sockets
	n.SocketParents = first.Parents
	n.socketParentLinks = first.ParentLinks
	if e = qualificationEngineV2Sockets(n.Sockets, n.Daemons, n.SocketParents, &n.RuntimeParent); e != nil {
		return n, e
	}
	current := qualificationEngineAnchor(n)
	// The receipt remains the authorization identity, in addition to the frozen
	// acquisition anchor; no first-attempt drift can silently rebind it.
	want := qualificationEngineAnchor(qualificationEngineNative{Boot: r.Boot, Filesystem: r.Filesystem, RuntimeParent: r.RuntimeParent, Daemons: r.Daemons, Sockets: r.Sockets, SocketParents: r.SocketParents})
	receiptAnchor := qualificationEngineAnchor(current)
	receiptAnchor.socketParentLinks = []uint64{}
	if !reflect.DeepEqual(receiptAnchor, want) {
		return n, fmt.Errorf("Engine native anchor differs from receipt")
	}
	if *anchor == nil {
		*anchor = &current
	} else if !reflect.DeepEqual(current, **anchor) {
		return n, fmt.Errorf("Engine native attempt anchor drift")
	}
	if e = qualificationEngineKnownNegatives(n, tables, *observed); e != nil {
		return n, e
	}
	// Every exit carrying a race rechecks the original anchor before it can be
	// offered to the outer boundary. A failed recheck preserves both facts in a
	// terminal composite; only an unwrapped direct race can authorize retry.
	defer func() {
		if race, ok := err.(*qualificationEngineFDRace); ok && race.ProcessStable {
			if e := qualificationEngineRecheckAnchor(ctx, ops, **anchor, *observed); e != nil {
				err = errors.Join(err, e)
			}
		}
	}()
	ps, e := ops.processes(ctx)
	*observed = append(*observed, ps)
	if pe := qualificationEngineKnownNegatives(n, tables, *observed); pe != nil {
		return n, errors.Join(e, pe)
	}
	if e != nil {
		return n, e
	}
	n.Listeners, e = qualificationEngineNativePolicy(n, tables, ps, false)
	if e != nil {
		return n, e
	}
	closing, e := ops.processes(ctx)
	*observed = append(*observed, closing)
	// Intermediate pairing is partial: an old table cannot establish that a
	// newly closed ordinary socket lacks an owner in the complete closing table.
	if pe := qualificationEngineKnownNegatives(n, tables, *observed); pe != nil {
		return n, errors.Join(e, pe)
	}
	if e != nil {
		return n, e
	}
	closingTables, e := ops.tables(ctx)
	if pe := qualificationEngineKnownNegatives(n, closingTables, *observed); pe != nil {
		return n, errors.Join(e, pe)
	}
	if e != nil {
		return n, e
	}
	if _, e = qualificationEngineNativePolicy(n, closingTables, closing, false); e != nil {
		return n, e
	}
	if e = qualificationEngineRecheckAnchor(ctx, ops, **anchor, *observed); e != nil {
		return n, e
	}
	if e = qualificationEngineCollectionClosure(tables, closingTables, ps, closing); e != nil {
		// The closure validator can also report malformed input; only its explicit
		// identity mismatch is a coverage race.
		if _, ok := e.(*qualificationEngineCoverageChanged); !ok {
			return n, e
		}
		return n, &qualificationEngineFDRace{Primitive: "coverage", Cause: e, ProcessStable: true}
	}
	n.Complete = true
	if e = qualificationEngineCurrent(r, n); e != nil {
		n.Complete = false
		return n, e
	}
	if e = qualificationContext(ctx); e != nil {
		n.Complete = false
		return n, e
	}
	return n, nil
}
func qualificationEngineRecheckAnchor(ctx context.Context, ops qualificationEngineNativeOps, want qualificationEngineNative, observed [][]qualificationEngineProcess) error {
	if e := qualificationContext(ctx); e != nil {
		return e
	}
	boot, e := ops.boot(ctx)
	if e != nil {
		return e
	}
	if boot != want.Boot {
		return fmt.Errorf("Engine boot anchor drift")
	}
	fs, parent, e := ops.filesystem(ctx)
	if e != nil {
		return e
	}
	if fs != want.Filesystem || parent != want.RuntimeParent {
		return fmt.Errorf("Engine filesystem anchor drift")
	}
	for i, d := range want.Daemons {
		got, e := ops.daemon(ctx, i, boot)
		if e != nil {
			return e
		}
		if got != d {
			return fmt.Errorf("Engine daemon anchor drift")
		}
	}
	rows, e := ops.tables(ctx)
	if pe := qualificationEngineKnownNegatives(want, rows, observed); pe != nil {
		return errors.Join(e, pe)
	}
	if e != nil {
		return e
	}
	paths, e := qualificationEngineDiscoverSocketPaths(rows)
	if e != nil {
		return e
	}
	sample, e := ops.sockets(ctx, paths, want.Daemons)
	if e != nil {
		return e
	}
	if e = qualificationEnginePairSocketInodes(sample.Sockets, rows); e != nil {
		return e
	}
	if !reflect.DeepEqual(sample.Sockets, want.Sockets) || !reflect.DeepEqual(sample.Parents, want.SocketParents) || !reflect.DeepEqual(append([]uint64{}, sample.ParentLinks...), want.socketParentLinks) {
		return fmt.Errorf("Engine socket anchor drift")
	}
	return qualificationContext(ctx)
}

type qualificationEngineCoverageChanged struct{}

func (*qualificationEngineCoverageChanged) Error() string {
	return "Engine listener table/process socket FD coverage changed"
}

// Directory traversal owns each ancestor until the next descriptor is open.
// The Engine binding preserves the existing ancestor policy while making every
// cleanup failure observable, including cancellation between open and close.
type qualificationEngineDirectoryOps struct {
	openRoot  func(context.Context) (int, error)
	openChild func(context.Context, int, string) (int, error)
	identity  func(context.Context, int) (string, error)
	close     func(context.Context, int) error
}

func qualificationEngineOpenDirectoryWith(ctx context.Context, path string, ops qualificationEngineDirectoryOps) (result int, ids []string, err error) {
	if !absolute(path) {
		return -1, nil, errQualification
	}
	if err = qualificationContext(ctx); err != nil {
		return -1, nil, err
	}
	fd, e := ops.openRoot(ctx)
	if e != nil {
		return -1, nil, e
	}
	transfer := false
	defer func() {
		if !transfer {
			err = qualificationEngineCleanup(err, ops.close(ctx, fd))
			if ce := qualificationContext(ctx); ce != nil {
				err = qualificationEngineCleanup(err, ce)
			}
		}
	}()
	parts := append([]string{""}, strings.Split(strings.TrimPrefix(path, "/"), "/")...)
	for i, part := range parts {
		if err = qualificationContext(ctx); err != nil {
			return -1, nil, err
		}
		if i > 0 {
			next, e := ops.openChild(ctx, fd, part)
			if e != nil {
				return -1, nil, e
			}
			old := fd
			fd = next // Own next even if closing its predecessor fails.
			if e = ops.close(ctx, old); e != nil {
				return -1, nil, e
			}
		}
		if err = qualificationContext(ctx); err != nil {
			return -1, nil, err
		}
		identity, e := ops.identity(ctx, fd)
		if e != nil {
			return -1, nil, e
		}
		ids = append(ids, identity)
	}
	if err = qualificationContext(ctx); err != nil {
		return -1, nil, err
	}
	transfer = true
	return fd, ids, nil
}
