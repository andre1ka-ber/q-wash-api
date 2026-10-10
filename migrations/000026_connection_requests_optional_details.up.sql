-- Public landing-page applications only collect a name and a phone; the rest
-- (contact person, address, box count) is filled in later by an admin before
-- approval. The existing CHECK (boxes_count > 0) still holds for non-NULL values.
ALTER TABLE connection_requests ALTER COLUMN contact_name DROP NOT NULL;
ALTER TABLE connection_requests ALTER COLUMN address DROP NOT NULL;
ALTER TABLE connection_requests ALTER COLUMN boxes_count DROP NOT NULL;
