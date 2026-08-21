# 4.4. Pull Request 確定・Merge 受け入れ要件

[03. システム仕様](../../03_system-spec/) F-07 に対する Why / What の受け入れ基準である。
finalize gate、merge gate、Pull Request mutation、完了 projection の実現方法は [詳細設計](02_design.md) で扱う。

## サブ機能一覧

| ID | サブ機能 | 優先度 |
| --- | --- | --- |
| 4.4.1 | Finalize 前検証 | 高 |
| 4.4.2 | Pull Request 確定 | 高 |
| 4.4.3 | Merge と完了投影 | 高 |

## 4.4.1. Finalize 前検証

**ユーザーストーリー**

- 誰が: repository の maintainer
- 何を: 承認と検証の対象が、実際に base へ入る Pull Request head と一致していてほしい
- なぜ: 承認後に差し替えられた未評価の変更を、review 済みとして base へ入れないため

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
  - Given Kudo の merge intent に紐付かない close / merge が人間または外部 automation により行われている。
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

- 誰が: merge 後に変更を追跡する人間
- 何を: 要件、変更、検証、独立 review、残存リスクが整理された Pull Request を base の履歴に残したい
- なぜ: 証跡を複数の session やログから集めずに、何がなぜ入ったかを後から辿れるから

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
  - Then 同じ Pull Request の draft が解除され、merge gate を評価できる状態になる。

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

## 4.4.3. Merge と完了投影

**ユーザーストーリー**

- 誰が: repository の maintainer
- 何を: 独立 review が承認した head だけが、証跡付きで base へ入ってほしい
- なぜ: merge 操作のために人間の在席を待たず、かつ未 review の変更が base へ入らないため

**事前条件**

- Pull Request の required body 確定と draft 解除が durable に記録されている。
- merge intent の idempotency identity を mutation 前に durable へ記録できる。

**受け入れ基準**

- **正常系: 承認済み head の Merge**
  - Given final approve、live PR の open / base / head 一致、required status check の success、mergeable が
    すべて成立している。
  - When `merge_pull_request` を実行する。
  - Then 承認済み head が merge commit として base へ入り、head branch が削除され、merge commit SHA と
    merged 状態が observation として固定される。

- **鮮度: Merge 直前の Head 変化**
  - Given 照合後、merge 要求までの間に branch へ外部 push が入る。
  - When 期待 head SHA を明示して merge を要求する。
  - Then merge は成立せず、stale として再 publish と再 review へ戻る。

- **異常系: Required Check の未達**
  - Given required status check が failure、または conflict が存在する。
  - When merge gate を評価する。
  - Then merge を実行せず、`merge_blocked` として human escalation へ送り、設定の回避や squash / rebase での
    強行を行わない。

- **待機: Required Check の Pending**
  - Given required status check がまだ pending である。
  - When merge gate を評価する。
  - Then 品質 verdict にも execution failure にもせず、retry budget を消費しない待機として backoff 再照合し、
    Operation の execution deadline を超えた時点で `merge_blocked` とする。

- **冪等性: 結果不明後の再試行**
  - Given merge 要求が timeout し、GitHub 上では既に merge が成立している。
  - When 同じ logical merge Operation を再試行する。
  - Then 記録済み intent と merge commit の親が期待 head であることを照合し、二重 merge を作らずに成功へ
    収束する。

- **外部干渉: Intent の無い Merge**
  - Given Kudo の merge intent が無い状態で PR が close / merge されている。
  - When live state を取得する。
  - Then 自分の mutation の成功として扱わず、`external_mutation_conflict` として human escalation へ送る。

- **正常系: 完了投影**
  - Given merge completion が durable に記録されている。
  - When 完了 projection を実行する。
  - Then Task Issue が close され、`ai-in-progress` を外して `ai-merged` が投影される。closing keyword で
    既に close 済みの場合は no-op として成功にする。

- **回復系: Projection Failure**
  - Given merge completion 後に label、comment、close の更新が一時的に失敗する。
  - When outbox projection を再試行する。
  - Then 成立済みの merge と completion は巻き戻されず、同じ状態が重複なく投影される。

- **境界: Release と Revert の非実行**
  - Given merge が成立している。
  - When Kudo の自動 workflow が完了する。
  - Then release、deploy、revert、merge 後の review comment 対応は実行されない。

**非機能要件**

- 冪等性: merge、branch 削除、Issue close、label 投影が retry 下で一度だけ成立する。
- 安全性: 承認済み head と一致しない head を merge しない。
- 回復性: GitHub status は durable workflow state の再生成可能な投影とする。
- 権限分離: merge を実行できるのは Issue Worker credential だけである。

**完了条件**

- 自動テスト: merge 成立 / head 不一致 / check failure / check pending / timeout 再試行 / intent 無し merged /
  projection retry を検証する。
- デモ: `ai-ready` を付けた Issue が、人手を介さず merge 済み・close 済みになるまで進む。

## 参照する正本

- [End-to-end workflow](../../05_design/02_workflow.md) §7–8
- [ADR-0005](../../05_design/decisions/0005-auto-merge.md) — merge gate、failure routing、完了 terminal
- [GitHub routing policy](../../05_design/04_github-routing.md) — Merge completion
- [Architecture](../../05_design/01_architecture.md) — Mutation authority
- [Implementation–Review Protocol](../../05_design/contracts/review-protocol-v1alpha1.md)
