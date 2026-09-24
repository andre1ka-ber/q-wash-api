-- Pool of pre-printed QR-code stickers, generated ahead of time in batches
-- and assigned one-per-washing-point later. seq is the source of truth for
-- the human-facing sticker number (formatted as "QW-0031" in Go, never
-- stored redundantly). token is the opaque, unguessable path segment of
-- the public scan URL — deliberately unrelated to seq so a scanner can't
-- enumerate codes by incrementing a number. A NULL washing_point_id means
-- "in the pool, not yet assigned"; Postgres treats NULLs as distinct for
-- a UNIQUE constraint, so any number of free codes can coexist.
CREATE TABLE qr_codes (
    id                          uuid PRIMARY KEY,
    seq                         bigserial NOT NULL UNIQUE,
    token                       varchar(64) NOT NULL UNIQUE,
    batch_label                 varchar(255) NOT NULL,
    status                      varchar(16) NOT NULL DEFAULT 'free' CHECK (status IN ('free', 'assigned', 'disabled')),
    washing_point_id            uuid REFERENCES washing_points (id) ON DELETE SET NULL,
    assigned_at                 timestamptz,
    disabled_at                 timestamptz,
    replacement_requested_at    timestamptz,
    created_at                  timestamptz NOT NULL DEFAULT now(),
    updated_at                  timestamptz NOT NULL DEFAULT now(),

    UNIQUE (washing_point_id)
);
