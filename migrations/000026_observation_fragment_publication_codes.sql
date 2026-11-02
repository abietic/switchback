-- migrate:up
CREATE OR REPLACE FUNCTION observation_fragment_safe_code(code text) RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$ SELECT code IN (
 'capture_cancelled','capture_invalid','compiler_failed','credential_detected',
 'provenance_invalid','scanner_unavailable','scope_invalid','source_invalid',
 'source_identity_conflict','parent_basis_unavailable','object_store_unavailable',
 'object_checksum_mismatch','projection_unavailable','projection_readback_mismatch',
 'basis_scan_unavailable','fragment_proof_invalid','parent_basis_pending',
 'ingestion_unavailable','projection_cancelled','projection_collision','projection_not_ready','readback_unavailable',
 'source_dependencies_invalid','source_metadata_unavailable','source_load_cancelled',
 'source_object_unavailable','source_object_missing','source_checksum_mismatch',
 'manifest_object_unavailable','manifest_object_missing','manifest_object_invalid',
 'report_object_unavailable','report_object_missing','report_object_invalid'
) $$;

-- migrate:down
DO $$ BEGIN
 RAISE EXCEPTION 'observation fragment evidence is retained; application rollback must disable projection';
END $$;
