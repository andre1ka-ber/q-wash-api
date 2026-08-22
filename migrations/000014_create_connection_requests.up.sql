-- Backs the admin app's "connection requests" onboarding queue — a new
-- point's owner applying to join the network. Approving one creates an
-- Owner + WashingPoint (see docs/PLAN_WEB_APPS.md, not yet wired to any
-- endpoint in this migration).
CREATE TABLE connection_requests (
    id             uuid PRIMARY KEY,
    business_name  varchar(255) NOT NULL,
    contact_name   varchar(255) NOT NULL,
    contact_phone  varchar(32) NOT NULL,
    address        varchar(500) NOT NULL,
    boxes_count    integer NOT NULL CHECK (boxes_count > 0),
    note           text,
    status         varchar(16) NOT NULL DEFAULT 'new'
                   CHECK (status IN ('new', 'approved', 'rejected')),
    reviewed_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    reviewed_at    timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_connection_requests_status ON connection_requests (status);
