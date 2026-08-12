---
name: Kudo task
about: Kudo が実行可能な task Issue
title: ""
labels: ""
assignees: "mrbaron3"
---

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
# 実装時に読む正本を優先順位の高い順に列挙する。ここに無いものはauthorityになりません。
authorityRefs: []
```

## Outcome

<!-- 完了後に外部から観測できる結果を書く。実装方法は書かない。 -->

## Scope

### Included

-

### Excluded

-

## Deliverables

<!-- 作成・変更する成果物と、その外部から観測できる役割を書く。 -->

-

## Acceptance Criteria

### AC-1

- Given:
- When:
- Then:

## Verification and Evidence

<!-- 必須の検証command、期待結果、残る外部・headed境界、完了時に残す証跡を書く。 -->

-

## Constraints and Invariants

-

## Decision Authority

<!-- Task内で決めてよい事項、ADR/別Issueが必要な事項、人間判断が必要な事項を分ける。 -->

- このIssue内で決めてよい:
- ADRまたは別Issueが必要:
- 人間判断が必要:

## Stop and Escalation Conditions

-

## Advisory Hints

-
