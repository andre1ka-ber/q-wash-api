CREATE TABLE otp_codes (
    id            uuid PRIMARY KEY,
    phone_number  varchar(32) NOT NULL,
    code_hash     varchar(255) NOT NULL,
    expires_at    timestamptz NOT NULL,
    attempts      integer NOT NULL DEFAULT 0,
    consumed_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_otp_codes_phone_number ON otp_codes (phone_number);
