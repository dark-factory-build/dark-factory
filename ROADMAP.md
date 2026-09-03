# Dark Factory roadmap

Dark Factory is a macOS-local runtime with a hosted browser console, a
durable daemon-owned work queue, and supervised provider attempts.

## Next capabilities

- Prove the implemented Claude Code and Codex launch paths with separately
  approved real-provider attempts before including them in a release.
- Add Linux runtime support while preserving the local-daemon and browser
  boundaries.
- Complete release-grade install, service replacement, rollback, and recovery
  proof for the managed macOS service.
- Revisit external intake and repository publication only as separately
  reviewed, provider-neutral capabilities.

## Boundaries

- `factoryd` owns durable state, admission, provider processes, Changes, and
  finalization.
- `factoryctl` and the browser use the same daemon operations; neither owns
  policy or lifecycle.
- Public state stays bounded and excludes credentials, prompts, raw provider
  output, source, and private deliberation.
- The runtime has no in-runtime updater or automatic repository publication.

GitHub issues and pull requests are the execution record. Detailed engineering
contracts and proof records remain in the development documentation.
