package postgres

import (
	"context"
	"encoding/json"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	app "github.com/abietic/switchback/internal/workerruntime/application"
	"math"
	"strconv"
	"strings"
)

func capacityProfile(ref app.CapacityProfile) (config.RuntimeResourceProfile, error) {
	p, err := config.ProductionProfileRegistry().ResolveRuntimeResource(config.RuntimeResourceProfileRef{ID: ref.ID, Revision: ref.Revision, Digest: ref.Digest})
	if err != nil || p.CPUUnits != 2 || p.MemoryBytes != 4<<30 || p.PIDsLimit != 512 || p.WritableLayerBytes != 10<<30 {
		return p, app.CapacityBindingMismatch
	}
	return p, nil
}
func addBounded(sum *uint64, n uint64) error {
	if n > math.MaxInt64 || *sum > math.MaxInt64-n {
		return app.CapacityEvidenceInvalid
	}
	*sum += n
	return nil
}
func eighty(n uint64) uint64 { return n/5*4 + n%5*4/5 }
func remaining(limit, used uint64) uint64 {
	if used >= limit {
		return 0
	}
	return limit - used
}

type residentCapacity struct {
	id, session, engine, runtime, container string
	generation                              int64
	cpu, memory, layer                      uint64
	profile                                 []byte
}

// importLegacy counts every persisted Runtime without our own removal proof,
// including terminated/lost rows. Old cleanup booleans cannot release capacity.
func (h *CapacityTx) importLegacy(ctx context.Context, t capacitysnapshot.Trust) error {
	rows, err := h.tx.Query(ctx, `SELECT r.worker_session_id,r.generation,r.runtime_instance_id,COALESCE(r.container_id,''),r.resource_profile_id FROM runtime_instances r LEFT JOIN runtime_capacity_residents c ON c.pool_id='local' AND c.worker_session_id=r.worker_session_id AND c.generation=r.generation WHERE c.commitment_id IS NULL`)
	if err != nil {
		return err
	}
	type old struct {
		s, r, c, p string
		g          int64
	}
	var legacy []old
	for rows.Next() {
		var x old
		if err = rows.Scan(&x.s, &x.g, &x.r, &x.c, &x.p); err != nil {
			rows.Close()
			return err
		}
		legacy = append(legacy, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	p, _ := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	profile, _ := json.Marshal(app.CapacityProfile{ID: p.ID, Revision: p.Revision, Digest: p.Digest})
	for _, x := range legacy {
		if x.p != p.ID {
			return app.CapacityInventoryUnknown
		}
		_, err = h.tx.Exec(ctx, `INSERT INTO runtime_capacity_residents(pool_id,worker_session_id,generation,profile_bytes,engine_id,runtime_instance_id,container_id,cpu_milli,memory_bytes,layer_bytes,origin) VALUES('local',$1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9,'legacy')`, x.s, x.g, profile, t.Identity.EngineID, x.r, x.c, p.CPUUnits*1000, p.MemoryBytes, p.WritableLayerBytes)
		if err != nil {
			return err
		}
	}
	return nil
}

// budget merges durable residents with the signed complete inventory. A missing
// pending/legacy resident is charged in full. An unknown observed container is
// blocked, never inferred to be a free or caller-authorized worker.
func (h *CapacityTx) budget(ctx context.Context, f capacitysnapshot.Facts, t capacitysnapshot.Trust, newResident bool, p config.RuntimeResourceProfile) error {
	if err := h.validateResidentIdentities(ctx, f); err != nil {
		return err
	}
	if err := h.importLegacy(ctx, t); err != nil {
		return err
	}
	var cpu, memory, storage uint64
	for _, c := range f.Core {
		milli := c.CPUQuotaMicros * 1000 / c.CPUPeriodMicros
		if c.CPUQuotaMicros*1000%c.CPUPeriodMicros != 0 {
			milli++
		}
		if err := addBounded(&cpu, milli); err != nil {
			return err
		}
		if err := addBounded(&memory, c.MemoryMax); err != nil {
			return err
		}
	}
	for _, d := range f.Storage {
		if _, err := h.tx.Exec(ctx, `INSERT INTO runtime_capacity_storage_domains(pool_id,domain_id,fs_uuid,project_id,path,hard_bytes) VALUES('local',$1,$2,$3,$4,$5) ON CONFLICT(pool_id,domain_id) DO NOTHING`, d.ID, t.Identity.FSUUID, d.ProjectID, d.Path, d.HardBytes); err != nil {
			return err
		}
		var same bool
		if err := h.tx.QueryRow(ctx, `SELECT fs_uuid=$2 AND project_id=$3 AND path=$4 AND hard_bytes=$5 FROM runtime_capacity_storage_domains WHERE pool_id='local' AND domain_id=$1`, d.ID, t.Identity.FSUUID, d.ProjectID, d.Path, d.HardBytes).Scan(&same); err != nil {
			return err
		}
		if !same {
			return app.CapacityIdentityDrift
		}
		if err := addBounded(&storage, remaining(d.HardBytes, d.UsedBytes)); err != nil {
			return err
		}
	}
	rows, err := h.tx.Query(ctx, `SELECT commitment_id::text,worker_session_id,generation,engine_id,COALESCE(runtime_instance_id,''),COALESCE(container_id,''),cpu_milli,memory_bytes,layer_bytes,profile_bytes FROM runtime_capacity_residents WHERE pool_id='local' AND removal_sequence IS NULL`)
	if err != nil {
		return err
	}
	var residents []residentCapacity
	for rows.Next() {
		var r residentCapacity
		if err = rows.Scan(&r.id, &r.session, &r.generation, &r.engine, &r.runtime, &r.container, &r.cpu, &r.memory, &r.layer, &r.profile); err != nil {
			rows.Close()
			return err
		}
		residents = append(residents, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	inventory := map[string]capacitysnapshot.Container{}
	for _, c := range f.Containers {
		inventory[c.SessionID+"\x00"+c.RuntimeID] = c
	}
	for _, r := range residents {
		if r.engine != t.Identity.EngineID {
			return app.CapacityIdentityDrift
		}
		var ref app.CapacityProfile
		if json.Unmarshal(r.profile, &ref) != nil {
			return app.CapacityInventoryUnknown
		}
		rp, err := capacityProfile(ref)
		if err != nil {
			return app.CapacityInventoryUnknown
		}
		if r.cpu != uint64(rp.CPUUnits*1000) || r.memory != uint64(rp.MemoryBytes) || r.layer != uint64(rp.WritableLayerBytes) {
			return app.CapacityInventoryUnknown
		}
		used := uint64(0)
		key := r.session + "\x00" + r.runtime
		if c, ok := inventory[key]; ok {
			if c.Generation != uint64(r.generation) || c.ProfileID != ref.ID || c.ProfileRevision != ref.Revision || c.ProfileDigest != ref.Digest || c.NanoCPUs != uint64(rp.CPUUnits)*1_000_000_000 || c.CPUQuotaMicros/c.CPUPeriodMicros != uint64(rp.CPUUnits) || c.CPUQuotaMicros%c.CPUPeriodMicros != 0 || c.MemoryMax != r.memory || c.PIDsMax != uint64(rp.PIDsLimit) || c.Layer.HardBytes != r.layer || r.container != "" && r.container != c.ContainerID {
				return app.CapacityInventoryUnknown
			}
			// Bind only an already known exact Runtime/Session/generation. Signed labels
			// by themselves cannot create a commitment or execution credential.
			if r.container == "" {
				if _, err = h.tx.Exec(ctx, `UPDATE runtime_capacity_residents SET container_id=$2 WHERE commitment_id=$1 AND container_id IS NULL`, r.id, c.ContainerID); err != nil {
					return err
				}
			}
			var project int64
			var path string
			if err = h.tx.QueryRow(ctx, `SELECT COALESCE(layer_project_id,0),COALESCE(layer_path,'') FROM runtime_capacity_residents WHERE commitment_id=$1`, r.id).Scan(&project, &path); err != nil {
				return err
			}
			if project != 0 && (uint64(project) != c.Layer.ProjectID || path != c.Layer.Path) {
				return app.CapacityIdentityDrift
			}
			if project == 0 {
				if _, err = h.tx.Exec(ctx, `UPDATE runtime_capacity_residents SET layer_project_id=$2,layer_path=$3 WHERE commitment_id=$1`, r.id, c.Layer.ProjectID, c.Layer.Path); err != nil {
					return err
				}
			}
			used = c.Layer.UsedBytes
			delete(inventory, key)
		}
		if err = addBounded(&cpu, r.cpu); err != nil {
			return err
		}
		if err = addBounded(&memory, r.memory); err != nil {
			return err
		}
		if err = addBounded(&storage, remaining(r.layer, used)); err != nil {
			return err
		}
	}
	if len(inventory) > 0 {
		return app.CapacityInventoryUnknown
	}
	if newResident {
		if err = addBounded(&cpu, uint64(p.CPUUnits*1000)); err != nil {
			return err
		}
		if err = addBounded(&memory, uint64(p.MemoryBytes)); err != nil {
			return err
		}
		if err = addBounded(&storage, uint64(p.WritableLayerBytes)); err != nil {
			return err
		}
	}
	if cpu > eighty(f.EngineCPUMilli) || memory > eighty(f.EngineMemoryBytes) || storage > eighty(f.Anchor.AvailableBytes) {
		return app.CapacityExhausted
	}
	return nil
}

// Validate the complete durable identity set BEFORE any observation-driven pin.
// A UNIQUE violation would abort the TX and roll back ObservePool's block; it is
// only a last database invariant, not the semantic conflict detector.
func (h *CapacityTx) validateResidentIdentities(ctx context.Context, f capacitysnapshot.Facts) error {
	rows, err := h.tx.Query(ctx, `SELECT worker_session_id,generation,COALESCE(runtime_instance_id,''),COALESCE(container_id,''),COALESCE(layer_project_id,0),COALESCE(layer_path,''),removal_sequence IS NOT NULL FROM runtime_capacity_residents WHERE pool_id='local' UNION ALL SELECT worker_session_id,generation,runtime_instance_id,COALESCE(container_id,''),0,'',false FROM runtime_instances`)
	if err != nil {
		return err
	}
	type identity struct {
		runtime, container, path string
		project                  uint64
		removed                  bool
	}
	identities := map[string]identity{}
	containerOwner := map[string]string{}
	runtimeOwner := map[string]string{}
	projectOwner := map[uint64]string{}
	pathOwner := map[string]string{}
	for rows.Next() {
		var session, runtime, container, path string
		var generation, project uint64
		var removed bool
		if err = rows.Scan(&session, &generation, &runtime, &container, &project, &path, &removed); err != nil {
			rows.Close()
			return err
		}
		key := session + "\x00" + strconv.FormatUint(generation, 10)
		old := identities[key]
		if old.runtime != "" && runtime != "" && old.runtime != runtime || old.container != "" && container != "" && old.container != container {
			rows.Close()
			return app.CapacityIdentityDrift
		}
		if runtime != "" {
			if owner, ok := runtimeOwner[runtime]; ok && owner != key {
				rows.Close()
				return app.CapacityIdentityDrift
			}
			runtimeOwner[runtime] = key
			old.runtime = runtime
		}
		if container != "" {
			if owner, ok := containerOwner[container]; ok && owner != key {
				rows.Close()
				return app.CapacityIdentityDrift
			}
			containerOwner[container] = key
			old.container = container
		}
		if project != 0 {
			if owner, ok := projectOwner[project]; ok && owner != key {
				rows.Close()
				return app.CapacityIdentityDrift
			}
			projectOwner[project] = key
			old.project = project
			old.path = path
			pathOwner[path] = key
		}
		old.removed = old.removed || removed
		identities[key] = old
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range f.Containers {
		key := c.SessionID + "\x00" + strconv.FormatUint(c.Generation, 10)
		old, ok := identities[key]
		if !ok || old.runtime == "" {
			return app.CapacityInventoryUnknown
		}
		if old.removed || old.runtime != c.RuntimeID || old.container != "" && old.container != c.ContainerID {
			return app.CapacityIdentityDrift
		}
		if owner, ok := containerOwner[c.ContainerID]; ok && owner != key {
			return app.CapacityIdentityDrift
		}
		if owner, ok := projectOwner[c.Layer.ProjectID]; ok && owner != key {
			return app.CapacityIdentityDrift
		}
		for path, owner := range pathOwner {
			if owner != key && (path == c.Layer.Path || strings.HasPrefix(path, c.Layer.Path+"/") || strings.HasPrefix(c.Layer.Path, path+"/")) {
				return app.CapacityIdentityDrift
			}
		}
		if old.project != 0 && (old.project != c.Layer.ProjectID || old.path != c.Layer.Path) {
			return app.CapacityIdentityDrift
		}
	}
	return nil
}
