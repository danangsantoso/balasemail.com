-- Tenancy, identity and the job queue.
CREATE TABLE workspaces (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name             text NOT NULL,
    plan             text NOT NULL DEFAULT 'starter',
    daily_send_limit int  NOT NULL DEFAULT 200,   -- ramps up with reputation (PRD §8)
    sms_credits      int  NOT NULL DEFAULT 0,
    postal_address   text NOT NULL DEFAULT '',    -- required in marketing footers
    status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active','suspended')),
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    email         text NOT NULL UNIQUE,
    full_name     text NOT NULL,
    password_hash text NOT NULL,
    role          text NOT NULL DEFAULT 'owner' CHECK (role IN ('owner','admin','member','viewer')),
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE api_keys (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name         text NOT NULL,
    prefix       text NOT NULL UNIQUE,
    secret_hash  text NOT NULL,
    last_used_at timestamptz,
    revoked_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE jobs (
    id           bigserial PRIMARY KEY,
    kind         text  NOT NULL,
    payload      jsonb NOT NULL DEFAULT '{}',
    priority     int   NOT NULL DEFAULT 0,
    status       text  NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','done','dead')),
    attempts     int   NOT NULL DEFAULT 0,
    max_attempts int   NOT NULL DEFAULT 8,
    run_at       timestamptz NOT NULL DEFAULT now(),
    locked_at    timestamptz,
    last_error   text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX jobs_ready_idx ON jobs (priority DESC, run_at) WHERE status = 'queued';
CREATE INDEX jobs_running_idx ON jobs (locked_at) WHERE status = 'running';
