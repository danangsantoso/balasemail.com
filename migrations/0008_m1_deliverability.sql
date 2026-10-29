-- M1: shared rate limiting, async bounce/complaint bookkeeping, customer webhooks.
CREATE TABLE rate_buckets (
    key          text NOT NULL,
    window_start timestamptz NOT NULL,
    count        int NOT NULL,
    PRIMARY KEY (key, window_start)
);

ALTER TABLE messages ADD COLUMN complained_at timestamptz;
ALTER TABLE messages ADD COLUMN bounce_reason text NOT NULL DEFAULT '';

CREATE TABLE webhook_endpoints (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    url          text NOT NULL,
    secret_enc   bytea NOT NULL,              -- AES-GCM encrypted signing secret
    events       text[] NOT NULL DEFAULT '{}', -- empty = all events
    enabled      boolean NOT NULL DEFAULT true,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhook_endpoints_ws_idx ON webhook_endpoints (workspace_id) WHERE enabled;

CREATE TABLE webhook_deliveries (
    id          bigserial PRIMARY KEY,
    endpoint_id uuid NOT NULL REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
    event_type  text NOT NULL,
    status_code int,
    error       text NOT NULL DEFAULT '',
    attempt     int NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhook_deliveries_endpoint_idx ON webhook_deliveries (endpoint_id, created_at DESC);
