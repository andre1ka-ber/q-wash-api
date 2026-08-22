CREATE TABLE service_price_options (
    id          uuid PRIMARY KEY,
    service_id  uuid NOT NULL REFERENCES services (id) ON DELETE CASCADE,
    name        varchar(255) NOT NULL,
    price_cents integer NOT NULL CHECK (price_cents >= 0),
    is_default  boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_service_price_options_service_id ON service_price_options (service_id);

-- at most one default price option per service; app logic ensures at least one exists
CREATE UNIQUE INDEX idx_service_price_options_one_default
    ON service_price_options (service_id)
    WHERE is_default;
