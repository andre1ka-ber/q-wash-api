CREATE TABLE services (
    id                uuid PRIMARY KEY,
    washing_point_id  uuid NOT NULL REFERENCES washing_points (id) ON DELETE RESTRICT,
    name              varchar(255) NOT NULL,
    description       text,
    duration_minutes  integer NOT NULL CHECK (duration_minutes > 0),
    picture_url       varchar(500),
    is_active         boolean NOT NULL DEFAULT true,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_services_washing_point_id ON services (washing_point_id);
