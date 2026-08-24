CREATE TABLE IF NOT EXISTS maintainer_operations (
    operation_id TEXT PRIMARY KEY CHECK (
        length(operation_id) = 36
        AND substr(operation_id, 9, 1) = '-'
        AND substr(operation_id, 14, 1) = '-'
        AND substr(operation_id, 19, 1) = '-'
        AND substr(operation_id, 24, 1) = '-'
        AND length(replace(operation_id, '-', '')) = 32
        AND replace(operation_id, '-', '') NOT GLOB '*[^0-9a-f]*'
    ),
    -- Constrained by shape, not by enumeration. An enumerated `kind` made
    -- every new typed operation need its own migration -- 0003 existed only
    -- to widen this list, and 0004 only to widen it again -- while providing
    -- no safety: `kind` is a compile-time constant at each call site and is
    -- never caller-supplied. What is worth enforcing is that it stays a short
    -- identifier, so a malformed one cannot be written.
    kind TEXT NOT NULL CHECK (
        length(kind) BETWEEN 1 AND 64
        AND kind NOT GLOB '*[^a-z_]*'
    ),
    request_digest TEXT NOT NULL CHECK (
        length(request_digest) = 64
        AND request_digest NOT GLOB '*[^0-9a-f]*'
    ),
    state TEXT NOT NULL CHECK (
        state IN ('planned', 'executing', 'completed', 'indeterminate')
    ),
    result_json TEXT CHECK (
        (state = 'completed'
            AND result_json IS NOT NULL
            AND length(CAST(result_json AS BLOB)) BETWEEN 2 AND 16384)
        OR (state != 'completed' AND result_json IS NULL)
    ),
    created_at INTEGER NOT NULL DEFAULT (unixepoch()) CHECK (created_at >= 0),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch()) CHECK (updated_at >= created_at)
) STRICT;
