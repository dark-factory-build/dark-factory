# `scripts/` audit

Audited against origin/main `6915d8328b0a8b8c32d359a9fcd3a7da08ac1349`.
Every root file in `scripts/` is listed below. `DEAD` means no repository
consumer of the kinds named in the task; no file met that definition. `DEV`
is repository, CI, release, fixture, or verification tooling.

| Script | Class | Evidence |
| --- | --- | --- |
| `bootstrap-maintainer-v2.sh` | DEV | control-plane bootstrap and CI fixtures |
| `check-toolchain-pins.sh` | DEV | CI toolchain gate |
| `cloudflare-env-clean.sh` | DEV | Cloudflare admin boundary and test |
| `dark-factory-browser-mcp.py` | DEV | documented provider/browser helper and tests |
| `github-repo-settings.sh` | DEV | repository configuration script |
| `github-step-summary.sh` | DEV | workflow summary helper |
| `go-check.sh` | DEV | source and UI gate |
| `go-ci-owned.sh` | DEV | process-sensitive CI gate |
| `go-e2e-tools.sh` | DEV | E2E toolchain gate |
| `go-e2e.sh` | DEV | Darwin E2E runner |
| `go-gate-environment.sh` | DEV | gate process supervisor |
| `go-service-e2e.sh` | DEV | disposable launchd E2E gate |
| `import-issues.sh` | DEV | repository issue import utility and test |
| `local-ci-environment.sh` | DEV | local CI environment boundary |
| `local-ci.sh` | DEV | repository gate orchestrator |
| `new-worktree.sh` | DEV | contributor worktree helper |
| `package-release.sh` | DEV | release artifact packaging |
| `prepare-release-source.sh` | DEV | release source selection and workflow |
| `publication-parents.sh` | DEV | publication ancestry gate |
| `publish-release.sh` | DEV | GitHub release publisher used by workflow |
| `render-homebrew-formula.sh` | DEV | release formula generation |
| `test-bootstrap-maintainer-v2.sh` | DEV | bootstrap fixture |
| `test-cloudflare-env.sh` | DEV | Cloudflare boundary fixture |
| `test-factory-browser-live.py` | DEV | documented browser configuration fixture |
| `test-factory-browser.py` | DEV | browser helper fixture |
| `test-github-step-summary.sh` | DEV | workflow summary fixture |
| `test-go-e2e-tools.sh` | DEV | E2E tool fixture |
| `test-go-gates.sh` | DEV | CI gate fault-injection fixture |
| `test-local-ci-environment.sh` | DEV | environment-boundary fixture |
| `test-new-worktree.sh` | DEV | worktree fixture |
| `test-package-release.sh` | DEV | package fixture |
| `test-prepare-release-source.sh` | DEV | release-source fixture |
| `test-publication-parents.sh` | DEV | ancestry fixture |
| `test-publish-release.sh` | DEV | release-publisher fixture |
| `test-repository-settings.sh` | DEV | repository-settings fixture |
| `test-verify-adversarial-review.sh` | DEV | review-policy fixture |
| `verify-adversarial-review.sh` | DEV | exact-head review gate |
| `with-cloudflare-env.sh` | DEV | Cloudflare credential boundary |
| `with-local-ci-lease.sh` | DEV | local gate lease wrapper |

The release lane (`factory-autonomy.py`, `factory-release.py`,
`factory-delivery.py` and their helpers) is deleted: factoryd releases itself.
The site deploy scripts are deleted: the public site deploys from
dark-factory-site `main` via Vercel's Git integration.
