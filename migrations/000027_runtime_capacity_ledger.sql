-- migrate:up
-- One immutable deployment identity. An unconfigured pool still participates in
-- lock ordering and terminal settlement; it never authorizes new execution.
CREATE TABLE runtime_capacity_pools (
 pool_id text PRIMARY KEY CHECK (pool_id='local'),
 pool_nonce uuid NOT NULL DEFAULT gen_random_uuid(),
 epoch uuid NOT NULL DEFAULT gen_random_uuid(),
 trust_bytes bytea,
 trust_digest text,
 latest_sequence bigint NOT NULL DEFAULT 0 CHECK (latest_sequence>=0),
 revision bigint NOT NULL DEFAULT 1 CHECK (revision>0),
 status text NOT NULL DEFAULT 'uninitialized',
 CHECK ((trust_bytes IS NULL)=(trust_digest IS NULL))
);
INSERT INTO runtime_capacity_pools(pool_id) VALUES ('local');
CREATE TABLE runtime_capacity_snapshots (
 pool_id text NOT NULL REFERENCES runtime_capacity_pools,
 epoch uuid NOT NULL,
 sequence bigint NOT NULL CHECK(sequence>0),
 envelope bytea NOT NULL,
 payload bytea NOT NULL,
 signature bytea NOT NULL CHECK(octet_length(signature)=64),
 digest text NOT NULL,
 observed_at timestamptz NOT NULL,
 received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(pool_id,epoch,sequence)
);
CREATE TABLE runtime_capacity_residents (
 commitment_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 pool_id text NOT NULL REFERENCES runtime_capacity_pools,
 worker_session_id text NOT NULL,
 generation bigint NOT NULL CHECK(generation>0),
 profile_bytes bytea NOT NULL,
 origin text NOT NULL DEFAULT 'reserved' CHECK(origin IN ('reserved','legacy')),
 engine_id text NOT NULL,
 runtime_instance_id text,
 container_id text,
 layer_project_id bigint CHECK(layer_project_id>0),
 layer_path text,
 CHECK((layer_project_id IS NULL)=(layer_path IS NULL)),
 cpu_milli bigint NOT NULL CHECK(cpu_milli>0),
 memory_bytes bigint NOT NULL CHECK(memory_bytes>0),
 layer_bytes bigint NOT NULL CHECK(layer_bytes>0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 removal_sequence bigint,
 UNIQUE(pool_id,worker_session_id,generation),
 UNIQUE(pool_id,runtime_instance_id),
 UNIQUE(pool_id,container_id)
);
CREATE TABLE runtime_capacity_storage_domains (
 pool_id text NOT NULL REFERENCES runtime_capacity_pools,
 domain_id text NOT NULL,
 fs_uuid text NOT NULL,
 project_id bigint NOT NULL CHECK(project_id>0),
 path text NOT NULL,
 hard_bytes bigint NOT NULL CHECK(hard_bytes>0),
 PRIMARY KEY(pool_id,domain_id),
 UNIQUE(pool_id,fs_uuid,project_id),
 UNIQUE(pool_id,path)
);
CREATE TABLE runtime_capacity_executions (
 token uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 pool_id text NOT NULL REFERENCES runtime_capacity_pools,
 tenant_id text NOT NULL,
 idempotency_key text NOT NULL,
 target_digest text NOT NULL,
 target_bytes bytea NOT NULL,
 worker_session_id text NOT NULL,
 commitment_id uuid NOT NULL REFERENCES runtime_capacity_residents,
 snapshot_sequence bigint NOT NULL,
 permit_id text NOT NULL UNIQUE,
 expires_at timestamptz NOT NULL DEFAULT(clock_timestamp()+interval '5 minutes'),
 run_id text,
 turn_id text,
 runtime_id text,
 consumed_at timestamptz,
 released_at timestamptz,
 release_reason text,
 UNIQUE(pool_id,tenant_id,idempotency_key),
 CHECK ((consumed_at IS NULL)=(run_id IS NULL)),
 CHECK ((run_id IS NULL)=(turn_id IS NULL)),
 CHECK ((run_id IS NULL)=(runtime_id IS NULL)),
 CHECK ((released_at IS NULL)=(release_reason IS NULL))
);
CREATE UNIQUE INDEX runtime_capacity_one_execution_per_session
 ON runtime_capacity_executions(pool_id,worker_session_id) WHERE released_at IS NULL;
-- Immutable signed evidence remains available for audit and replay.
CREATE FUNCTION runtime_capacity_snapshot_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'capacity snapshot is immutable'; END $$;
CREATE TRIGGER runtime_capacity_snapshot_immutable BEFORE UPDATE OR DELETE ON runtime_capacity_snapshots
 FOR EACH ROW EXECUTE FUNCTION runtime_capacity_snapshot_immutable();
-- migrate:down
DO $$ BEGIN RAISE EXCEPTION 'capacity commitments and signed evidence require explicit reconciliation; rollback must not erase them'; END $$;
