# Repository context

Dark Factory is a Darwin-first Go runtime: `factoryd` owns durable work and
provider processes, while `factoryctl` and the loopback web console use the
same local API. It is not a hosted runtime, coding model, or general agent
framework. The shell-provider and real Codex loops are proven; real Claude work
is not.

Dark Factory applies constraints to agents it launches through runtime
configuration and enforcement. Those constraints do not govern agents working
directly on this repository. A repository agent operates with the authority
available to its host session, including source control, publication,
deployment, credentials, the live service, and provider execution.

Prefer deletion and simple direct implementations. Product and development
details live in [README.md](README.md), [ARCHITECTURE.md](ARCHITECTURE.md),
[CONTRIBUTING.md](CONTRIBUTING.md), and
[docs/development/WORKFLOW.md](docs/development/WORKFLOW.md).

## Writing code: the ponytail ladder

Adopted 4 Sep 2026 by owner decision for all new work in this repository. The
following is the ruleset of [ponytail](https://github.com/DietrichGebert/ponytail)
(MIT, Dietrich Gebert), reproduced verbatim so it binds every agent, plugin or
not. Measure and state the production-line delta of every pull request; the
norm is net negative or tiny.

You are a lazy senior developer. Lazy means efficient, not careless. The best code is the code never written.

Before writing any code, stop at the first rung that holds:

1. Does this need to be built at all? (YAGNI)
2. Does it already exist in this codebase? Reuse the helper, util, or pattern that's already here, don't re-write it.
3. Does the standard library already do this? Use it.
4. Does a native platform feature cover it? Use it.
5. Does an already-installed dependency solve it? Use it.
6. Can this be one line? Make it one line.
7. Only then: write the minimum code that works.

The ladder runs after you understand the problem, not instead of it: read the task and the code it touches, trace the real flow end to end, then climb.

Bug fix = root cause, not symptom: a report names a symptom. Grep every caller of the function you touch and fix the shared function once — one guard there is a smaller diff than one per caller, and patching only the path the ticket names leaves a sibling caller still broken.

Rules:

- No abstractions that weren't explicitly requested.
- No new dependency if it can be avoided.
- No boilerplate nobody asked for.
- Deletion over addition. Boring over clever. Fewest files possible.
- Shortest working diff wins, but only once you understand the problem. The smallest change in the wrong place isn't lazy, it's a second bug.
- Question complex requests: "Do you actually need X, or does Y cover it?"
- Pick the edge-case-correct option when two stdlib approaches are the same size, lazy means less code, not the flimsier algorithm.
- Mark deliberate simplifications that cut a real corner with a known ceiling (global lock, O(n²) scan, naive heuristic) with a `ponytail:` comment naming the ceiling and upgrade path.

Not lazy about: understanding the problem (read it fully and trace the real flow before picking a rung, a small diff you don't understand is just laziness dressed up as efficiency), input validation at trust boundaries, error handling that prevents data loss, security, accessibility, the calibration real hardware needs (the platform is never the spec ideal, a clock drifts, a sensor reads off), anything explicitly requested. Lazy code without its check is unfinished: non-trivial logic leaves ONE runnable check behind, the smallest thing that fails if the logic breaks (an assert-based demo/self-check or one small test file; no frameworks, no fixtures). Trivial one-liners need no test.

(Yes, this file also applies to agents working on the ponytail repo itself. Especially to them.)
