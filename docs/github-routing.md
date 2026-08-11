# GitHub routing policy

## Purpose

本書は、GitHub Issue を Kudo の対応候補として見つける条件、webhook と polling の統合、claim 前の再検証、GitHub 上へ投影する status label を定義する。

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
- 検証中に Issue Revision または参照 content が変わっていない

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

claim 成功後に assignee または status label が手で変更されても、それだけを implicit cancel command にしない。Issue close や Issue body/authority の変更を active Run が検出した場合は安全な checkpoint で停止し、stale/needs-human rule に従う。cancel は将来、明示的な versioned Operation として設計する。

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
| `ai-review-waiting` | Kudo | 規定を満たす Pull Request を作成済みで、人間の PR review 待ち |
| `ai-needs-human` | Kudo | 自動判断できない理由で停止し、人間の契約・authority・環境対応が必要 |

`ai-ready`だけが人間所有の trigger で、残りは PostgreSQL の durable state から作る projection である。label の手動変更で内部 Run state を上書きしない。Controller は desired label set を冪等に reconcile する。

### Transition rules

| Durable event | Remove | Add |
| --- | --- | --- |
| claim committed | `ai-ready`, `ai-needs-human`, `ai-review-waiting` | `ai-in-progress` |
| Run needs human | `ai-ready`, `ai-in-progress`, `ai-review-waiting` | `ai-needs-human` |
| PR handoff committed | `ai-ready`, `ai-in-progress`, `ai-needs-human` | `ai-review-waiting` |

state transition と projection intent を同じ database transaction に記録し、outbox が GitHub mutation を retry する。GitHub API failure で確定済み Run state を巻き戻さない。polling が一時的に残った`ai-ready`を再発見しても、active Run constraint で二重 Run を防ぐ。

dependency 待ち、capacity 待ち、一時 transport failure では`ai-ready`を消費しない。test/final review の`request_changes`は自動修正 loop なので`ai-in-progress`を保つ。

### Human escalation

`ai-needs-human`の対象には次を含む。

- Review Result が`needs_human`
- Contract、Acceptance、authority の矛盾、不足、曖昧さ
- Issue Revision、authority、base/head の unexpected change による stale input
- 危険な mutation に対する明示的許可不足
- 自動選択できない仕様判断
- 必須 credential または外部設定が人間の操作なしに復旧できない状態
- bounded retry を超え、operator の診断が必要な execution failure

Controller は label と同時に、Run ID、停止 phase、理由 code、観測内容、必要な対応、evidence reference を含む一つの日本語 status comment を作成または更新する。comment reply は実装 authority にしない。

人間は Issue 本文または`authorityRefs`が指す正本を修正し、再度`ai-ready`を付ける。reconciliation が安全な再開または新しい Run を確定した時点で、Kudo は`ai-needs-human`を外して`ai-in-progress`へ投影する。

### Review waiting

`ai-review-waiting`は internal test review や final implementation review を意味しない。Kudo が PR を作成し、人間の Pull Request review へ handoff 済みであることだけを表す。

この状態で`ai-ready`を追加しても、同じ Issue の新しい implementation Run を暗黙に開始しない。PR review comment 対応、再実装、cancel、merge 後 status は、この workflow に versioned command を追加する別 decision まで人間が扱う。

`ai-reviewing`、`ai-completed`、`ai-failed`、`ai-blocked`は導入しない。詳細な phase、retry、dependency、failure は PostgreSQL と status comment/telemetry で追跡する。
