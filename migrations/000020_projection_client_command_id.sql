-- migrate:up

ALTER TABLE projection_deltas
    ADD COLUMN client_command_id text;

-- migrate:down

ALTER TABLE projection_deltas
    DROP COLUMN client_command_id;
