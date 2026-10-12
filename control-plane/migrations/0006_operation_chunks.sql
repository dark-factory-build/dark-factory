CREATE TABLE IF NOT EXISTS maintainer_operation_chunks (
    -- No REFERENCES clause: renaming maintainer_operations during a rebuild
    -- would rewrite it and fail the schema audit. The journal deletes an
    -- operation's chunks when it completes or is refused.
    operation_id TEXT NOT NULL,
    chunk_index INTEGER NOT NULL CHECK (chunk_index BETWEEN 0 AND 127),
    -- Canonical base64 of at most 256 KiB, so one chunk fits factoryd's
    -- 512 KiB attempt envelope.
    content TEXT NOT NULL CHECK (length(content) BETWEEN 4 AND 349528),
    PRIMARY KEY (operation_id, chunk_index)
) STRICT;
