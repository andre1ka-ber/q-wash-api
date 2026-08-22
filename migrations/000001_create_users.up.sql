CREATE TABLE users (
    id             uuid PRIMARY KEY,
    phone_number   varchar(32) NOT NULL,
    name           varchar(255),
    role           varchar(16) NOT NULL DEFAULT 'customer'
                   CHECK (role IN ('customer', 'staff', 'admin')),
    last_login_at  timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_users_phone_number ON users (phone_number);
