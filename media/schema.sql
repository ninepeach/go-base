-- Apply this migration before starting Media.
-- File metadata is stored separately from object bytes.
CREATE TABLE IF NOT EXISTS media_objects (
    id UUID PRIMARY KEY,
    bucket TEXT NOT NULL,
    object_key TEXT NOT NULL,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    state TEXT NOT NULL CHECK (state IN ('ready', 'deleting')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (bucket, object_key)
);
CREATE INDEX IF NOT EXISTS media_objects_state_idx ON media_objects (state);
