-- One row per recorded hit of a code's public scan URL — the raw event
-- log behind the pool's scan-count stats and the booking-attribution
-- heuristic (see internal/qrcode.Manager.Stats and
-- internal/queue.Repository.CountCreatedNearTimes).
CREATE TABLE qr_scans (
    id            uuid PRIMARY KEY,
    qr_code_id    uuid NOT NULL REFERENCES qr_codes (id) ON DELETE CASCADE,
    scanned_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_qr_scans_qr_code_id_scanned_at ON qr_scans (qr_code_id, scanned_at);
