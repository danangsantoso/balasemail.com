-- PRD F4 (OTP) and F11 (SMS via SMTP bridge).
CREATE TABLE otps (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    channel      text NOT NULL CHECK (channel IN ('email','sms')),
    recipient    text NOT NULL,
    purpose      text NOT NULL DEFAULT 'login',
    code_hash    bytea NOT NULL,
    code_enc     bytea,                -- kept only when SMS fallback is requested
    attempts     int NOT NULL DEFAULT 0,
    max_attempts int NOT NULL DEFAULT 5,
    expires_at   timestamptz NOT NULL,
    verified_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX otps_recipient_idx ON otps (workspace_id, recipient, created_at DESC);

CREATE TABLE sms_messages (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    to_phone            text NOT NULL,
    sender_id           text NOT NULL DEFAULT '',
    body                text NOT NULL,
    segments            int  NOT NULL,
    encoding            text NOT NULL,
    source              text NOT NULL DEFAULT 'api',   -- api|smtp|otp|sequence
    status              text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','sent','failed')),
    provider            text NOT NULL DEFAULT '',
    provider_message_id text NOT NULL DEFAULT '',
    error               text NOT NULL DEFAULT '',
    created_at          timestamptz NOT NULL DEFAULT now(),
    sent_at             timestamptz
);
CREATE INDEX sms_messages_ws_idx ON sms_messages (workspace_id, created_at DESC);
