-- Initial schema.
--
-- Timestamps are RFC3339 UTC TEXT: readable in the sqlite3 shell and
-- lexicographically sortable, which is what every ORDER BY here relies on.
-- Booleans are INTEGER 0/1.

CREATE TABLE businesses (
  id           INTEGER PRIMARY KEY,
  name         TEXT NOT NULL,
  website      TEXT NOT NULL DEFAULT '',
  domain       TEXT NOT NULL DEFAULT '',  -- normalized dedup key (see internal/dedup)
  name_key     TEXT NOT NULL DEFAULT '',  -- "name|city" fallback dedup key
  email        TEXT NOT NULL DEFAULT '',
  phone        TEXT NOT NULL DEFAULT '',
  address      TEXT NOT NULL DEFAULT '',
  city         TEXT NOT NULL DEFAULT '',
  region       TEXT NOT NULL DEFAULT '',
  country      TEXT NOT NULL DEFAULT '',
  rating       REAL,                      -- NULL unless discover ran --with-ratings
  review_count INTEGER,
  place_id     TEXT,
  source       TEXT NOT NULL,             -- csv | places | manual
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);

-- Dedup is enforced by the database, not by application discipline.
CREATE UNIQUE INDEX ux_businesses_domain   ON businesses(domain)   WHERE domain <> '';
CREATE UNIQUE INDEX ux_businesses_name_key ON businesses(name_key) WHERE domain = '' AND name_key <> '';
CREATE UNIQUE INDEX ux_businesses_place_id ON businesses(place_id) WHERE place_id IS NOT NULL;

CREATE TABLE runs (
  id          INTEGER PRIMARY KEY,
  command     TEXT NOT NULL,
  params      TEXT NOT NULL DEFAULT '{}',
  status      TEXT NOT NULL,              -- running | completed | failed | aborted
  started_at  TEXT NOT NULL,
  finished_at TEXT,
  error       TEXT NOT NULL DEFAULT '',
  stats       TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX ix_runs_command ON runs(command, started_at DESC);

-- Resumability checkpoint. A run that dies partway leaves its completed items
-- marked done; the next run of the same command skips them.
CREATE TABLE run_items (
  run_id     INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  item_key   TEXT NOT NULL,               -- business id, or "page:2" for Places paging
  status     TEXT NOT NULL,               -- pending | done | failed | skipped
  error      TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  PRIMARY KEY (run_id, item_key)
) WITHOUT ROWID;
CREATE INDEX ix_run_items_status ON run_items(run_id, status);

-- One row per observed fact. Appended when a value CHANGES; re-seeing the
-- same value bumps last_seen_at. That preserves transitions ("started running
-- ads" is the buying moment) without a duplicate row per weekly re-enrichment.
CREATE TABLE signals (
  id            INTEGER PRIMARY KEY,
  business_id   INTEGER NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
  source        TEXT NOT NULL,            -- csv | website | places | meta_ads | manual
  type          TEXT NOT NULL,            -- see internal/model/signal.go
  value         TEXT NOT NULL,
  confidence    REAL NOT NULL DEFAULT 1.0,
  detail        TEXT NOT NULL DEFAULT '', -- JSON evidence: form action, matched sentence
  note          TEXT NOT NULL DEFAULT '',
  observed_at   TEXT NOT NULL,            -- first time this value was seen
  last_seen_at  TEXT NOT NULL,            -- most recent confirmation
  superseded_at TEXT,                     -- non-NULL once a differing value replaced it
  run_id        INTEGER REFERENCES runs(id) ON DELETE SET NULL,
  CHECK (confidence >= 0.0 AND confidence <= 1.0)
);

-- Value is part of the key because automation_tag is multi-valued: a business
-- running HubSpot and Calendly has two current facts, not one that overwrites
-- the other. Single-valued types are kept single by superseding on change.
CREATE UNIQUE INDEX ux_signals_current
  ON signals(business_id, source, type, value) WHERE superseded_at IS NULL;
CREATE INDEX ix_signals_history ON signals(business_id, type, observed_at DESC);
CREATE INDEX ix_signals_type    ON signals(type, value) WHERE superseded_at IS NULL;

CREATE TABLE scores (
  id                 INTEGER PRIMARY KEY,
  business_id        INTEGER NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
  run_id             INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  score              INTEGER NOT NULL,    -- normalized 0..100; what --min-score compares
  raw_score          INTEGER NOT NULL,    -- signed weighted sum before normalization
  max_possible       INTEGER NOT NULL,    -- sum of positive weights in the active config
  confidence         REAL    NOT NULL,    -- source coverage, 0..1
  needs_manual_check INTEGER NOT NULL DEFAULT 0,
  breakdown          TEXT NOT NULL,       -- JSON [{rule,label,points,evidence}]
  explanation        TEXT NOT NULL,       -- plain English; opens the cold email
  weights_hash       TEXT NOT NULL,       -- which weights.yaml produced this
  created_at         TEXT NOT NULL,
  CHECK (score >= 0 AND score <= 100),
  CHECK (confidence >= 0.0 AND confidence <= 1.0)
);
CREATE INDEX ix_scores_latest  ON scores(business_id, created_at DESC);
CREATE INDEX ix_scores_ranking ON scores(score DESC, business_id);

CREATE TABLE outreach (
  business_id INTEGER PRIMARY KEY REFERENCES businesses(id) ON DELETE CASCADE,
  status      TEXT NOT NULL DEFAULT 'not_contacted',
  notes       TEXT NOT NULL DEFAULT '',
  updated_at  TEXT NOT NULL
);
CREATE INDEX ix_outreach_status ON outreach(status);

CREATE TABLE outreach_events (
  id          INTEGER PRIMARY KEY,
  business_id INTEGER NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
  from_status TEXT NOT NULL DEFAULT '',
  to_status   TEXT NOT NULL,
  note        TEXT NOT NULL DEFAULT '',
  at          TEXT NOT NULL
);
CREATE INDEX ix_outreach_events_business ON outreach_events(business_id, at DESC);

CREATE TABLE suppression (
  business_id INTEGER PRIMARY KEY REFERENCES businesses(id) ON DELETE CASCADE,
  reason      TEXT NOT NULL,
  created_at  TEXT NOT NULL
);

-- Suppressing only by business_id leaks: re-running discover recreates the
-- business under a fresh id and it reappears in tomorrow's brief. The domain
-- is what actually identifies "these people asked not to be contacted".
CREATE TABLE suppression_domains (
  domain     TEXT PRIMARY KEY,
  reason     TEXT NOT NULL,
  created_at TEXT NOT NULL
) WITHOUT ROWID;

-- Local billing counter. Google's budget alerts notify but do not stop usage,
-- so the monthly ceiling is enforced against this table before each call.
CREATE TABLE api_calls (
  id        INTEGER PRIMARY KEY,
  provider  TEXT NOT NULL,                -- google_places | meta_ads
  sku       TEXT NOT NULL,                -- text_search_essentials | place_details_pro | ...
  endpoint  TEXT NOT NULL,
  billable  INTEGER NOT NULL DEFAULT 1,
  cached    INTEGER NOT NULL DEFAULT 0,   -- cache hits are logged, never billed
  run_id    INTEGER REFERENCES runs(id) ON DELETE SET NULL,
  called_at TEXT NOT NULL,
  month     TEXT NOT NULL                 -- 'YYYY-MM'; makes the ceiling check one indexed count
);
CREATE INDEX ix_api_calls_month ON api_calls(provider, month, billable);

-- Suppression is enforced structurally. list, export and brief read this view
-- and never touch `businesses`, so a command added later cannot forget the check.
CREATE VIEW v_active_businesses AS
SELECT b.*
FROM businesses b
LEFT JOIN suppression s         ON s.business_id = b.id
LEFT JOIN suppression_domains d ON d.domain = b.domain AND b.domain <> ''
WHERE s.business_id IS NULL AND d.domain IS NULL;

-- Present belief, as opposed to the full history in `signals`.
CREATE VIEW v_current_signals AS
SELECT * FROM signals WHERE superseded_at IS NULL;

-- Most recent score per business. Keyed on MAX(id) rather than MAX(created_at):
-- ids are monotonic, so the newest row wins even when two scoring runs land in
-- the same second, and the view returns exactly one row per business.
CREATE VIEW v_latest_scores AS
SELECT s.*
FROM scores s
JOIN (SELECT MAX(id) AS id FROM scores GROUP BY business_id) latest
  ON latest.id = s.id;
