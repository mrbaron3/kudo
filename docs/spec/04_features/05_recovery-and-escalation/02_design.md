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
| immutable inputに対するprotocol validation error | Worker Result / AttemptFailureとして受理せず、`ProtocolError`を記録してOperation queue stateを`failed_terminal`、Runを`protocol_validation_failed`の`needs_human`へ送る |

retry budgetはclaim時に`kudo.escalation-policy/v1alpha1`へ`attemptRetries`として固定する。既定`3`、許容範囲`1`〜`10`で、値は初回Attemptの後に許す追加Attempt数である。timeout、rate limit、network、provider crash、GitHub transport、provider invalid responseは一つのlogical Operation内で同じcounterを共有し、次のAttemptを作成するたびに1を消費する。quality verdict、stale input、protocol validation errorは消費しない。provider invalid responseとはproviderのraw出力をversioned Resultへdecodeできない失敗であり、既に構築されたimmutable envelope/refのvalidation違反とは区別する。

counterはlogical Operationかつ無人区間単位で持つ。escalation時に区間counterを0へ戻す一方、Attempt numberと生涯Attempt数は巻き戻さない。retry対象か否かは固定された`FailureClass`から判定し、自由文messageから推測しない。

## Escalation

Controller は停止 phase、reason code、evidence reference、必要な human action を durable transition として保存し、
同じ transaction で GitHub comment / label の outbox entry を作る。reviewer は round 上限を知らず、Controller が
versioned Result を受理した後に gate ごとの counter を評価する。

## Resume と Supersede

停止時にControllerは次の二層を`ResumeIdentity`として保存する。

- `InputIdentity`: `ContextManifestRef`と`ExecutionPolicyRef`
- `CheckpointIdentity`: 停止phase、phaseで有効なfixed/published/checks head、`ArtifactManifestRef`、ordered `policyRefs`、Pull Request ref、test/final approvalのReview Result binding

Issue Observation、Pull Request Observation、Escalation Policy、無人区間counterはResume Identityに含めない。Observationはaudit lineageであり、Escalation Policyとcounterはreview判断のsemantic inputではないためである。進行中Runはclaim時にpinしたEscalation Policyを継続して使い、resume時のdeployment設定へ差し替えない。

再reconciliationは人間による`ai-ready`再付与を確認し、live stateから同じ構成のidentityを再構築する。

- Resume Identity全体が同じなら、`needs_human`から保存済み停止phaseへ戻し、そのphaseに対応するOperationまたはReviewをfresh Attemptとして再dispatchする。publish/finalizeはlive GitHub stateを照合するidempotent recoveryとして再実行する。
- `InputIdentity`が変わった場合は旧Runをsupersededとし、新しいRun/review lineageを作る。以前のapprovalを移さない。
- `CheckpointIdentity`だけが変わった場合は旧approvalをstaleとし、同じRunをresumeしない。validな新規inputとして安全にclaimできる場合だけsupersedeし、PR close/merge等でできない場合は`needs_human`を維持する。
- Issue ObservationまたはPull Request Observationだけが変わった場合はaudit lineageを追加し、identityを維持する。

resume / supersedeの選択、paused Runのversion確認、writer排他、次Operationのenqueueは一つのtransactionで決定する。

## 検証方針

- fake clock と store で lease expiry、heartbeat、backoff、reaper を決定論的に検証する。
- crash point を state commit 前後と external mutation 前後へ置き、再実行が収束することを検証する。
- error classごとのrouting、error classをまたぐretry budget、protocol violationのResult非受理・即時terminal routing、review round counterをtable-driven testで検証する。
- composite Resume Identityによるresume / supersede / checkpoint不一致とconcurrent resumptionの排他を検証する。

## 参照

- [End-to-end workflow](../../05_design/02_workflow.md) — Durable states、Escalation and resumption
- [Architecture](../../05_design/01_architecture.md) — Durable model、Queue / lease / recovery
- [Task Context Protocol](../../05_design/contracts/task-context-v1alpha1.md) — Escalation Policy
- [ADR-0003](../../05_design/decisions/0003-review-round-limit.md)
