package capacitysnapshot

import (
	"fmt"
	"strconv"
	"strings"
)

// All referenced tables must exist after the official migration script. Missing
// schema is an error, never a claim that an old configured pool is absent.
const qualificationPGInitialQuery = `SELECT pool_nonce::text || '|' || epoch::text || '|' || count(*) OVER()::text FROM runtime_capacity_pools WHERE pool_id='local' AND status='uninitialized' AND trust_bytes IS NULL AND trust_digest IS NULL AND latest_sequence=0 AND revision=1 AND (SELECT count(*) FROM runtime_capacity_pools)=1 AND NOT EXISTS(SELECT FROM runtime_capacity_snapshots) AND NOT EXISTS(SELECT FROM runtime_capacity_residents) AND NOT EXISTS(SELECT FROM runtime_capacity_storage_domains) AND NOT EXISTS(SELECT FROM runtime_capacity_executions);`

type qualificationPGPreparation struct {
	Schema       string `json:"schema"`
	CID          string `json:"cid"`
	Nonce        string `json:"nonce"`
	StaticDigest string `json:"static_digest"`
	PoolNonce    string `json:"pool_nonce"`
	Epoch        string `json:"epoch"`
	PID          uint64 `json:"pid"`
	Start        uint64 `json:"start"`
}

func qualificationParsePGInitial(b []byte, p *qualificationPGPreparation) error {
	v := strings.Split(strings.TrimSpace(string(b)), "|")
	if len(v) != 3 || !qualificationUUIDPattern.MatchString(v[0]) || !qualificationUUIDPattern.MatchString(v[1]) {
		return fmt.Errorf("PG has no exact initial pool baseline")
	}
	n, e := strconv.Atoi(v[2])
	if e != nil || n != 1 {
		return errQualification
	}
	p.PoolNonce, p.Epoch = v[0], v[1]
	return nil
}
