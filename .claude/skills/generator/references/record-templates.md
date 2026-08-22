# 記録テンプレート（PR body / PR comment）

すべての記録はGitHub上の該当PRへ書く（GitHubがsingle source of truth。ローカルやこの会話は
正本ではない）。構造化部分はYAML、散文は日本語。JSONは使わない。

machine blockはHTMLコメントで包み、開始markerで種類を識別できるようにする。人間が編集し得る
surfaceなので、後続judgmentではSHA・digestの値を再検証してから使う。

## claim checkpoint（draft PR body・作成時）

```markdown
Issue #<n> のGenerator実行Run。workflowは `.claude/skills/generator/SKILL.md` に従う。

<!-- kudo:claim-checkpoint
schema: kudo.claim-checkpoint/skill-v1
issue: <n>
baseSha: <baseSha>
issueBodyDigest: sha256:<hex>
acceptanceCriteriaIds: [AC-1, AC-2, ...]
authorityRefs:
  - <path>
-->
```

## test plan + RED evidence（PR comment・Step 2完了時）

```markdown
<!-- kudo:test-plan head=<testHead> -->
## Test plan（head: `<testHead>`）

| AC | Test case | 期待RED理由 |
| --- | --- | --- |
| AC-1 | `TestXxx/case` | <未実装のどのbehaviorに起因して失敗するか> |

test-only fixture / helper: <なし、または一覧と理由>

## RED evidence

- command: `go test ./...`
- exit status: <code>
- 失敗の原因が対象behaviorの未実装であることの根拠:

```text
<stdout/stderrの該当抜粋>
```
```

## review verdict（PR comment・各round完了時）

reviewerサブエージェントの出力YAMLを改変せずそのまま貼る。

```markdown
<!-- kudo:verdict kind=<kind> head=<sha> round=<k> -->
## <kind> review round <k>/3

（記録者注: このverdictはread-onlyのreviewerサブエージェントの出力をGenerator sessionが
転記したもの）

```yaml
<reviewer出力YAMLをそのまま>
```
```

## test-revision-report（PR comment・test_revision_required発生時）

```markdown
<!-- kudo:test-revision-report head=<rollbackHead> -->
## test_revision_required

- rollback先（approved test head）: `<approvedTestHead>`
- 実装中に判明した、承認済みテストを変更しなければならない理由: <理由>
- 予定する変更: <テスト側の変更方針>

test validity round予算を1消費し、テスト修正から再開する。
```

## GREEN + checks evidence（PR comment・Step 4完了時）

```markdown
<!-- kudo:evidence-green head=<finalHead> -->
## GREEN + checks evidence（head: `<finalHead>`）

- `go test ./...`: pass
- `mise run check`: pass（format:check / vet / test）

```text
<結果の要約抜粋>
```
```

## 最終PR body（Step 6・finalize時に全置換）

```markdown
## 概要

Closes #<n>

<Outcomeに対して何を実現したかの要約。数行>

## Acceptance Criteria対応

| AC | テスト | 結果 |
| --- | --- | --- |
| AC-1 | `TestXxx/case` | pass |

## Evidence / Verdict

- test plan + RED: <comment URL>
- GREEN + checks: <comment URL>
- test_validity: approve（round <k>/3）<comment URL>
- final_implementation: approve（round <k>/3）<comment URL>

## 残存リスク

<advisory findings、未検証事項、運用上の注意。なければ「なし」と書かずに、
なぜ残存リスクがないと判断したかを一文で書く>

<!-- kudo:finalize
schema: kudo.finalize/skill-v1
baseSha: <baseSha>
finalHead: <finalHead>
approvedTestHead: <approvedTestHead>
-->
```
