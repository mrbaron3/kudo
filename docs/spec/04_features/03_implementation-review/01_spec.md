# 4.3. Implementation・GREEN・Final Review 受け入れ要件

[03. システム仕様](../../03_system-spec/) F-05 / F-06 に対する Why / What の受け入れ基準である。
implementation lane、evidence binding、review routing の実現方法は [詳細設計](02_design.md) で扱う。

## サブ機能一覧

| ID | サブ機能 | 優先度 |
| --- | --- | --- |
| 4.3.1 | Implementation | 高 |
| 4.3.2 | GREEN と Refactor | 高 |
| 4.3.3 | Final Review | 高 |

## 4.3.1. Implementation

**ユーザーストーリー**

- 誰が: Kudo に実装を依頼した Task Issue 作成者
- 何を: 承認済み test を満たす最小限の production implementation を作ってほしい
- なぜ: 合意した期待 behavior を変えずに、要求へ直接対応する変更を得られるから

**事前条件**

- Test Validity Review が exact test-only head を `approve` している。
- approved Result、canonical Task Context、Context Manifest、current head が固定されている。

**受け入れ基準**

- **正常系: 承認済み Test に対する実装**
  - Given test validity approval と同一の test が Run worktree にある。
  - When fresh `implement` session を開始する。
  - Then Acceptance Criteria を満たす production code が追加され、承認済み test 自体は変更されない。

- **順序制約: Approval 前の実装防止**
  - Given test validity Result が未確定、`request_changes`、`needs_human`、または stale である。
  - When implementation を開始しようとする。
  - Then `implement` は dispatch されず、production code は変更されない。

- **差し戻し: Test 変更が必要**
  - Given implementation 中に、承認済み test の誤りまたは不足が判明する。
  - When test を変更する必要性が報告される。
  - Then implementation session では test を書き換えず、test authoring / review gate へ戻る。

- **隔離: Fresh Implementation Session**
  - Given test authoring と review が以前に完了している。
  - When `implement` を開始する。
  - Then transcript や private memory を引き継がず、明示された Result と artifact だけを入力にする。

- **範囲: Scope 外変更の防止**
  - Given Task Context が変更対象と非対象を明示している。
  - When production implementation を作る。
  - Then 差分は Scope と Acceptance Criteria に必要な範囲に限定される。

**非機能要件**

- 追跡性: implementation input から approved test Result と Task Context を辿れる。
- 隔離性: provider session の継続に依存せず、commit と artifact から再構築できる。
- セキュリティ: Issue Worker の権限を Task の repository / Run workspace に限定する。

**完了条件**

- 自動テスト: approval gate / test不変 / test gateへの差し戻し / fresh session を検証する。
- 証跡: implementation patch と入力にした approved Result が同じ Run lineage から参照できる。

## 4.3.2. GREEN と Refactor

**ユーザーストーリー**

- 誰が: Pull Request を確認する人間 reviewer
- 何を: 実装が test を通し、整理後も behavior と required checks を維持している証拠を見たい
- なぜ: 一時的に test が通っただけでなく、提出 head の品質を判断できるから

**事前条件**

- Production implementation が作られている。
- 対象 test、Issue Verification、repository required checks が識別できる。
- Task Contextまたはauthorityにperformance boundが宣言されているか判定でき、宣言時はTask固有の測定commandと実行条件が識別できる。

**受け入れ基準**

- **正常系: GREEN**
  - Given 承認済み test と production implementation が同じ head にある。
  - When 対象 test と必要な regression test を実行する。
  - Then すべて成功し、command、result、environment、head が GREEN evidence として固定される。

- **正常系: Behavior を保つ Refactor**
  - Given 対象 test が GREEN になっている。
  - When 重複、命名、構造を整理する。
  - Then 同じ test と regression test が引き続き成功し、observable behavior は変わらない。

- **検証: Required Checks**
  - Given refactor 後の final candidate head がある。
  - When Issue Verification と repository required checks を実行する。
  - Then すべての required command result が final head へ bind される。

- **条件付き証跡: Performance Bound**
  - Given Task Contextまたはauthorityにperformance boundが宣言されている。
  - When final candidate headを検証する。
  - Then Task固有の測定commandを固定した条件で実行し、command、条件、環境identity、複数回実行の要約、boundとの比較を`performance-evidence`としてfinal headへbindする。

- **異常系: Test または Check Failure**
  - Given 対象 test、regression test、required checks のいずれかが失敗する。
  - When final review の前提を検証する。
  - Then final review は開始されず、failure evidence をもとに implementation lane で修正される。

- **証跡: Final Head の Publish**
  - Given GREEN、refactor 後の再検証、required checks がすべて揃っている。
  - When `publish_head` を実行する。
  - Then exact final head が同じ draft Pull Request へ公開され、PR observation が保存される。

**非機能要件**

- 再現性: evidence は command と environment identity を含む。
- 完全性: final review に必要な evidence の欠落を許可しない。
- 冪等性: final head の再 publish で Pull Request を重複作成しない。
- 境界: performance boundが宣言されていないTaskに標準測定harnessを推測して必須化しない。

**完了条件**

- 自動テスト: GREEN / refactor後の再検証 / check failure / evidence不足に加え、bound宣言時の`performance-evidence`必須化を検証する。
- 証跡: final head、GREEN、required checks、条件付き`performance-evidence`が一つの Artifact Manifest へ bind される。

## 4.3.3. Final Review

**ユーザーストーリー**

- 誰が: Pull Request を引き継ぐ人間 reviewer
- 何を: final implementation を独立 reviewer が統一された品質基準で評価した結果を受け取りたい
- なぜ: correctness だけでなく、回帰、scope、security、evidence を確認した状態から人間 review を始められるから

**事前条件**

- final head が draft Pull Request へ publish され、required evidence が固定されている。
- final implementation policy を含む supported Review Request が作成されている。

**受け入れ基準**

- **正常系: Approve**
  - Given live Issue / PR と Request が一致し、必須観点に blocking finding がない。
  - When Review Worker が exact final head を評価する。
  - Then applicability 宣言を含む `approve` Result が固定され、Pull Request handoff gate が開く。

- **修正系: Request Changes**
  - Given 自動修正可能な correctness、regression、scope、quality、security、evidence の finding がある。
  - When review が完了する。
  - Then versioned `request_changes` Result が fresh `repair_implementation` session へ渡される。

- **再評価: Repair 後の Head**
  - Given `request_changes` に対応して production code が修正された。
  - When checks と publish が完了する。
  - Then 新しい head と Artifact Manifest に対する別の final review が必須となる。

- **停止系: Needs Human**
  - Given authority conflict または自動化の判断境界を越える residual risk がある。
  - When review が完了する。
  - Then `needs_human` Result が固定され、Pull Request は ready for review にされない。

- **鮮度: Review Input の変更**
  - Given head、artifact、Context Manifest、Execution Policy、policy ref のいずれかが review 中に変わる。
  - When Result binding を検証する。
  - Then 以前の approval は stale となり、新しい input へ移し替えられない。

- **失敗分類: Review 実行失敗**
  - Given transport failure、unsupported protocol、invalid Result が発生する。
  - When Controller が結果を処理する。
  - Then 品質 verdict には変換されず、retry または protocol failure として扱われる。

**非機能要件**

- 独立性: Review Worker は implementation workspace、write credential、provider session を共有しない。
- 一貫性: required / conditional perspective は versioned policy に従う。
- 可観測性: finding、applicability、policy ref、対象 digest を Result から追跡できる。

**完了条件**

- 自動テスト: approve / request_changes / repair後再review / needs_human / stale を検証する。
- 境界テスト: Review Worker が source、branch、Pull Request を変更できないことを確認する。

## 参照する正本

- [End-to-end workflow](../../05_design/02_workflow.md) §5〜§6
- [Implementation–Review Protocol](../../05_design/contracts/review-protocol-v1alpha1.md)
- [Final Implementation Review Policy](../../05_design/review-policies/final-implementation-v1alpha1.md)
- [ADR-0002](../../05_design/decisions/0002-pr-anchored-review.md)
