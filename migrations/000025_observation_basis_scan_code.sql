-- migrate:up
CREATE OR REPLACE FUNCTION observation_fragment_safe_code(code text) RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$ SELECT code IN (
 'capture_cancelled','capture_invalid','compiler_failed','credential_detected',
 'provenance_invalid','scanner_unavailable','basis_scan_unavailable','scope_invalid','source_invalid',
 'source_identity_conflict','parent_basis_unavailable','object_store_unavailable',
 'object_checksum_mismatch','projection_unavailable','projection_readback_mismatch'
) $$;

-- migrate:down
DO $$ BEGIN
 RAISE EXCEPTION 'observation fragment evidence is retained; application rollback must disable projection';
END $$;
