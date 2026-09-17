# Onboarding and Settings refinement handoff

This source handoff records the owner-refinement pass for task
`cda2c991491acd9e348ffdacede702ac`. It changes only the existing web UI
composition; pairing, account, terminal, task, and browser authority remain
owned by their existing client/controller surfaces.

## State → primary action → optional disclosure

| Observed state | Primary action | Optional disclosure |
| --- | --- | --- |
| Local Settings open | Pair a phone or link a provider login | Run limits, only when the operator chooses them |
| No discovered logins | Refresh accounts | None; signing in remains the provider CLI’s job |
| Discovered unlinked login | Choose a label and link it | Provider identity (email/organisation) when available |
| Linked login in use | Rename or leave it linked | Unlink is disabled until agents are reassigned |
| Remote device has no factory | Paste or open a one-shot pairing link | None; install advice appears only if enabling alerts requires it |

Non-actionable factory metrics, local diagnostic address/revision, account
home paths, default model details, and setup recipes were removed from the
common flow. Credentials and login history remain with the provider; unlinking
does not delete them. Pairing and revoke confirmations remain state-dependent.

## Evidence and limits

The retained host handoff records the actual production baseline at 1440×900
and 390×844 (scale 1), including unreachable/connect, permission/install,
service/security, pairing, and phone branches. Those captures are host-owned
and are not present in this daemon Change, so this worker makes no connected,
hosted, or production screenshot claim. The source baseline is
`5b32c0a6c2a5c3cbf63538c7342b6467d273b237`; current main provenance supplied by
the host is `9449faab` and the served site baseline is separately identified
as `00aff040dc10c47e25244670b6cbc1368d43e7b5` with runtime package lock
`3fbf6742`.

Focused render tests cover the local settings and remote empty states. Full
web checks and exact-head independent rendered-flow review remain handoff and
host gates; this Change does not publish, merge, deploy, or claim delivery.
