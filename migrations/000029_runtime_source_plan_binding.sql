-- migrate:up
-- Immutable input facts under the original consumed execution owner. This is
-- neither a new reservation nor physical growth debt or a capacity grant.
ALTER TABLE runtime_capacity_executions
 ADD COLUMN source_plan_bytes bytea,
 ADD COLUMN source_plan_digest text,
 ADD COLUMN source_plan_bound_at timestamptz,
 ADD CONSTRAINT runtime_capacity_source_plan_complete CHECK (
  (source_plan_bytes IS NULL AND source_plan_digest IS NULL AND source_plan_bound_at IS NULL)
  OR (source_plan_bytes IS NOT NULL AND source_plan_digest IS NOT NULL AND source_plan_bound_at IS NOT NULL
      AND octet_length(source_plan_bytes)>0 AND source_plan_digest ~ '^sha256:[0-9a-f]{64}$'
      AND consumed_at IS NOT NULL)
 );
CREATE FUNCTION runtime_capacity_source_plan_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.source_plan_bound_at IS NOT NULL THEN
  IF TG_OP='DELETE' THEN
   RAISE EXCEPTION 'runtime source plan is durable immutable evidence';
  END IF;
  IF NEW.source_plan_bytes IS DISTINCT FROM OLD.source_plan_bytes
     OR NEW.source_plan_digest IS DISTINCT FROM OLD.source_plan_digest
     OR NEW.source_plan_bound_at IS DISTINCT FROM OLD.source_plan_bound_at THEN
   RAISE EXCEPTION 'runtime source plan is immutable after first pin';
  END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER runtime_capacity_source_plan_immutable BEFORE UPDATE OR DELETE ON runtime_capacity_executions
 FOR EACH ROW EXECUTE FUNCTION runtime_capacity_source_plan_immutable();
-- migrate:down
DO $$ BEGIN RAISE EXCEPTION 'runtime source plan evidence must be retained; rollback refuses to erase durable input bindings'; END $$;
