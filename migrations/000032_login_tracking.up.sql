-- Login tracking for the admin "Аналитика → Платформа" view: which kind of
-- device (android/ios/web/other) and app version each account last used, and
-- one row per successful login so per-point activity (sessions, active days)
-- can be counted. No IP address and no raw User-Agent are stored.
ALTER TABLE users
    ADD COLUMN last_platform    varchar(16),
    ADD COLUMN last_app_version varchar(32);

CREATE TABLE login_events (
    id               uuid PRIMARY KEY,
    user_id          uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role             varchar(16) NOT NULL,
    washing_point_id uuid REFERENCES washing_points (id) ON DELETE SET NULL,
    platform         varchar(16) NOT NULL,
    app_version      varchar(32),
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_login_events_created_at ON login_events (created_at);
CREATE INDEX idx_login_events_point_created ON login_events (washing_point_id, created_at);
