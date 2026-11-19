-- Contacts (PRD F1/F2/F7) and suppression list (PRD §8).
CREATE TABLE contacts (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    email             text NOT NULL,
    first_name        text NOT NULL DEFAULT '',
    last_name         text NOT NULL DEFAULT '',
    company           text NOT NULL DEFAULT '',
    title             text NOT NULL DEFAULT '',
    phone             text NOT NULL DEFAULT '',
    tags              text[] NOT NULL DEFAULT '{}',
    fields            jsonb  NOT NULL DEFAULT '{}',
    source            text NOT NULL DEFAULT 'manual',   -- manual|import|finder|api
    source_url        text NOT NULL DEFAULT '',         -- audit trail for scraped data
    legal_basis       text NOT NULL DEFAULT 'legitimate_interest',
    validation_status text NOT NULL DEFAULT 'unverified',
    validation_score  int,
    validated_at      timestamptz,
    unsubscribed_at   timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, email)
);
CREATE INDEX contacts_tags_idx ON contacts USING gin (tags);
CREATE INDEX contacts_validation_idx ON contacts (workspace_id, validation_status);

CREATE TABLE suppressions (
    id           bigserial PRIMARY KEY,
    workspace_id uuid REFERENCES workspaces(id) ON DELETE CASCADE, -- NULL = global
    value        text NOT NULL,                                    -- lowercase email or domain
    kind         text NOT NULL CHECK (kind IN ('email','domain')),
    reason       text NOT NULL,                                    -- unsubscribe|hard_bounce|complaint|manual|erasure_request
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT suppressions_ws_value_key UNIQUE NULLS NOT DISTINCT (workspace_id, value)
);
CREATE INDEX suppressions_value_idx ON suppressions (value);
