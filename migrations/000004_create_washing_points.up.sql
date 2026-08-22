CREATE TABLE washing_points (
    id           uuid PRIMARY KEY,
    name         varchar(255) NOT NULL,
    address      varchar(500) NOT NULL,
    latitude     double precision NOT NULL,
    longitude    double precision NOT NULL,
    boxes_count  integer NOT NULL DEFAULT 2 CHECK (boxes_count > 0),
    open_time    varchar(5) NOT NULL DEFAULT '08:00',  -- "HH:MM", 24h, zero-padded
    close_time   varchar(5) NOT NULL DEFAULT '20:00',  -- "HH:MM", 24h, zero-padded
    is_active    boolean NOT NULL DEFAULT true,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CHECK (open_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    CHECK (close_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    CHECK (close_time > open_time)
);
