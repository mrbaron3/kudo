# GitHub routing policy

## Purpose

本書は、GitHub Issue を Kudo の対応候補として見つける条件、webhook と polling の統合、claim 前の
再検証、GitHub 上へ記録する status label、check run、status comment、Issue close を定義する。

Routing metadata は implementation authority ではない。Issue 本文と`authorityRefs`が実装入力であり、
assignee、label、webhook payload、Project field、Kudo comment で不足した Contract を補わない。

## Candidate selection

configured repository 内の Issue が次をすべて満たす場合だけ、Kudo の対応候補とする。

- Pull Request ではない
- live state が`open`
- assignees に GitHub login`mrbaron3`を含む
- labels に`ai-ready`を含む

他の assignee または label が併存していても除外しない。`bug`、`enhancement`、priority、Project
status は routing に使わない。login と label name は configuration で上書き可能だが、deployment 内では
一意に固定する。

candidate と claimable は別である。Issue Worker は live GitHub API から現在値を再取得し、さらに次を
検証する。

- Issue Contract が strict parse でき、`kind: task`、`readiness: ready`である
- Acceptance Criteria、native relationship、authority reference が整合する
- `dependsOn`の全成果が完了し、選択した base へ統合済みである
- 同じ IssueRef に active Run（open な kudo branch / PR）がない
- 同じ IssueRef に merged な kudo PR が存在しない
- 検証中に Issue Observation または参照 content が変わっていない

結果は少なくとも次を区別する。

| Result | Meaning | Retry / recording |
| --- | --- | --- |
| `claimed` | candidate と claim 条件を満たし、branch と draft PR を確定した | `ai-in-progress`を記録 |
| `waiting_dependency` | contract は有効だが dependency 未完了 | `ai-ready`を残し、polling で再評価 |
| `waiting_capacity` | 実行 slot がない | `ai-ready`を残し、内部 scheduler で再評価 |
| `skipped_not_candidate` | state、assignee、label、Issue 種別が対象外 | no-op |
| `skipped_already_merged` | 同じ IssueRef に merged な kudo PR が存在する。reopen 後の`ai-ready`再付与を含む | `ai-ready`を外して`ai-merged`を再記録し、案内 comment を更新 |
| `claim_rejected` | 人間が直すべき contract/authority 不備 | `ai-needs-human`を記録 |
| `failed_transport` | GitHub timeout、rate limit、network failure | label を変えず backoff retry |

`waiting_dependency`と`waiting_capacity`を`needs_human`や failed として表示しない。transport failure
を contract rejection または review verdict に変換しない。

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

Trigger は observability に使う。candidate 判定や Issue Contract の入力には使わない。
`ReconcileIssue`は何度呼ばれても、live state の観測から同じ phase を導出し、同じ安全な結果になる。
重複した trigger は観測の再実行になるだけで、mutation の重複は marker と CAS が防ぐ。

## Webhook ingress

`POST /webhooks/github`は GitHub `issues` event を受ける。adapter は raw body に対する signature を
検証してから parse し、対象 IssueRef の reconcile を trigger して応答する。durable な受信記録は
持たない。webhook は遅延、重複、順不同、欠落し得る通知であり、payload の Issue snapshot を source of
truth にしない。欠落は polling が回収する。

少なくとも次の action を reconciliation trigger として受け付ける。

- `opened`
- `reopened`
- `edited`
- `assigned`
- `unassigned`
- `labeled`
- `unlabeled`
- `closed`

候補成立に直接関係しない action でも、同じ reconciliation を実行すれば live state に基づいて no-op に
できる。

claim 成功後に assignee または status label が手で変更されても、それだけを implicit cancel command に
しない。Kudo 自身の merge completion による close を除き、Issue close や Issue body/authority の変更を
active Run が検出した場合は安全な checkpoint で停止し、stale / needs-human rule に従う。cancel は
将来、明示的な versioned Operation として設計する。

## Polling fallback

Polling は任意の追加機能ではなく、Webhook 欠落を回復する必須経路である。

- startup 時に一度実行する。
- 正常稼働中は既定15分ごとに実行する。
- 間隔が15分なのは、polling が低遅延経路ではなく取りこぼし回復経路だからである。低遅延は webhook が担う。
- configured repository ごとに、open、target assignee、`ai-ready`を満たす Issue と、open な kudo PR を
  pagination して列挙する。
- 取得した各 IssueRef を webhook と同じ`ReconcileIssue`へ投入する。open な kudo PR は途中 phase の Run
  であり、webhook を失っていても polling がここから進行を再開する。
- poll cycle 自体に claim、dependency、label mutation の business logic を置かない。
- rate limit または一時 failure 時は jitter 付き backoff を使い、最後の成功時刻と backlog を監視する。

Polling の query result も authority ではない。claim 直前の live Issue read を省略しない。全候補を
繰り返し発見しても、branch ref create の atomicity と marker により二重実行しない。

## Labels

Kudo が認識する label は次の4種類に限定する。

| Label | Owner | Meaning |
| --- | --- | --- |
| `ai-ready` | Human | 完成した Issue Contract に対する新規実行または escalation 後の再評価依頼 |
| `ai-in-progress` | Kudo | Run を claim 済みで、自動 test/review/implementation workflow が進行中 |
| `ai-merged` | Kudo | 承認済み head を base へ merge 済みで、Issue は close されている |
| `ai-needs-human` | Kudo | 自動判断できない理由で停止し、人間の契約・authority・環境対応が必要 |

`ai-ready`だけが人間所有の trigger で、残りは導出 phase からの記録である。Controller は desired
label set を冪等に reconcile する。Kudo 所有の label を人間が手で外しても、次の reconcile が check
run / comment / PR の観測から同じ phase を再導出して label を戻す。escalation の解除は label 操作では
なく、`ai-ready`の再付与だけが resume / supersede を起動する。

### Transition rules

| Derived event | Remove | Add |
| --- | --- | --- |
| claim 完了 | `ai-ready`, `ai-needs-human` | `ai-in-progress` |
| Run needs human | `ai-ready`, `ai-in-progress`, `ai-merged` | `ai-needs-human` |
| merge 完了 | `ai-ready`, `ai-in-progress`, `ai-needs-human` | `ai-merged` |
| already-merged 再依頼検出 | `ai-ready` | `ai-merged`（再記録） |

`ai-merged`を持つ IssueRef は claimable 条件（merged な kudo PR の不存在）で claim へ進まないため、
claim 完了の除去対象に`ai-merged`は現れない。label を手で外しても merged な kudo PR という観測が
正本であり、再依頼は`skipped_already_merged`として処理される。

merge completion は label 記録と同時に Task Issue を close する。PR body の closing keyword で GitHub
が先に close していた場合は観測して no-op にする。close は base が default branch のときだけ効く
副作用に依存させない。

GitHub API failure で導出済み phase は巻き戻らない。記録の失敗は次の reconcile が retry する。
polling が一時的に残った`ai-ready`を再発見しても、branch ref create の atomicity が二重 Run を防ぐ。

dependency 待ち、capacity 待ち、一時 transport failure では`ai-ready`を消費しない。test/final review
の`request_changes`は自動修正 loop なので`ai-in-progress`を保つ。ただし当該 gate の review round
上限に達した`request_changes`は自動 loop を終了させ、`ai-needs-human`を記録する。

## Check runs

Kudo が記録する check run は App 所有であり、commit SHA へ構造的に束縛される。名前空間は`kudo/`に
固定し、**作成 App identity を記録の一部として扱う**。actor と identity の対応は
[architecture.md](01_architecture.md) の Actor model を正とする。

| Check run | 対象 head | 作成者（App identity） | 内容 |
| --- | --- | --- | --- |
| `kudo/evidence-red` | test head | Implementer | RED command、exit status、出力抜粋、environment identity |
| `kudo/evidence-green` | final head | Implementer | GREEN command evidence |
| `kudo/evidence-checks` | final head | Implementer | refactor 後 required checks と Issue Verification evidence |
| `kudo/test-validity` | test head | Reviewer | test validity review の verdict と request identity（digest） |
| `kudo/final-implementation` | final head | Reviewer | final review の verdict、applicability 宣言、request identity |

verdict check run の output に記録する machine block（verdict、request digest、claim checkpoint の
digest 群）が gate 判定の正本である。conclusion（success / action_required / neutral）は人間向けの
表示であり、機械はこれに依存しない。evidence check run の conclusion は CI の成否と混同されないよう
`neutral`とする。output の上限（64KiB）を超える内容は決定論的に truncate し、全文の digest を併記する。

gate 判定は name と作成 App の両方で検証する。Reviewer App 名義でない`kudo/test-validity`は verdict
として扱わない。branch protection の required status check に`kudo/final-implementation`を **Reviewer
App を source として** 宣言すると、GitHub 自体が「Reviewer の final approve なしに merge できない」を
構造的に強制する。Implementer App は Reviewer 名義の check run を作れないため、自己承認は規約ではなく
構造で塞がる。deployment にはこの宣言を推奨する。

verdict の正本は両 gate とも check run であり、GitHub native の PR Review（APPROVE /
REQUEST_CHANGES）は使わない（2026-08-22 決定）。native review は`test_validity`という部分 gate を
表現できず（APPROVE が required approving reviews を final gate 前に満たしてしまう）、admin による
dismiss という可変性も持ち込むためである。人間レビュアーと bot レビュアーが同じ PR 上で混在する
複数人運用が始まった場合に限り、final approve の native review **投影**（正本は check run のまま）を
再検討する。

## Human escalation

停止理由は機械可読な code で分類する。Controller は error 文字列や自由記述で分岐しない。message 表現を
変えただけで分岐が壊れ、逆に分岐を保つために message を固定する必要が生じるためである。

| code | 意味 |
| --- | --- |
| `review_needs_human` | Review Result の verdict が`needs_human` |
| `review_round_limit_exceeded` | review gate の無人 round 予算を使い切った。`test_validity`側は implement 発の`test_revision_required`差し戻しも予算を消費する。reviewer の判断ではなく Controller の予算切れである |
| `retry_budget_exhausted` | bounded retry を超え、operator の診断が必要な execution failure |
| `protocol_validation_failed` | immutable envelope、Result、ref 等が versioned protocol を満たさず、同じ input の retry では復旧できない |
| `contract_authority_conflict` | Contract、Acceptance Criteria、authority の矛盾、不足、曖昧さ |
| `external_mutation_conflict` | Kudo の merge intent に紐付かない PR の close/merge のように、blind mutation できない外部干渉 |
| `merge_blocked` | required check failure、conflict、branch protection の拒否など、承認済み head を安全に merge できない外形条件 |
| `unsafe_mutation_unauthorized` | 危険な mutation に対する明示的許可不足 |
| `specification_decision_required` | 自動選択できない仕様判断 |
| `external_configuration_required` | 必須 credential または外部設定が人間の操作なしに復旧できない状態 |

`review_needs_human`、`review_round_limit_exceeded`、`retry_budget_exhausted`、
`protocol_validation_failed`は Controller が導出 phase または機械可読な`ProtocolError`から自ら導出
する。Worker や adapter からの明示的 escalation 要求ではこれらを指定できない。指定できると、上限に
達していない Run を「上限到達」として停止したり、検証済み Result を protocol 違反として偽装したり
でき、code と Run の lineage が食い違う。

Context Manifest（Task Context、authority content、base）、Execution Policy、head の unexpected
change は escalation ではなく stale として扱い、古い Run を superseded にして再 claim へ回す。
再 claim が contract 不備で通らない場合だけ`contract_authority_conflict`として escalate する。

Controller は label と同時に、停止 phase、理由 code、観測内容、必要な対応、evidence への参照、
ResumeIdentity の machine block を含む一つの日本語 status comment を作成または更新する。comment
reply は実装 authority にしない。

### Review round ledger

`review_round_limit_exceeded`の status comment には、最終 round の finding だけでなく**全 round の
finding を round 順に並べた ledger**を載せる。最終 round だけでは、人間が差し戻しに対して何をすべきか
選べない。

- 同じ finding が反復している = 実装が指摘を直せていない。実装能力、context、provider 選択の問題。
- 毎回違う finding が出ている = 何を作るべきかが決まっていない。Issue Contract または authority の問題。

ledger は PR 上の marker 付き finding comment と verdict check run から round 順に組み立てる。
`test_validity`側の ledger には、round を消費した`test_revision_required`の`test-revision-report`も
round 順に含める。実装が test を差し戻し続けることも、人間が読むべき反復の材料である。

各 finding には canonical fingerprint（`severity`、`summary`、`expected`、`observed`から計算する
digest）を併記する。fingerprint の完全一致は「reviewer が字義どおり同じことを再度述べた」という
曖昧さのない証拠である。**一致しないことは「違う指摘である」ことの証拠にはならない**片側の signal
であり、その旨を ledger に明記する。Kudo は同一性の自動判定を行わない。model 由来の finding `id`は
round をまたいで安定せず、前 round の finding を reviewer へ渡すと fresh session isolation を壊し、
Controller が fuzzy 一致で判定すると control plane が review 判断を代行することになるためである。
判断そのものは人間が行う。

Escalation Policy が固定した上限値と、その policy の digest も comment に含める。「なぜこの回数で
止まったのか」の根拠を Run から確定できるようにする。

**今回の無人区間の round 数と、Run の生涯 round 数・差し戻し回数を併記する。** 差し戻すたびに round
予算は満額へ戻るため、この数字が繰り返しを可視化する唯一の材料になる。

人間が介入してもなお gate を通らないことは、round 予算の不足ではなく、Issue Contract、authority、
分割の粒度、Execution Policy の provider/model 選択のどれかが誤っているという signal である。Kudo は
この状況に対して自動停止の上限を置かない。上限を置くと signal を読む前に数字が判断を代行し、「これ
以上は無理」という結論を機械が出すことになる。どの前提が誤っているかは差し戻しのたびに人間が判断する。

Kudo が保証するのは「無人で回り続けないこと」と「区間の終わりごとに判断材料を渡すこと」であり、
「何回で諦めるか」は判断そのものなので自動化しない。

人間は Issue 本文または`authorityRefs`が指す正本を修正し、再度`ai-ready`を付ける。reconciliation が
安全な再開または新しい Run を確定した時点で、Kudo は`ai-needs-human`を外して`ai-in-progress`を記録する。

### Merge completion

`ai-merged`は internal test review や final implementation review の結果ではなく、「approved head が
base へ入った」という外形事実の記録である。この label が付いた Issue は close 済みであり、`open`を
要求する candidate 条件を満たさないため polling で再発見されない。

Issue を reopen して`ai-ready`を追加しても、同じ Issue の新しい implementation Run を暗黙に開始
しない。claimable 条件が「同じ IssueRef に merged な kudo PR が存在しない」を要求するため、
reconciliation は再依頼を`skipped_already_merged`として終了し、`ai-ready`を外して`ai-merged`を
再記録する。同時に、再実行には新しい Task Issue の作成または versioned command の追加が必要である旨の
日本語 comment を作成または更新する。再実装、cancel、revert、merge 後の PR review comment 対応は、
この workflow に versioned command を追加する別 decision まで人間が扱う。

`merge_blocked`で停止した Run は PR を open のまま残す。Kudo は required check、conflict、protection
設定を自動で回避しない。人間が原因を解消して`ai-ready`を付け直した時点で、reconciliation が安全な
resume または supersede を判断する。

`ai-reviewing`、`ai-completed`、`ai-failed`、`ai-blocked`は導入しない。詳細な phase、retry、
dependency、failure は check run、status comment、telemetry で追跡する。

## Repository 設定の前提と推奨

merge の可否は repository 設定として宣言的に表現し、Kudo は設定を変更も回避もしない。本節を
deployment 側 GitHub 設定の正本とする。

- **前提**: 対象 base branch で merge commit が許可されている。squash / rebase のみの repository では
  `merge_pull_request`が`merge_blocked`になる。
- **前提**: 人間が必須とする quality gate は required status check として宣言する。required でない CI
  は merge gate に影響しない。
- **推奨**: `kudo/final-implementation`を required status check に宣言し、Kudo の final approve なしの
  merge を GitHub 自体に拒否させる。
- **推奨**: `Require branches to be up to date before merging`を有効にする。無効の場合、並行 Run が
  互いの merge 結果を取り込まないまま、textual には mergeable な変更を同じ base へ重ねられるため、
  review した head と merge 後の base 合成状態が意味的に食い違う可能性が残る。この残余 risk は Kudo の
  review では検出できず、base 側の CI と人間の revert 判断が受け皿になる。有効にした場合は、他 Run の
  merge のたびに遅れた Run が`merge_blocked`で停止し、人間の`ai-ready`再付与と supersede による追従
  （全 test / review のやり直し）が必要になる。Kudo は自動 rebase / base merge を行わない。どちらを
  選ぶかは repository の並行度と安全要求に応じた deployment 判断である。
