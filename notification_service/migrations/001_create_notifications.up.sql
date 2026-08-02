-- 001_create_notifications.up.sql

CREATE TABLE IF NOT EXISTS notifications (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id    UUID         NOT NULL,
    channel     VARCHAR(20)  NOT NULL CHECK (channel IN ('email', 'webhook')),
    recipient   TEXT         NOT NULL,
    message     TEXT         NOT NULL,
    status      VARCHAR(20)  NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'sent', 'failed')),
    sent_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_notifications_event_id ON notifications (event_id);
CREATE INDEX IF NOT EXISTS idx_notifications_status   ON notifications (status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_notifications_event_channel ON notifications (event_id, channel);
