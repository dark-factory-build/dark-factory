# Worker Git write boundary investigation

The current worker capability is not an enforceable per-Change Git boundary.
`internal/provider/provider.go` grants a Codex worker the complete project
`.git` directory as writable, so an ordinary command can use `--git-dir` or
`GIT_WORK_TREE` to update `refs/heads/main`, another Change branch, or another
worktree's index. The same code gives Claude no filesystem sandbox at all, so
Claude has the same capability without relying on Git's normal working
directory.

A path-only Codex profile cannot be narrowed to the existing Change branch.
Git must create `refs/heads/factory/<12-hex>.lock` beside the ref, which
requires write access to the shared `refs/heads/factory` directory. Granting
that directory grants every Change branch. A grant to the ref path itself
does not cover its sibling lock, and object writes are necessarily shared by
content-addressed fan-out. The linked worktree's admin directory and the
Change worktree can be scoped, but the branch lock remains the unsplittable
part.

The local managed runtime also refuses an ad-hoc `sandbox-exec` reproduction
at `sandbox_apply`, so no claim is made about a live sandbox result here. The
source-level policy and Git lock layout are sufficient to establish the
boundary gap; the preserved operator checkout and retained Change were not
touched.

## Bounded alternatives

1. Give each worker a private Git admin/repository with read-only alternates
   for project objects, then have the daemon import the resulting commit and
   update the Change branch. This is the narrow enforceable design, but it is
   a lifecycle change rather than a provider-policy tweak.
2. Keep the shared Git directory but put a daemon-owned ref-update service in
   front of worker commits and deny direct Git metadata writes. This requires
   changing the worker commit workflow and cannot be implemented by a prompt,
   hook, or environment variable.
3. As an interim mitigation only, keep Codex's current sandbox and remove
   shared Git write access from Claude; this prevents neither the Codex
   `--git-dir` attack nor normal Claude shell mutation and must not be reported
   as protection.

No production-line code change is claimed because neither available launch
path can enforce the requested invariant without one of those larger
architectural changes.
