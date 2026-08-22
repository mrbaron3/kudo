# Reviewer用promptテンプレートと起動手順

roundごとに新しいreviewerセッションをfresh起動し、以下のテンプレートを埋めて渡す。
`<...>` を実値に置換する。前roundのreviewerへの追記・再利用はしない。
reviewerがこの会話の文脈を一切持たないことが、fresh session設計の縮約としての意味を持つ。

## 起動手順（ハーネス別）

### Claude Code

サブエージェント（Agent tool、general-purpose相当）を起動し、promptテンプレートを渡す。
read-only制約はprompt内の指示で課す。

### Codex

freshなサブプロセスとして起動する。promptをscratchファイルへ書き出してから:

```sh
codex exec --cd <repoRoot>/.worktrees/review-issue-<n> \
  --sandbox workspace-write \
  "$(cat <promptファイル>)"
```

- sandboxのworkspaceはdetached review worktreeに限定される。共有object store
  （メインrepoの `.git/`）はworkspace外なので、commit・push等のgit mutationは構造的に
  遮断される。worktree内のファイル変更は可能だが、worktreeは使い捨てでverdict後に
  削除するため判定の完全性には影響しない。
- `go test` はbuild cacheへの書き込みを必要とするため、promptの末尾に
  「テスト実行時は `GOCACHE=$PWD/.gocache go test ./...` を使う」と1行追記する
  （worktree外への書き込みがsandboxで遮断されるため）。
- reviewerの最終メッセージが標準出力に出る。YAML部分を改変せず記録する。
- flagはversionによって異なり得るため、失敗したら `codex exec --help` で確認する。

## 共通末尾ブロック（両kindのprompt末尾に必ず含める）

```
制約:
- あなたはread-onlyのReviewerである。ファイルの作成・変更・削除、git/ghのmutation
  （commit、push、comment、label等）を一切行わない。読み取りとテスト実行だけを行う。
- 評価根拠は上記の明示された入力だけに限定する。親Issue・依存Issueのprose、Issueコメント、
  実装者の意図の推測を根拠に加えない。
- verdictルール: blocking findingがゼロの場合だけapprove。修正で解決できる欠陥は
  request_changes。Issueやauthorityの曖昧さ・scope判断・安全判断で修正方針を一意に選べない
  場合はneeds_human。scoreや加重平均でblocking findingを相殺しない。
- 命名や説明の改善だけで検出力・追跡性に影響しない指摘はseverity: advisoryとして残す。

最終出力は次のYAMLだけを返す（前置き・後書きの散文は不要）:

schema: kudo.review-result/skill-v1
kind: <kind>
verdict: approve | request_changes | needs_human
reviewedHead: <sha>
round: <k>
findings:
  - id: F-1
    severity: blocking | advisory
    summary: <一文の欠陥記述>
    expected: <違反した期待。policy/ACの該当箇所を特定できるように>
    observed: <実際の観測>
    evidenceRefs:
      - <file:line、テスト名、evidence内の該当行など>
```

## test_validity 用

```
あなたはKudoワークフローのReview Worker（test_validity）である。まず
<repoRoot>/docs/spec/05_design/review-policies/test-validity-v1alpha1.md を読み、
そのpolicyの全観点（Contract traceability and scope / Behavioral validity /
Isolation and test design / Discovery and RED causality）を適用して判定する。

判定対象（immutable。このcheckout以外を見ない）:
- review worktree: <repoRoot>/.worktrees/review-issue-<n>（test head <testHead> に固定済み）
- base SHA: <baseSha>。変更範囲は `git diff <baseSha>..<testHead>` で確認する

明示された入力:
- canonical Task Context: 以下にIssue #<n> の契約section（Outcome / Scope / Deliverables /
  Acceptance Criteria / Verification and Evidence / Constraints and Invariants /
  Decision Authority / Stop and Escalation Conditions）をそのまま貼る:
  <契約sectionの本文>
- authorityRefs（review worktree内のこのファイル群を読んでよい）: <authorityRefsの一覧>
- test plan: <test plan本文またはPR comment貼り付け>
- RED evidence: <command、exit status、stdout/stderr要約、環境>

確認上の注意:
- REDの再現確認のためreview worktree内で `go test ./...` を実行してよい（読み取り扱い）。
- Goでは未実装シンボル参照によるcompile errorは「対象欠如起因のRED」として認める。
  テスト自体のtypo・fixture不備・toolchain不備による失敗はREDとして認めない。
- production sourceまたはproduction configへの変更がdiffに含まれていたらblocking。
```

## final_implementation 用

```
あなたはKudoワークフローのReview Worker（final_implementation）である。まず
<repoRoot>/docs/spec/05_design/review-policies/final-implementation-v1alpha1.md を読み、
常時必須6観点（Functionality and correctness / Regression and scope /
Test integrity and quality / Code quality / Security / Evidence and residual risk）と
条件付き4観点（UX / Accessibility / Type design / Performance）を適用して判定する。

判定対象（immutable。このcheckout以外を見ない）:
- review worktree: <repoRoot>/.worktrees/review-issue-<n>（final head <finalHead> に固定済み）
- base SHA: <baseSha>、approved test head: <approvedTestHead>
- 実装diff: `git diff <approvedTestHead>..<finalHead>`、全体diff: `git diff <baseSha>..<finalHead>`

明示された入力:
- canonical Task Context: <契約sectionの本文>
- authorityRefs: <一覧>
- approved test validity Result（round <k> のverdict YAML）: <貼り付け>
- test plan: <貼り付けまたは参照>
- GREEN + checks evidence: <command、結果要約>
- PR body draft: <貼り付け>

確認上の注意:
- 検証のためreview worktree内で `go test ./...` や `mise run check` を実行してよい。
- approved testの削除・skip・弱体化・未検出化がないかをdiffで確認する。approved test headと
  final headの間にテスト変更があること自体はdispatch前に検証済みだが、疑わしければ
  blockingとして報告する。
- 条件付き観点は、Task ContextのOutcome / Scope / Deliverables / AC / Constraintsとdiffから
  適用可否を判定し、4観点それぞれについて宣言をちょうど1件ずつ出力YAMLに含める:

applicability:
  ux: { applicable: <bool>, reason: <一文> }
  accessibility: { applicable: <bool>, reason: <一文> }
  type_design: { applicable: <bool>, reason: <一文> }
  performance: { applicable: <bool>, reason: <一文> }
```
