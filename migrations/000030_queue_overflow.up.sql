-- Overflow ("out of queue") bookings: staff-created bookings that sit in a
-- virtual per-point box outside the numbered boxes (box_number = 0). They
-- ignore capacity and may overlap each other and real bookings, so they are
-- exempt from the no-overlap EXCLUDE constraint. The scheduled time is only
-- the client's expected arrival.
ALTER TABLE queue ADD COLUMN is_overflow boolean NOT NULL DEFAULT false;

ALTER TABLE queue DROP CONSTRAINT queue_box_number_check;
ALTER TABLE queue ADD CONSTRAINT queue_box_number_check
    CHECK ((is_overflow AND box_number = 0) OR (NOT is_overflow AND box_number > 0));

ALTER TABLE queue DROP CONSTRAINT queue_no_overlapping_box_bookings;
ALTER TABLE queue ADD CONSTRAINT queue_no_overlapping_box_bookings
    EXCLUDE USING gist (
        washing_point_id WITH =,
        box_number WITH =,
        tstzrange(scheduled_start_at, scheduled_end_at) WITH &&
    ) WHERE (status NOT IN ('canceled', 'no_show') AND NOT is_overflow);
