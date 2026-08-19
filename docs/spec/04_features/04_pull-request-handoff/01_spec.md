# 4.4. Pull Request 確定・人間への Handoff 受け入れ要件

[03. システム仕様](../../03_system-spec/) F-07 に対する Why / What の受け入れ基準である。
finalize gate、Pull Request mutation、status projection の実現方法は [詳細設計](02_design.md) で扱う。

## サブ機能一覧

| ID | サブ機能 | 優先度 |
| --- | --- | --- |
| 4.4.1 | Finalize 前検証 | 高 |
| 4.4.2 | Pull Request 確定 | 高 |
| 4.4.3 | 人間への Handoff | 高 |

## 4.4.1. Finalize 前検証

**ユーザーストーリー**

- 誰が: Pull Request を引き継ぐ人間 reviewer
- 何を: 承認と検証の対象が、実際に review する Pull Request head と一致していてほしい
- なぜ: 承認後に差し替えられた未評価の変更を review ready と誤認しないため

**事前条件**

- Test Validity Review と Final Review がいずれも `approve` である。
- final head、required checks、Pull Request reference が durable state に固定されている。

**受け入れ基準**

- **正常系: Identity 一致**
  - Given final approval、required checks、live Pull Request が同じ head / base / repository を指している。
  - When finalize gate を評価する。
  - Then `finalize_pull_request` を実行できる状態になる。

- **鮮度: Head の不一致**
  - Given final approval 後に Pull Request branch へ別の commit が push されている。
  - When live head を照合する。
  - Then finalize は実行されず、以前の approval は現在 head に再利用されない。

- **鮮度: Base または PR Identity の不一致**
  - Given Pull Request の base または対象 Pull Request が Run に固定した値と違う。
  - When finalize gate を評価する。
  - Then stale として停止し、自動的に期待値へ書き戻さない。

- **外部干渉: Close または Merge 済み**
  - Given Pull Request が人間または外部 automation により close / merge されている。
  - When live state を取得する。
  - Then 品質 verdict に変換せず、理由と観測結果を添えて human escalation へ送る。

- **異常系: Required Evidence 不足**
  - Given GREEN、required checks、またはいずれかの Review Result が不足している。
  - When finalize 前提を検証する。
  - Then Pull Request body 更新と draft 解除は行われない。

**非機能要件**

- 決定性: 同じ durable input と live observation から同じ gate result を得る。
- 安全性: mismatch を自動 mutation で解消しない。
- 可観測性: gate に使用した head、base、PR identity、evidence digest を追跡できる。

**完了条件**

- 自動テスト: identity一致 / head不一致 / base不一致 / close / evidence不足を検証する。
- 証跡: finalize decision と根拠にした live Pull Request observation が関連付けられている。

## 4.4.2. Pull Request 確定

**ユーザーストーリー**

- 誰が: Pull Request を引き継ぐ人間 reviewer
- 何を: 要件、変更、検証、独立 review、残存リスクが整理された Pull Request を受け取りたい
- なぜ: 証跡を複数の session やログから集めずに、人間 review を開始できるから

**事前条件**

- Finalize 前検証を通過している。
- Issue Worker が expected Pull Request observation と required artifact を受け取っている。

**受け入れ基準**

- **正常系: Required Body の確定**
  - Given final head に bind された Task Context、test plan、evidence、Review Result がある。
  - When Issue Worker が Pull Request を finalize する。
  - Then Task Issue、Acceptance Criteria、RED / GREEN / checks、両 Review Result、residual risk、Run / base / head が body に含まれる。

- **正常系: Draft 解除**
  - Given required body が確定し、live head が approval と一致している。
  - When finalize mutation を実行する。
  - Then 同じ Pull Request の draft が解除され、人間が review 可能な状態になる。

- **権限: Mutation Role**
  - Given Controller または Review Worker が finalize 相当の処理を行おうとする。
  - When Pull Request mutation authority を検証する。
  - Then 拒否され、Issue Worker だけが body と draft state を変更できる。

- **冪等性: 結果不明後の再試行**
  - Given mutation request が timeout し、GitHub 上では既に desired state が成立している。
  - When 同じ logical finalize Operation を再試行する。
  - Then 新しい Pull Request や重複更新を作らず、既存の desired state を成功として確認する。

- **外部干渉: Expected State の変化**
  - Given finalize Operation の発行後に live head または base が変わっている。
  - When mutation 直前に再照合する。
  - Then body 更新や draft 解除を行わず、stale result を返す。

**非機能要件**

- 冪等性: retry が一つの Pull Request と desired state へ収束する。
- 追跡性: body の各 evidence reference から immutable artifact を取得できる。
- セキュリティ: Pull Request write credential を Issue Worker だけに与える。

**完了条件**

- 自動テスト: body生成 / draft解除 / role拒否 / timeout再試行 / mutation直前stale を検証する。
- デモ: final approval 済み draft Pull Request が、証跡付きで ready for review になる。

## 4.4.3. 人間への Handoff

**ユーザーストーリー**

- 誰が: Kudo の実行結果を確認する人間 reviewer
- 何を: 自動実装が完了した Issue と Pull Request を、明確な状態と根拠付きで引き継ぎたい
- なぜ: 自動化の責任範囲と、その後に人間が行う判断を混同しないため

**事前条件**

- Pull Request の required body と draft 解除が確認されている。
- handoff completion を durable に記録できる。

**受け入れ基準**

- **正常系: Review Waiting への遷移**
  - Given Pull Request finalize が成功している。
  - When handoff completion を確定する。
  - Then durable state が先に更新され、Issue に `ai-review-waiting` が投影される。

- **回復系: Status Projection Failure**
  - Given handoff completion 後に label または comment の更新が一時的に失敗する。
  - When outbox projection を再試行する。
  - Then finalized Pull Request と completion は巻き戻されず、同じ status が重複なく投影される。

- **境界: Merge と Close の非実行**
  - Given Pull Request が ready for review になっている。
  - When Kudo の自動 workflow が完了する。
  - Then merge、Issue close、release、人間の review comment 対応は実行されない。

- **追跡性: Handoff Evidence**
  - Given 人間 reviewer が Issue または Pull Request を開く。
  - When handoff 内容を確認する。
  - Then Task Issue、Run、final head、検証 evidence、両 review verdict、residual risk を辿れる。

**非機能要件**

- 回復性: GitHub status は durable workflow state の再生成可能な投影とする。
- 可観測性: handoff completion と projection attempt を別々に追跡できる。
- 権限分離: 人間だけが merge、close、release の最終判断を行う。

**完了条件**

- 自動テスト: completion / projection retry / merge非実行 / evidence link を検証する。
- デモ: Issue と ready Pull Request の双方から同じ Run evidence を確認できる。

## 参照する正本

- [End-to-end workflow](../../05_design/02_workflow.md) §7
- [GitHub routing policy](../../05_design/04_github-routing.md) — Review waiting
- [Architecture](../../05_design/01_architecture.md) — Mutation authority
- [Implementation–Review Protocol](../../05_design/contracts/review-protocol-v1alpha1.md)
