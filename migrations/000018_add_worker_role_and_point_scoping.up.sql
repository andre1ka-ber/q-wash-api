-- Fourth role, for shift technicians (the "worker" web app). Auth stays
-- exactly as-is: worker logs in via the same POST /auth/login
-- username+password staff/admin already use.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('customer', 'staff', 'admin', 'worker'));

-- Scopes staff/worker to the one point they work at; stays NULL for admin
-- (network-wide) and customer.
ALTER TABLE users
    ADD COLUMN washing_point_id uuid REFERENCES washing_points (id) ON DELETE SET NULL;

CREATE INDEX idx_users_washing_point_id ON users (washing_point_id);

-- Backfill: the existing seeded staff user is scoped to the one existing
-- point (there's only one to scope to today).
UPDATE users SET washing_point_id = (SELECT id FROM washing_points ORDER BY created_at LIMIT 1)
WHERE role = 'staff';
