CREATE TABLE IF NOT EXISTS rzd_calculations (
    id uuid PRIMARY KEY,
    idempotency_key varchar(128) NOT NULL UNIQUE,
    scenario_id text NOT NULL CHECK (scenario_id IN ('S0','S1','S2','S3')),
    status text NOT NULL CHECK (status IN ('queued','running','succeeded','failed')),
    engine_profile text NOT NULL,
    snapshot_id text NOT NULL,
    input_sha256 text NOT NULL CHECK (length(input_sha256) = 64),
    input_payload jsonb NOT NULL,
    result jsonb,
    error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS rzd_calculations_created_idx ON rzd_calculations (created_at DESC, id DESC);

ALTER TABLE rzd_calculations ADD COLUMN IF NOT EXISTS source_revision bigint NOT NULL DEFAULT 0;
ALTER TABLE rzd_calculations ADD COLUMN IF NOT EXISTS base_accepted_version bigint NOT NULL DEFAULT 0;
ALTER TABLE rzd_calculations ADD COLUMN IF NOT EXISTS base_accepted_packet_id uuid;
ALTER TABLE rzd_calculations ADD COLUMN IF NOT EXISTS source_sha256 text NOT NULL DEFAULT '';
ALTER TABLE rzd_calculations ADD COLUMN IF NOT EXISTS baseline_sha256 text NOT NULL DEFAULT '';
ALTER TABLE rzd_calculations ADD COLUMN IF NOT EXISTS context_sha256 text NOT NULL DEFAULT '';
