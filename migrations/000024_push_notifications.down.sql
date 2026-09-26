DROP INDEX IF EXISTS uq_notifications_queue_kind;
ALTER TABLE notifications DROP COLUMN IF EXISTS kind;

-- Push rows can't satisfy the old channel check.
DELETE FROM notifications WHERE channel = 'push';
ALTER TABLE notifications DROP CONSTRAINT notifications_channel_check;
ALTER TABLE notifications
    ADD CONSTRAINT notifications_channel_check CHECK (channel IN ('sms'));

DROP TABLE IF EXISTS device_tokens;
