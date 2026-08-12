## Contract

<!-- 実行前の確認 comment。parse には影響しない。 -->

```yaml
schema: kudo.issue/v1alpha1
kind: task
# readiness は人間が最後に ready へ変更する
readiness: ready
parent: github://mrbaron3/kudo/issues/1
dependsOn:
  - github://mrbaron3/kudo/issues/31
  - github://mrbaron3/kudo/issues/9
acceptanceCriteriaIds:
  - AC-1
  - AC-2
  - AC-3
authorityRefs:
  - AGENTS.md
  - docs/contracts/issue-contract-v1alpha1.md
  - github://mrbaron3/kudo/issues/1
```

## Outcome

<!--
複数行の comment も
parse には影響しない。
-->

外部から観測できる結果を書く。

## Authority and Inputs

1. `AGENTS.md`
2. `docs/contracts/issue-contract-v1alpha1.md`

## Scope

### Included

- 対象とする範囲

### Excluded

- 対象外の範囲

## Deliverables

- 成果物の一覧

## Acceptance Criteria

### AC-1

- Given: 前提 1
- When: 操作 1
- Then: 期待 1

### AC-2

- Given: 前提 2
- When: 操作 2
- Then: 期待 2

### AC-3

- Given: 前提 3
- When: 操作 3
- Then: 期待 3

## Verification and Evidence

fence 内の `##` は heading として扱わない。

```sh
echo "## Fake Heading"
mise run check
```

## Constraints and Invariants

- 不変条件

## Decision Authority

- このIssue内で決めてよい: 内部構成
- ADRまたは別Issueが必要: 契約変更

## Stop and Escalation Conditions

- 停止条件

## Advisory Hints

- 非拘束の助言
