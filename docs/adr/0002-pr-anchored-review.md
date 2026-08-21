# ADR-0002: PR-anchored review と Review Worker 設計

- Status: accepted（2026-08-18）
- 関連Issue: [#25](https://github.com/mrbaron3/kudo/issues/25)、[#28](https://github.com/mrbaron3/kudo/issues/28)、[#29](https://github.com/mrbaron3/kudo/issues/29)、[#43](https://github.com/mrbaron3/kudo/issues/43)、[#44](https://github.com/mrbaron3/kudo/issues/44)、[#47](https://github.com/mrbaron3/kudo/issues/47)
- 実装Task: [#49](https://github.com/mrbaron3/kudo/issues/49)（本ADRの確定とprotocol/workflow改訂）
- Supersede対象: [migration-from-servo.md](../spec/06_project/03_migration-from-servo.md)「New Kudo decisions」の「PR作成前のfinal implementation review gate」、および[workflow.md](../spec/05_design/02_workflow.md)旧§6–7のPR作成順序
- Supersedeされた箇所: D1のterminal記述は[ADR-0005](0005-auto-merge.md)が置き換える。ready化はhandoff terminalではなくmergeの前提段階になった
- Supersedeされた箇所: Issue freshnessの保存済みObservation照合は[ADR-0006](0006-live-context-reconstruction.md)がlive再compileへ置き換える

## Context

決定時点のworkflowはfinal implementation reviewのapproveをPull Request作成の前提gateとし、PRは正常handoffの終端でだけ作られていた。一方でServoのレビューは全観点を毎回評価するpanel方式であり、Kudoは#47のversioned policyで常時必須観点と条件付き観点を分離した。しかし次の3点は未設計だった。

1. レビューを起動・繋留する単位。Controller内部のRun状態だけが起点で、人間はfinal approveまで何も見えない。
2. 条件付き観点の適用可否を「誰が・いつ・どう」判定するか。policyは適用条件を定義したが、判定の実行主体と監査可能性は決めていない。
3. `request_changes`後の再reviewラウンドで評価範囲をどう扱うか。

製品判断として「レビューの起点はPRとする」を採用し、上記3点を同時に決めた。

## Decision

### D1. 全review roundをPRへ繋留する

- Issue WorkerはRED evidence固定後、branchをpushしdraft Pull Requestを冪等に作成する。
- `test_validity`と`final_implementation`の全Review Requestは、このPRとそのhead SHAへ繋留される。
- final approveはPR作成ではなく、PR bodyの確定とdraft→ready遷移をgateする（terminalは[ADR-0005](0005-auto-merge.md)が`merged`へ置き換えた）。
- PR mutationの権限はIssue Workerだけが持つ現行のmutation authorityを変更しない。Review WorkerはPRのread権限だけを追加で得る。

人間がPR timelineで全過程（test → RED → 実装 → review round）を追跡でき、レビューの起点・round・staleness判定が「PRへのpublish」という単一の観測可能な単位に揃うことが理由である。RED状態のdraft PRが公開されるが、これはTDDの位相の正直な表示であり隠すべき異常ではない。

### D2. 観点の適用可否はreview sessionが判断し、Resultへ構造化して残す

- 事前の観点選択stage（rule classifierや別のmodel呼び出し）を置かない。品質を判定するのと同じfresh sessionが、policyの適用条件表を正本として適用可否の判断と適用観点の評価を行う。
- Review Resultは全条件付き観点についてapplicability宣言（`applicable`、機械判定可能な`reason` code、`evidenceRefs`）を必須とし、宣言が欠けたResultはbinding境界で受理しない。宣言はreviewer自身の判断であり、handlerやControllerが代筆・補完しない。
- 決定論に残すのは機械的に検証できる部分だけとする（判断入力のdigest固定、宣言の形式検証、bound宣言時の測定evidenceの数値照合）。適用判断の再計算可能性は要求せず、監査要件は「何をなぜ評価対象外としたか」が構造化されてevidenceへ辿れることで満たす。
- 性能の**測定**はreviewの中で実行しない。測定は本質的に非決定的であり、read-onlyかつimmutable inputからverdictを導くreviewの再現性を壊すためである。測定はRED/GREENと同じくIssue Worker側のevidenceパターンに従う。

### D3. 再reviewラウンドで観点を縮小しない

各roundは、そのroundの適用判断で適用となった観点をすべて再評価し、前roundの観点別結果を持ち越さない。コスト削減はD2の適用判定と、修正roundのdiffが自然に小さいことで得る。

## Alternatives

いずれも本決定の過程で検討し退けた。

- **事前の観点選択stage（rule classifier / 別model呼び出し）**: 適用可否は変更の意味に対する判断であり、path patternやfile classの代理変数へ写像すると判断の質が落ち、判定主体が分裂して監査も難しくなる。
- **観点ごとのsession fan-out（Servo方式）**: session数がround×観点で増える。1 fresh sessionが適用判断と全適用観点を評価すれば、典型的なbackend-only Taskのfinal reviewは10観点でなく6観点で済み、除外理由も機械検証可能な形で残る。
- **delta-scoped再review / 観点別verdict cache**: 前round結果の持ち越しはfresh session isolationを壊し、cross-cutting regressionを見逃す。計測でreview costが支配的と判明した場合に別ADRで再検討する（Revisit conditions）。

## 設計の現在形

本ADRが導入した設計の規範は仕様側を正本とする。

- workflow上の順序・publish・state遷移・gate semantics: [workflow.md](../spec/05_design/02_workflow.md) §3〜§7
- Review Requestの`pullRequest` identity、`pullRequestObservation` lineage、applicability宣言、staleness規則: [review-protocol-v1alpha1.md](../spec/05_design/contracts/review-protocol-v1alpha1.md)
- `publish_head` / `finalize_pull_request`のOperation契約とcompare-and-push: [operation-protocol-v1alpha1.md](../spec/05_design/contracts/operation-protocol-v1alpha1.md)
- 条件付き観点の適用条件とperformance測定evidence規則: [final-implementation-v1alpha1.md](../spec/05_design/review-policies/final-implementation-v1alpha1.md)
- Review Workerのhandler pipeline: [architecture.md](../spec/05_design/01_architecture.md)「Review Worker」

## Consequences

### 影響を受ける文書・Issue

| 対象 | 変更 |
| --- | --- |
| workflow.md | §3以降の順序（publish挿入、PR作成時点、finalize/ready化）、state図、gate semantics |
| architecture.md | Issue Worker（早期publish責務）、Review Worker（PR read、handler pipeline、宣言完全性検証） |
| contracts/review-protocol-v1alpha1.md | Request field追加、Result applicability宣言、staleness規則、PR observation schema |
| migration-from-servo.md | 「PR作成前のfinal review gate」のsupersede追記 |
| review-policies/final-implementation-v1alpha1.md | Performance適用条件と測定evidence規則 |
| #25 / #28 / #29 / #43 / #44 | 実装scopeの更新（handler pipeline、publish再定義、logical name語彙、terminal taxonomy） |

### 利点

- 人間がPR timelineで全過程を追跡でき、needs_human時の文脈がPRに揃う。
- 観点別の適用判断が理由とevidence付きで構造化されて残り、Servoの「毎回全観点」の深掘りコストをsession内の適用判断で削る。

### 代償・リスク

- RED状態のdraft PRが公開され、通知やCI失敗表示のノイズが増える。draft状態とbodyのphase表示で緩和する。
- PRという外部干渉面（人間push、close、base変更）がRun中に増える。compare-and-push、live observation、staleness規則で防御するが、reconciliationの検証項目は増える。
- review pathのGitHub API依存が増え、可用性への感度は上がる。
- 観点の適用判断がattempt間で揺れうる。verdictとfinding自体が既にmodel由来で揺れる前提の契約であり、宣言の形式検証とevidence規律で監査する。

### 未決事項（deferred）

- **CI check runsのcorroborating oracle化**: verdict入力の決定論を壊さない形の設計が必要。v1ではreviewの入力にしない。
- **performance measurement harnessの標準化**: 本ADRはevidenceの位置づけだけを決める。
- **findingのPR comment projection**: projection authorityの拡張になるため別decisionとする。
- **決定論的fact抽出のevidence化**: 適用判断の材料をdeterministicに抽出する拡張。判断は引き続きsessionが行う。
- **汎用PR reviewer（人間作成PRのreview）**: PRからTask Context / Issue Contractを解決する契約が別途必要。本ADRのscope外。

## Revisit conditions

- 計測でreview costが支配的と判明した場合、delta-scoped再review / 観点別verdict cacheをcross-cutting regression riskの評価とあわせて別ADRで再検討する。
- 観点の適用判断のattempt間の揺れが、宣言の形式検証とevidence規律では監査しきれない頻度で観測された場合、事前の決定論的fact抽出（未決事項）の導入を再検討する。
