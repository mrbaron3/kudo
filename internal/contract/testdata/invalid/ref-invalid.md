## Contract

```yaml
schema: kudo.issue/v1alpha1
kind: task
readiness: ready
parent: github://mrbaron3/kudo/pulls/7
dependsOn:
  - github://mrbaron3/kudo/issues/07
  - gitlab://mrbaron3/kudo/issues/3
  - github://mrbaron3/kudo/issues/+31
acceptanceCriteriaIds:
  - AC-1
authorityRefs:
  - /etc/passwd
  - docs/../AGENTS.md
```

## Outcome

完了後に外部から観測できる結果。

## Scope

### Included

- 対象

### Excluded

- 対象外

## Deliverables

- 成果物

## Acceptance Criteria

### AC-1

- Given: 前提
- When: 操作
- Then: 期待結果

## Verification and Evidence

- `mise run check`

## Constraints and Invariants

- 不変条件

## Decision Authority

- このIssue内で決めてよい: 内部構成

## Stop and Escalation Conditions

- 停止条件
