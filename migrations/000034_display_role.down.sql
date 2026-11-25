-- Display accounts cannot exist without the role, so they are removed; they can
-- be created again after migrating up.
DELETE FROM users WHERE role = 'display';

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('customer', 'staff', 'admin', 'worker'));
