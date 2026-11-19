-- PRD F3: multi-layer follow-up funnels.
CREATE TABLE sequences (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name         text NOT NULL,
    from_name    text NOT NULL DEFAULT '',
    from_email   text NOT NULL,
    reply_to     text NOT NULL DEFAULT '',
    definition   jsonb NOT NULL,
    send_window  jsonb NOT NULL DEFAULT '{}',
    status       text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','active','paused')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE enrollments (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    sequence_id     uuid NOT NULL REFERENCES sequences(id) ON DELETE CASCADE,
    contact_id      uuid NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    current_node    text NOT NULL,
    status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active','completed','stopped')),
    stop_reason     text NOT NULL DEFAULT '',
    next_run_at     timestamptz NOT NULL DEFAULT now(),
    last_message_id uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (sequence_id, contact_id)
);
CREATE INDEX enrollments_due_idx ON enrollments (next_run_at) WHERE status = 'active';
