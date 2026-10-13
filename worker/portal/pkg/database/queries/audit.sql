-- Zero values of the filters do not filter. text matches a part of the target or the detail, whatever the case.

-- name: CreateAuditEntry :exec
INSERT INTO
    audit (time, ip, action, target, result, detail)
VALUES
    (?, ?, ?, ?, ?, ?);

-- name: ListAuditEntries :many
SELECT
    *
FROM
    audit
WHERE
    (CAST(@action AS TEXT) = '' OR action = @action)
    AND (CAST(@result AS TEXT) = '' OR result = @result)
    AND (CAST(@text AS TEXT) = '' OR instr(lower(target), lower(@text)) > 0 OR instr(lower(detail), lower(@text)) > 0)
    AND (CAST(@since AS INTEGER) = 0 OR time >= @since)
    AND (CAST(@until AS INTEGER) = 0 OR time < @until)
    AND (CAST(@before AS INTEGER) = 0 OR id < @before)
ORDER BY
    id DESC
LIMIT
    @max_rows;

-- name: ExportAuditEntries :many
SELECT
    *
FROM
    audit
WHERE
    (CAST(@action AS TEXT) = '' OR action = @action)
    AND (CAST(@result AS TEXT) = '' OR result = @result)
    AND (CAST(@text AS TEXT) = '' OR instr(lower(target), lower(@text)) > 0 OR instr(lower(detail), lower(@text)) > 0)
    AND (CAST(@since AS INTEGER) = 0 OR time >= @since)
    AND (CAST(@until AS INTEGER) = 0 OR time < @until)
    AND (CAST(@before AS INTEGER) = 0 OR id < @before)
ORDER BY
    id;

-- name: DeleteAuditEntriesBefore :exec
DELETE FROM audit
WHERE
    time < ?;

-- name: DeleteAuditEntriesBeyond :exec
DELETE FROM audit
WHERE
    id <= (
        SELECT
            id
        FROM
            audit
        ORDER BY
            id DESC
        LIMIT
            1
        OFFSET
            ?
    );
