# resource-optimizer

Kubernetes rightsizing for EKS: measures container CPU and memory usage from Prometheus/VictoriaMetrics,
recommends requests and limits, estimates the savings, and applies changes automatically for workloads
whose mode is `auto-patch` (in-place pod resize or rollout, with guardrails and automatic rollback).

**Current state and how to open the running app: [docs/STATUS.md](docs/STATUS.md).**

Status: phases 1–3 and the phase-4 admin area + cluster onboarding delivered and running on the local k3d fleet; WO-15–18 not started.

**Install on Amazon EKS: [docs/install/eks.md](docs/install/eks.md)** (one chart, `deploy/charts/resource-optimizer`).

- [roadmap.md](roadmap.md) – phases and work-orders
- [DECISIONS.md](DECISIONS.md) – architecture decisions
- [docs/work-orders/](docs/work-orders/) – one file per work package
