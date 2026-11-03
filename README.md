# resource-optimizer

Kubernetes rightsizing for EKS: measures container CPU and memory usage from Prometheus/VictoriaMetrics,
recommends requests and limits, estimates the savings, and applies changes automatically for workloads
whose mode is `auto-patch` (in-place pod resize or rollout, with guardrails and automatic rollback).

Status: design and planning. No components are built yet.

- [roadmap.md](roadmap.md) – phases and work-orders
- [DECISIONS.md](DECISIONS.md) – architecture decisions
- [docs/work-orders/](docs/work-orders/) – one file per work package
