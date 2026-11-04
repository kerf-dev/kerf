# WO-24 Admin area: end-to-end

- **Phase / wave:** 4 / 2
- **Depends on:** WO-20, WO-21, WO-22 merged and deployed
- **Owns:** `test/e2e/` (new admin scenarios), `make/e2e.mk`
- **Decisions:** D-022, D-023, D-024

## Deliverables
These scenarios go in the existing suite; they drive the hub API as a logged-in user with a session cookie and CSRF token:
1. A namespace policy created via the admin API appears as a hub-labelled CR on the spoke and gets applied. The `config_changed` audit has the actor.
2. Pause blocks new applies (`blocked/Paused`), rollbacks still happen, and resume restores.
3. Approve once applies exactly one recommendation; the next one is not applied.
4. Revert restores the previous policy version and the CR follows.
5. A Git-managed CR is never modified or deleted, and is listed read-only in the inventory.
6. Roles: a viewer gets 403 on writes; an operator can pause but cannot edit defaults.
7. Optional: one Playwright smoke against the deployed UI (login → pause → resume).

## Definition of done
`make e2e && make e2e` passes with the new scenarios, in under 30 minutes.
