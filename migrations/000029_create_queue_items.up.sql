-- A booking can include several services (cabinet/customer multi-select).
-- queue.service_id / queue.price_option_id stay as a mirror of the first
-- item (position 0) so clients and queries that only know one service keep
-- working; they are dropped by a later migration once old app builds are gone.
CREATE TABLE queue_items (
    id               uuid PRIMARY KEY,
    queue_id         uuid NOT NULL REFERENCES queue (id) ON DELETE CASCADE,
    service_id       uuid NOT NULL REFERENCES services (id) ON DELETE RESTRICT,
    price_option_id  uuid NOT NULL REFERENCES service_price_options (id) ON DELETE RESTRICT,
    position         integer NOT NULL CHECK (position >= 0),
    created_at       timestamptz NOT NULL DEFAULT now(),

    UNIQUE (queue_id, position),
    UNIQUE (queue_id, service_id)
);

CREATE INDEX idx_queue_items_service_id ON queue_items (service_id);
CREATE INDEX idx_queue_items_price_option_id ON queue_items (price_option_id);

-- Backfill: every existing booking becomes a one-item booking. uuid_generate_v7
-- is not available, so ids reuse the queue id (unique per booking at position 0).
INSERT INTO queue_items (id, queue_id, service_id, price_option_id, position, created_at)
SELECT id, id, service_id, price_option_id, 0, created_at FROM queue;
