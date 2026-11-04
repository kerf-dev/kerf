# WO-22 Admin area: web

- **Phase / wave:** 4 / 1
- **Depends on:** WO-19 merged (develop against MSW mocks of the new endpoints; switch to the live hub when WO-20 lands)
- **Owns:** `web/`, `deploy/charts/web/`, `make/web.mk`
- **Decisions:** D-014, D-022, D-023, D-024

## Deliverables
- **Login and session:**
  - login page and session handling (`/v1/me`)
  - role-aware nav; `/admin` visible to operator and admin, with admin-only sections hidden from operators
  - CSRF header on writes
  - the nginx proxy forwards cookies for `/auth` and `/v1` (and drops the injected ui token for user sessions)
  - logout
- **Admin pages:** all eight from plan section 4.
  - Automation overview with pause/resume
  - Defaults form with inherited/default hints and impact preview
  - Clusters list/detail, token create/rotate/revoke shown once, register wizard with Helm snippet
  - Policies table (Hub/Git badges), editor with selector builder + live match list, effective-policy simulator, version history diff + revert, YAML export
  - Recommender profiles + Recompute now
  - Pricing discounts/overrides
  - Access: users and API tokens
  - Audit filter `config`
- **Write actions in existing pages:**
  - Workload "Automation" card: modes/strategy/profile override, Exclude, Clear override, Approve once
  - Opportunities bulk select → Set mode / Exclude, with a confirmation showing count + savings impact
  - type-the-cluster-name confirmation for `prod` clusters
- **UX:** optimistic-locking conflicts (412) are shown with a reload option; every form has validation messages from the shared rules; there are empty, loading and error states; light/dark; tablet width.

## Definition of done
All pages work against MSW mocks (contract-validated, as today) and against the live hub. Playwright covers: login as each role, change defaults with preview, create policy, pause cluster, approve once, revert. The full WO-14 verification passes.

## Verification
```bash
cd web && npm ci && npm run lint && npm run typecheck && npm run test -- --run && npm run build && npm run e2e
```
