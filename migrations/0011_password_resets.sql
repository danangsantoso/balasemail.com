-- Password reset links sent by email. Only a hash of the token is stored.
CREATE TABLE password_resets (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  text NOT NULL UNIQUE,
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX password_resets_user_idx ON password_resets (user_id, created_at);

-- Sessions issued before the last password change are rejected.
ALTER TABLE users ADD COLUMN password_changed_at timestamptz;
