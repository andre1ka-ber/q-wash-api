CREATE TABLE notifications (
    id          uuid PRIMARY KEY,
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    queue_id    uuid REFERENCES queue (id) ON DELETE SET NULL,
    status      varchar(16) NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'sent', 'failed')),
    channel     varchar(16) NOT NULL DEFAULT 'sms'
                CHECK (channel IN ('sms')),
    text        text NOT NULL,
    send_at     timestamptz NOT NULL,
    sent_at     timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_notifications_user_id ON notifications (user_id);
CREATE INDEX idx_notifications_status_send_at ON notifications (status, send_at);
