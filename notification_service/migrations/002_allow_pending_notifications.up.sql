ALTER TABLE notifications
ALTER COLUMN status SET DEFAULT 'pending';

ALTER TABLE notifications
DROP CONSTRAINT IF EXISTS notifications_status_check;

ALTER TABLE notifications
ADD CONSTRAINT notifications_status_check
CHECK (status IN ('pending', 'sent', 'failed'));
