-- +migrate Up
-- The tables already exist in portals that created them on start, so everything is IF NOT EXISTS.
CREATE TABLE IF NOT EXISTS portal_sessions (
    hash TEXT PRIMARY KEY,
    binding TEXT NOT NULL,
    created INTEGER NOT NULL,
    expires INTEGER NOT NULL,
    last_seen INTEGER NOT NULL,
    ip TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS audit (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    time INTEGER NOT NULL,
    ip TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    target TEXT NOT NULL DEFAULT '',
    result TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS audit_time ON audit (time);

CREATE INDEX IF NOT EXISTS audit_action ON audit (action);
