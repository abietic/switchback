-- migrate:up

CREATE TABLE observation_fragment_activations (
 version text PRIMARY KEY CHECK (version <> ''),
 activated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE observation_fragment_run_exclusions (
 version text NOT NULL REFERENCES observation_fragment_activations(version),
 worker_run_id text NOT NULL REFERENCES worker_runs(worker_run_id),
 tenant_id text NOT NULL CHECK (tenant_id <> ''),
 user_id text NOT NULL CHECK (user_id <> ''),
 workspace_id text NOT NULL CHECK (workspace_id <> ''),
 worker_session_id text NOT NULL REFERENCES worker_sessions(worker_session_id),
 PRIMARY KEY (version,worker_run_id)
);

-- Single persisted-code allowlist. Never store provider, PostgreSQL or raw error text.
CREATE FUNCTION observation_fragment_safe_code(code text) RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$ SELECT code IN (
 'capture_cancelled','capture_invalid','compiler_failed','credential_detected',
 'provenance_invalid','scanner_unavailable','scope_invalid','source_invalid',
 'source_identity_conflict','parent_basis_unavailable','object_store_unavailable',
 'object_checksum_mismatch','projection_unavailable','projection_readback_mismatch'
) $$;

CREATE TABLE observation_candidates (
 kind text NOT NULL CHECK (kind IN ('run_basis','turn_basis','model')),
 source_id text NOT NULL CHECK (source_id <> ''),
 frame_digest text NOT NULL CHECK (frame_digest ~ '^sha256:[0-9a-f]{64}$'),
 frame bytea,
 receipt bytea,
 tenant_id text NOT NULL CHECK (tenant_id <> ''),
 user_id text NOT NULL CHECK (user_id <> ''),
 workspace_id text NOT NULL CHECK (workspace_id <> ''),
 worker_session_id text NOT NULL REFERENCES worker_sessions(worker_session_id),
 worker_run_id text NOT NULL REFERENCES worker_runs(worker_run_id),
 provenance jsonb NOT NULL CHECK (jsonb_typeof(provenance)='object' AND (provenance->>'schema_version') IS NOT DISTINCT FROM 'observation.provenance.v1'),
 status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','retryable','published','blocked')),
 attempts integer NOT NULL DEFAULT 0 CHECK (attempts>=0),
 lease_token bigint NOT NULL DEFAULT 0 CHECK (lease_token>=0),
 lease_until timestamptz,
 next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 error_code text CHECK (observation_fragment_safe_code(error_code)),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(kind,source_id),
 UNIQUE(kind,source_id,frame_digest),
 UNIQUE(kind,source_id,frame_digest,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id),
 CHECK ((status='running')=(lease_until IS NOT NULL)),
 CHECK (frame IS NULL OR frame_digest='sha256:'||encode(sha256(frame),'hex')),
 CHECK ((frame IS NOT NULL AND octet_length(frame)>0 AND receipt IS NOT NULL AND octet_length(receipt)>0)
 OR (status='blocked' AND frame IS NULL AND receipt IS NULL AND error_code IS NOT NULL))
);

CREATE TABLE observation_publications (
 kind text NOT NULL,
 source_id text NOT NULL,
 frame_digest text NOT NULL,
 object_key text NOT NULL CHECK (object_key <> ''),
 object_size bigint NOT NULL CHECK (object_size>=0),
 object_checksum text NOT NULL CHECK (object_checksum ~ '^sha256:[0-9a-f]{64}$'),
 tenant_id text NOT NULL,
 user_id text NOT NULL,
 workspace_id text NOT NULL,
 worker_session_id text NOT NULL,
 worker_run_id text NOT NULL,
 published_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(kind,source_id),
 UNIQUE(kind,source_id,frame_digest),
 FOREIGN KEY(kind,source_id,frame_digest,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id)
 REFERENCES observation_candidates(kind,source_id,frame_digest,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id)
);

CREATE TABLE observation_fragment_jobs (
 version text NOT NULL CHECK (version <> ''),
 kind text NOT NULL,
 source_id text NOT NULL,
 frame_digest text NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','retryable','passed','blocked')),
 attempts integer NOT NULL DEFAULT 0 CHECK (attempts>=0),
 lease_token bigint NOT NULL DEFAULT 0 CHECK (lease_token>=0),
 lease_until timestamptz,
 next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 error_code text CHECK (observation_fragment_safe_code(error_code)),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(version,kind,source_id),
 FOREIGN KEY(kind,source_id,frame_digest) REFERENCES observation_candidates(kind,source_id,frame_digest),
 FOREIGN KEY(kind,source_id,frame_digest) REFERENCES observation_publications(kind,source_id,frame_digest),
 CHECK ((status='running')=(lease_until IS NOT NULL))
);

CREATE INDEX observation_candidates_claimable ON observation_candidates(next_attempt_at,source_id)
 WHERE status IN ('pending','retryable','running');
CREATE INDEX observation_fragment_jobs_claimable ON observation_fragment_jobs(version,next_attempt_at,source_id)
 WHERE status IN ('pending','retryable','running');

CREATE TABLE observation_fragment_requests (
 tenant_id text NOT NULL CHECK (tenant_id <> ''),
 actor_user_id text NOT NULL CHECK (actor_user_id <> ''),
 idempotency_key text NOT NULL CHECK (idempotency_key <> ''),
 request_bytes bytea NOT NULL CHECK (octet_length(request_bytes)>0),
 request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
 version text NOT NULL CHECK (version <> ''),
 source_set jsonb NOT NULL CHECK (jsonb_typeof(source_set)='array' AND jsonb_array_length(source_set)>0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(actor_user_id,idempotency_key)
);

CREATE TABLE observation_fragment_conflicts (
 source_id text NOT NULL CHECK (source_id <> ''),
 kind text NOT NULL CHECK (kind IN ('run_basis','turn_basis','model')),
 original_digest text NOT NULL CHECK (original_digest ~ '^sha256:[0-9a-f]{64}$'),
 conflicting_digest text NOT NULL CHECK (conflicting_digest ~ '^sha256:[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(source_id,conflicting_digest),
 FOREIGN KEY(kind,source_id,original_digest) REFERENCES observation_candidates(kind,source_id,frame_digest)
);

-- Only work-state columns are mutable. Freeze all evidence columns on every
-- ledger; deletion is prohibited even when no child foreign key references it.
CREATE FUNCTION observation_fragment_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE mutable text[] := ARRAY[]::text[];
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'observation fragment evidence is immutable'; END IF;
 IF TG_TABLE_NAME IN ('observation_candidates','observation_fragment_jobs') THEN
  mutable := ARRAY['status','attempts','lease_token','lease_until','next_attempt_at','error_code'];
  IF NEW.attempts<OLD.attempts OR NEW.lease_token<OLD.lease_token THEN
   RAISE EXCEPTION 'observation fragment fence must be monotonic';
  END IF;
  IF NEW.status='running' AND OLD.status<>'running' AND NEW.lease_token<>OLD.lease_token+1 THEN
   RAISE EXCEPTION 'observation fragment running requires a new fence';
  END IF;
  IF NEW.status='running' AND OLD.status='running' AND NEW.lease_token=OLD.lease_token
   AND NEW.lease_until>OLD.lease_until THEN
   RAISE EXCEPTION 'observation fragment lease extension requires a new fence';
  END IF;
  IF (NEW.lease_token<>OLD.lease_token OR NEW.attempts<>OLD.attempts) AND
   (NEW.status<>'running' OR NEW.lease_token<>OLD.lease_token+1 OR NEW.attempts<>OLD.attempts+1) THEN
   RAISE EXCEPTION 'observation fragment claim must advance fence and attempt together';
  END IF;
  IF OLD.status IN ('published','passed','blocked') AND to_jsonb(NEW) IS DISTINCT FROM to_jsonb(OLD) THEN
   RAISE EXCEPTION 'observation fragment terminal state is immutable';
  END IF;
 END IF;
 IF (to_jsonb(NEW)-mutable) IS DISTINCT FROM (to_jsonb(OLD)-mutable) THEN
  RAISE EXCEPTION 'observation fragment evidence is immutable';
 END IF;
 RETURN NEW;
END $$;

CREATE TRIGGER observation_candidates_immutable BEFORE UPDATE OR DELETE ON observation_candidates
 FOR EACH ROW EXECUTE FUNCTION observation_fragment_guard();
CREATE TRIGGER observation_publications_immutable BEFORE UPDATE OR DELETE ON observation_publications
 FOR EACH ROW EXECUTE FUNCTION observation_fragment_guard();
CREATE TRIGGER observation_fragment_jobs_immutable BEFORE UPDATE OR DELETE ON observation_fragment_jobs
 FOR EACH ROW EXECUTE FUNCTION observation_fragment_guard();
CREATE TRIGGER observation_fragment_activations_immutable BEFORE UPDATE OR DELETE ON observation_fragment_activations
 FOR EACH ROW EXECUTE FUNCTION observation_fragment_guard();
CREATE TRIGGER observation_fragment_run_exclusions_immutable BEFORE UPDATE OR DELETE ON observation_fragment_run_exclusions
 FOR EACH ROW EXECUTE FUNCTION observation_fragment_guard();
CREATE TRIGGER observation_fragment_requests_immutable BEFORE UPDATE OR DELETE ON observation_fragment_requests
 FOR EACH ROW EXECUTE FUNCTION observation_fragment_guard();
CREATE TRIGGER observation_fragment_conflicts_immutable BEFORE UPDATE OR DELETE ON observation_fragment_conflicts
 FOR EACH ROW EXECUTE FUNCTION observation_fragment_guard();

-- Both sides are checked at commit: no published candidate without a matching
-- publication, and no publication whose candidate is not published.
CREATE FUNCTION observation_fragment_publication_binding() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE candidate_status text; publication_exists boolean;
BEGIN
 SELECT status INTO candidate_status FROM observation_candidates WHERE kind=NEW.kind AND source_id=NEW.source_id;
 SELECT EXISTS(SELECT 1 FROM observation_publications WHERE kind=NEW.kind AND source_id=NEW.source_id)
 INTO publication_exists;
 IF (candidate_status='published') IS DISTINCT FROM publication_exists THEN
  RAISE EXCEPTION 'observation fragment publication binding is incomplete';
 END IF;
 RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER observation_candidate_publication_binding AFTER INSERT OR UPDATE ON observation_candidates
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION observation_fragment_publication_binding();
CREATE CONSTRAINT TRIGGER observation_publication_candidate_binding AFTER INSERT ON observation_publications
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION observation_fragment_publication_binding();

-- migrate:down
DO $$ BEGIN
 RAISE EXCEPTION 'observation fragment evidence is retained; application rollback must disable projection';
END $$;
