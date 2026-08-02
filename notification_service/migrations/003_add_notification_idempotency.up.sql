CREATE UNIQUE INDEX IF NOT EXISTS idx_notifications_event_channel
ON notifications (event_id, channel);
