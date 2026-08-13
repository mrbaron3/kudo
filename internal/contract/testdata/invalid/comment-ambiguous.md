## Contract

```yaml
schema: kudo.issue/v1alpha1
kind: task
readiness: ready
parent: null
dependsOn: []
acceptanceCriteriaIds:
  - AC-1
authorityRefs: []
```

## Outcome

<!-- note --> 完了後に外部から観測できる結果。

## Scope

### Included

- 対象

### Excluded

- 対象外

## Deliverables

- 成果物 <!-- 行末側の混在 -->

## Acceptance Criteria

### AC-1

- Given: 前提
- When: 操作
- Then: 期待結果

## Verification and Evidence

- `mise run check`
`AGENTS.md` <!-- 可視内容が inline code span だけの混在 -->

## Constraints and Invariants

- 不変条件
<!-- `authorityRefs の順序 --> comment 外に残る可視内容 `AGENTS.md`

## Decision Authority

- このIssue内で決めてよい: 内部構成

## Stop and Escalation Conditions

- 停止条件
