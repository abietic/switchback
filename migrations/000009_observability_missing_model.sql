-- migrate:up

ALTER TABLE run_observability_projections
    DROP CONSTRAINT run_observability_projections_model_observation_count_check;

ALTER TABLE run_observability_projections
    ADD CONSTRAINT run_observability_projections_model_observation_count_check
    CHECK (model_observation_count >= 0);

-- migrate:down

DELETE FROM run_observability_projections
WHERE model_observation_count = 0;

ALTER TABLE run_observability_projections
    DROP CONSTRAINT run_observability_projections_model_observation_count_check;

ALTER TABLE run_observability_projections
    ADD CONSTRAINT run_observability_projections_model_observation_count_check
    CHECK (model_observation_count > 0);
