# ADR-0005: 承認済み head の自動 merge と完了 terminal

- Status: accepted（2026-08-21）
- 関連Issue: 未起票（プロダクトゴールの見直しで決定）
- 実装Task: [#62](https://github.com/mrbaron3/kudo/issues/62)（merge_pull_requestと完了投影）、[#60](https://github.com/mrbaron3/kudo/issues/60)（finalize / merge前のready化）
- 関連ADR: [ADR-0002](0002-pr-anchored-review.md)（全review roundをPRへ繋留する判断を前提とする）、[ADR-0003](0003-review-round-limit.md)（自動loopの上限）
- Supersede対象: ADR-0002 D1の「ready化と`ai-review-waiting`投影がhandoff terminalである」、[01. プロダクト設計](../../01_product-design/) §5の「Pull Requestのmerge、Issue close」除外

## Context

これまでKudoの正常終端は「PRをready化し、人間のPR reviewへ渡すこと」だった。人間は毎回、独立reviewが既にapproveした差分に対してmergeボタンを押す役割を担う。この一手が入る限り、Runの完了は人間の在席時間に律速され、`dependsOn`の「成果物がbaseへ統合済み」という条件も人間のmerge操作を待つ。

プロダクトゴールに自動mergeを含める判断を採用した。この判断自体は「誰がmergeを決めるか」の変更であり、決めた瞬間に次が未設計になる。

1. mergeのgate。`final_implementation`のapproveは品質判断だが、mergeにはlive headの一致、required checkの結果、conflictの有無、branch protectionという外形条件が要る。どこまでをreviewの判断とし、どこからをControllerの外形判定とするか。
2. 自分のmergeと外部mergeの区別。現行設計は「PRの外部close/merge」を一律に`external_mutation_conflict`として`needs_human`へ送る。Kudo自身がmergeするようになると、この規則は自分の成功を干渉として誤検出する。
3. merge後の後処理の境界。branch削除、Issue close、release、deployのどこまでを製品責任にするか。
4. 終端の語彙。`ai-review-waiting`は「人間のPR review待ち」を意味する label であり、自動mergeの下では指すべき状態が存在しない。

## Decision

### D1. 自動mergeを正常完了に含め、terminalを`merged`にする

- Runの正常終端phaseは`awaiting_human_review`ではなく`merged`とする。
- Kudoは`merge_pull_request` Operationで、承認済みheadをbaseへmergeし、head branchを削除する。
- Task Issueのcloseと`ai-merged` labelは、確定した`merged`状態からの投影として扱う。
- release、deploy、merge後のrevert判断は製品境界の外に置く。

### D2. mergeのgateは品質verdictと外形条件の合成であり、reviewにmerge判断をさせない

Controllerが`merge_pull_request`を発行できるのは次がすべて成立する場合だけである。

1. `final_implementation`のapproveが、live PR headと一致するfinal headとArtifact Manifestへbindされている。
2. required PR bodyの確定とdraft解除（`finalize_pull_request`）がdurableに記録されている。
3. live PRがopenで、baseがContext Manifestのbaseと一致し、headがapproved headと一致する。
4. base branchのrequired status checkが同じhead SHAに対してsuccessである。
5. GitHubがmergeableを返す（conflictが無い）。

Review Workerはこの判定に関与しない。reviewerが返すのは品質verdictだけであり、「mergeしてよいか」というrepository運用上の判断をmodelへ委譲しない。逆にControllerは、外形条件がすべて揃っても品質approveが無ければmergeしない。

（2026-08-21追記）この評価はControllerがread-onlyのpull request / required status check観測で行う（[Runtime platform](../03_runtime-platform.md)のcredential表がpull requests read、checks readを与える）。`merge_pull_request`はfinalize完了後にenqueueし、外形条件が揃うまで`retry_wait`のまま待機させ、揃った時点でeligibleにする。Issue Workerは実行の開始時にlive contextを、mutation直前にlive PRを再照合し、API側の期待head照合と合わせて評価と実行の間の窓を閉じる。その窓でcheckやprotectionが変化してGitHubがmergeを拒否した場合、Issue Workerは品質verdictへ変換せず、拒否の観測をevidenceとした`needs_human` Resultで返し、Controllerが`merge_blocked`として扱う。

### D3. mergeは承認済みheadへ束縛したcompare-and-mergeで行う

- Issue Workerはmerge要求に期待head SHAを明示的に渡し、GitHub側でheadが一致する場合だけmergeが成立するようにする。branchへのcompare-and-pushと同じ規律である。
- mutation直前のlive照合とAPI側の期待head照合を二重に持つのは冗長ではない。照合とmergeの間に外部pushが入る窓を、Kudo側の再照合だけでは閉じられないためである。
- head不一致は品質verdictでもmerge失敗でもなくstaleとして扱い、再publishと再reviewへ戻す。

### D4. merge commitを使い、squashとrebaseを採用しない

- merge methodは merge commit に固定する。repositoryがmerge commitを許可していない場合はmergeせず`merge_blocked`として停止する。
- RED、GREEN、refactorの各commitはKudoが証跡として固定したものであり、squashはbase側でこのlineageを失わせる。rebaseはSHAを書き換え、approvalがbindしたcommitがbaseに存在しなくなる。
- merge methodをExecution PolicyやIssue Contractのfieldにしない。Taskごとに変えてよい判断ではなく、変えられると「どのTaskのlineageがbaseに残るか」がIssue本文の書き方で決まってしまう。

### D5. 後処理はbranch削除とIssue closeまでとする

- merge成功後、head branchを冪等に削除する。既に存在しない場合はno-opとして成功にする。
- Task Issueをcloseする。PR bodyのclosing keywordでGitHubが先にcloseしていてもよく、その場合は観測してno-opとする。closing keywordはbaseがdefault branchのときだけ効くため、closeの成立をGitHubの副作用に依存させない。
- dependency gateは「依存Issueがcompletedで、その成果物が選択したbase commitへ統合済み」を要求する。自動mergeとIssue closeにより、この二条件が同じ完了点で成立する。

### D6. 自分のmergeと外部mergeはdurable intentで区別する

- `merge_pull_request`のidempotency identity（repository、Run、PR、expected head、operation kind）をmutation前にdurableへ記録する。
- merged状態を観測したとき、記録済みintentがあり、merge commitがexpected headを親に持つならKudo自身のmutationの再観測として扱い、成功へ収束させる。
- intentが無い状態でclosedまたはmergedを観測した場合は、従来どおり`external_mutation_conflict`として`needs_human`へ送る。
- この区別が無いと、merge応答を受け取る前のcrashが自分の成功を外部干渉として誤検出し、成功したRunを人間へ差し戻す。

### D7. merge阻害は品質verdictにせず`merge_blocked`として扱う

| 観測 | 扱い |
| --- | --- |
| required checkがpending | execution failureではなくOperationの`retry_wait`。backoffして再照合し、retry budgetを消費しない。Operationのexecution deadlineを超えたら`merge_blocked` |
| required checkがfailure | `merge_blocked`で`needs_human` |
| conflict（mergeableでない） | `merge_blocked`で`needs_human` |
| branch protectionがmergeを拒否 | `merge_blocked`で`needs_human` |
| merge commitがrepositoryで不許可 | `merge_blocked`で`needs_human` |
| live headがapproved headと不一致 | stale。再publishと再reviewへ戻す |
| intentの無いclosed / merged観測 | `external_mutation_conflict`で`needs_human` |

required checkのfailureを`request_changes`へ読み替えない。reviewerは同じ判断を再実行するだけで、CI failureの原因がKudoの差分にあるとは限らないためである。逆にKudoが自動でrepository設定を緩めることもしない。

### D8. finalize / merge は開始時に live context を再検証する（2026-08-21 追記）

「Run 中に Context Manifest が変われば進行を止める」という不変条件は、live 再構築を行う Operation でしか enforce されない。final approve 後に Issue が意味的に編集される窓を検出できる最後の enforcement point は`finalize_pull_request`と`merge_pull_request`の開始時であり、merge は取り消せない mutation である。したがって両 Operation は model session を持たないが、開始時に [ADR-0006](0006-live-context-reconstruction.md) と同じ live 再構築・Context Manifest 照合を必須とする。不一致は`stale_input`として返し、mutation を行わない。完了時の照合は要求しない。mutation 確定後に stale を検出しても取り消せないためである。`publish_head`は対象外で、publish 後の staleness は次の Review Request の開始時照合が検出する。

## 設計詳細

### 1. Operation `merge_pull_request`

- Required input: approved final head、approved final Review Result、finalize済みPull Request reference、期待head SHA、idempotency identity。
- Output: merge commit SHA、head branch削除の結果、merged状態を含む`pull-request-observation`。
- `preservesHead`である。承認済みheadを進めず、baseへ統合するだけのOperationであり、Result headが入力headと一致しない成功はbinding境界で拒否する。
- `finalize_pull_request`とは別のOperationにする。finalize後のcrashがbody確定をやり直す必要は無く、gate入力（live check、mergeable）とfailure modeも別であるため、同じOperationへ畳むと再試行の単位が粗くなる。

### 2. Phase

```text
awaiting_final_review --approve--> finalizing_pull_request
finalizing_pull_request --PR ready + body finalized--> merging_pull_request
merging_pull_request --merged + branch deleted--> merged
merging_pull_request --> needs_human（merge_blocked / external_mutation_conflict）
```

`merged`はterminalであり、Issue closeと`ai-merged`はこのphaseからの投影である。projection failureは確定した`merged`を巻き戻さない。

### 3. Label

`ai-review-waiting`を廃止し、`ai-merged`を終端labelとする。`ai-merged`はphaseの表示ではなく「approved headがbaseへ入った」という外形事実の投影であり、同時にIssueはcloseされている。`ai-completed`のような汎用の完了labelを置かないという既存方針は維持する。何が起きたかを名前で確定できないlabelは、状態の説明をstatus commentへ押し出すためである。

## Consequences

### 影響を受ける文書

- [01. プロダクト設計](../../01_product-design/) — ゴール、責任、境界、完成条件
- [03. システム仕様](../../03_system-spec/) — アクター、F-07、標準workflow
- [End-to-end workflow](../02_workflow.md) — §7、durable state、外部干渉
- [GitHub routing policy](../04_github-routing.md) — label、transition、escalation code
- [Worker Operation Protocol](../contracts/operation-protocol-v1alpha1.md) — `merge_pull_request` kindとartifact要件
- [Implementation–Review Protocol](../contracts/review-protocol-v1alpha1.md) — approveとmergeの関係、外部merge判定
- [Architecture](../01_architecture.md) — Issue Workerの責務、retry policy
- [Final Implementation Review Policy](../review-policies/final-implementation-v1alpha1.md) — approveの位置づけ
- [4.4 Pull Request 確定・Merge](../../04_features/04_pull-request-handoff/) — 受け入れ要件と詳細設計
- [ADR-0002](0002-pr-anchored-review.md) — D1のterminal記述

### 利点

- 完了がKudo側で閉じ、`dependsOn`のreadinessが人間のmerge操作に律速されない。
- 承認済みheadとmerge対象headの一致がAPI境界で保証され、「reviewしていない変更がbaseへ入る」経路が閉じる。
- mergeの可否がrepository設定（branch protection、required check、base branch）として宣言的に表現され、案件ごとの口頭合意にならない。

### 代償・リスク

- 人間のPR reviewがgateから外れる。誤りはbaseへ入ってから発見され、修正はrevertまたは新しいIssueになる。緩和策はbranch protectionとrequired check、review policyの観点、そしてbase branchをKudo専用に限定できる設定であり、いずれも人間が事前に置く。
- baseが進んだ場合、reviewしたheadとmerge後のbase状態は同じではない。`Require branches to be up to date`を有効にすればprotectionが拒否して`merge_blocked`になるが、Kudoは自動でrebaseもbase mergeも行わない。取り込みは新しいheadを作る行為であり、再reviewを要求するためである。推奨設定と残余riskの正本は[GitHub routing policy](../04_github-routing.md)「Repository 設定の前提と推奨」に置く（2026-08-21追記）。
- `merge_blocked`が新しい定常的な差し戻し理由になる。CIが不安定なrepositoryでは、review roundではなくmerge段階での停止が増える。

### 未決事項（deferred）

- merge queue / 直列化。複数Runが同じbaseへ同時にmergeする場合の順序は、現状GitHub側のconflict検出とprotectionに委ねる。
- base更新への自動追従（auto-rebase、base mergeの自動取り込み）と、その後の再review規則。
- merge後のrevert自動化とrollback判断。
- `ai-merged`後にIssueをreopenして`ai-ready`を付けた場合の再実行。現行どおり暗黙の再実行は行わず、versioned commandを追加する別decisionまで人間が扱う。
