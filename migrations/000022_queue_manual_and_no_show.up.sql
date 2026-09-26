-- Cabinet "Очередь" tab: bookings created by staff for walk-in clients
-- (source = 'manual'), a 'no_show' status, and the car's plate number.
ALTER TABLE queue ADD COLUMN source varchar(8) NOT NULL DEFAULT 'app'
    CHECK (source IN ('app', 'qr', 'manual'));

ALTER TABLE cars ADD COLUMN plate varchar(32);

ALTER TABLE queue DROP CONSTRAINT queue_status_check;
ALTER TABLE queue ADD CONSTRAINT queue_status_check
    CHECK (status IN ('queue', 'waiting', 'washing', 'ready', 'canceled', 'no_show'));

-- A no-show frees its box the same way a cancellation does.
ALTER TABLE queue DROP CONSTRAINT queue_no_overlapping_box_bookings;
ALTER TABLE queue ADD CONSTRAINT queue_no_overlapping_box_bookings
    EXCLUDE USING gist (
        washing_point_id WITH =,
        box_number WITH =,
        tstzrange(scheduled_start_at, scheduled_end_at) WITH &&
    ) WHERE (status NOT IN ('canceled', 'no_show'));
