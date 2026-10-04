# `scripts/` audit

Audited against origin/main `6915d8328b0a8b8c32d359a9fcd3a7da08ac1349`.
Every root file in `scripts/` is listed below. `DEAD` means no repository
consumer of the kinds named in the task; no file met that definition. `DEV`
is repository, CI, release, fixture, or verification tooling. `PRODUCT` is a
host-owned controller or hosted-site path that an external installation needs
but that currently depends on the owner machine, legacy factory home,
owner-authenticated `gh`/Vercel, `release_configs`, or the hard-coded hosted
repository/domain.

| Script | Class | Evidence |
| --- | --- | --- |
| `bootstrap-maintainer-v2.sh` | DEV | control-plane bootstrap and CI fixtures |
| `check-toolchain-pins.sh` | DEV | CI toolchain gate |
| `cloudflare-env-clean.sh` | DEV | Cloudflare admin boundary and test |
| `cold-review.sh` | DEV | independent review gate |
| `dark-factory-browser-mcp.py` | DEV | documented provider/browser helper and tests |
| `deploy-runtime.py` | DEV | release-lane shim over `factoryctl release` |
| `deploy-site.sh` | PRODUCT | owner Vercel project and `app.darkfactory.build` deployment; issue #971 |
| `factory-autonomy.py` | PRODUCT | legacy launchd controller; `factory_home`, journals, and `release_configs`; issue #972 |
| `factory-delivery.py` | PRODUCT | operator-owned deployment receipts enqueue an overseer follow-up through `factoryctl`; issue #973 |
| `factory-publication.py` | DEV | pure publication-footer contract library |
| `factory-release.example.json` | DEAD | no workflow, documentation, test, service-packaging path, or script references it |
| `factory-release.py` | PRODUCT | release controller using operator `release_configs`, `gh`, and host hooks; issue #977 |
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
| `local-ci-lease.sh` | DEV | local heavy-gate lease |
| `local-ci.sh` | DEV | repository gate orchestrator |
| `new-worktree.sh` | DEV | contributor worktree helper |
| `package-release.sh` | DEV | release artifact packaging |
| `prepare-release-source.sh` | DEV | release source selection and workflow |
| `publication-parents.sh` | DEV | publication ancestry gate |
| `publish-release.sh` | DEV | GitHub release publisher used by workflow |
| `render-homebrew-formula.sh` | DEV | release formula generation |
| `test-bootstrap-maintainer-v2.sh` | DEV | bootstrap fixture |
| `test-cloudflare-env.sh` | DEV | Cloudflare boundary fixture |
| `test-cold-review.sh` | DEV | cold-review fixture |
| `test-deploy-site.sh` | DEV | deployment fixture |
| `test-factory-autonomy.py` | DEV | controller unit/integration fixture |
| `test-factory-browser-live.py` | DEV | documented browser configuration fixture |
| `test-factory-browser.py` | DEV | browser helper fixture |
| `test-factory-delivery.py` | DEV | delivery unit fixture |
| `test-factory-release.py` | DEV | release controller fixture |
| `test-github-step-summary.sh` | DEV | workflow summary fixture |
| `test-go-e2e-tools.sh` | DEV | E2E tool fixture |
| `test-go-gates.sh` | DEV | CI gate fault-injection fixture |
| `test-local-ci-environment.sh` | DEV | environment-boundary fixture |
| `test-local-ci-lease-mutations.sh` | DEV | lease mutation fixture |
| `test-local-ci-lease.sh` | DEV | lease fixture |
| `test-new-worktree.sh` | DEV | worktree fixture |
| `test-package-release.sh` | DEV | package fixture |
| `test-prepare-release-source.sh` | DEV | release-source fixture |
| `test-publication-parents.sh` | DEV | ancestry fixture |
| `test-publish-release.sh` | DEV | release-publisher fixture |
| `test-repository-settings.sh` | DEV | repository-settings fixture |
| `test-verification-profile.mjs` | DEV | browser profile unit fixture |
| `test-verify-adversarial-review.sh` | DEV | review-policy fixture |
| `test-verify-live-runtime.py` | DEV | runtime verifier unit fixture |
| `verification-profile.mjs` | DEV | browser verification support library |
| `verify-adversarial-review.sh` | DEV | exact-head review gate |
| `verify-live-browser.mjs` | DEV | documented hosted-console smoke verifier |
| `verify-live-runtime.py` | DEV | parameterized installed-runtime verifier |
| `verify-live-site.py` | PRODUCT | owner Vercel/browser environment and hosted-domain verifier; issue #979 |
| `with-cloudflare-env.sh` | DEV | Cloudflare credential boundary |
| `with-local-ci-lease.sh` | DEV | local gate lease wrapper |

The DEAD set contains only `factory-release.example.json`; it had no
repository consumer and is deleted by this change. PRODUCT follow-ups are
tracked as one issue per script; each issue
names the daemon or `factoryctl` home as the destination and the repository
copy/legacy fixture set to delete after cutover.
