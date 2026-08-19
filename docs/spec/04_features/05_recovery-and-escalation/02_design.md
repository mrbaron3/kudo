# 4.5. Retry・Recovery・Human Escalation 詳細設計

[機能仕様](01_spec.md)を、durable Operation、scoped lease、error classification、explicit resumption として
実現する。

## Durable execution model

Run は business workflow の identity、Operation は一つの論理 action、Attempt は実際の一回の実行を表す。
Operation の input digest は attempt 間で維持し、attempt number、lease、heartbeat、failure は実行ごとに記録する。

Controller は state transition と次 Operation の enqueue を同じ PostgreSQL transaction で確定する。Worker は
eligible Operation を lease し、heartbeat を更新しながら処理する。process crash で lease が失効した場合、
reaper が Operation を再度 eligible にする。

## Error classification

| 分類 | Routing |
| --- | --- |
| timeout、rate limit、一時 network / provider failure | retry budget 内で新しい Attempt を作る |
| invalid provider output | bounded retry 後に execution failure とする |
| quality `request_changes` | 同じ gate の修正 Operation へ進める |
| stale semantic input | 現在の identity に対する新しい評価を要求する |
| authority conflict、安全判断、外部 close / merge | `needs_human` へ遷移する |
| protocol validation error | quality verdict に変換せず terminal または明示的修復へ送る |

retry policy は error class ごとに定義し、同じ enum や自由文メッセージから推測しない。

## Escalation

Controller は停止 phase、reason code、evidence reference、必要な human action を durable transition として保存し、
同じ transaction で GitHub comment / label の outbox entry を作る。reviewer は round 上限を知らず、Controller が
versioned Result を受理した後に gate ごとの counter を評価する。

## Resume と Supersede

再 reconciliation は live input と保存済み digest を比較する。

- Context Manifest、Execution Policy、base、head、artifact、policy ref が同じなら安全な checkpoint から resume する。
- semantic input が変わった場合は旧 Run を superseded とし、新しい Run / review lineage を作る。
- Issue Observation だけが変わり semantic input が同じ場合は audit lineage を追加して identity を維持する。

resume / supersede の選択と writer 排他は一つの transaction で決定する。

## 検証方針

- fake clock と store で lease expiry、heartbeat、backoff、reaper を決定論的に検証する。
- crash point を state commit 前後と external mutation 前後へ置き、再実行が収束することを検証する。
- error class ごとの routing、retry budget、review round counter を table-driven test で検証する。
- resume / supersede の digest 比較と concurrent resumption の排他を検証する。

## 参照

- [End-to-end workflow](../../05_design/02_workflow.md) — Durable states、Escalation and resumption
- [Architecture](../../05_design/01_architecture.md) — Durable model、Queue / lease / recovery
- [Task Context Protocol](../../05_design/contracts/task-context-v1alpha1.md) — Escalation Policy
- [ADR-0003](../../05_design/decisions/0003-review-round-limit.md)
