-- Fails if any car-less walk-in booking exists; those rows must be
-- resolved by hand first (there is no safe car to invent for them).
ALTER TABLE queue ALTER COLUMN car_id SET NOT NULL;
