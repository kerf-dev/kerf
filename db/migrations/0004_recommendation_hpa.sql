-- WO-11: HPA coordination of hpa-coupled workloads (WorkloadRecommendation.hpa, API shape).
-- Number pending the orchestrator's assignment (0003 is WO-12's).

-- +goose Up
ALTER TABLE recommendations ADD COLUMN IF NOT EXISTS hpa jsonb;

-- +goose Down
ALTER TABLE recommendations DROP COLUMN IF EXISTS hpa;
