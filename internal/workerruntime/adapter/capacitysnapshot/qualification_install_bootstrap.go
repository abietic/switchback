package capacitysnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"github.com/abietic/switchback/internal/platform/capacityjournal"
	"golang.org/x/sys/unix"
)

const qualificationPublicationPrivate = "/mnt/lima-sbquota/core/capacity-publication-private"
const qualificationPublicationPublic = "/mnt/lima-sbquota/core/capacity-publication-public"

type qualificationPublicationEmpty struct {
	Schema      string   `json:"schema"`
	Nonce       string   `json:"nonce"`
	InputDigest string   `json:"input_digest"`
	Private     []string `json:"private"`
	Public      []string `json:"public"`
}
type qualificationPublicationBootstrap struct {
	Schema      string   `json:"schema"`
	Candidate   string   `json:"candidate"`
	OpenDigest  string   `json:"open_digest"`
	TrustDigest string   `json:"trust_digest"`
	EmptyDigest string   `json:"empty_digest"`
	Private     []string `json:"private"`
	Public      []string `json:"public"`
	Journal     string   `json:"journal"`
	Lock        string   `json:"lock"`
}
type qualificationPublicationFS struct {
	directory       func(context.Context, string) (*os.File, []string, error)
	stat            func(context.Context, int) (unix.Stat_t, error)
	private, public string
}

func qualificationPublicationDirectories(ctx context.Context, k qualificationPublicationFS) (*os.File, []string, *os.File, []string, error) {
	p, pi, e := k.directory(ctx, k.private)
	if e != nil {
		return nil, nil, nil, nil, e
	}
	q, qi, e := k.directory(ctx, k.public)
	if e != nil {
		p.Close()
		return nil, nil, nil, nil, e
	}
	ps, e := k.stat(ctx, int(p.Fd()))
	if e != nil {
		p.Close()
		q.Close()
		return nil, nil, nil, nil, e
	}
	qs, e := k.stat(ctx, int(q.Fd()))
	if e != nil || ps.Uid != 0 || ps.Gid != 0 || qs.Uid != 0 || qs.Gid != 0 || ps.Mode&07777 != 0700 || qs.Mode&07777 != 0750 || ps.Nlink == 0 || qs.Nlink == 0 || (ps.Dev == qs.Dev && ps.Ino == qs.Ino) {
		p.Close()
		q.Close()
		return nil, nil, nil, nil, fmt.Errorf("publication bootstrap directories unsafe")
	}
	return p, qualificationStableDirectories(pi), q, qualificationStableDirectories(qi), nil
}
func qualificationPublicationRecordEmpty(ctx context.Context, d qualificationCandidateDirectory, k qualificationPublicationFS) error {
	if e := d.claim(ctx, "publication-preflight"); e != nil {
		return e
	}
	var input qualificationStaged
	if e := d.decode(ctx, "intent-input.json", &input); e != nil {
		return e
	}
	if e := qualificationValidateIntentInput(input); e != nil {
		return e
	}
	p, pi, q, qi, e := qualificationPublicationDirectories(ctx, k)
	if e != nil {
		return e
	}
	defer p.Close()
	defer q.Close()
	if e = qualificationInstallNames(p, nil); e != nil {
		return e
	}
	if e = qualificationInstallNames(q, nil); e != nil {
		return e
	}
	return d.put(ctx, "publication-empty.json", qualificationPublicationEmpty{"switchback.qualification.publication_empty.v1", input.Nonce, qualificationDigest(input), pi, qi})
}
func qualificationPublicationRead(ctx context.Context, k qualificationPublicationFS, dir *os.File, name string, mode uint32, size int) (qualificationFile, error) {
	var out qualificationFile
	fd, e := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return out, e
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	s, e := k.stat(ctx, fd)
	if e != nil || s.Uid != 0 || s.Gid != 0 || s.Nlink != 1 || s.Mode&unix.S_IFMT != unix.S_IFREG || uint32(s.Mode)&07777 != mode || s.Size != int64(size) {
		return out, errQualification
	}
	raw := make([]byte, size)
	if size > 0 {
		n, e := f.Read(raw)
		if e != nil || n != size {
			return out, errQualification
		}
	}
	after, e := k.stat(ctx, fd)
	if e != nil || qualificationIdentity(after) != qualificationIdentity(s) {
		return out, errQualification
	}
	fresh, e := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return out, e
	}
	entry, e := k.stat(ctx, fresh)
	_ = unix.Close(fresh)
	if e != nil || qualificationIdentity(entry) != qualificationIdentity(s) {
		return out, errQualification
	}

	return qualificationFile{qualificationIdentity(s), raw}, qualificationContext(ctx)
}
func qualificationBootstrapPublication(ctx context.Context, d qualificationCandidateDirectory, k qualificationPublicationFS) error {
	if e := d.claim(ctx, "publication-bootstrap"); e != nil {
		return e
	}
	files, e := qualificationInstallMaterial(ctx, d)
	if e != nil {
		return e
	}
	var empty qualificationPublicationEmpty
	var input qualificationStaged
	var c qualificationCandidateRecord
	for _, x := range []struct {
		name string
		v    any
	}{{"publication-empty.json", &empty}, {"intent-input.json", &input}, {"candidate.json", &c}} {
		if e = d.decode(ctx, x.name, x.v); e != nil {
			return e
		}
	}
	if empty.Schema != "switchback.qualification.publication_empty.v1" || empty.Nonce != input.Nonce || empty.InputDigest != qualificationDigest(input) {
		return errQualification
	}
	p, pi, q, qi, e := qualificationPublicationDirectories(ctx, k)
	if e != nil {
		return e
	}
	defer p.Close()
	defer q.Close()
	if !reflect.DeepEqual(pi, empty.Private) || !reflect.DeepEqual(qi, empty.Public) {
		return fmt.Errorf("publication directories changed since empty proof")
	}
	if e = qualificationInstallNames(p, nil); e != nil {
		return e
	}
	if e = qualificationInstallNames(q, nil); e != nil {
		return e
	}
	var trust Trust
	if strictJSON(files["trust.json"], &trust) != nil || ValidateTrust(trust) != nil {
		return errQualification
	}
	canonical, e := json.Marshal(trust)
	if e != nil {
		return e
	}
	raw, e := capacityjournal.Encode(capacityjournal.Binding(canonical), 0)
	if e != nil {
		return e
	}
	create := func(name string, b []byte, mode uint32) (qualificationFile, error) {
		fd, e := unix.Openat(int(p.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if e != nil {
			return qualificationFile{}, e
		}
		f := os.NewFile(uintptr(fd), name)
		if len(b) > 0 {
			n, e := f.Write(b)
			if e != nil || n != len(b) {
				f.Close()
				return qualificationFile{}, fmt.Errorf("publication bootstrap write failed")
			}
		}
		if e = f.Chmod(os.FileMode(mode)); e != nil {
			f.Close()
			return qualificationFile{}, e
		}
		if e = f.Sync(); e != nil {
			f.Close()
			return qualificationFile{}, e
		}
		if e = f.Close(); e != nil {
			return qualificationFile{}, e
		}
		return qualificationPublicationRead(ctx, k, p, name, mode, len(b))
	}
	lock, e := create("writer.lock", nil, 0600)
	if e != nil {
		return e
	}
	journal, e := create("sequence.bin", raw[:], 0400)
	if e != nil {
		return e
	}
	if e = p.Sync(); e != nil {
		return e
	}
	if e = q.Sync(); e != nil {
		return e
	}
	receipt := qualificationPublicationBootstrap{"switchback.qualification.publication_bootstrap.v1", c.Fingerprint, c.OpenDigest, Digest(files["trust.json"]), qualificationDigest(empty), pi, qi, journal.identity, lock.identity}
	return d.put(ctx, "publication-bootstrap.json", receipt)
}
func qualificationPublicationCheck(ctx context.Context, d qualificationCandidateDirectory, k qualificationPublicationFS, files map[string][]byte) error {
	var r qualificationPublicationBootstrap
	var empty qualificationPublicationEmpty
	var c qualificationCandidateRecord
	for _, x := range []struct {
		name string
		v    any
	}{{"publication-bootstrap.json", &r}, {"publication-empty.json", &empty}, {"candidate.json", &c}} {
		if e := d.decode(ctx, x.name, x.v); e != nil {
			return e
		}
	}
	if r.Schema != "switchback.qualification.publication_bootstrap.v1" || r.Candidate != c.Fingerprint || r.OpenDigest != c.OpenDigest || r.TrustDigest != Digest(files["trust.json"]) || r.EmptyDigest != qualificationDigest(empty) || !reflect.DeepEqual(r.Private, empty.Private) || !reflect.DeepEqual(r.Public, empty.Public) {
		return errQualification
	}
	p, pi, q, qi, e := qualificationPublicationDirectories(ctx, k)
	if e != nil {
		return e
	}
	defer p.Close()
	defer q.Close()
	if !reflect.DeepEqual(pi, r.Private) || !reflect.DeepEqual(qi, r.Public) {
		return errQualification
	}
	if e = qualificationInstallNames(p, []string{"writer.lock", "sequence.bin"}); e != nil {
		return e
	}
	if e = qualificationInstallNames(q, nil); e != nil {
		return e
	}
	lock, e := qualificationPublicationRead(ctx, k, p, "writer.lock", 0600, 0)
	if e != nil || lock.identity != r.Lock {
		return errQualification
	}
	journal, e := qualificationPublicationRead(ctx, k, p, "sequence.bin", 0400, capacityjournal.Size)
	if e != nil || journal.identity != r.Journal {
		return errQualification
	}
	var trust Trust
	if strictJSON(files["trust.json"], &trust) != nil {
		return errQualification
	}
	canonical, e := json.Marshal(trust)
	if e != nil {
		return e
	}
	high, e := capacityjournal.Decode(journal.data, capacityjournal.Binding(canonical))
	if e != nil || high != 0 {
		return errQualification
	}
	encoded, _ := capacityjournal.Encode(capacityjournal.Binding(canonical), 0)
	if !bytes.Equal(encoded[:], journal.data) {
		return errQualification
	}
	return qualificationContext(ctx)
}
func BootstrapQualificationPublication(ctx context.Context) error {
	return bootstrapQualificationPublicationFixed(ctx)
}
