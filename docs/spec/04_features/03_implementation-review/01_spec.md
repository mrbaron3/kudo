# 4.3. Implementation・GREEN・Final Review 機能仕様

承認済み test を変更せずに production code を実装し、GREEN、refactor、required checks の証跡を
固定したうえで、独立した Review Worker が final head を評価する機能である。

## 目的

- test validity gate で承認された期待 behavior を production implementation で満たす。
- 実装結果、検証結果、review verdict を同じ immutable head へ結び付ける。
- 実装者の session や workspace から独立した final quality judgment を得る。

## 4.3.1. Implementation

- test validity approval 後にだけ fresh implementation session を開始する。
- canonical Task Context、approved test / result、current head、Context Manifest を明示的に渡す。
- test を通す最小限の production implementation から始める。
- test の変更が必要になった場合は変更せず、test authoring / review gate へ戻す。
- implementation の継続を前 provider conversation に依存させない。

## 4.3.2. GREEN と Refactor

- 対象 test と必要な regression test の GREEN evidence を記録する。
- GREEN 後に重複、命名、構造を整理し、refactor 後も同じ behavior を検証する。
- Issue の Verification と repository required checks を再実行する。
- command、result、environment、artifact identity を final head へ bind する。
- required evidence が揃うまで final review を開始しない。

## 4.3.3. Final Review

- live Issue と Pull Request の freshness を確認してから品質判定を行う。
- correctness、regression、scope、test quality、code quality、security、evidence を常に評価する。
- 条件付き観点は applicability、理由 code、evidence を Result に明示する。
- `request_changes` は fresh repair session へ渡し、変更後の head を必ず再 review する。
- transport failure、protocol error、stale input を品質 verdict に変換しない。

## 受け入れ上の不変条件

- approved test validity result と同一の test を implementation input にする。
- final review は publish 済み final head と immutable evidence だけを対象にする。
- final approval は exact head、artifact、Context Manifest、Execution Policy、policy reference へ bind する。
- implementation と review は mutable workspace、provider session、private memory を共有しない。
- test を変更した場合は以前の test approval を失効させ、test gate から再評価する。

## 正本

- [03. システム仕様](../../03_system-spec/) F-05 / F-06
- [End-to-end workflow](../../../workflow.md) §5〜§6
- [Implementation–Review Protocol](../../../contracts/review-protocol-v1alpha1.md)
- [Final Implementation Review Policy](../../../review-policies/final-implementation-v1alpha1.md)
- [ADR-0002](../../../decisions/0002-pr-anchored-review.md)
