-- Subscription plans: every new tenant starts on a time-boxed trial and is
-- moved to a paid plan by a platform admin (payment gateway comes in M4).
ALTER TABLE workspaces ALTER COLUMN plan SET DEFAULT 'trial';
ALTER TABLE workspaces ADD CONSTRAINT workspaces_plan_check
    CHECK (plan IN ('trial', 'starter', 'growth', 'business', 'enterprise'));

-- End of the trial or of the paid period; NULL means no end (existing tenants, enterprise).
ALTER TABLE workspaces ADD COLUMN plan_ends_at timestamptz;

-- Pending upgrade request from the tenant admin, cleared when the plan changes.
ALTER TABLE workspaces ADD COLUMN requested_plan  text NOT NULL DEFAULT '';
ALTER TABLE workspaces ADD COLUMN requested_cycle text NOT NULL DEFAULT ''
    CONSTRAINT workspaces_requested_cycle_check CHECK (requested_cycle IN ('', 'monthly', 'yearly'));
ALTER TABLE workspaces ADD COLUMN requested_at timestamptz;
