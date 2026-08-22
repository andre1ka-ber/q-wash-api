-- Username + password login, for staff/admin surfaces (the queue board and
-- the staff panel) that shouldn't use phone+OTP. Customers keep using
-- phone+OTP; these columns stay NULL for them. Postgres unique indexes
-- already allow multiple NULLs, so no partial-index WHERE clause is needed.
ALTER TABLE users
    ADD COLUMN username      varchar(50),
    ADD COLUMN password_hash varchar(255);

CREATE UNIQUE INDEX idx_users_username ON users (username);
