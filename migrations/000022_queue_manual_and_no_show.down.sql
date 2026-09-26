UPDATE queue SET status = 'canceled', canceled_at = COALESCE(canceled_at, now()) WHERE status = 'no_show';

ALTER TABLE queue DROP CONSTRAINT queue_no_overlapping_box_bookings;
ALTER TABLE queue ADD CONSTRAINT queue_no_overlapping_box_bookings
    EXCLUDE USING gist (
        washing_point_id WITH =,
        box_number WITH =,
        tstzrange(scheduled_start_at, scheduled_end_at) WITH &&
    ) WHERE (status <> 'canceled');

ALTER TABLE queue DROP CONSTRAINT queue_status_check;
ALTER TABLE queue ADD CONSTRAINT queue_status_check
    CHECK (status IN ('queue', 'waiting', 'washing', 'ready', 'canceled'));

ALTER TABLE cars DROP COLUMN IF EXISTS plate;
ALTER TABLE queue DROP COLUMN IF EXISTS source;
