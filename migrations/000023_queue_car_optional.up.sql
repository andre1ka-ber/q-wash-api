-- Walk-in bookings added by staff may have no car data at all (only the
-- client's phone is required).
ALTER TABLE queue ALTER COLUMN car_id DROP NOT NULL;
