-- Overflow rows cannot exist without the column; queue_items and
-- booking_photos rows go with them (ON DELETE CASCADE), notifications keep
-- their row with queue_id set to NULL.
DELETE FROM queue WHERE is_overflow;

ALTER TABLE queue DROP CONSTRAINT queue_no_overlapping_box_bookings;
ALTER TABLE queue ADD CONSTRAINT queue_no_overlapping_box_bookings
    EXCLUDE USING gist (
        washing_point_id WITH =,
        box_number WITH =,
        tstzrange(scheduled_start_at, scheduled_end_at) WITH &&
    ) WHERE (status NOT IN ('canceled', 'no_show'));

ALTER TABLE queue DROP CONSTRAINT queue_box_number_check;
ALTER TABLE queue ADD CONSTRAINT queue_box_number_check CHECK (box_number > 0);

ALTER TABLE queue DROP COLUMN is_overflow;
