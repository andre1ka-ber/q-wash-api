-- FCM device tokens: one row per installed app instance, owned by whichever
-- user last registered it (a phone that switches account re-registers the
-- same token, which moves it).
CREATE TABLE device_tokens (
    id          uuid PRIMARY KEY,
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token       text NOT NULL UNIQUE,
    platform    varchar(16) NOT NULL CHECK (platform IN ('android', 'ios')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_device_tokens_user_id ON device_tokens (user_id);

ALTER TABLE notifications DROP CONSTRAINT notifications_channel_check;
ALTER TABLE notifications
    ADD CONSTRAINT notifications_channel_check CHECK (channel IN ('sms', 'push'));

-- Booking-stage notifications (reminder_1h, late_5m, started, finished) carry
-- a kind so each stage is sent at most once per booking.
ALTER TABLE notifications ADD COLUMN kind varchar(32);

CREATE UNIQUE INDEX uq_notifications_queue_kind
    ON notifications (queue_id, kind)
    WHERE kind IS NOT NULL AND queue_id IS NOT NULL;
