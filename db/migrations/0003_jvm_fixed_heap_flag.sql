-- Risk flag jvm-fixed-heap (JVM with a fixed -Xmx; memory audit-only).

-- +goose Up
ALTER TABLE recommendations DROP CONSTRAINT recommendations_risk_flags_check;
ALTER TABLE recommendations ADD CONSTRAINT recommendations_risk_flags_check
    CHECK (risk_flags <@ ARRAY['throttled', 'oom-killed', 'restarting', 'hpa-coupled',
                               'sidecar', 'short-lived', 'low-confidence', 'jvm-fixed-heap']::text[]);

-- +goose Down
UPDATE recommendations SET risk_flags = array_remove(risk_flags, 'jvm-fixed-heap');
ALTER TABLE recommendations DROP CONSTRAINT recommendations_risk_flags_check;
ALTER TABLE recommendations ADD CONSTRAINT recommendations_risk_flags_check
    CHECK (risk_flags <@ ARRAY['throttled', 'oom-killed', 'restarting', 'hpa-coupled',
                               'sidecar', 'short-lived', 'low-confidence']::text[]);
