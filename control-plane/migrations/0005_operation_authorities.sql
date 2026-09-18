CREATE TABLE IF NOT EXISTS maintainer_operation_authorities (
    operation_id TEXT PRIMARY KEY,
    owner TEXT NOT NULL CHECK (owner = 'legacy' OR (
        length(owner) = 64 AND owner NOT GLOB '*[^0-9a-f]*'
    )),
    repository TEXT NOT NULL CHECK (
        (owner = 'legacy' AND repository = '') OR
        (owner != 'legacy' AND length(repository) BETWEEN 3 AND 256 AND repository = lower(repository))
    )
) STRICT;
