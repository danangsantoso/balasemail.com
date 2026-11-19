-- Prospect searches by region and business category (PRD F1): businesses come
-- from OpenStreetMap, emails from their listing and their own website.
CREATE TABLE finder_searches (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_by    uuid REFERENCES users(id) ON DELETE SET NULL,
    region        text NOT NULL,
    category      text NOT NULL,
    max_places    int  NOT NULL,
    crawl         bool NOT NULL DEFAULT true,
    save_contacts bool NOT NULL DEFAULT false,
    tags          text[] NOT NULL DEFAULT '{}',
    status        text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'searching', 'crawling', 'done', 'failed')),
    error         text NOT NULL DEFAULT '',
    saved         int  NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz
);
CREATE INDEX finder_searches_ws_idx ON finder_searches (workspace_id, created_at DESC);

CREATE TABLE finder_places (
    search_id  uuid NOT NULL REFERENCES finder_searches(id) ON DELETE CASCADE,
    idx        int  NOT NULL,
    name       text NOT NULL,
    category   text NOT NULL DEFAULT '',
    address    text NOT NULL DEFAULT '',
    city       text NOT NULL DEFAULT '',
    phone      text NOT NULL DEFAULT '',
    website    text NOT NULL DEFAULT '',
    source_url text NOT NULL DEFAULT '',
    emails     text[] NOT NULL DEFAULT '{}',
    crawled    bool NOT NULL DEFAULT false,  -- website visited (or nothing to visit)
    note       text NOT NULL DEFAULT '',
    PRIMARY KEY (search_id, idx)
);
