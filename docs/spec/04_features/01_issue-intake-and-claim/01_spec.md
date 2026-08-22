# 4.1. Issue 受付・Contract 検証・Claim 受け入れ要件

[03. システム仕様](../../03_system-spec/) F-01 / F-02 に対する Why / What の受け入れ基準である。
component、adapter の実現方法は [詳細設計](02_design.md) で扱い、protocol field と
canonical encoding は versioned contract を正本とする。

## サブ機能一覧

| ID | サブ機能 | 優先度 |
| --- | --- | --- |
| 4.1.1 | Candidate 検出 | 高 |
| 4.1.2 | Contract 検証 | 高 |
| 4.1.3 | Claim と Context 固定 | 高 |

## 4.1.1. Candidate 検出

**ユーザーストーリー**

- 誰が: Kudo を運用するリポジトリ管理者
- 何を: 実行条件を満たした Task Issue を、通知の欠落や重複にかかわらず検出したい
- なぜ: 実行依頼が取りこぼされず、同じ依頼が二重に開始されないから

**事前条件**

- 対象 repository と GitHub App installation が設定済みである。
- candidate 条件と status label が [GitHub routing policy](../../05_design/04_github-routing.md) で確定している。

**受け入れ基準**

- **正常系: Webhook による検出**
  - Given open、non-PR、assignee `mrbaron3`、label `ai-ready` を満たす Issue がある。
  - When 対応する署名済み webhook を受信する。
  - Then live GitHub state で条件が再確認され、同じ IssueRef の reconciliation が開始される。

- **回復系: Webhook の欠落**
  - Given candidate Issue の webhook が到達していない。
  - When startup または既定15分ごとの polling が実行される。
  - Then webhook と同じ `ReconcileIssue` に IssueRef が渡され、実行候補として検出される。

- **冪等性: 重複通知**
  - Given 同じ Issue に対する webhook と polling が重複している。
  - When 両方の trigger が処理される。
  - Then reconciliation は一つの論理結果へ収束し、writer-capable Run は重複作成されない。

- **候補外: 条件不足**
  - Given Issue が closed、Pull Request、assignee 不一致、または `ai-ready` 未付与のいずれかである。
  - When webhook または polling で発見される。
  - Then candidate 外として終了し、failure、retry、human escalation は発生しない。

- **整合性: Payload と live state の不一致**
  - Given webhook payload では candidate だが、その後 live Issue から `ai-ready` が外れている。
  - When reconciliation が candidate 条件を評価する。
  - Then payload の古い本文や label ではなく live state が採用され、Run は開始されない。

- **候補外: Merge 済み Issue の再依頼**
  - Given `merged` terminal の Run を持つ Issue が reopen され、`ai-ready` が再付与されている。
  - When reconciliation が claimable 条件を評価する。
  - Then 新しい Run は開始されず、`ai-ready` が外れて `ai-merged` が再投影され、再実行には新しい Task Issue
    または versioned command が必要である旨の comment が更新される。

**非機能要件**

- 回復性: webhook を唯一の検出経路にせず、polling で最終的に収束する。
- セキュリティ: webhook は署名検証に成功した場合だけ ingress として受理する。
- 可観測性: trigger 種別、IssueRef、candidate 判定結果、skip 理由を追跡できる。

**完了条件**

- 自動テスト: webhook / polling / 重複通知 / 候補外 / merge済み再依頼 / stale payload を各1ケース以上検証する。
- 証跡: 同一 IssueRef への重複 trigger が一つの reconciliation result に収束することを確認できる。

## 4.1.2. Contract 検証

**ユーザーストーリー**

- 誰が: Kudo に実装を依頼する Task Issue 作成者
- 何を: 目的、範囲、受け入れ条件、authority、検証方法、判断境界を明示した契約として渡したい
- なぜ: model が不足情報を推測せず、意図した範囲だけを実装できるから

**事前条件**

- Candidate 検出を通過した live Task Issue がある。
- 対応する Issue Contract version を Kudo がサポートしている。

**受け入れ基準**

- **正常系: 完全な Contract**
  - Given required section と Contract block が一意に記述され、Acceptance Criteria ID が本文と対応している。
  - When Task Issue を検証する。
  - Then contract が受理され、authority と dependency の解決へ進める。

- **異常系: Missing または曖昧な入力**
  - Given required section の欠落、unknown field、duplicate field、または曖昧な値がある。
  - When strict parse を行う。
  - Then contract rejection となり、不足値を comment、parent、provider conversation から補完しない。

- **異常系: Acceptance Criteria の不整合**
  - Given Contract block が参照する Acceptance Criteria ID に本文がない、または同じ ID が重複している。
  - When traceability を検証する。
  - Then 実行可能な contract として受理されず、該当する不整合が特定される。

- **権限境界: 暗黙 Context の拒否**
  - Given parent、dependency、Issue comment に実装指示があるが、Task の `authorityRefs` に含まれていない。
  - When canonical Task Context を構築する。
  - Then それらの prose は実装入力に含まれず、parent は hierarchy、dependency は readiness gate に限定される。

- **失敗分類: Authority 解決失敗**
  - Given authority reference が競合している、または取得時に一時的な transport failure が起きる。
  - When authority を解決する。
  - Then conflict は human decision、transport failure は retry 対象として区別され、品質 verdict にならない。

**非機能要件**

- 決定性: 同じ Issue body と contract version から同じ validation result を得る。
- セキュリティ: 明示されていない外部 prose や provider memory を authority として使用しない。
- 可観測性: rejection category と対象 section / field を機密情報なしで追跡できる。

**完了条件**

- 自動テスト: valid / missing / unknown / duplicate / ambiguous / AC不整合を fixture で検証する。
- 自動テスト: parent、dependency、comment が暗黙の実装 context に入らないことを検証する。

## 4.1.3. Claim と Context 固定

**ユーザーストーリー**

- 誰が: workflow を調停する Controller
- 何を: 検証済みTask Issueの実行権と期待input digestを一意のRunに固定したい
- なぜ: 中断、retry、並行triggerがあってもlive GitHubから入力を再構築して同一性を確認できるから

**事前条件**

- Candidate 条件と Contract 検証を通過している。
- dependency、authority、base commit を live source から解決できる。

**受け入れ基準**

- **正常系: Claim 成功**
  - Given dependency が完了し、authority と base commit に競合がない。
  - When Issue Worker が live claim evidence を返す。
  - Then Run、Compiler version、Issue Observation / Task Context / Context Manifestのdigest、Execution Policy、base SHAがdurableに固定され、Issue由来のcanonical YAML本文は保存されない。

- **待機系: Dependency 未完了**
  - Given `dependsOn` のいずれかが未完了である。
  - When claim requirements を評価する。
  - Then dependency blocked として記録され、writer-capable Run の実行 phase は開始されない。

- **競合系: Concurrent Claim**
  - Given 同じ IssueRef に対して複数の reconciliation が同時に claim を試みる。
  - When claim が確定される。
  - Then 一つだけが writer-capable Run を取得し、残りは同じ active Run へ収束する。

- **回復系: Status 投影失敗**
  - Given Run と claim が durable に確定した後、GitHub label 更新が一時的に失敗する。
  - When projection を再試行する。
  - Then Run は巻き戻されず、次の reconcile が `ai-in-progress` の記録を冪等に収束させる。

- **異常系: Authority または Base の競合**
  - Given authority の内容が整合しない、または期待 base と live base が一致しない。
  - When claim を確定しようとする。
  - Then claim success とせず、理由と evidence を残して human decision または stale handling へ送る。

**非機能要件**

- 永続性: claim success、期待digest、base、policyはprocess memoryだけに保持しない。raw Issue bodyとcanonical YAML本文はlive sourceから再構築する。
- 冪等性: 同じ IssueRef への再実行で writer-capable Run を増やさない。
- 可観測性: claim identity、input digest、base SHA、blocked / conflict 理由を追跡できる。

**完了条件**

- 自動テスト: claim success / dependency blocked / concurrent claim / projection retry / authority conflict を検証する。
- 障害テスト: durable commit 後かつ GitHub projection 前の停止から同じ Run に収束する。
- 自動テスト: 後続Operationがlive Issueを再compileし、同一digestなら継続、意味的変更ならstaleになることを検証する。

## 参照する正本

- [End-to-end workflow](../../05_design/02_workflow.md) §1〜§2
- [Issue Contract](../../05_design/contracts/issue-contract-v1alpha1.md)
- [Task Context Protocol](../../05_design/contracts/task-context-v1alpha1.md)
- [GitHub routing policy](../../05_design/04_github-routing.md)
