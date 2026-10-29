-- PRD F5: business domains with ownership, SPF, DKIM and DMARC checks.
CREATE TABLE sender_domains (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id       uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name               text NOT NULL,
    verification_token text NOT NULL,
    dkim_selector      text NOT NULL,
    dkim_private_key   bytea NOT NULL,           -- AES-GCM encrypted PEM
    dkim_public_key    text NOT NULL,            -- base64 DER for the DNS record
    verified           boolean NOT NULL DEFAULT false,
    spf_ok             boolean NOT NULL DEFAULT false,
    dkim_ok            boolean NOT NULL DEFAULT false,
    dmarc_ok           boolean NOT NULL DEFAULT false,
    last_checked_at    timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, name)
);
-- A domain can be verified by only one workspace.
CREATE UNIQUE INDEX sender_domains_verified_name ON sender_domains (name) WHERE verified;
