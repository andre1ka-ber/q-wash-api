UPDATE connection_requests SET contact_name = business_name WHERE contact_name IS NULL;
UPDATE connection_requests SET address = '' WHERE address IS NULL;
UPDATE connection_requests SET boxes_count = 1 WHERE boxes_count IS NULL;
ALTER TABLE connection_requests ALTER COLUMN contact_name SET NOT NULL;
ALTER TABLE connection_requests ALTER COLUMN address SET NOT NULL;
ALTER TABLE connection_requests ALTER COLUMN boxes_count SET NOT NULL;
