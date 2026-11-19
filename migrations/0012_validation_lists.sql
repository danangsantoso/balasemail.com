-- Email lists uploaded for validation (CSV, TXT or Excel) and their per-row results.
CREATE TABLE validation_lists (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    filename     text NOT NULL,
    format       text NOT NULL,
    status       text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
    total        int  NOT NULL DEFAULT 0,
    skipped      int  NOT NULL DEFAULT 0,  -- duplicate rows
    created_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz
);
CREATE INDEX validation_lists_ws_idx ON validation_lists (workspace_id, created_at DESC);

CREATE TABLE validation_list_items (
    list_id       uuid NOT NULL REFERENCES validation_lists(id) ON DELETE CASCADE,
    row_no        int  NOT NULL,           -- row in the uploaded file
    email         text NOT NULL,
    first_name    text NOT NULL DEFAULT '',
    last_name     text NOT NULL DEFAULT '',
    company       text NOT NULL DEFAULT '',
    status        text NOT NULL DEFAULT '', -- empty until checked
    reason        text NOT NULL DEFAULT '',
    score         int  NOT NULL DEFAULT 0,
    mx_host       text NOT NULL DEFAULT '',
    disposable    bool NOT NULL DEFAULT false,
    role_based    bool NOT NULL DEFAULT false,
    free_provider bool NOT NULL DEFAULT false,
    catch_all     bool NOT NULL DEFAULT false,
    checked_at    timestamptz,
    PRIMARY KEY (list_id, row_no)
);
CREATE INDEX validation_list_items_pending_idx ON validation_list_items (list_id, row_no) WHERE status = '';
