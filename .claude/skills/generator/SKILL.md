---
name: generator
description: >-
  Task IssueをKudo設計のTDDワークフロー（claim → テスト作成+RED → test validity
  review → 実装+GREEN → refactor → PR作成）で実装し、人間へ返さずPull Requestまで
  到達する。明示的呼び出し専用:
  Claude Codeでは /generator <issue番号|URL>、Codexでは $generator メンション。
argument-hint: "[issue番号またはURL]"
disable-model-invocation: true
---

# Generator: Issue → PR 実行ワークフロー

指定されたTask Issueを、このリポジトリの設計（正本: `docs/spec/05_design/02_workflow.md`）に
沿って実装し、Pull Requestを作成する。このスキルは、Kudo runtimeが自動化するワークフローを
Claude Code / Codexセッション上で実行するための縮約である。手順を省略したくなったら、それは
設計上のgateを迂回しようとしているsignalなので、迂回せず正規の手順に戻る。

## このスキルの範囲

- 実行するのはclaimからPull Request作成までである。
- 行うレビューは**test validity（テストの妥当性）だけ**である。テストが実装の契約を固定する
  ので、ここだけは実装前に独立した目を通す。
- **PR完成後の実装レビューとmergeはスコープ外**であり、人間または別のプロセスが担う。この
  スキルは「レビュー可能な状態のPR」までを作る。
- 範囲を広げたくなったら、スキルを直す前に設計判断として合意を取る。

## 実行原則: PRの作成まで人間へ返さない

呼び出しからPull Request作成までの間、人間へ判断を戻さない。人間への返却点はPRだけである。
test validityレビューの`needs_human`もround予算超過も、途中で停止する理由にはしない。判断が
割れる論点はこのセッションが決裁し、決定と根拠を記録して続行する（「決裁（adjudication）」
section）。

停止してよいのは、続行すると誤った成果物になるか、構造的に続行できない場合だけである:

- claim競合（branch `kudo/issue-<n>` が既に存在する）
- Issue契約の不備・欠落（strict parse失敗）
- `stale_input`（実行中にIssueのcanonical sectionが意味的に変わった）
- transport/tooling失敗が再試行予算（3回）を使い切った

これらは品質判断ではなく、入力または実行環境が壊れているという観測である。品質判断
（テストが妥当か）を理由に人間へ返さない。

## 記録面: GitHubへ出るのはPRだけ

test plan、RED evidence、reviewerのverdict、決裁、GREEN evidenceは**すべてローカルのrun record
に記録し、GitHubへは投稿しない**。GitHubへ出る成果物は最後に作るPR（branch、commit、PR本文）
だけである。レビューはこのセッション内で完結する手順であって、GitHubに残すべき記録では
ないという判断による。

- run record: `.worktrees/run-issue-<n>/` に取得順の連番ファイル（`01-claim-checkpoint.md`、
  `02-test-plan-red.md`、…）として書く。実装worktreeの中には置かない（commitへ混入させない
  ため）。形式は `references/record-templates.md`。
- 記録は取得した時点で即座に書く。後からまとめて書かない（headが進むと何に対するevidenceか
  曖昧になるため）。
- 代償: リポジトリ設計の「GitHubがsingle source of truth」から外れ、evidenceの監査可能性は
  ローカル記録に依存する。run recordはfinalizeでも削除せず残し、最終報告でパスを伝える。
  セッションとdiskが失われたらevidenceは失われる（branchは残るので、再開ではなくsupersede
  として扱う）。

## 役割の対応

| Kudo設計上のactor | このスキルでの担い手 |
| --- | --- |
| Issue Worker（Implementer） | このセッション本体。worktree・branch・PRを変更できる唯一の主体 |
| Review Worker（Reviewer） | test validityを判定する読み取り専用のfresh reviewerセッション。roundごとに新規起動し、判定対象headに固定したdetached worktreeだけを見る |
| Controller | このセッションが手順として代行。gate判定とround予算の管理に加え、reviewerが解けない論点の決裁も行う |

## ハーネス対応（Claude Code / Codex）

このスキルは両ハーネスで同一手順を実行する。差分は2点だけ:

- **呼び出しと引数**: Claude Codeは `/generator <issue>`（引数は本文へ展開される）。
  Codexは `$generator` メンションで、同じユーザーメッセージ中のissue番号/URLが引数。
- **reviewerの起動方法**: Claude Codeはread-only指示付きサブエージェント（Agent tool）。
  Codexは `codex exec` によるfreshなサブプロセス。具体的な起動手順は
  `references/review-prompts.md` の冒頭に従う。

どちらの場合もreviewerがfreshなsession（この会話の文脈を持たない）であることが設計上の
要件であり、会話内で自問自答するreviewはreviewとして認めない。テストのレビューを人間へ
依頼して代替することもしない。

設計との既知の乖離（単一credential・単一セッション実行に伴う縮約。これ以外の逸脱はしない）:

- **evidenceとverdictはcheck runにもPR commentにも記録せず、ローカルのrun recordに留める**
  （「記録面」section）。自己承認の防止は構造（別App identity）ではなく手順で担保するため、
  reviewerの判定文をこのセッションが書き換えないこと。run recordにはreviewer出力のYAMLを
  そのまま貼る。
- **Pull Requestはclaim直後ではなく、実装完了（GREEN + refactor + checks pass）後に作る。**
  設計ではPRがRun identityであり全roundの記録面だが、このスキルは記録をローカルに持ち、PRの
  存在からphaseを導出するControllerも自動resumeも無い。Run identityはclaim branch
  `kudo/issue-<n>` が担い、RED位相のPRは作らない。
- **`needs_human`とround予算超過で停止しない。** 設計ではControllerが`needs_human` phaseへ
  送るが、このスキルにはlabelもwebhookも無く、人間が観測できる面はPRだけである。reviewerの
  判定は改変せず記録したうえで、このセッションが決裁して続行する。
- **final implementation reviewは行わない**（スコープ外）。設計ではfinal approveがready化の
  gateだが、このスキルにはそのgateが無いので、実装完了・checks passの状態をそのまま表す
  ready PRとして提出する。
- label（`ai-ready`等）とwebhook/pollingは使わない。ユーザーの明示的呼び出しがtriggerを代替する。
- mergeは行わない。成果物はPRであり、レビューとmerge判断は人間に残す。

## 入力

呼び出し引数をissue番号またはURLとして解釈する。

- 引数: $ARGUMENTS
- 上の行に `$ARGUMENTS` が文字どおり残っている場合（Codex等、引数展開のないハーネス）は、
  呼び出し時のユーザーメッセージからissue番号/URLを読み取る。

欠落・曖昧なら会話文脈から推測して補完せず、必要な入力を指摘して停止する
（contract discipline）。入力の不備は決裁の対象ではない。

## 予算と失敗の分類

- review round予算: `test_validity` 3回（Escalation Policy既定）。`test_revision_required` は
  この予算を1消費する。予算は「同じgateで自動修正を何回まで回すか」の上限であり、超過は
  escalationではなく決裁への切り替えを意味する。
- transport/tooling失敗（gh・git・ネットワーク）: 3回まで再試行し、quality判定とは混ぜない。
  再試行予算を使い切ったら停止して報告する。
- reviewerの`needs_human`、round予算超過、reviewerの指摘に同意できずに続行する判断は、
  いずれも決裁事由であって停止事由ではない。
- claim競合・契約不備・`stale_input` だけが停止事由である（実行原則）。

## 決裁（adjudication）

reviewerが`needs_human`を返した場合、round予算を使い切ってもblocking findingが残る場合、
またはreviewerの指摘を退けて続行する場合は、このセッションがControllerとして決裁する。

1. 決裁の材料は、reviewerへ渡したのと同じ明示的入力に限る（Issueのcanonical section、
   `authorityRefs`、diff、evidence）。会話の記憶や親Issueのproseを根拠にしない。
2. 権限の順序で解釈を選ぶ: Issueの `Decision Authority` → `authorityRefs` の内容 →
   リポジトリの設計正本（`docs/spec/`）→ 既存実装の慣行。上位で決まる論点を下位で覆さない。
3. 同程度に根拠のある解釈が残る場合は、可逆性の高い方、scopeの小さい方、確定済みテストの
   検出力を落とさない方を選ぶ。
4. reviewerのverdict本文は改変しない。決裁は独立した記録（`kudo:adjudication`）として、論点、
   選んだ解釈、根拠（authorityの該当箇所）、退けた代替、影響範囲、可逆性、人間に確認して
   ほしい点を書く。
5. Issueの `Decision Authority` が人間に留保していると読める論点を決裁した場合は、PR本文の
   「人間の確認が必要な決定」へ必ず列挙する。PRのレビューとmergeが人間のgateとして残るので、
   決裁の存在自体はPR提出を妨げない。
6. 破壊的操作、認証情報の扱い、外部への公開副作用のように安全側へ倒せない論点は決裁の対象外
   とする。その部分は実装せずscope外として記録し、PRの残存リスクへ明記して続行する。

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
6. run recordを作る: `.worktrees/run-issue-<n>/` を作り、claim checkpointを
   `01-claim-checkpoint.md` として書く（形式は `references/record-templates.md`）。
7. claim commit（empty commit `claim: #<n> <title>`）をpushする。**この時点ではPRを作らない。**
   このRunのidentityはbranch `kudo/issue-<n>` である。

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
4. commitしてpushし、test head SHAを記録する。test plan + RED evidenceをrun recordへ書く。

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
4. verdict（YAML）を受け取り、そのままrun recordへ書く。review worktreeを
   `git worktree remove` する。
5. gate判定:
   - `approve`（blocking findingゼロのときのみ有効）→ Step 4へ。この時のtest headを
     **確定test head**として固定する。
   - `request_changes` → findingsに対応してテストを修正し、新しいRED evidenceを取り、push
     して**新しいroundとして3から再実行**する。前roundのreviewerは再利用せず、必ず
     fresh起動する（session再開をしない設計）。round予算を1消費。
   - `needs_human`、または予算を使い切ってもblocking findingが残る場合 → 決裁する
     （「決裁」section）。決裁記録をrun recordへ書き、決裁時のtest headを確定test headとして
     固定してStep 4へ進む。確定の根拠がapproveか決裁かは記録に残す。

## Step 4: 実装とGREEN（implement → refactor）

確定test headより前にproduction実装を開始しない（順序不変条件）。決裁で確定させた場合も、
テストを先に固定してから実装するという順序は変えない。

1. staleness check（Step 3-1と同じ）。
2. 確定test headのテスト・fixture・helper・test commandを一切変更せず、テストを通す最小限の
   実装を行う。
3. テスト変更が必要だと判明したら `test_revision_required`: headを確定test headへ
   `git reset --hard` でrollbackし（実装はローカルstashに退避してよいが、公開lineageは
   巻き戻す）、test-revision-reportをrun recordへ書いて、Step 2のテスト修正からやり直す。
   test validityのround予算を1消費する。
4. GREEN: `go test ./...` 全pass。その後refactorし、`mise run check` を完全passさせる。
5. pushしてfinal head SHAを記録し、GREEN + checks evidenceをrun recordへ書く。

## Step 5: Pull Request作成

実装が完了した（GREEN + refactor + `mise run check` pass）このタイミングでPRを作る。RED位相の
PRを残さないための順序であって、gateを緩めるものではない（Step 3を通過していなければここへ
来ない）。

1. 自己検証してからPRを作る:
   - `git diff <確定testHead> HEAD -- <テスト関連path>` が空である（テスト変更があるなら
     Step 3をやり直してtest headを確定し直す。test gate迂回の検出）
   - final headで `mise run check` が成功している
   - 最終staleness check（Issue digest照合）
2. `gh pr create --base main --head kudo/issue-<n> --title "<Issue title>"` でPRを作る。
   draftにしないのは、実装完了・checks passという状態を正直に表すためである。PRのレビューと
   mergeはこのスキルのスコープ外なので、レビュー可能な状態で提出する。
3. PR bodyを書く（テンプレートは `references/record-templates.md`）。必須項目: Taskへの
   closing keyword（`Closes #<n>`）、outcome要約、AC ↔ テスト対応表、決裁事項と人間の確認が
   必要な決定、残存リスク、base/head SHA。verdictやevidenceの全文は載せない（記録面の規約）。
   散文は日本語。
4. 実装worktreeとreview worktreeを `git worktree remove` で削除する。run record
   （`.worktrees/run-issue-<n>/`）は削除せず残す（GitHubに無い唯一のevidenceであるため）。
5. ユーザーへ報告する: PR URL、test validityの結果（approve / 決裁と消費round数）、決裁事項と
   人間に確認してほしい点、advisory findingsの要約、残存リスク、run recordのパス。

## 横断ルール

- PR・commit messageの散文は日本語（リポジトリのCommunicationルール）。
- machine block・verdict等の構造化表現はYAML/Markdown。JSONは使わない。
- evidence・verdict・決裁は取得した時点で即座にrun recordへ書く。GitHubへは投稿しない。
- スキル実行中に本ワークフローの範囲外の問題（無関係なbug等）を見つけても本PRへ混入させない。
  報告に含めるに留める。ただし対象変更が直接作ったblocking riskは先送りしない。
