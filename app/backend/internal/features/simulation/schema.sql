CREATE TABLE IF NOT EXISTS rzd_sim_runs (
    id uuid PRIMARY KEY,
    state jsonb NOT NULL,
    created_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS rzd_sim_attempts (
    id uuid PRIMARY KEY,
    run_id uuid NOT NULL REFERENCES rzd_sim_runs(id),
    condition_revision integer NOT NULL,
    state_version integer NOT NULL,
    plan_version integer NOT NULL,
    input_sha256 text NOT NULL,
    payload jsonb NOT NULL,
    status text NOT NULL,
    result jsonb,
    message text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS rzd_sim_plans (
    run_id uuid NOT NULL REFERENCES rzd_sim_runs(id),
    version integer NOT NULL,
    attempt_id uuid UNIQUE REFERENCES rzd_sim_attempts(id),
    plan jsonb NOT NULL,
    assignments jsonb NOT NULL,
    actor_id text NOT NULL,
    decision_tick integer NOT NULL,
    PRIMARY KEY (run_id, version)
);
