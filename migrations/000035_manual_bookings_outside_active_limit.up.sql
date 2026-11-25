-- Staff-entered (manual) bookings no longer take the customer's single active
-- booking, so one washing point cannot block a customer from booking.
DROP INDEX queue_one_active_booking_per_user;
CREATE UNIQUE INDEX queue_one_active_booking_per_user
    ON queue (user_id)
    WHERE status IN ('pending', 'queue', 'waiting', 'washing') AND source <> 'manual';
