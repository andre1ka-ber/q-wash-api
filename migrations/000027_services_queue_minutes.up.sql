ALTER TABLE services ADD COLUMN queue_minutes integer CHECK (queue_minutes > 0);
