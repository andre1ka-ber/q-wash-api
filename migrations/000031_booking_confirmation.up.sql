-- Manual booking confirmation: a washing point can require staff to confirm
-- online (app/QR) bookings. Such a booking starts as 'pending', holds its
-- box slot (the no-overlap EXCLUDE only exempts canceled/no_show) and counts
-- as the customer's one active booking until staff confirm (-> 'queue') or
-- reject (-> 'canceled') it.
ALTER TABLE washing_points ADD COLUMN require_confirmation boolean NOT NULL DEFAULT false;

ALTER TABLE queue DROP CONSTRAINT queue_status_check;
ALTER TABLE queue ADD CONSTRAINT queue_status_check
    CHECK (status IN ('pending', 'queue', 'waiting', 'washing', 'ready', 'canceled', 'no_show'));

DROP INDEX queue_one_active_booking_per_user;
CREATE UNIQUE INDEX queue_one_active_booking_per_user
    ON queue (user_id)
    WHERE status IN ('pending', 'queue', 'waiting', 'washing');
