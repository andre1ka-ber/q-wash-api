CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE queue (
    id                  uuid PRIMARY KEY,
    status              varchar(16) NOT NULL DEFAULT 'queue'
                        CHECK (status IN ('queue', 'waiting', 'washing', 'ready', 'canceled')),
    user_id             uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    car_id              uuid NOT NULL REFERENCES cars (id) ON DELETE RESTRICT,
    service_id          uuid NOT NULL REFERENCES services (id) ON DELETE RESTRICT,
    price_option_id     uuid NOT NULL REFERENCES service_price_options (id) ON DELETE RESTRICT,
    washing_point_id    uuid NOT NULL REFERENCES washing_points (id) ON DELETE RESTRICT,
    box_number          integer NOT NULL CHECK (box_number > 0),
    scheduled_start_at  timestamptz NOT NULL,
    scheduled_end_at    timestamptz NOT NULL,
    notes               text,
    canceled_at         timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    CHECK (scheduled_end_at > scheduled_start_at)
);

CREATE INDEX idx_queue_user_id ON queue (user_id);
CREATE INDEX idx_queue_washing_point_id_scheduled_start_at ON queue (washing_point_id, scheduled_start_at);

-- DB-level guarantee (in addition to app-level checks) that the same box at the
-- same washing point never has two overlapping, non-canceled bookings.
ALTER TABLE queue ADD CONSTRAINT queue_no_overlapping_box_bookings
    EXCLUDE USING gist (
        washing_point_id WITH =,
        box_number WITH =,
        tstzrange(scheduled_start_at, scheduled_end_at) WITH &&
    ) WHERE (status <> 'canceled');
