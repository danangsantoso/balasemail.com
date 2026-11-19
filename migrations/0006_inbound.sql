-- PRD F8 (unique aliases), F9 (auto-reply), F10 (auto-forward).
CREATE TABLE aliases (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    domain_id     uuid NOT NULL REFERENCES sender_domains(id) ON DELETE CASCADE,
    address       text NOT NULL UNIQUE,
    display_name  text NOT NULL DEFAULT '',
    forward_to    text[] NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE inbound_rules (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name            text NOT NULL,
    priority        int  NOT NULL DEFAULT 100,
    enabled         boolean NOT NULL DEFAULT true,
    match           jsonb NOT NULL DEFAULT '{}',
    action          jsonb NOT NULL,
    stop_processing boolean NOT NULL DEFAULT false,
    rr_cursor       int  NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX inbound_rules_ws_idx ON inbound_rules (workspace_id, priority);

CREATE TABLE inbound_emails (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id       uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    rcpt               text NOT NULL,
    from_email         text NOT NULL,
    from_name          text NOT NULL DEFAULT '',
    subject            text NOT NULL DEFAULT '',
    text_body          text NOT NULL DEFAULT '',
    message_id_header  text NOT NULL DEFAULT '',
    in_reply_to        text NOT NULL DEFAULT '',
    classification     text NOT NULL DEFAULT 'unknown',
    related_message_id uuid REFERENCES messages(id) ON DELETE SET NULL,
    automated          boolean NOT NULL DEFAULT false,
    raw                bytea,
    received_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX inbound_emails_ws_idx ON inbound_emails (workspace_id, received_at DESC);

-- Loop prevention: at most one auto-reply per rule+sender per window.
CREATE TABLE auto_reply_log (
    rule_id  uuid NOT NULL REFERENCES inbound_rules(id) ON DELETE CASCADE,
    sender   text NOT NULL,
    sent_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (rule_id, sender)
);
