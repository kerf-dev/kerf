-- GPU visibility: utilisation, memory, cost and suggestions of a workload's GPUs
-- (WorkloadRecommendation.gpu, API shape). NULL for workloads without GPU requests.

-- +goose Up
ALTER TABLE recommendations ADD COLUMN IF NOT EXISTS gpu jsonb;

-- +goose Down
ALTER TABLE recommendations DROP COLUMN IF EXISTS gpu;
