# ADR-0005: 承認済み head の自動 merge と完了 terminal

- Status: accepted（2026-08-21）
- 関連Issue: 未起票（プロダクトゴールの見直しで決定）
- 実装Task: [#62](https://github.com/mrbaron3/kudo/issues/62)（merge_pull_requestと完了投影）、[#60](https://github.com/mrbaron3/kudo/issues/60)（finalize / merge前のready化）
- 関連ADR: [ADR-0002](0002-pr-anchored-review.md)（全review roundをPRへ繋留する判断を前提とする）、[ADR-0003](0003-review-round-limit.md)（自動loopの上限）
- Supersede対象: ADR-0002 D1の「ready化と`ai-review-waiting`投影がhandoff terminalである」、[01. プロダクト設計](../spec/01_product-design/) §5の「Pull Requestのmerge、Issue close」除外

## Context

これまでKudoの正常終端は「PRをready化し、人間のPR reviewへ渡すこと」だった。人間は毎回、独立reviewが既にapproveした差分に対してmergeボタンを押す役割を担う。この一手が入る限り、Runの完了は人間の在席時間に律速され、`dependsOn`の「成果物がbaseへ統合済み」という条件も人間のmerge操作を待つ。

プロダクトゴールに自動mergeを含める判断を採用した。この判断自体は「誰がmergeを決めるか」の変更であり、決めた瞬間に次が未設計になる。

1. mergeのgate。approveは品質判断だが、mergeにはlive headの一致、required checkの結果、conflictの有無、branch protectionという外形条件が要る。どこまでをreviewの判断とし、どこからをControllerの外形判定とするか。
2. 自分のmergeと外部mergeの区別。現行設計は「PRの外部close/merge」を一律に`external_mutation_conflict`として`needs_human`へ送る。Kudo自身がmergeするようになると、この規則は自分の成功を干渉として誤検出する。
3. merge後の後処理の境界。branch削除、Issue close、release、deployのどこまでを製品責任にするか。
4. 終端の語彙。`ai-review-waiting`は「人間のPR review待ち」を意味するlabelであり、自動mergeの下では指すべき状態が存在しない。

## Decision

### D1. 自動mergeを正常完了に含め、terminalを`merged`にする

Runの正常終端phaseは`awaiting_human_review`ではなく`merged`とする。Kudoは`merge_pull_request` Operationで承認済みheadをbaseへmergeし、head branchを削除する。Task Issueのcloseと`ai-merged` labelは確定した`merged`状態からの投影として扱う。release、deploy、merge後のrevert判断は製品境界の外に置く。

### D2. mergeのgateは品質verdictと外形条件の合成であり、reviewにmerge判断をさせない

Controllerが`merge_pull_request`を発行できるのは、final approveのbinding、finalize（body確定とdraft解除）のdurableな記録、live PRのopen / base / head一致、required status checkのsuccess、mergeableの全条件が成立する場合だけである。Review Workerはこの判定に関与しない——reviewerが返すのは品質verdictだけであり、「mergeしてよいか」というrepository運用上の判断をmodelへ委譲しない。逆にControllerは、外形条件がすべて揃っても品質approveが無ければmergeしない。

外形条件の評価はControllerのread-only観測で行い、揃うまで`retry_wait`で待機させる。評価と実行の間の窓でGitHubがmergeを拒否した場合、Issue Workerは品質verdictへ変換せず、拒否の観測をevidenceとした`needs_human` Resultで返し、Controllerが`merge_blocked`として扱う。

### D3. mergeは承認済みheadへ束縛したcompare-and-mergeで行う

merge要求に期待head SHAを明示的に渡し、GitHub側でheadが一致する場合だけmergeが成立するようにする。mutation直前のlive照合とAPI側の期待head照合を二重に持つのは冗長ではない——照合とmergeの間に外部pushが入る窓を、Kudo側の再照合だけでは閉じられない。head不一致は品質verdictでもmerge失敗でもなくstaleとして扱い、再publishと再reviewへ戻す。

### D4. merge commitを使い、squashとrebaseを採用しない

RED、GREEN、refactorの各commitはKudoが証跡として固定したものであり、squashはbase側でこのlineageを失わせ、rebaseはSHAを書き換えてapprovalがbindしたcommitをbaseから消す。repositoryがmerge commitを許可していない場合はmergeせず`merge_blocked`として停止する。merge methodをExecution PolicyやIssue Contractのfieldにしない——Taskごとに変えてよい判断ではなく、変えられると「どのTaskのlineageがbaseに残るか」がIssue本文の書き方で決まってしまう。

### D5. 後処理はbranch削除とIssue closeまでとする

merge成功後、head branchを冪等に削除し、Task Issueをcloseする。closing keywordでGitHubが先にcloseしていてもno-opとして観測する（closing keywordはbaseがdefault branchのときだけ効くため、closeの成立をGitHubの副作用に依存させない）。dependency gateの「依存Issueがcompletedで、成果物がbaseへ統合済み」という二条件が、自動mergeとIssue closeにより同じ完了点で成立する。

### D6. 自分のmergeと外部mergeはdurable intentで区別する

`merge_pull_request`のidempotency identityをmutation前にdurableへ記録し、merged観測時に記録済みintentがあり、merge commitがexpected headを親に持つなら自分のmutationの再観測として成功へ収束させる。intentが無いclosed / merged観測は従来どおり`external_mutation_conflict`として`needs_human`へ送る。この区別が無いと、merge応答を受け取る前のcrashが自分の成功を外部干渉として誤検出し、成功したRunを人間へ差し戻す。

### D7. merge阻害は品質verdictにせず`merge_blocked`として扱う

required checkのpendingは`retry_wait`（retry budgetを消費しない）、checkのfailure・conflict・branch protection拒否・merge commit不許可は`merge_blocked`で`needs_human`、live head不一致はstale、intentの無いclosed/merged観測は`external_mutation_conflict`とする。required checkのfailureを`request_changes`へ読み替えない——reviewerは同じ判断を再実行するだけで、CI failureの原因がKudoの差分にあるとは限らない。逆にKudoが自動でrepository設定を緩めることもしない。

### D8. finalize / mergeは開始時にlive contextを再検証する

「Run中にContext Manifestが変われば進行を止める」という不変条件は、live再構築を行うOperationでしかenforceされない。final approve後にIssueが意味的に編集される窓を検出できる最後のenforcement pointは`finalize_pull_request`と`merge_pull_request`の開始時であり、mergeは取り消せないmutationである。したがって両Operationはmodel sessionを持たないが、開始時に[ADR-0006](0006-live-context-reconstruction.md)と同じlive再構築・照合を必須とする。不一致は`stale_input`として返しmutationを行わない。完了時の照合は要求しない——mutation確定後にstaleを検出しても取り消せない。`publish_head`は対象外で、publish後のstalenessは次のReview Requestの開始時照合が検出する。

## 設計の現在形

本ADRが導入した規範は仕様側を正本とする。

- merge gateの条件・実行手順・外部干渉の扱い: [workflow.md](../spec/05_design/02_workflow.md)「8. Merge と完了投影」
- `merge_pull_request`のOperation契約（`preservesHead`、必須output、intent再確認）: [operation-protocol-v1alpha1.md](../spec/05_design/contracts/operation-protocol-v1alpha1.md)
- label（`ai-merged`）、reason code、repository設定の前提と推奨: [github-routing.md](../spec/05_design/04_github-routing.md)
- finalize / merge gateの詳細設計: [04_features/04](../spec/04_features/04_pull-request-handoff/02_design.md)
- approveとmergeの関係: [review-protocol-v1alpha1.md](../spec/05_design/contracts/review-protocol-v1alpha1.md)「Gate semantics」

## Consequences

### 影響を受ける文書

01_product-design、03_system-spec、workflow.md、github-routing.md、operation-protocol-v1alpha1.md、review-protocol-v1alpha1.md、architecture.md、final-implementation-v1alpha1.md、04_features/04、ADR-0002（D1のterminal記述）。

### 利点

- 完了がKudo側で閉じ、`dependsOn`のreadinessが人間のmerge操作に律速されない。
- 承認済みheadとmerge対象headの一致がAPI境界で保証され、「reviewしていない変更がbaseへ入る」経路が閉じる。
- mergeの可否がrepository設定として宣言的に表現され、案件ごとの口頭合意にならない。

### 代償・リスク

- 人間のPR reviewがgateから外れる。誤りはbaseへ入ってから発見され、修正はrevertまたは新しいIssueになる。緩和策はbranch protection、required check、review policyの観点、base branchをKudo専用に限定できる設定であり、いずれも人間が事前に置く。
- baseが進んだ場合、reviewしたheadとmerge後のbase状態は同じではない。Kudoは自動でrebaseもbase mergeも行わない——取り込みは新しいheadを作る行為であり、再reviewを要求する。推奨設定と残余riskは[github-routing.md](../spec/05_design/04_github-routing.md)「Repository 設定の前提と推奨」を正本とする。
- `merge_blocked`が新しい定常的な差し戻し理由になる。CIが不安定なrepositoryでは、review roundではなくmerge段階での停止が増える。

### 未決事項（deferred）

- merge queue / 直列化。複数Runが同じbaseへ同時にmergeする場合の順序は、現状GitHub側のconflict検出とprotectionに委ねる。
- base更新への自動追従（auto-rebase、base mergeの自動取り込み）と、その後の再review規則。
- merge後のrevert自動化とrollback判断。
- `ai-merged`後にIssueをreopenして`ai-ready`を付けた場合の再実行。versioned commandを追加する別decisionまで人間が扱う。

## Revisit conditions

- 複数Runの同一baseへの同時mergeで、GitHub側のconflict検出とprotectionに委ねる現状が実測で問題化した場合、merge queue / 直列化を別ADRで設計する。
- `merge_blocked`による停止がreview roundの差し戻しより支配的になった場合、base追従の自動化（と再review規則）を別ADRで検討する。
- 人間reviewをgateから外した代償（merge後に発見される誤り）が、branch protectionとreview policyで緩和しきれない頻度で観測された場合、terminalの定義を再検討する。
