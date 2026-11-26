-- Customer passkeys (WebAuthn). credential holds the library's credential
-- record (public key, sign counter, backup flags, AAGUID) as one document.
CREATE TABLE passkeys (
    id             uuid PRIMARY KEY,
    user_id        uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    credential_id  bytea NOT NULL UNIQUE,
    credential     jsonb NOT NULL,
    platform       varchar(16) NOT NULL CHECK (platform IN ('android', 'ios', 'web', 'other')),
    last_used_at   timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_passkeys_user_id ON passkeys (user_id);

-- One-time WebAuthn challenges, deleted by the first attempt to use them.
-- user_id is NULL for a sign-in challenge: the caller is not known yet.
CREATE TABLE passkey_challenges (
    id          uuid PRIMARY KEY,
    user_id     uuid REFERENCES users (id) ON DELETE CASCADE,
    purpose     varchar(16) NOT NULL CHECK (purpose IN ('register', 'login')),
    session     jsonb NOT NULL,
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_passkey_challenges_expires_at ON passkey_challenges (expires_at);
