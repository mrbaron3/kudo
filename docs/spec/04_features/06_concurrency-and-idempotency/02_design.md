# 4.6. Dependency・並行実行・冪等性 詳細設計

[機能仕様](01_spec.md)を、dependency graph、database constraint、scoped lease、compare-and-mutate として
実現する。

## Scheduling model

Task Issue を graph node、`dependsOn` を readiness edge とする。Controller は dependency の live completion
evidence をもとに ready / blocked を判定し、parent relationship や Issue 番号を ordering input にしない。

ready Run は全体 lock を取得せず、configured capacity の範囲で個別に schedule する。capacity 待ちは
dependency blocked と別の reason を持ち、dependency graph の意味を変えない。

## 排他スコープ

| Scope | 保証 |
| --- | --- |
| IssueRef | active writer-capable Run は最大一つ |
| Run | state-advancing Operation は同時に一つ |
| Operation | 一つの有効 lease holder だけが Result を確定できる |
| Worktree | lease を持つ Issue Worker 一つだけが書き込める |
| Review input | review 中の exact head と artifact は immutable |

PostgreSQL constraint と compare-and-swap transition を最終的な排他の正本とする。process-local mutex は
最適化には使えても correctness の根拠にしない。

## Stable identity

- inbox: GitHub delivery ID または poll trigger identity
- reconciliation: repository と Issue number
- Run: IssueRef と active / supersede transition
- Operation: Run、kind、semantic input digest
- Attempt: Operation ID と monotonic attempt number
- GitHub mutation: target、operation kind、expected state、desired state
- projection: durable transition と projection kind

同じ identity に異なる semantic input が来た場合は duplicate として捨てず、conflict として扱う。

## Compare-and-mutate

Issue Worker は branch push、draft PR ensure、body update、draft 解除の直前に live state を取得する。
expected state と一致した場合だけ mutation し、response または再取得結果を durable Result に記録する。
timeout で結果が不明な場合は、同じ desired state が既に成立しているか確認してから再試行する。

Controller の label / comment projection は transactional outbox と stable projection identity で再送する。

## 検証方針

- concurrent claim、lease acquisition、Run transition を複数 goroutine と deterministic store fake で検証する。
- webhook / polling の順序と重複を組み合わせ、Run が一つに収束することを検証する。
- mutation 前後の timeout、外部 push、close / merge を fake GitHub で検証する。
- 複数 Issue が独立 workspace で同時進行でき、global lock によって直列化されないことを検証する。

## 参照

- [End-to-end workflow](../../05_design/02_workflow.md) — Idempotency and recovery
- [Architecture](../../05_design/01_architecture.md) — Durable model、Scheduling and concurrency
- [Worker Operation Protocol](../../05_design/contracts/operation-protocol-v1alpha1.md) — Attempt and lease
- [Runtime platform](../../05_design/03_runtime-platform.md) — PostgreSQL queue と workspace isolation
