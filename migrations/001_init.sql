CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS batches (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  started_at TEXT,
  finished_at TEXT,
  published_within_days INTEGER NOT NULL,
  keyword_cap INTEGER NOT NULL,
  hide_previously_seen INTEGER NOT NULL,
  search_quota_units INTEGER NOT NULL DEFAULT 0,
  error_summary TEXT NOT NULL DEFAULT '',
  total_results INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS keywords (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  batch_id INTEGER NOT NULL,
  term TEXT NOT NULL,
  normalized_term TEXT NOT NULL,
  phase TEXT NOT NULL,
  provider TEXT NOT NULL DEFAULT '',
  position INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  UNIQUE(batch_id, normalized_term, phase, provider)
);

CREATE TABLE IF NOT EXISTS videos (
  video_id TEXT PRIMARY KEY,
  watch_url TEXT NOT NULL,
  shorts_url TEXT NOT NULL,
  title TEXT NOT NULL,
  channel_title TEXT NOT NULL,
  published_at TEXT NOT NULL,
  duration_sec INTEGER NOT NULL,
  views INTEGER NOT NULL,
  verified_short TEXT NOT NULL,
  thumbnail_url TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  first_seen_batch_id INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS batch_hits (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  batch_id INTEGER NOT NULL,
  video_id TEXT NOT NULL,
  source_keyword TEXT NOT NULL,
  duplicate_seen INTEGER NOT NULL,
  hidden_by_default INTEGER NOT NULL,
  age_days INTEGER NOT NULL,
  views_per_day REAL NOT NULL,
  breakout_score REAL NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(batch_id, video_id, source_keyword)
);

CREATE TABLE IF NOT EXISTS llm_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  batch_id INTEGER NOT NULL,
  provider TEXT NOT NULL,
  prompt TEXT NOT NULL,
  raw_response TEXT NOT NULL,
  status TEXT NOT NULL,
  error_message TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  finished_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS job_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  batch_id INTEGER NOT NULL,
  sequence INTEGER NOT NULL,
  level TEXT NOT NULL,
  stage TEXT NOT NULL,
  message TEXT NOT NULL,
  payload TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_keywords_batch_phase ON keywords(batch_id, phase, position);
CREATE INDEX IF NOT EXISTS idx_hits_batch ON batch_hits(batch_id);
CREATE INDEX IF NOT EXISTS idx_logs_batch_seq ON job_logs(batch_id, sequence);
