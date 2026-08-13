# Repository Instructions

## Purpose

Kudo is a greenfield Go implementation of a lightweight TDD issue-to-PR runtime. Servo is reference material, not a source-code dependency or compatibility target.

## 開発手順

実装は TDD で進める。まず期待する振る舞いを示す失敗するテストを書き、テストを通す最小限の実装を行い、その後テストが通る状態を保ちながらリファクタリングする。

## Required checks

Run the fastest relevant checks first. Before handing off a change, run:

```sh
mise run check
```

Use deterministic unit tests with fakes at GitHub, process, clock, filesystem, model-provider, and telemetry boundaries. Live integrations must be opt-in and must not be the only proof of behavior.

## Architecture boundaries

- Keep GitHub polling, webhook, and API code as thin adapters around run-once application operations.
- Only the Issue Worker may mutate the implementation worktree, branch, or pull request.
- The Review Worker is read-only and returns a versioned verdict for immutable inputs.
- The Controller validates transitions and routes artifacts; it does not replace review judgment.
- Treat the Task Issue as the execution-context root. Parent Issues are hierarchy, dependencies are readiness gates, and neither contributes prose or session context unless the Task explicitly declares it as authority.
- Implementation and review may run in the same OS process, but must not share mutable worktrees, provider sessions, conversational memory, or application-private state.
- Start a fresh provider session for every model-bearing worker operation and pass only explicit Issue context, versioned results, and immutable artifact references across operations.
- Allow dependency-free ready Issues to run concurrently in isolated Runs. Scope claims and execution leases to an Issue or Run; do not serialize the repository with a global lock.
- Prefer one Go module and one deployable binary until measured constraints justify another boundary.
- Prefer the Go standard library. Add dependencies only at explicit boundaries and explain why.
- Use Docker Compose as the canonical runtime. Run the same binary as separate Controller, Issue Worker, Review Worker, and migration containers, with PostgreSQL as authoritative workflow state and queue.
- Do not mount the Docker socket or the Issue Worker workspace into Controller or Review Worker containers. Artifact sharing must remain content-addressed and immutable.

## Contract discipline

- Treat files under `docs/contracts/` as protocol baselines. Change documentation, parsing, and tests together.
- Reject missing or ambiguous required input; do not infer contract fields from conversational context.
- Keep transport failures separate from review verdicts.
- A changed commit or artifact digest makes the previous review result stale.

## Deferred work

Do not add pass@k, multi-candidate evaluation, scoring dashboards, or an evaluation harness without a separate design decision. Runtime telemetry may be emitted, but external observability storage is not authoritative workflow state.

## Communication

Use Japanese for repository-facing prose, Issues, and pull requests unless an existing technical contract requires an English identifier.
