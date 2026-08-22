DROP INDEX IF EXISTS idx_users_washing_point_id;
ALTER TABLE users DROP COLUMN IF EXISTS washing_point_id;

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('customer', 'staff', 'admin'));
