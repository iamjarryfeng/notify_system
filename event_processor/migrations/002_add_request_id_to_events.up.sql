ALTER TABLE events
ADD COLUMN IF NOT EXISTS request_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_events_request_id ON events (request_id);