-- HPA coordination of hpa-coupled workloads (WorkloadRecommendation.hpa, API shape).

-- +goose Up
ALTER TABLE recommendations ADD COLUMN IF NOT EXISTS hpa jsonb;

-- +goose Down
ALTER TABLE recommendations DROP COLUMN IF EXISTS hpa;
