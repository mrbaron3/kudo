# ADR-0007: 縦の貫通sliceをdelivery orderの単位とする

- Status: accepted（2026-08-19、Epic構成への反映を2026-08-21追記）
- 関連Issue: [#13](https://github.com/mrbaron3/kudo/issues/13)、[#14](https://github.com/mrbaron3/kudo/issues/14)、[#15](https://github.com/mrbaron3/kudo/issues/15)、[#16](https://github.com/mrbaron3/kudo/issues/16)、[#17](https://github.com/mrbaron3/kudo/issues/17)、[#20](https://github.com/mrbaron3/kudo/issues/20)、[#21](https://github.com/mrbaron3/kudo/issues/21)、[#22](https://github.com/mrbaron3/kudo/issues/22)、[#24](https://github.com/mrbaron3/kudo/issues/24)、[#29](https://github.com/mrbaron3/kudo/issues/29)
- Supersede対象: なし。[implementation-plan.md](../spec/06_project/01_implementation-plan.md)のMilestone 3〜6を完成の定義としては維持し、実行順序の単位としては置き換える
- 前提となるADR: [ADR-0001](0001-compose-runtime.md)（Compose runtime）、[ADR-0002](0002-pr-anchored-review.md)（PR-anchored review）

## Context

2026-08-19時点（`5815cdf`）の非テストGo行数は次のとおりだった。

| package | 非テスト行数 | 割合 | テスト行数 |
| --- | ---: | ---: | ---: |
| `internal/contract` | 4,579 | 73% | 4,857 |
| `internal/workflow` | 768 | 12% | 584 |
| `internal/adapter/postgres` | 920 | 15% | 84 |
| `cmd/kudo` | 36 | 1% | 60 |

コードの73%が「canonical encodingとdigestとvalidation」に費やされる一方、GitHub adapter、provider adapter、Artifact Store、Run workspace、Issue Worker、Review Worker、Controllerはいずれも未実装であり、**Kudoは自身のIssueを1件もPull Requestにしたことがなかった**。11個のversioned protocolはv1alpha1として完成度高く作り込まれていたが、製品境界そのものはまだ動く可能性があり（「executionコンテキストだけを切り出したものは望むものではない」という指摘が未決着）、動きうる境界に対して外部consumerを持たないalpha protocolを先に完成させた状態にあった。層ごとに横へ厚く作るdelivery orderが、この「文書の完成」と「コードの完成」の混同を構造的に許していた。

この判断はcontract層への否定ではない。canonical encoding、digest規則、identity binding、staleness判定は、後から入れると過去のartifactとdigestが再現しなくなる種類の設計であり、先に作ったこと自体は合理的である。結論は「**contract層はもう十分であり、これ以上磨かず動くものへ接続すべき**」の一点である。

決定に先立ち、次の3点を実測で確認した。

- **GitHub adapter最小形**: claim貫通に新規で必要なのは薄いread client、`TokenSource` seam、candidate filter、claim use caseの4つだけで、parse・canonical artifact・identity・Run永続化・state machineは既存3 packageで揃っている。public repositoryのIssue/content/commit readは未認証で成立する。
- **provider adapter最小形**: codex 0.147.0とclaude 2.1.226の両方がheadless + JSON Schema制約付きstructured outputで動作する。同時に、project doc auto-discoveryを無効化しないと対象repositoryの`AGENTS.md` / `CLAUDE.md`がsessionへ黙って入る（codexで+8,897 input token、claudeで`cache_creation`が3,769→75,263 token）。
- **Artifact StoreとRED evidence最小形**: 貫通する3 sliceが踏むoperation kindの必須outputは合計8本のlogical nameで足りる。ただし`test-plan` / `red-evidence` / `source-bundle`は`artifactKindRules`に未登録で、現行validationに弾かれる（contract空白）。

## Decision

### D1. delivery orderの単位を層ではなく縦の貫通sliceにする

Milestoneごとに層を完成させるのをやめ、「Issue 1件がPull Requestになる」という1本の経路を、各層の最小部分だけを繋いで先に通す。sliceはS1（claim）→ S2（RED evidence）→ S3（draft PR publish）→ S4（test review 1 round）→ S5（final reviewとready化）の5本とし、**S3到達をもって製品境界の疑義を実物に対して再評価する**ことが本ADRの主目的である。S4以降は貫通後の話であり、S3の結果によって順序を組み直してよい。

各sliceの範囲（作るもの・意図的に雑にするもの・落とさないもの・未決事項）の現在形は[implementation-plan.md](../spec/06_project/01_implementation-plan.md)のDelivery order節が正本である。引き継がれるのは実装コードとartifactであってRun instanceではない——provider adapter実装でExecution Policy digestが変わるとS1のRunは`SemanticInputChanged`でsupersedeされる。これは仕様どおりの挙動として受け入れる。

### D2. contract層をfeature freezeする

`internal/contract`への変更は、(1) 貫通の実装が実際に詰まった（既存の型・語彙で表現できない成果物がある）、(2) 実装と同時に文書・parser・fixture・testを更新できる、のいずれかを満たすときだけ行う。「まだ足りていない気がする」「対称性が欠けている」を理由にした追加はしない。既知のcontract空白（[implementation-plan.md](../spec/06_project/01_implementation-plan.md)「貫通で必ず踏むcontract空白」）はfreezeの例外とし、踏んだ実装PRの中で埋める。

### D3. 「雑にする」ことを各stepで明示的に宣言する

各sliceで「何を作るか」と「何を意図的に雑にするか」の両方を書き、暗黙の手抜きを作らない。雑にしたものは`05_design/01_architecture.md`や`05_design/contracts/`へ書かず、実装PRと[Evaluation harness — deferred](../spec/06_project/04_evaluation-harness.md)へ記録する。temporary shortcutをtarget architectureとして文書化しないという既存のdelivery ruleをここでも守る。

### D4. sliceを跨ぐIssueは分割し、dependency宣言をsliceの順序へ合わせる

`dependsOn`はKudo自身が読むreadiness gateであり、実作業順と食い違ったまま放置すると、Kudoが自分自身を動かす段階で必ず矛盾する。S1が「`dependsOn`が非空ならclaimしない」と決めている以上、この整合は代償ではなく貫通のhard prerequisiteである。

食い違いには2種類あり、直し方が違う。(a) 後回しにするIssueへの依存は宣言を付け替える。(b) 1つのIssueが2つのsliceにまたがる場合はIssueを分割する——依存の付け替えでは解けない。実在する依存（例: finalizeがfinal approveを要すること）を`dependsOn`の付け替えで隠すのは誤りであり、正しい診断は「Issueの粒度がsliceより粗い」である。`dependsOn`を実装側から黙って書き換えず、分割と付け替えはIssue所有者が行う。

### Milestone計画との関係

Milestone 3〜6をsupersedeしない。deliverableとexit criteriaは完成の定義として維持し、変わるのは実行順序の単位だけである。各Milestoneはあるsliceで開始され、幅を戻す段で完了する。「Milestone Nが完了した」と言えるのはexit criteriaを全部満たしたときだけであり、貫通到達はそれを意味しない。未達分は[implementation-plan.md](../spec/06_project/01_implementation-plan.md)の台帳で追跡する。

**Epicはmilestoneではなくsliceに対応させる（追記 2026-08-21）。** 当初この決定を欠いており、Epic構成が未更新のまま残った。作業単位はEpicであり、実行順序の単位がsliceである以上、両者が食い違うとEpic単位の作業が成立しない。S1〜S3をEpicとして新設し、milestone Epicは完成の定義と未達台帳として残す。したがってEpicとmilestoneは1対1ではなく、milestoneの進捗はimplementation-plan.mdの未達表を正とする。Task Issueが必ず1つのEpicに属する規律は変えない。Epic構成の現在形は[implementation-plan.md](../spec/06_project/01_implementation-plan.md)のEpic構成節が正本である。

## Alternatives

- **層ごとのmilestone順（現状維持）**: 「Issueが1件もPRになっていない」という最大のリスクを解消する時点が最も遅い。文書とコードの完成の混同を構造的に許し続けるため退けた。
- **contract層の追加整備を先に完了させる**: 動きうる製品境界に対して外部consumerを持たないprotocolを磨き続けることになる。貫通で「実際に詰まった箇所」を根拠に変更するほうが精度が高い。
- **S1〜S5を1つの大きな貫通として一気に通す**: S3（人間の見えるPR）で製品境界を再評価する機会を失う。S4以降の内訳はS3の実測を見てから決めるほうが手戻りが小さい。

## Consequences

- 実行順序の正本が[implementation-plan.md](../spec/06_project/01_implementation-plan.md)のDelivery order節になり、slice詳細・落とさないもの・contract空白・未決事項はそこで管理される。
- 6件のIssueの`dependsOn`付け替えと2件のIssue分割（#16、#29）が貫通開始前に必要になる（D4）。readiness gateの意味が一時的に弱まる。
- 貫通で作るRunは捨てる前提になる。最初のRunが「本物の履歴」にならないことは受け入れる。
- Milestone exit criteriaの一部が長期間未達のまま残る。台帳の追跡が形骸化すると「動いたから完成」という誤読が起きる。これが本ADRが導入する最大のリスクである。
- 層ごとの品質が非対称になる（contract層はテスト充実、adapter層は薄いtestで開始）。reviewで「同じ厚さ」を期待しない合意が要る。
- live検証には`assignee` + `ai-ready` + `readiness: ready`を満たす検証用Issueが別途要る。既存Issueに`ai-ready`を付けると本番相当のclaimが走る。
- 更新される文書: [implementation-plan.md](../spec/06_project/01_implementation-plan.md)（Delivery order節にslice定義・落とさないもの・contract空白・未決事項・Epic構成を置く）、対象Issue群（scope絞り込みと分割）。

## Revisit conditions

次のいずれかが成立した場合、本ADRを新しいADRで再検討する。

- S3（draft PR publish）へ到達しても製品境界の疑義が解消しない。この場合、貫通の対象そのものを再定義する必要がある。
- 貫通がS1〜S3で到達できず、原因が**contract層の不足**であると判明した。この場合はD2のfeature freezeを解く。
- 貫通の過程で**使われないcontractが体系的に見つかった**。何を削るかを別ADRで決める（alphaで外部consumerを持たないため削除は可能である）。
- provider CLIのheadless契約（structured output、project doc無効化flag、state directory env）が上流変更で壊れ、session isolationがCLI flagでは実現できなくなった。
- 幅を戻す段で、exit criteria未達台帳が追跡不能な規模へ膨らんだ。sliceの薄さが過剰であったことを意味する。

## References

- [Implementation plan](../spec/06_project/01_implementation-plan.md)（slice定義とEpic構成の正本）
- [Architecture](../spec/05_design/01_architecture.md)
- [GitHub routing](../spec/05_design/04_github-routing.md)
- [Runtime platform](../spec/05_design/03_runtime-platform.md)
- [Issue Contract v1alpha1](../spec/05_design/contracts/issue-contract-v1alpha1.md)
- [Operation Protocol v1alpha1](../spec/05_design/contracts/operation-protocol-v1alpha1.md)
