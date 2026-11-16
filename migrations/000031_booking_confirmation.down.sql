-- Unconfirmed bookings already hold their slot and the customer's single
-- active booking, so promoting them to 'queue' cannot violate a constraint.
UPDATE queue SET status = 'queue' WHERE status = 'pending';

DROP INDEX queue_one_active_booking_per_user;
CREATE UNIQUE INDEX queue_one_active_booking_per_user
    ON queue (user_id)
    WHERE status IN ('queue', 'waiting', 'washing');

ALTER TABLE queue DROP CONSTRAINT queue_status_check;
ALTER TABLE queue ADD CONSTRAINT queue_status_check
    CHECK (status IN ('queue', 'waiting', 'washing', 'ready', 'canceled', 'no_show'));

ALTER TABLE washing_points DROP COLUMN require_confirmation;
