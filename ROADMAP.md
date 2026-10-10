# Dark Factory roadmap

Dark Factory is a macOS-only local runtime with a hosted browser console, a
durable daemon-owned work queue, and supervised provider attempts.

## Next capabilities

- Prove a Claude Code overseer with a live run; Codex workers and overseers
  and a Claude Code worker are already proven with real work.
- Complete release-grade install, service replacement, rollback, and recovery
  proof for the managed macOS service.
- Prove each configured unattended project end to end with its selected
  GitHub backlog, worker limits, review policy, and deployment target.

## Boundaries

- `factoryd` owns durable state, admission, provider processes, Changes, and
  finalization.
- `factoryctl` and the browser use the same daemon operations; neither owns
  policy or lifecycle.
- Public state stays bounded and excludes credentials, prompts, raw provider
  output, source, and private deliberation.
- The console installs no updates, and repository publication happens only
  through a configured GitHub connection.

GitHub issues and pull requests are the execution record. Detailed engineering
contracts and proof records remain in the development documentation.
