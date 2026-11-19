-- Campaigns (PRD F7) and the unified outbound message log (PRD F6).
CREATE TABLE campaigns (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name         text NOT NULL,
    from_name    text NOT NULL DEFAULT '',
    from_email   text NOT NULL,
    reply_to     text NOT NULL DEFAULT '',
    subject      text NOT NULL,
    html         text NOT NULL DEFAULT '',
    text_body    text NOT NULL DEFAULT '',
    tag_filter   text[] NOT NULL DEFAULT '{}',
    send_per_minute int NOT NULL DEFAULT 60,
    status       text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','sending','sent','paused')),
    launched_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE messages (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    category          text NOT NULL CHECK (category IN ('marketing','sequence','transactional','otp','auto_reply','forward','relay')),
    campaign_id       uuid REFERENCES campaigns(id) ON DELETE SET NULL,
    enrollment_id     uuid,
    step_id           text NOT NULL DEFAULT '',
    contact_id        uuid REFERENCES contacts(id) ON DELETE SET NULL,
    from_email        text NOT NULL,
    from_name         text NOT NULL DEFAULT '',
    reply_to          text NOT NULL DEFAULT '',
    to_email          text NOT NULL,
    subject           text NOT NULL DEFAULT '',
    html              text NOT NULL DEFAULT '',
    text_body         text NOT NULL DEFAULT '',
    headers           jsonb NOT NULL DEFAULT '{}',
    in_reply_to       text NOT NULL DEFAULT '',
    raw               bytea,                       -- pre-built MIME for SMTP relay
    message_id_header text NOT NULL UNIQUE,
    status            text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','sent','failed','bounced','suppressed')),
    provider          text NOT NULL DEFAULT '',
    error             text NOT NULL DEFAULT '',
    queued_at         timestamptz NOT NULL DEFAULT now(),
    sent_at           timestamptz,
    opened_at         timestamptz,
    clicked_at        timestamptz,
    replied_at        timestamptz,
    bounced_at        timestamptz,
    unsubscribed_at   timestamptz
);
CREATE INDEX messages_campaign_idx   ON messages (campaign_id) WHERE campaign_id IS NOT NULL;
CREATE INDEX messages_enrollment_idx ON messages (enrollment_id) WHERE enrollment_id IS NOT NULL;
CREATE INDEX messages_ws_sent_idx    ON messages (workspace_id, sent_at);

CREATE TABLE message_events (
    id           bigserial PRIMARY KEY,
    message_id   uuid NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL,
    type         text NOT NULL,   -- sent|open|click|reply|bounce|unsubscribe|complaint
    meta         jsonb NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX message_events_message_idx ON message_events (message_id);
