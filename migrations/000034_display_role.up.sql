-- Fifth role for a point's lobby screen, which may only read that point's board.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('customer', 'staff', 'admin', 'worker', 'display'));
