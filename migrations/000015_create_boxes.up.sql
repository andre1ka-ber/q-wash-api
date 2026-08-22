-- Individual washing bays within a point. washing_points.boxes_count stays
-- the capacity number availability math uses; a Box row is metadata + an
-- open/closed flag layered on top of one of those numbered slots.
CREATE TABLE boxes (
    id                uuid PRIMARY KEY,
    washing_point_id  uuid NOT NULL REFERENCES washing_points (id) ON DELETE CASCADE,
    number            integer NOT NULL CHECK (number > 0),
    label             varchar(255),
    is_open           boolean NOT NULL DEFAULT true,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),

    UNIQUE (washing_point_id, number)
);

-- Backfill one Box row per existing point per box number, so the cabinet/
-- worker apps have something to list immediately once their endpoints
-- land, instead of every existing point starting with zero boxes.
INSERT INTO boxes (id, washing_point_id, number)
SELECT gen_random_uuid(), wp.id, n.number
FROM washing_points wp
CROSS JOIN LATERAL generate_series(1, wp.boxes_count) AS n(number);
