## Contract

<!-- 全sectionを確定してreadinessをreadyに変更し、mrbaron3がassignされていることを確認した後、最後に実行依頼としてai-ready labelを付ける。 -->

```yaml
schema: kudo.issue/v1alpha1
kind: task
readiness: draft
# 親Epicがなければnull。親本文は暗黙に実装contextへ入りません。
parent: null
dependsOn: []
acceptanceCriteriaIds:
  - AC-1
authorityRefs: []
```

## Outcome

<!-- 完了後に外部から観測できる結果を書く。実装方法は書かない。 -->

観測できる結果。

## Authority and Inputs

<!-- 正とする仕様・文書・Issueと、矛盾時の優先順位を書く。依存成果物はmain上のpathを指す。 -->

1. `AGENTS.md`

## Scope

### Included

- 対象

### Excluded

- 対象外

## Deliverables

<!-- 作成・変更する成果物と、その外部から観測できる役割を書く。 -->

- 成果物

## Acceptance Criteria

### AC-1

- Given: 前提
- When: 操作
- Then: 期待

## Verification and Evidence

<!-- 必須の検証command、期待結果、残る外部・headed境界、完了時に残す証跡を書く。 -->

- `mise run check`

## Constraints and Invariants

- 不変条件

## Decision Authority

<!-- Task内で決めてよい事項、ADR/別Issueが必要な事項、人間判断が必要な事項を分ける。 -->

- このIssue内で決めてよい: 内部構成
- ADRまたは別Issueが必要: 契約変更
- 人間判断が必要: 仕様矛盾の解消

## Stop and Escalation Conditions

- 停止条件

## Advisory Hints

- 助言
