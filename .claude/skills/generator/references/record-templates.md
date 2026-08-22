# 記録テンプレート（run record / PR body）

記録面は2つだけである。実行中のevidence・verdict・決裁はローカルのrun record
`.worktrees/run-issue-<n>/` へ取得順の連番ファイルとして書き、GitHubへは投稿しない。GitHubへ
出るのは最後に作るPRの本文だけであり、そこには判断に必要な要約だけを載せる（SKILL.mdの
「記録面」section）。

| 段階 | 記録 | 書く先 |
| --- | --- | --- |
| Step 1 | claim checkpoint | run record `01-claim-checkpoint.md` |
| Step 2 | test plan + RED evidence | run record `02-test-plan-red.md` |
| Step 3 | test_validity verdict（roundごと）、決裁 | run record `03-…`, `04-…` |
| Step 4 | test-revision-report、GREEN + checks evidence | run record |
| Step 5 | 成果物の要約 | PR body |

構造化部分はYAML、散文は日本語。JSONは使わない。machine blockはHTMLコメントで包み、開始marker
で種類を識別できるようにする。

## claim checkpoint（Step 1・run record）

```markdown
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

## test plan + RED evidence（Step 2完了時・run record）

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

## review verdict（Step 3・各round完了時・run record）

reviewerサブエージェントの出力YAMLを改変せずそのまま貼る。verdictが`needs_human`でも改変も
省略もせず記録し、その直後に決裁記録を別途置く。

```markdown
<!-- kudo:verdict kind=test_validity head=<sha> round=<k> -->
## test_validity review round <k>/3

（記録者注: このverdictはread-onlyのreviewerサブエージェントの出力をGenerator sessionが
転記したもの）

```yaml
<reviewer出力YAMLをそのまま>
```
```

## 決裁（adjudication・`needs_human` / round予算超過 / 指摘を退けるとき・run record）

reviewerのverdictとは別の記録として書く。verdict本文は書き換えない。

```markdown
<!-- kudo:adjudication head=<sha> round=<k> -->
## 決裁（test_validity round <k>）

- 契機: needs_human | round予算超過 | 指摘への不同意
- 対象finding: <F-1 等。複数なら列挙>
- 論点: <何が一意に決まらなかったか>
- 決定: <選んだ解釈と、それに基づいて何をしたか>
- 根拠: <Issueの該当section、authorityRefsのファイル:行、設計正本の該当箇所>
- 退けた代替: <他の解釈と、退けた理由>
- 影響範囲と可逆性: <どこまで波及するか、後から変更できるか>
- 人間に確認してほしい点: <なし、または具体的な問い。ありならPR bodyへも転記する>

<!-- kudo:adjudication-machine
schema: kudo.adjudication/skill-v1
kind: test_validity
head: <sha>
round: <k>
trigger: needs_human | round_limit_exceeded | disagreement
unresolvedBlockingFindings: [F-1, ...]
decisionAuthorityReserved: <bool>
-->
```

## test-revision-report（test_revision_required発生時・run record）

```markdown
<!-- kudo:test-revision-report head=<rollbackHead> -->
## test_revision_required

- rollback先（確定test head）: `<確定testHead>`
- 実装中に判明した、確定済みテストを変更しなければならない理由: <理由>
- 予定する変更: <テスト側の変更方針>

test validity round予算を1消費し、テスト修正から再開する。
```

## GREEN + checks evidence（Step 4完了時・run record）

```markdown
<!-- kudo:evidence-green head=<finalHead> -->
## GREEN + checks evidence（head: `<finalHead>`）

- `go test ./...`: pass
- `mise run check`: pass（format:check / vet / test）

```text
<結果の要約抜粋>
```
```

## PR body（Step 5・PR作成時）

GitHubへ出る唯一の記録面。verdict YAMLやevidence全文は載せず、レビューとmergeを人間が判断
するのに必要なものだけを書く。

```markdown
## 概要

Closes #<n>

<Outcomeに対して何を実現したかの要約。数行>

## Acceptance Criteria対応

| AC | テスト | 結果 |
| --- | --- | --- |
| AC-1 | `TestXxx/case` | pass |

## 決裁事項

<Issueやauthorityの解釈が一意に決まらず、このRunが決めた事項を1件ずつ。論点・決定・根拠を
各1〜2行で。無ければ「なし」>

## 人間の確認が必要な決定

<Issueの Decision Authority が人間に留保していると読める論点をこのRunが決裁した場合に列挙する。
無ければ「なし」>

## 残存リスク

<advisory findings、未検証事項、運用上の注意。なければ「なし」と書かずに、
なぜ残存リスクがないと判断したかを一文で書く>

<!-- kudo:pull-request
schema: kudo.pull-request/skill-v1
issue: <n>
baseSha: <baseSha>
finalHead: <finalHead>
testHead: <確定testHead>
testHeadDecidedBy: review_approve | adjudication
checks: mise-run-check-pass
-->
```
