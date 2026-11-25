-- Reversible only while no customer holds more than one active booking: the
-- stricter index fails to build otherwise. Finish or cancel the extra manual
-- bookings first.
DROP INDEX queue_one_active_booking_per_user;
CREATE UNIQUE INDEX queue_one_active_booking_per_user
    ON queue (user_id)
    WHERE status IN ('pending', 'queue', 'waiting', 'washing');
