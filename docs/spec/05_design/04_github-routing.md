# GitHub routing policy

## Purpose

本書は、GitHub Issue を Kudo の対応候補として見つける条件、webhook と polling の統合、claim 前の再検証、GitHub 上へ投影する status label と Issue close を定義する。

Routing metadata は implementation authority ではない。Issue 本文と`authorityRefs`が実装入力であり、assignee、label、webhook payload、Project field、Kudo comment で不足した Contract を補わない。

## Candidate selection

configured repository 内の Issue が次をすべて満たす場合だけ、Kudo の対応候補とする。

- Pull Request ではない
- live state が`open`
- assignees に GitHub login`mrbaron3`を含む
- labels に`ai-ready`を含む

他の assignee または label が併存していても除外しない。`bug`、`enhancement`、priority、Project status は routing に使わない。login と label name は configuration で上書き可能だが、deployment 内では一意に固定する。

candidate と claimable は別である。Issue Worker は live GitHub API から現在値を再取得し、さらに次を検証する。

- Issue Contract が strict parse でき、`kind: task`、`readiness: ready`である
- Acceptance Criteria、native relationship、authority reference が整合する
- `dependsOn`の全成果が完了し、選択した base へ統合済みである
- 同じ IssueRef に active Run がない
- 検証中にIssue Observationまたは参照contentが変わっていない

結果は少なくとも次を区別する。

| Result | Meaning | Retry / projection |
| --- | --- | --- |
| `claimed` | candidate と claim 条件を満たし、Run を確定した | `ai-in-progress`へ投影 |
| `waiting_dependency` | contract は有効だが dependency 未完了 | `ai-ready`を残し、pollingで再評価 |
| `waiting_capacity` | 実行 slot がない | `ai-ready`を残し、内部schedulerで再評価 |
| `skipped_not_candidate` | state、assignee、label、Issue種別が対象外 | no-op |
| `claim_rejected` | 人間が直すべき contract/authority 不備 | `ai-needs-human`へ投影 |
| `failed_transport` | GitHub timeout、rate limit、network failure | labelを変えずbackoff retry |

`waiting_dependency`と`waiting_capacity`を`needs_human`や failed として表示しない。transport failure を contract rejection または review verdict に変換しない。

## Unified reconciliation

Webhook と polling は次の同じ application operation へ収束する。

```text
ReconcileIssue
├─ repository identity
├─ Issue number
└─ Trigger
   ├─ webhook delivery ID + action
   ├─ startup reconciliation ID
   └─ scheduled poll ID
```

Trigger は observability と inbox deduplication に使う。candidate 判定や Issue Contract の入力には使わない。`ReconcileIssue`は何度呼ばれても、live state と active Run constraint から同じ安全な結果になる。

## Webhook ingress

Controller の`POST /webhooks/github`は GitHub `issues` event を受ける。adapter は raw body に対する signature を検証してから parse し、delivery ID を durable inbox の unique key として記録する。durable acceptance 後は model/claim 完了を待たずに成功応答を返す。

少なくとも次の action を reconciliation trigger として受け付ける。

- `opened`
- `reopened`
- `edited`
- `assigned`
- `unassigned`
- `labeled`
- `unlabeled`
- `closed`

候補成立に直接関係しない action でも、同じ reconciliation を実行すれば live state に基づいて no-op にできる。Webhook は遅延、重複、順不同、欠落し得る通知であり、payload の Issue snapshot を source of truth にしない。

claim 成功後に assignee または status label が手で変更されても、それだけを implicit cancel command にしない。Kudo 自身の merge completion による close を除き、Issue close や Issue body/authority の変更を active Run が検出した場合は安全な checkpoint で停止し、stale/needs-human rule に従う。cancel は将来、明示的な versioned Operation として設計する。

## Polling fallback

Polling は任意の追加機能ではなく、Webhook 欠落を回復する必須経路である。

- Controller startup 時に一度実行する。
- 正常稼働中は既定60秒ごとに実行し、複数 instance では leader lease により同じ poll cycle を一つにする。
- configured repository ごとに、open、target assignee、`ai-ready`を満たす Issue を pagination して列挙する。
- 取得した各 IssueRef を webhook と同じ`ReconcileIssue`へ投入する。
- poll cycle 自体に claim、dependency、label mutation の business logic を置かない。
- rate limit または一時 failure 時は jitter 付き backoff を使い、最後の成功時刻と backlog を監視する。

Polling の query result も authority ではない。claim 直前の live Issue read を省略しない。全候補を繰り返し発見しても、IssueRef の active Run uniqueness と Operation idempotency により二重実行しない。

候補 discovery とは別に、Controller は PostgreSQL 内の expired lease、retry due Operation、outbox backlog を継続的に recover する。この recovery loop は GitHub search の成否に依存しない。

## Labels

Kudo が認識する label は次の4種類に限定する。

| Label | Owner | Meaning |
| --- | --- | --- |
| `ai-ready` | Human | 完成した Issue Contract に対する新規実行または escalation 後の再評価依頼 |
| `ai-in-progress` | Kudo | Run を claim 済みで、自動 test/review/implementation workflow が進行中 |
| `ai-merged` | Kudo | 承認済み head を base へ merge 済みで、Issue は close されている |
| `ai-needs-human` | Kudo | 自動判断できない理由で停止し、人間の契約・authority・環境対応が必要 |

`ai-ready`だけが人間所有の trigger で、残りは PostgreSQL の durable state から作る projection である。label の手動変更で内部 Run state を上書きしない。Controller は desired label set を冪等に reconcile する。

### Transition rules

| Durable event | Remove | Add |
| --- | --- | --- |
| claim committed | `ai-ready`, `ai-needs-human`, `ai-merged` | `ai-in-progress` |
| Run needs human | `ai-ready`, `ai-in-progress`, `ai-merged` | `ai-needs-human` |
| merge completed | `ai-ready`, `ai-in-progress`, `ai-needs-human` | `ai-merged` |

merge completion は label と同時に Task Issue の close intent を同じ transaction へ記録する。PR body の closing keyword で GitHub が先に close していた場合は観測して no-op にする。close は base が default branch のときだけ効く副作用に依存させない。

state transition と projection intent を同じ database transaction に記録し、outbox が GitHub mutation を retry する。GitHub API failure で確定済み Run state を巻き戻さない。polling が一時的に残った`ai-ready`を再発見しても、active Run constraint で二重 Run を防ぐ。

dependency 待ち、capacity 待ち、一時 transport failure では`ai-ready`を消費しない。test/final review の`request_changes`は自動修正 loop なので`ai-in-progress`を保つ。ただし当該 gate の review round 上限に達した`request_changes`は自動 loop を終了させ、`ai-needs-human`へ投影する（[ADR-0003](decisions/0003-review-round-limit.md)）。

### Human escalation

停止理由は機械可読な code で分類する。Controller は error 文字列や自由記述で分岐しない。message 表現を変えただけで分岐が壊れ、逆に分岐を保つために message を固定する必要が生じるためである。

| code | 意味 |
| --- | --- |
| `review_needs_human` | Review Result の verdict が`needs_human` |
| `review_round_limit_exceeded` | review gate の round 上限に達しても blocking finding が解消しなかった。reviewer の判断ではなく Controller の予算切れである |
| `retry_budget_exhausted` | bounded retry を超え、operator の診断が必要な execution failure |
| `protocol_validation_failed` | immutable envelope、Result、ref等がversioned protocolを満たさず、同じinputのretryでは復旧できない |
| `contract_authority_conflict` | Contract、Acceptance Criteria、authority の矛盾、不足、曖昧さ |
| `external_mutation_conflict` | Kudo の merge intent に紐付かない PR の close/merge のように、blind mutation できない外部干渉 |
| `merge_blocked` | required check failure、conflict、branch protection の拒否など、承認済み head を安全に merge できない外形条件（[ADR-0005](decisions/0005-auto-merge.md)） |
| `unsafe_mutation_unauthorized` | 危険な mutation に対する明示的許可不足 |
| `specification_decision_required` | 自動選択できない仕様判断 |
| `external_configuration_required` | 必須 credential または外部設定が人間の操作なしに復旧できない状態 |

`review_needs_human`、`review_round_limit_exceeded`、`retry_budget_exhausted`、`protocol_validation_failed`は Controller が Run state または機械可読な`ProtocolError`から自ら導出する。Worker や adapter からの明示的 escalation 要求ではこれらを指定できない。指定できると、上限に達していない Run を「上限到達」として停止したり、検証済みResultをprotocol違反として偽装したりでき、code と Run の lineage が食い違う。

Context Manifest（Task Context、authority content、base）、Execution Policy、head、artifact の unexpected change は escalation ではなく stale として扱い、古い Run を superseded にして再 claim へ回す。再 claim が contract 不備で通らない場合だけ`contract_authority_conflict`として escalate する。

Controller は label と同時に、Run ID、停止 phase、理由 code、観測内容、必要な対応、evidence reference を含む一つの日本語 status comment を作成または更新する。comment reply は実装 authority にしない。

#### Review round ledger

`review_round_limit_exceeded`の status comment には、最終 round の finding だけでなく**全 round の finding を round 順に並べた ledger**を載せる。最終 round だけでは、人間が差し戻しに対して何をすべきか選べない。

- 同じ finding が反復している = 実装が指摘を直せていない。実装能力、context、provider 選択の問題。
- 毎回違う finding が出ている = 何を作るべきかが決まっていない。Issue Contract または authority の問題。

ledger は Run の review lineage（各 round の Review Request / Result binding）から組み立てる。Run aggregate は counter と理由 code だけを持ち、finding 本文を保持しない。

各 finding には canonical fingerprint（`severity`、`summary`、`expected`、`observed`から計算する digest）を併記する。fingerprint の完全一致は「reviewer が字義どおり同じことを再度述べた」という曖昧さのない証拠である。**一致しないことは「違う指摘である」ことの証拠にはならない**片側の signal であり、その旨を ledger に明記する。Kudo は同一性の自動判定を行わない。model 由来の finding `id`は round をまたいで安定せず、前 round の finding を reviewer へ渡すと fresh session isolation を壊し、Controller が fuzzy 一致で判定すると control plane が review 判断を代行することになるためである。判断そのものは人間が行う。

Escalation Policy が固定した上限値と、その policy の digest も comment に含める。「なぜこの回数で止まったのか」の根拠を Run から確定できるようにする。

**今回の無人区間の round 数と、Run の生涯 round 数・差し戻し回数を併記する。** 差し戻すたびに round 予算は満額へ戻るため、この数字が繰り返しを可視化する唯一の材料になる。

人間が介入してもなお gate を通らないことは、round 予算の不足ではなく、Issue Contract、authority、分割の粒度、Execution Policy の provider/model 選択のどれかが誤っているという signal である。Kudo はこの状況に対して自動停止の上限を置かない。上限を置くと signal を読む前に数字が判断を代行し、「これ以上は無理」という結論を機械が出すことになる。どの前提が誤っているかは差し戻しのたびに人間が判断する。

Kudo が保証するのは「無人で回り続けないこと」と「区間の終わりごとに判断材料を渡すこと」であり、「何回で諦めるか」は判断そのものなので自動化しない。

人間は Issue 本文または`authorityRefs`が指す正本を修正し、再度`ai-ready`を付ける。reconciliation が安全な再開または新しい Run を確定した時点で、Kudo は`ai-needs-human`を外して`ai-in-progress`へ投影する。

### Merge completion

`ai-merged`は internal test review や final implementation review の結果ではなく、「approved head が base へ入った」という外形事実の投影である。この label が付いた Issue は close 済みであり、`open`を要求する candidate 条件を満たさないため polling で再発見されない。

Issue を reopen して`ai-ready`を追加しても、同じ Issue の新しい implementation Run を暗黙に開始しない。再実装、cancel、revert、merge 後の PR review comment 対応は、この workflow に versioned command を追加する別 decision まで人間が扱う。

`merge_blocked`で停止した Run は PR を open のまま残す。Kudo は required check、conflict、protection 設定を自動で回避しない。人間が原因を解消して`ai-ready`を付け直した時点で、reconciliation が安全な resume または supersede を判断する。

`ai-reviewing`、`ai-completed`、`ai-failed`、`ai-blocked`は導入しない。詳細な phase、retry、dependency、failure は PostgreSQL と status comment/telemetry で追跡する。
