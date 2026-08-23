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
    kind TEXT NOT NULL CHECK (
        kind IN ('create_pull_request', 'submit_pull_request_review')
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
