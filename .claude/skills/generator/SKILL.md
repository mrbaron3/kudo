---
name: generator
description: >-
  Task IssueをKudo設計のTDDワークフロー（claim → テスト作成+RED → test validity
  review → 実装+GREEN → refactor → final implementation review → PR finalize）で
  実装し、Pull Requestを作成する。明示的呼び出し専用:
  Claude Codeでは /generator <issue番号|URL>、Codexでは $generator メンション。
argument-hint: "[issue番号またはURL]"
disable-model-invocation: true
---

# Generator: Issue → PR 実行ワークフロー

指定されたTask Issueを、このリポジトリの設計（正本: `docs/spec/05_design/02_workflow.md`）に
沿って実装し、レビュー済みのPull Requestを作成する。このスキルは、Kudo runtimeが自動化する
ワークフローをClaude Codeセッション上で実行するための忠実な縮約である。手順を省略したく
なったら、それは設計上のgateを迂回しようとしているsignalなので停止して理由を報告する。

## 役割の対応

| Kudo設計上のactor | このスキルでの担い手 |
| --- | --- |
| Issue Worker（Implementer） | このセッション本体。worktree・branch・PRを変更できる唯一の主体 |
| Review Worker（Reviewer） | 読み取り専用のfresh reviewerセッション。roundごとに新規起動し、判定対象headに固定したdetached worktreeだけを見る |
| Controller | このセッションが手順として代行。escalation先は人間（ユーザー） |

## ハーネス対応（Claude Code / Codex）

このスキルは両ハーネスで同一手順を実行する。差分は2点だけ:

- **呼び出しと引数**: Claude Codeは `/generator <issue>`（引数は本文へ展開される）。
  Codexは `$generator` メンションで、同じユーザーメッセージ中のissue番号/URLが引数。
- **reviewerの起動方法**: Claude Codeはread-only指示付きサブエージェント（Agent tool）。
  Codexは `codex exec` によるfreshなサブプロセス。具体的な起動手順は
  `references/review-prompts.md` の冒頭に従う。

どちらの場合もreviewerがfreshなsession（この会話の文脈を持たない）であることが設計上の
要件であり、会話内で自問自答するreviewはreviewとして認めない。

設計との既知の乖離（単一credential実行に伴う縮約。これ以外の逸脱はしない）:

- verdictはcheck runではなくPR commentへ記録する。自己承認の防止は構造（別App identity）では
  なく手順で担保するため、reviewerの判定文をこのセッションが書き換えないこと。verdict comment
  にはreviewer出力のYAMLをそのまま貼る。
- label（`ai-ready`等）とwebhook/pollingは使わない。ユーザーの明示的呼び出しがtriggerを代替する。
- mergeは行わない。成果物はready化されたPRであり、merge判断は人間に残す。

## 入力

呼び出し引数をissue番号またはURLとして解釈する。

- 引数: $ARGUMENTS
- 上の行に `$ARGUMENTS` が文字どおり残っている場合（Codex等、引数展開のないハーネス）は、
  呼び出し時のユーザーメッセージからissue番号/URLを読み取る。

欠落・曖昧なら会話文脈から推測して補完せず、必要な入力を指摘して停止する
（contract discipline）。

## 予算と失敗の分類

- review round予算: `test_validity` 3回、`final_implementation` 3回（Escalation Policy既定）。
  `test_revision_required` はtest側roundを1消費する。
- transport/tooling失敗（gh・git・ネットワーク）: 3回まで再試行し、quality判定とは混ぜない。
- 予算超過・`needs_human`・claim競合・stale_input・契約不備は、いずれも失敗ではなく
  escalation。現状・理由・再開に必要な人間の判断を明記してユーザーへ報告し、停止する。

---

## Step 1: 契約検証とclaim

1. `git fetch origin` し、baseを `origin/main` のSHAに固定する。
2. `gh issue view <n>` でIssueを取得し、strict parseする。次のいずれかに該当したら
   claim拒否として理由を報告し停止する（`docs/spec/05_design/contracts/issue-contract-v1alpha1.md` 参照）:
   - Issueがopenでない、またはPRである
   - Contract blockが `schema: kudo.issue/v1alpha1` / `kind: task` / `readiness: ready` でない
   - `dependsOn`（およびGitHubのblocked-by関係）に未closeのIssueがある
   - `authorityRefs` のいずれかがbase commitに解決できない
   - 必須H2 section（Contract / Outcome / Scope / Deliverables / Acceptance Criteria /
     Verification and Evidence / Constraints and Invariants / Decision Authority /
     Stop and Escalation Conditions）に欠落・重複がある
3. claim checkpointを固定する: Issue本文のsha256 digest、base SHA、AC ID一覧。
4. atomic claim: `git push origin <baseSha>:refs/heads/kudo/issue-<n>`。既にbranchが存在して
   失敗したら他のRunがclaim済みなので、上書きせず停止して報告する。
5. worktreeを作る: `.git/info/exclude` に `.worktrees/` が無ければ追記し（追跡される
   `.gitignore` は触らない）、`git worktree add .worktrees/issue-<n> kudo/issue-<n>`。
   以後の実装作業はすべてこのworktree内で行い、ユーザーのcheckoutを汚さない。
6. claim commit（empty commit `claim: #<n> <title>`）をpushし、draft PRを作成する。
   PR bodyにはclaim checkpointのmachine blockを入れる
   （形式は `references/record-templates.md` を読む）。PR番号がこのRunのidentityになる。

## Step 2: テスト作成とRED（author_tests）

実装コンテキストは次だけに限定する。親Issue・依存Issueのprose、Issueコメント、過去の会話の
記憶を実装根拠にしない（Task Issueがexecution-context rootであり、`authorityRefs` だけが
実装authorityだから）:

- Issue本文のcanonical section群（Outcome / Scope / Deliverables / AC / Verification /
  Constraints / Decision Authority / Stop conditions）
- `authorityRefs` に列挙されたファイルの内容（base commit時点）

手順:

1. test planを書く: 全AC IDについて少なくとも1つのtest caseを対応させ、AC ↔ test case ↔
   期待RED理由を双方向に追跡できる表にする。
2. テストだけを書く。production sourceとproduction configは変更しない。テスト実行に必要な
   test-only fixture・helperは可だが、test planのmappingに明示する。fakeはGitHub・process・
   clock・filesystem・provider・telemetry境界に置く（CLAUDE.mdのテスト規律）。
3. RED evidenceを取る: `go test ./...` を実行し、失敗が「対象behaviorが未実装であること」に
   起因すると確認する。Goでは未実装シンボル参照によるcompile errorも対象欠如起因のREDと
   認めるが、テスト自体のtypoやfixture不備による失敗は不可。exit statusだけでなく
   stdout/stderrから期待した失敗であることを確認する。`mise run check` の完全passはこの段階
   では要求しない（REDで失敗するのが正しい位相の表示）。
4. commitしてpushし、test head SHAを記録する。test plan + RED evidenceをPR commentに記録する
   （テンプレートは `references/record-templates.md`）。

## Step 3: test validityレビュー（サブエージェント）

1. staleness check: Issueを再取得しdigestを照合する。canonical sectionの内容が変わっていたら
   `stale_input` として停止・報告する（formatting/typoのみの差分は記録して続行してよい。
   Issue Observationの変化はauditであってsemantic inputの変化ではない）。
2. 判定対象を不変にする: `git worktree add --detach .worktrees/review-issue-<n> <testHead>`。
3. reviewerセッションをfresh起動する（起動方法は `references/review-prompts.md` 冒頭の
   ハーネス別手順）。promptは同ファイルのtest_validity用テンプレートに従う。reviewerは
   `docs/spec/05_design/review-policies/test-validity-v1alpha1.md` を自分で読んで適用する。
   read-only（fileの変更・git/gh mutation禁止）で、渡された明示的入力以外
   （親Issueのprose、実装側worktree、この会話の文脈）を評価根拠にしない。
4. verdict（YAML）を受け取り、そのままPR commentへ記録する。review worktreeを
   `git worktree remove` する。
5. gate判定:
   - `approve`（blocking findingゼロのときのみ有効）→ Step 4へ。approve済みtest head SHAを固定する。
   - `request_changes` → findingsに対応してテストを修正し、新しいRED evidenceを取り、push
     して**新しいroundとして3から再実行**する。前roundのreviewerは再利用せず、必ず
     fresh起動する（session再開をしない設計）。round予算を1消費。
   - `needs_human`、または予算超過 → escalation。

## Step 4: 実装とGREEN（implement → refactor）

test validity approveより前にproduction実装を開始しない（順序不変条件）。

1. staleness check（Step 3-1と同じ）。
2. approve済みテスト・fixture・helper・test commandを一切変更せず、テストを通す最小限の
   実装を行う。
3. テスト変更が必要だと判明したら `test_revision_required`: headをapprove済みtest headへ
   `git reset --hard` でrollbackし（実装はローカルstashに退避してよいが、公開lineageは
   巻き戻す）、test-revision-report commentを記録して、Step 2のテスト修正からやり直す。
   test validityのround予算を1消費する。
4. GREEN: `go test ./...` 全pass。その後refactorし、`mise run check` を完全passさせる。
5. pushしてfinal head SHAを記録し、GREEN + checks evidenceをPR commentに記録する。

## Step 5: final implementationレビュー（サブエージェント）

1. 決定論的前提を自分で検証してからreviewerを起動する:
   - `git diff <approvedTestHead> HEAD -- <テスト関連path>` が空である（テスト変更があるなら
     Step 3の再承認を先に得る。gate迂回の検出）
   - final headで `mise run check` が成功している
   - staleness check
2. detached review worktreeをfinal headで作り、reviewerセッションをfresh起動する。
   promptは `references/review-prompts.md` のfinal_implementation用テンプレート。policyは
   `docs/spec/05_design/review-policies/final-implementation-v1alpha1.md`。条件付き観点
   （UX / Accessibility / Type design / Performance）はそれぞれ適用可否の宣言を1件ずつ
   必須とする。
3. verdictをPR commentへ記録し、review worktreeを削除する。
4. gate判定はStep 3と同じ。`request_changes` はrepair_implementationとして修正 →
   新evidence → 新round（fresh reviewer、予算3、テストを弱体化させない）。

## Step 6: finalize（PR ready化）

1. 最終staleness check。final approve verdictがlive headと一致していることを確認する
   （approve後にcommitを積んだら再レビューが必要）。
2. PR bodyを確定する（テンプレートは `references/record-templates.md`）。必須項目:
   Taskへのclosing keyword（`Closes #<n>`）、outcome要約、AC ↔ テスト ↔ evidence対応表、
   evidence/verdict commentへの参照、残存リスク、base/head SHA。散文は日本語。
3. `gh pr ready <PR番号>` でdraftを解除する。mergeはしない。
4. 実装worktreeを `git worktree remove` で削除する（localは使い捨てのworkspace。branchとPRが
   正本）。
5. ユーザーへ報告する: PR URL、各gateの消費round数、blocking/advisory findingsの要約、
   残存リスク。

## 横断ルール

- PR・comment・commit messageの散文は日本語（リポジトリのCommunicationルール）。
- machine block・verdict等の構造化表現はYAML/Markdown。JSONは使わない。
- 各段階のevidence・verdictは取得した時点で即座にPR commentへ記録する。後からまとめて
  書かない（headが進むと何に対するevidenceか曖昧になるため）。
- スキル実行中に本ワークフローの範囲外の問題（無関係なbug等）を見つけても本PRへ混入させない。
  報告に含めるに留める。ただし対象変更が直接作ったblocking riskは先送りしない。
