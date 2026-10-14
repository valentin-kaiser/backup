-- name: CreateSession :exec
INSERT INTO
    portal_sessions (hash, binding, created, expires, last_seen, ip)
VALUES
    (?, ?, ?, ?, ?, ?);

-- name: GetSession :one
SELECT
    binding,
    expires,
    last_seen
FROM
    portal_sessions
WHERE
    hash = ?;

-- name: TouchSession :exec
UPDATE portal_sessions
SET
    last_seen = ?
WHERE
    hash = ?;

-- name: DeleteSession :exec
DELETE FROM portal_sessions
WHERE
    hash = ?;

-- name: DeleteAllSessions :exec
DELETE FROM portal_sessions;

-- name: PurgeSessions :exec
DELETE FROM portal_sessions
WHERE
    expires <= @now
    OR last_seen <= @idle_before;
