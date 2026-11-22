-- Platform-wide rules the admin app edits ("Настройки"). One row (id = 1).
-- Defaults keep the behavior from before this table existed: the booking
-- window is wider than the 6 days the mobile app offers, and the cancel
-- cutoff, no-show auto-cancel and SMS ticket are all off.
CREATE TABLE platform_settings (
    id                        smallint PRIMARY KEY CHECK (id = 1),
    booking_horizon_days      integer NOT NULL DEFAULT 7   CHECK (booking_horizon_days BETWEEN 1 AND 60),
    no_show_auto_cancel_min   integer NOT NULL DEFAULT 0   CHECK (no_show_auto_cancel_min BETWEEN 0 AND 240),
    client_cancel_cutoff_min  integer NOT NULL DEFAULT 0   CHECK (client_cancel_cutoff_min BETWEEN 0 AND 1440),
    sms_confirmation          boolean NOT NULL DEFAULT false,
    new_point_moderation      boolean NOT NULL DEFAULT true,
    updated_at                timestamptz NOT NULL DEFAULT now()
);

INSERT INTO platform_settings (id) VALUES (1);
