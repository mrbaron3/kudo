# 4.6. Dependency・並行実行・冪等性 詳細設計

[機能仕様](01_spec.md)を、dependency graph、GitHub CAS primitive、compare-and-mutate として実現する。

## Scheduling model

Task Issue を graph node、`dependsOn` を readiness edge とする。Controller は dependency の live completion
evidence をもとに ready / blocked を判定し、parent relationship や Issue 番号を ordering input にしない。

ready Run は全体 lock を取得せず、configured capacity の範囲で個別に schedule する。capacity 待ちは
dependency blocked と別の reason を持ち、dependency graph の意味を変えない。

## 排他スコープ

| Scope | 保証 | 機構 |
| --- | --- | --- |
| IssueRef | active Run は最大一つ | branch `kudo/issue-<n>` の ref create の atomicity |
| Run | state-advancing Operation は同時に一つ | in-process 排他（単一 instance 前提） |
| Branch mutation | 期待 head からの変更だけが成立する | compare-and-push |
| Merge | 期待 head だけが merge される | SHA 指定 merge |
| Worktree | その Run を実行する Issue Worker 一つだけが書き込める | process 内の所有権 |
| Review input | review 中の exact head と payload は immutable | commit SHA と digest 照合 |

破壊を防ぐ最終的な排他の正本は GitHub の CAS（ref create、compare-and-push、SHA 指定 merge）である。
in-process の排他は単一 instance 前提の効率のためであり、誤って二重起動しても CAS が blind mutation を
防ぐ。

## Stable identity

- reconciliation: repository と Issue number
- Run: kudo PR 番号
- Operation: Run、kind、semantic input digest
- Attempt: Operation ID と attempt number（telemetry）
- GitHub mutation: target、operation kind、expected state、desired state
- 記録（check run / comment / label）: marker（kind、round、head、digest）

同じ identity に異なる semantic input が来た場合は duplicate として捨てず、conflict として扱う。

## Compare-and-mutate

Issue Worker は branch push、draft PR ensure、body update、draft 解除の直前に live state を取得する。
expected state と一致した場合だけ mutation し、response または再取得結果を durable Result に記録する。
timeout で結果が不明な場合は、同じ desired state が既に成立しているか確認してから再試行する。

Controller の label / comment / check run 記録は marker を検索してから行う冪等 mutation であり、失敗は次の reconcile が再試行する。

## 検証方針

- concurrent claim と reconcile の並行実行を複数 goroutine と deterministic な fake GitHub で検証する。
- webhook / polling の順序と重複を組み合わせ、Run が一つに収束することを検証する。
- mutation 前後の timeout、外部 push、close / merge を fake GitHub で検証する。intent 有無による merged 観測の解釈差も同じ fake で検証する。
- 複数 Issue が独立 workspace で同時進行でき、global lock によって直列化されないことを検証する。

## 参照

- [End-to-end workflow](../../05_design/02_workflow.md) — Idempotency and recovery
- [Architecture](../../05_design/01_architecture.md) — State model、Scheduling and concurrency
- [Worker Operation Protocol](../../05_design/contracts/operation-protocol-v1alpha1.md) — Attempt rules
- [Runtime platform](../../05_design/03_runtime-platform.md) — Filesystem と workspace
