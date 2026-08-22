-- Lets a box be paused mid-wash without touching the forward-only status
-- machine (queue -> waiting -> washing -> ready) — only meaningful while
-- status = 'washing'. Not wired to any endpoint yet; see
-- docs/PLAN_WEB_APPS.md phase 7 (worker app pause/resume).
ALTER TABLE queue ADD COLUMN paused_at timestamptz;
