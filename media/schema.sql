-- Explicit migration: application chooses when to execute this SQL.
-- For custom schemas/tables, render the identifier placeholders using a trusted
-- migration tool; never interpolate untrusted identifiers.
CREATE TABLE IF NOT EXISTS media_objects (
    id UUID PRIMARY KEY,
    bucket TEXT NOT NULL,
    object_key TEXT NOT NULL,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    state TEXT NOT NULL CHECK (state IN ('pending', 'ready', 'deleting')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (bucket, object_key)
);
CREATE INDEX IF NOT EXISTS media_objects_cleanup_idx ON media_objects (state, updated_at)
WHERE state IN ('pending', 'deleting');
