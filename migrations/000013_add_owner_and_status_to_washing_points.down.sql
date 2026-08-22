DROP INDEX IF EXISTS idx_washing_points_owner_id;

ALTER TABLE washing_points ADD COLUMN is_active boolean NOT NULL DEFAULT true;
UPDATE washing_points SET is_active = (status = 'active');

ALTER TABLE washing_points
    DROP COLUMN amenities,
    DROP COLUMN description,
    DROP COLUMN status,
    DROP COLUMN owner_id;
