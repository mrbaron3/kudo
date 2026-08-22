# 4.5. Retry・Recovery・Human Escalation 詳細設計

[機能仕様](01_spec.md)を、observation-driven な再実行、error classification、explicit resumption として
実現する。

## Execution model

Run は business workflow の identity（kudo PR）、Operation は一つの論理 action、Attempt は実際の一回の
実行を表す。Operation の input digest は attempt 間で維持し、attempt number、failure は telemetry に
記録する。

Controller は導出 phase から次 action を決めて in-process dispatch する。process crash で進行中の
attempt が消えた場合、restart 後の再観測が同じ phase を導出し、同じ action を新しい fresh attempt と
して再実行する。durable な checkpoint は record surface（commit、check run、comment、PR body machine
block）にだけ置く。

## Error classification

| 分類 | Routing |
| --- | --- |
| timeout、rate limit、一時 network / provider failure | retry budget 内で新しい Attempt を作る |
| invalid provider output | bounded retry 後に execution failure とする |
| quality `request_changes` | 同じ gate の修正 Operation へ進める |
| implement 発の `test_revision_required` | `test_validity` の round 予算を1消費し、上限内なら `revise_tests` へ、上限到達で `needs_human` |
| stale semantic input | 現在の identity に対する新しい評価を要求する |
| authority conflict、安全判断、intent に紐付かない外部 close / merge、`merge_blocked` | `needs_human` へ遷移する |
| immutable inputに対するprotocol validation error | Worker Result / AttemptFailureとして受理せず、`ProtocolError`を記録してOperationを`failed_terminal`とし、Runを`protocol_validation_failed`の`needs_human`へ送る |

retry budgetはclaim時に`kudo.escalation-policy/v1alpha1`へ`attemptRetries`として固定する。既定`3`、許容範囲`1`〜`10`で、値は初回Attemptの後に許す追加Attempt数である。timeout、rate limit、network、provider crash、GitHub transport、provider invalid responseは一つのlogical Operation内で同じcounterを共有し、次のAttemptを作成するたびに1を消費する。quality verdict、stale input、protocol validation errorは消費しない。provider invalid responseとはproviderのraw出力をversioned Resultへdecodeできない失敗であり、既に構築されたimmutable envelope/refのvalidation違反とは区別する。

counterはlogical Operationかつ無人区間単位でprocess memoryに持つ。escalation時に区間counterを0へ戻す
一方、生涯Attempt数はtelemetryで追跡する。process再起動でattempt counterは失われるが、review round予算と
escalationが無人loopの外側の防波堤になる。retry対象か否かは固定された`FailureClass`から判定し、自由文
messageから推測しない。

## Escalation

Controller は停止 phase、reason code、evidence への参照、必要な human action、ResumeIdentity を status
comment の machine block へ記録し、`ai-needs-human` label を付ける。reviewer は round 上限を知らず、
Controller が versioned Result を受理した後に gate ごとの counter（marker 付き記録の計数）を評価する。

## Resume と Supersede

停止時にControllerは次の二層を`ResumeIdentity`として保存する。

- `InputIdentity`: `ContextManifestRef`と`ExecutionPolicyRef`
- `CheckpointIdentity`: 停止phase、phaseで有効なhead群、ordered `policyRefs`、Pull Request ref、test/final approvalのverdict check run binding

Issue Observation、Escalation Policy、無人区間counterはResume Identityに含めない。Observationは観測
auditであり、Escalation Policyとcounterはreview判断のsemantic inputではないためである。進行中Runは
claim時にpinしたEscalation Policyを継続して使い、resume時のdeployment設定へ差し替えない。

再reconciliationは人間による`ai-ready`再付与を確認し、claim checkpointに固定したCompiler versionで
live Issue/authorityを再取得・再compileして同じ構成のidentityを再構築する。

- Resume Identity全体が同じなら、`needs_human`から保存済み停止phaseへ戻し、そのphaseに対応するOperationまたはReviewをfresh Attemptとして再dispatchする。publish/finalizeはlive GitHub stateを照合するidempotent recoveryとして再実行する。
- `InputIdentity`が変わった場合は旧Runをsupersededとし、新しいRun/review lineageを作る。以前のapprovalを移さない。
- `CheckpointIdentity`だけが変わった場合は旧approvalをstaleとし、同じRunをresumeしない。validな新規inputとして安全にclaimできる場合だけsupersedeし、intentに紐付かないPR close/merge等でできない場合は`needs_human`を維持する。
- Issueのraw bodyだけの非意味的差分はtelemetryへ記録し、identityを維持する。

resume / supersedeは同じreconcile内で排他的に選択する。supersedeはPR closeとbranch削除を先に完了させ、
新しいclaimはbranch ref createのatomicityが排他する。

## 検証方針

- fake clock と fake GitHub で backoff、restart 後の再導出、attempt 再実行を決定論的に検証する。
- crash point を state commit 前後と external mutation 前後へ置き、再実行が収束することを検証する。
- error classごとのrouting、error classをまたぐretry budget、protocol violationのResult非受理・即時terminal routing、review round counterをtable-driven testで検証する。
- composite Resume Identityによるresume / supersede / checkpoint不一致とconcurrent resumptionの排他を検証する。

## 参照

- [End-to-end workflow](../../05_design/02_workflow.md) — Durable states、Escalation and resumption
- [Architecture](../../05_design/01_architecture.md) — State model、Recovery and failure taxonomy
- [Task Context Protocol](../../05_design/contracts/task-context-v1alpha1.md) — Escalation Policy
