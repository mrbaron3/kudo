# 4.2. Test Authoring・RED・Test Validity Review 機能仕様

Acceptance Criteria を test へ変換し、その test が未実装 behavior を適切に検出できることを、
production implementation の開始前に独立した review で確認する TDD gate である。

## 目的

- Acceptance Criteria と test case の traceability を確立する。
- 偶然失敗する test ではなく、対象 behavior の欠如に起因する RED を証明する。
- test author と隔離された Review Worker により test 自体の妥当性を評価する。

## 4.2.1. Test Authoring

- canonical Task Context と authority content から test plan と test code を先に作成する。
- test plan に Acceptance Criteria と test case の対応を含める。
- production behavior を先回りで実装せず、test-only checkpoint を作る。
- model-bearing Operation ごとに fresh provider session を開始する。
- revision 時は versioned finding と immutable artifact を新しい session へ明示的に渡す。

## 4.2.2. RED Evidence

- 対象 behavior の未実装を理由として test が期待どおり失敗することを確認する。
- 環境故障、無関係な既存 failure、compile infrastructure failure を RED とみなさない。
- command、exit status、bounded stdout / stderr、environment identity を記録する。
- test-only commit、patch、test plan、RED evidence を Artifact Manifest へ bind する。
- RED 固定後の exact head を draft Pull Request へ publish する。

## 4.2.3. Test Validity Review

- live Issue body digest と Pull Request の open / draft / head / base を review 前に照合する。
- implementation workspace を使わず、exact head から独立した read-only checkout を作る。
- canonical Task Context、test plan、test patch、RED evidence、versioned policy だけを review input にする。
- verdict を `approve`、`request_changes`、`needs_human` として versioned Result に固定する。
- changed head、artifact、Context Manifest、Execution Policy、policy reference に以前の approval を再利用しない。

## 受け入れ上の不変条件

- test validity approval 前に production implementation を開始しない。
- test-only head を draft Pull Request へ publish してから review を開始する。
- test authoring、revision、review はそれぞれ fresh provider session で実行する。
- `request_changes` は同じ論理 lane へ返すが、以前の conversation を resume しない。
- transport failure、protocol error、stale input を品質 verdict に変換しない。

## 正本

- [03. システム仕様](../../03_system-spec/) F-03 / F-04
- [End-to-end workflow](../../../workflow.md) §3〜§4
- [Worker Operation Protocol](../../../contracts/operation-protocol-v1alpha1.md)
- [Implementation–Review Protocol](../../../contracts/review-protocol-v1alpha1.md)
- [Test Validity Review Policy](../../../review-policies/test-validity-v1alpha1.md)
- [ADR-0002](../../../decisions/0002-pr-anchored-review.md)
