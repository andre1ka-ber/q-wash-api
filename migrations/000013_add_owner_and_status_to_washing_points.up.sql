ALTER TABLE washing_points
    ADD COLUMN owner_id     uuid REFERENCES owners (id) ON DELETE SET NULL,
    ADD COLUMN status       varchar(16) NOT NULL DEFAULT 'active'
                            CHECK (status IN ('active', 'paused', 'pending_review')),
    ADD COLUMN description  text,
    ADD COLUMN amenities    text[];

-- Backfill status from the column it replaces.
UPDATE washing_points SET status = CASE WHEN is_active THEN 'active' ELSE 'paused' END;

ALTER TABLE washing_points DROP COLUMN is_active;

CREATE INDEX idx_washing_points_owner_id ON washing_points (owner_id);
