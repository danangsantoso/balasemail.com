-- Multi-tenant roles: tenant "admin" / "user", plus platform super admins.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
UPDATE users SET role = 'admin' WHERE role IN ('owner', 'admin');
UPDATE users SET role = 'user'  WHERE role NOT IN ('admin');
ALTER TABLE users ALTER COLUMN role SET DEFAULT 'user';
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('admin', 'user'));

ALTER TABLE users ADD COLUMN platform_role text NOT NULL DEFAULT ''
    CONSTRAINT users_platform_role_check CHECK (platform_role IN ('', 'superadmin'));
ALTER TABLE users ADD COLUMN disabled_at timestamptz;
ALTER TABLE users ADD COLUMN last_login_at timestamptz;

CREATE INDEX users_workspace_idx ON users (workspace_id);
CREATE INDEX workspaces_created_idx ON workspaces (created_at);
