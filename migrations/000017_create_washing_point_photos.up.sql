CREATE TABLE washing_point_photos (
    id                uuid PRIMARY KEY,
    washing_point_id  uuid NOT NULL REFERENCES washing_points (id) ON DELETE CASCADE,
    url               varchar(500) NOT NULL,
    is_cover          boolean NOT NULL DEFAULT false,
    sort_order        integer NOT NULL DEFAULT 0,
    created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_washing_point_photos_washing_point_id ON washing_point_photos (washing_point_id);
