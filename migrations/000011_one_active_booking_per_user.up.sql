-- DB-level guarantee (in addition to the app-level check in
-- Manager.CreateBooking) that a user never has more than one active
-- (queue/waiting/washing) booking at a time — the whole customer-facing
-- queue model assumes exactly one.
CREATE UNIQUE INDEX queue_one_active_booking_per_user
    ON queue (user_id)
    WHERE status IN ('queue', 'waiting', 'washing');
