DROP TABLE login_events;
ALTER TABLE users
    DROP COLUMN last_platform,
    DROP COLUMN last_app_version;
