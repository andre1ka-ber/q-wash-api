-- A business that owns one or more washing points. Kept separate from
-- users: an owner doesn't necessarily have their own login (staff log in
-- per-point instead), but admin needs somewhere to hang contact info and
-- group points by owner regardless. See docs/PLAN_WEB_APPS.md.
CREATE TABLE owners (
    id             uuid PRIMARY KEY,
    name           varchar(255) NOT NULL,
    contact_name   varchar(255),
    contact_phone  varchar(32),
    contact_email  varchar(255),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
