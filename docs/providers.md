# Provider contract

Dark Factory currently supports one provider: `shell`. The provider boundary
is the Go function

```go
func Build(Request) (Launch, error)
```

`Build` returns launch facts only. The daemon and runner own the Change working
directory, task input, PTY, process group, output, wait, and cleanup. A
provider cannot select a source path or lifecycle result, and there is no
provider registry, plugin, fallback, or provider-owned supervision framework.

## Supported provider

The shell provider is fixed to `/bin/sh`. Its launch uses the runner-owned task
descriptor path:

```go
executable: "/bin/sh"
argv:       []string{"/bin/sh", runner.ProviderTaskPath}
```

The task bytes are bounded, valid UTF-8 without NUL, and written to the
descriptor after the launch gates pass. The PTY is reserved for later
interactive terminal traffic.

## Unavailable providers

`claude_code` and `codex` are recognized provider values but are unavailable.
They fail closed after admission; no launch template is promised for them.
They can become supported only after an exact integration and causal OS-effect
review.

Provider changes must preserve admission-time selection, daemon-owned process
lifecycle, exact task delivery, and deterministic failure when a required
launch fact is unavailable.
