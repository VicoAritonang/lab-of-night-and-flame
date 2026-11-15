-- Run manually: psql "$NEON_DB_URL" -v ON_ERROR_STOP=1 -f sql/init.sql
-- Additive/idempotent. Does not touch legacy SQLite or drop existing user tables.
BEGIN;
CREATE SCHEMA IF NOT EXISTS arl;
CREATE TABLE IF NOT EXISTS arl.schema_versions(version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS arl.projects (
 id text PRIMARY KEY, name text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO arl.projects(id,name) VALUES ('default','Default lab') ON CONFLICT DO NOTHING;
-- Versioned JSONB documents preserve provider-specific payloads without losing fields.
-- Domain entities: datasets, samples, experiments, campaigns, trials, attempts,
-- assessments, predictions, connections, catalogs, results, reports and settings.
CREATE TABLE IF NOT EXISTS arl.documents (
 project_id text NOT NULL REFERENCES arl.projects(id), kind text NOT NULL, id text NOT NULL,
 data jsonb NOT NULL CHECK (jsonb_typeof(data)='object'), version bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id,kind,id)
);
CREATE INDEX IF NOT EXISTS documents_listing ON arl.documents(project_id,kind,created_at DESC);
CREATE INDEX IF NOT EXISTS documents_data ON arl.documents USING gin(data jsonb_path_ops);
CREATE INDEX IF NOT EXISTS documents_campaign ON arl.documents(project_id,kind,(data->>'campaign_id'));
CREATE INDEX IF NOT EXISTS documents_dataset ON arl.documents(project_id,kind,(data->>'dataset_id'));
CREATE INDEX IF NOT EXISTS documents_trial ON arl.documents(project_id,kind,(data->>'trial_id'));
CREATE TABLE IF NOT EXISTS arl.jobs (
 id text PRIMARY KEY, project_id text NOT NULL REFERENCES arl.projects(id), kind text NOT NULL,
 payload jsonb NOT NULL, state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','running','done','failed')),
 dedup_key text NOT NULL, result jsonb, error text, attempts integer NOT NULL DEFAULT 0,
 lease_token text, lease_until timestamptz, next_send_at timestamptz NOT NULL DEFAULT now(),
 sent_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(project_id,dedup_key)
);
CREATE INDEX IF NOT EXISTS jobs_outbox ON arl.jobs(next_send_at) WHERE state IN ('queued','running');
CREATE TABLE IF NOT EXISTS arl.stream_events (
 ordinal bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 project_id text NOT NULL REFERENCES arl.projects(id), channel text NOT NULL,
 event jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS stream_events_channel ON arl.stream_events(project_id,channel,ordinal);
CREATE TABLE IF NOT EXISTS arl.artifacts (
 project_id text NOT NULL REFERENCES arl.projects(id), digest text NOT NULL,
 mime text NOT NULL, bytes integer NOT NULL CHECK(bytes BETWEEN 1 AND 20971520),
 width integer NOT NULL, height integer NOT NULL, content bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(project_id,digest)
);
CREATE TABLE IF NOT EXISTS arl.uploads (
 id text PRIMARY KEY, project_id text NOT NULL REFERENCES arl.projects(id),
 filename text NOT NULL, content bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS arl.sessions (
 id text PRIMARY KEY, project_id text NOT NULL REFERENCES arl.projects(id), service text NOT NULL,
 resume_hash text NOT NULL, config jsonb NOT NULL,
 connected_owner text, connected_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(), last_seen timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS sessions_project ON arl.sessions(project_id,last_seen DESC);
CREATE TABLE IF NOT EXISTS arl.traces (
 id text PRIMARY KEY, project_id text NOT NULL REFERENCES arl.projects(id),
 session_id text NOT NULL REFERENCES arl.sessions(id), external_id text NOT NULL, name text NOT NULL,
 status text NOT NULL DEFAULT 'running', checks jsonb NOT NULL DEFAULT '{}',
 event_count integer NOT NULL DEFAULT 0, total_bytes bigint NOT NULL DEFAULT 0, started_at double precision NOT NULL, finished_at double precision,
 UNIQUE(session_id,external_id)
);
CREATE INDEX IF NOT EXISTS traces_listing ON arl.traces(project_id,started_at DESC);
CREATE TABLE IF NOT EXISTS arl.trace_events (
 ordinal bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 session_id text NOT NULL REFERENCES arl.sessions(id), trace_id text NOT NULL REFERENCES arl.traces(id),
 event_id text NOT NULL, client_digest text NOT NULL, kind text NOT NULL, phase text NOT NULL,
 billing_id text, received_at double precision NOT NULL, event jsonb NOT NULL,
 UNIQUE(session_id,event_id)
);
CREATE INDEX IF NOT EXISTS trace_events_order ON arl.trace_events(trace_id,ordinal);
CREATE UNIQUE INDEX IF NOT EXISTS trace_billing_unique ON arl.trace_events(trace_id,billing_id) WHERE billing_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS arl.provider_limits (
 project_id text NOT NULL REFERENCES arl.projects(id), provider text NOT NULL, next_at timestamptz NOT NULL,
 PRIMARY KEY(project_id,provider)
);
CREATE TABLE IF NOT EXISTS arl.audit (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, project_id text NOT NULL REFERENCES arl.projects(id),
 ts double precision NOT NULL, actor text NOT NULL, action text NOT NULL, resource text NOT NULL,
 detail jsonb NOT NULL DEFAULT '{}'
);
INSERT INTO arl.schema_versions(version) VALUES(1) ON CONFLICT DO NOTHING;
COMMIT;
