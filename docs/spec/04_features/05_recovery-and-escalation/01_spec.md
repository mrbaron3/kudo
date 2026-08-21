# 4.5. Retry・Recovery・Human Escalation 受け入れ要件

[03. システム仕様](../../03_system-spec/) F-08 に対する Why / What の受け入れ基準である。
Operation、Attempt、lease、error routing、resumption の実現方法は [詳細設計](02_design.md) で扱う。

## サブ機能一覧

| ID | サブ機能 | 優先度 |
| --- | --- | --- |
| 4.5.1 | Retry | 高 |
| 4.5.2 | Recovery | 高 |
| 4.5.3 | Human Escalation と再開 | 高 |

## 4.5.1. Retry

**ユーザーストーリー**

- 誰が: Kudo を運用するリポジトリ管理者
- 何を: 一時的な外部障害では作業を失わず、安全な範囲で自動再試行してほしい
- なぜ: 人間が毎回同じ Issue を再投入せずに workflow を継続できるから

**事前条件**

- logical Operation の identity と immutable input が durable に保存されている。
- retry budget がclaim時のEscalation Policy snapshotで確定している。

**受け入れ基準**

- **回復系: 一時的 Transport Failure**
  - Given timeout、rate limit、一時 network failure、または一時 provider failure が発生する。
  - When retry budget が残っている。
  - Then 同じ logical Operation に新しい Attempt が作られ、bounded backoff 後に再実行される。

- **上限: Attempt Retry Budget**
  - Given deployment configurationにretry上限の明示値がない。
  - When claim時にEscalation Policyを固定する。
  - Then `attemptRetries`は既定`3`となり、初回Attemptの後に同じlogical Operationへ作成する追加Attemptごとに1を消費する。
  - And timeout、rate limit、一時network failure、provider crash、GitHub transport failure、providerのinvalid structured outputはerror classをまたいで同じ予算を共有する。

- **隔離: Attempt ごとの Fresh Session**
  - Given model-bearing Operation の以前の Attempt が失敗している。
  - When 次の Attempt を開始する。
  - Then 新しい provider process と state directory を使い、以前の session を resume しない。

- **分類: Quality Verdict の非Retry化**
  - Given Review Worker が `request_changes` または `needs_human` を返す。
  - When Controller が Result を処理する。
  - Then transport retry として再実行せず、対応する repair lane または escalation へ routing する。

- **分類: Provider Invalid Output**
  - Given providerがversioned Resultへdecodeできないinvalid structured outputを返す。
  - When provider adapterがAttempt failureへ分類する。
  - Then quality verdictへ変換せず、`provider_invalid_response`として同じbounded retry budgetを使う。

- **分類: Immutable Protocol Violation**
  - Given unsupported version、missing required field、unknown kind、ref/digest不一致等を、immutable inputのbinding boundaryで検出する。
  - When protocol validation errorを処理する。
  - Then provider invalid outputまたはtransport failureへ読み替えず、Worker Result / AttemptFailureとして受理しない。
  - And `ProtocolError`をdurable evidenceとして記録し、retry budgetを消費せずOperation queue stateを`failed_terminal`、Runを`protocol_validation_failed`の`needs_human`へ遷移させる。

- **上限: Retry Budget 超過**
  - Given 同じ logical Operation が retry budget を使い切っている。
  - When 再び retryable failure が発生する。
  - Then 無限再試行せず、停止理由と attempts の evidence が durable に残る。
  - And human escalationで無人区間counterを0へ戻しても、Attempt lineageと生涯Attempt数は保持する。

**非機能要件**

- 回復性: retry は process-local counter ではなく durable Attempt history に基づく。
- 安全性: 同じ input identity の retry だけを許可する。
- 可観測性: Operation、Attempt、error class、backoff、terminal reason を追跡できる。

**完了条件**

- 自動テスト: retryable / verdict / protocol failure / budget超過を fake clock で検証する。
- 自動テスト: model-bearing retry が以前の provider session state を使用しないことを確認する。

## 4.5.2. Recovery

**ユーザーストーリー**

- 誰が: Kudo を運用するリポジトリ管理者
- 何を: worker や host が停止しても、確定済み checkpoint から処理を再開してほしい
- なぜ: 長い issue-to-PR workflow を最初からやり直さず、重複 mutation も避けられるから

**事前条件**

- Run、Operation、Attempt、lease、commit、artifact reference が durable に記録されている。
- 外部 mutation に stable identity と expected state がある。

**受け入れ基準**

- **回復系: Lease Expiry 後の再取得**
  - Given Operation 実行中に worker process が停止し、heartbeat が途絶える。
  - When lease が期限切れになり reaper が処理する。
  - Then 別の eligible worker が同じ Operation を新しい Attempt として取得できる。

- **再構築: Immutable Checkpoint**
  - Given 以前の provider process と mutable memory が失われている。
  - When 新しい Attempt を開始する。
  - Then commit、Context Manifest、Execution Policy、artifact reference だけから必要な入力を再構築する。

- **整合性: State Commit 後の停止**
  - Given durable transition は commit 済みだが、次の dispatch または GitHub projection 前に process が停止する。
  - When Controller が再起動する。
  - Then 確定済み state を正として dispatch / outbox を再開し、前 phase へ巻き戻らない。

- **冪等性: Mutation 結果不明**
  - Given GitHub mutation 後、response 受信前に connection が切れる。
  - When Operation を回復する。
  - Then live state で desired mutation の成立を確認してから再実行し、副作用を重複させない。

- **異常系: Immutable Input の欠損**
  - Given recovery に必要な commit または artifact digest を取得できない。
  - When Attempt を再構築する。
  - Then 推測した入力で継続せず、terminal failure または human escalation として停止する。

**非機能要件**

- 永続性: PostgreSQL を workflow state と queue の正本にする。
- 完全性: artifact は content-addressed かつ write-once とする。
- 可観測性: crash 前後の lease owner、checkpoint、Attempt lineage を追跡できる。

**完了条件**

- 障害テスト: state commit 前後、external mutation 前後、Result保存前後の停止を検証する。
- 自動テスト: lease expiry 後の再取得が同じ logical Operation に収束する。

## 4.5.3. Human Escalation と再開

**ユーザーストーリー**

- 誰が: Kudo の停止判断を引き継ぐ Task Issue の責任者
- 何を: 自動化できない理由、証拠、必要な対応を明示された状態で判断したい
- なぜ: 不明なまま再実行せず、安全な修正後に同じ作業を再開できるから

**事前条件**

- 自動化の判断境界と、attempt retry / review round上限を持つEscalation Policyがclaim時に固定されている。
- Issue へ status と日本語 comment を投影できる。

**受け入れ基準**

- **停止系: Needs Human**
  - Given authority conflict、安全判断、attempt retry / review round上限、immutable protocol validation failure、または外部干渉が発生する。
  - When Controller が自動継続不能と判断する。
  - Then 停止 phase、reason code、evidence、必要な human action が durable に保存され、`ai-needs-human` が投影される。

- **上限: Review Round Budget**
  - Given 当該 gate の `request_changes` round が無人区間の上限に達する。
  - When Controller が verdict を受理する。
  - Then reviewer の verdict は変更せず、次の repair Operation を発行しないで `needs_human` へ遷移する。

- **再開: Resume Identity が同一**
  - Given 人間が必要な対応を行い、`ai-ready`を再付与する。
  - When reconciliationが停止時の`ContextManifestRef`、`ExecutionPolicyRef`、停止phase、該当head、`ArtifactManifestRef`、ordered `policyRefs`、Pull Request ref、既存approval bindingから成るResume Identityを再構築し、live stateと比較する。
  - Then 全fieldが同一の場合だけ、安全な停止phaseへ同じRunをresumeし、attempt retryとreview roundの無人区間counterは満額から再開する。
  - And Issue ObservationまたはPull Request Observationだけの差分はaudit lineageへ追記し、Resume Identityを変えない。

- **再開: Semantic Input または Checkpoint が変更**
  - Given Context Manifest、Execution Policy、head、artifact manifest、policy ref、Pull Request ref、approval bindingのいずれかが変更されている。
  - When `ai-ready` 再付与後に reconciliation する。
  - Then semantic inputが変わり新しいvalid inputを構築できる場合は旧Runをsupersededとし、新しいRunとreview lineageを作り、以前のapprovalを移さない。
  - And 外部close/merge等で安全なcheckpointを構築できない場合は同じRunをresumeせず、`needs_human`のまま停止する。

- **排他: Concurrent Resumption**
  - Given 複数 trigger が paused Run の再開を同時に試みる。
  - When resume / supersede を確定する。
  - Then 同じ Issue に writer-capable Run が二つ存在しない。

- **境界: 明示操作なしの再開防止**
  - Given Issue が `ai-needs-human` のままである。
  - When polling または再起動が発生する。
  - Then 人間による `ai-ready` 再付与なしには自動再開しない。

**非機能要件**

- 説明可能性: reason code と evidence から停止判断を再現できる。
- 安全性: escalation を retry loop の一状態として自動解除しない。
- 可観測性: 無人区間 counter と生涯 counter を別々に追跡できる。

**完了条件**

- 自動テスト: round上限 / composite Resume Identityのresume / supersede / checkpoint不一致 / concurrent resumption / 明示操作なしを検証する。
- デモ: `ai-needs-human` comment の指示に従った修正と `ai-ready` 再付与から安全に再開できる。

## 参照する正本

- [End-to-end workflow](../../05_design/02_workflow.md) — Durable states、Escalation and resumption、Recovery
- [Architecture](../../05_design/01_architecture.md) — Queue、lease、recovery
- [Worker Operation Protocol](../../05_design/contracts/operation-protocol-v1alpha1.md)
- [ADR-0003](../../05_design/decisions/0003-review-round-limit.md)
