CREATE TABLE booking_photos (
    id          uuid PRIMARY KEY,
    queue_id    uuid NOT NULL REFERENCES queue (id) ON DELETE CASCADE,
    url         varchar(500) NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_booking_photos_queue_id ON booking_photos (queue_id);
