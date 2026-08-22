-- Per-weekday operating hours, replacing washing_points.open_time/
-- close_time as the eventual source of truth for the availability
-- algorithm (docs/PLAN_WEB_APPS.md phase 5 — not switched over yet by
-- this migration; open_time/close_time are untouched and still what
-- internal/queue reads). Purely additive for now.
CREATE TABLE washing_point_schedules (
    id                uuid PRIMARY KEY,
    washing_point_id  uuid NOT NULL REFERENCES washing_points (id) ON DELETE CASCADE,
    weekday           smallint NOT NULL CHECK (weekday BETWEEN 0 AND 6), -- 0=Monday..6=Sunday
    is_open           boolean NOT NULL DEFAULT true,
    open_time         varchar(5), -- "HH:MM", NULL when is_open = false
    close_time        varchar(5),
    break_start       varchar(5),
    break_end         varchar(5),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),

    UNIQUE (washing_point_id, weekday),
    CHECK (open_time IS NULL OR open_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    CHECK (close_time IS NULL OR close_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    CHECK (break_start IS NULL OR break_start ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    CHECK (break_end IS NULL OR break_end ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$')
);

-- Backfill: every existing point gets the same 7-day schedule it already
-- effectively has via open_time/close_time (same hours every day, no
-- break, always open).
INSERT INTO washing_point_schedules (id, washing_point_id, weekday, is_open, open_time, close_time)
SELECT gen_random_uuid(), wp.id, d.weekday, true, wp.open_time, wp.close_time
FROM washing_points wp
CROSS JOIN generate_series(0, 6) AS d(weekday);
