CREATE TABLE IF NOT EXISTS rzd_approval_context (
 workflow_id text PRIMARY KEY CHECK(workflow_id='demo-chain'),
 source_revision bigint NOT NULL DEFAULT 1,
 approval_revision bigint NOT NULL DEFAULT 0,
 packet_version bigint NOT NULL DEFAULT 0,
 active_packet_id uuid,
 accepted_packet_id uuid,
 accepted_version bigint NOT NULL DEFAULT 0
);
ALTER TABLE rzd_approval_context ADD COLUMN IF NOT EXISTS source_changed_at timestamptz;
INSERT INTO rzd_approval_context(workflow_id) VALUES ('demo-chain') ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS rzd_approval_grants (
 user_id text PRIMARY KEY,
 name text NOT NULL,
 role text NOT NULL CHECK(role IN ('coordinator','participant','auditor')),
 organization_id text NOT NULL,
 subjects text[] NOT NULL,
 valid_from timestamptz NOT NULL,
 valid_until timestamptz NOT NULL,
 revoked boolean NOT NULL DEFAULT false,
 version bigint NOT NULL DEFAULT 1,
 CHECK(valid_from<valid_until)
);
CREATE TABLE IF NOT EXISTS rzd_approval_packets (
 id uuid PRIMARY KEY,
 version bigint NOT NULL UNIQUE,
 calculation_id uuid NOT NULL REFERENCES rzd_calculations(id),
 source_revision bigint NOT NULL,
 base_accepted_version bigint NOT NULL,
 status text NOT NULL CHECK(status IN ('pending','accepted','superseded','stale','review_required')),
 created_at timestamptz NOT NULL,
 content jsonb NOT NULL,
 acceptance jsonb
);
CREATE TABLE IF NOT EXISTS rzd_approval_responses (
 id uuid PRIMARY KEY,
 packet_id uuid NOT NULL REFERENCES rzd_approval_packets(id),
 subject_id text NOT NULL,
 revision bigint NOT NULL,
 actor_id text NOT NULL REFERENCES rzd_approval_grants(user_id),
 grant_version bigint NOT NULL,
 decision text NOT NULL CHECK(decision IN ('agree','rejected','conditional','revoked')),
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL,
 body jsonb NOT NULL,
 UNIQUE(packet_id,subject_id,revision)
);
CREATE TABLE IF NOT EXISTS rzd_approval_acts (
 id uuid PRIMARY KEY,
 packet_id uuid NOT NULL UNIQUE REFERENCES rzd_approval_packets(id),
 accepted_version bigint NOT NULL UNIQUE,
 actor_id text NOT NULL REFERENCES rzd_approval_grants(user_id),
 accepted_at timestamptz NOT NULL,
 body jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS rzd_approval_idempotency (
 actor_id text NOT NULL,
 action text NOT NULL,
 key text NOT NULL,
 body_sha256 text NOT NULL,
 packet_id uuid,
 PRIMARY KEY(actor_id,action,key)
);
CREATE TABLE IF NOT EXISTS rzd_approval_audit (
 id bigserial PRIMARY KEY,
 workflow_id text NOT NULL,
 actor_id text NOT NULL,
 action text NOT NULL,
 packet_id uuid,
 details jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS rzd_planning_sources (
 workflow_id text NOT NULL REFERENCES rzd_approval_context(workflow_id),
 revision bigint NOT NULL,
 input_sha256 text NOT NULL CHECK(length(input_sha256)=64),
 payload jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(workflow_id,revision)
);
CREATE TABLE IF NOT EXISTS rzd_planning_idempotency (
 actor_id text NOT NULL,
 action text NOT NULL,
 key text NOT NULL,
 body_sha256 text NOT NULL,
 calculation_id uuid REFERENCES rzd_calculations(id),
 source_revision bigint,
 PRIMARY KEY(actor_id,action,key)
);
