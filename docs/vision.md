# Product vision

## Product statement

Kudo は、決まった形式で書かれた GitHub Issue を受け取り、テストを先に作成し、そのテスト自体を独立 reviewer が承認してから実装し、証跡付き Pull Request を人間へ届ける自動実装 runtime である。

人間が実装 session を監視し続けなくても、Webhook の欠落や process 再起動から復旧し、どの Issue、commit、test、review verdict を根拠に PR が作られたかを追跡できることを目指す。

## Users and responsibilities

人間は、Task Issue の目的、scope、受入条件、authority、検証方法、判断境界を確定し、対象 assignee と`ai-ready` label で実行を依頼する。Kudo は、不足した仕様を会話履歴から推測せず、実行可能性を検証してから作業する。

Kudo は、テスト作成、独立 review、実装、refactor、検証、PR 作成を再現可能な workflow として進める。品質判断が必要な review verdict は Review Worker が所有し、Controller は verdict を上書きしない。

人間は最終的な Pull Request review、merge、release を所有する。Kudo の正常な handoff point は PR が作成され、Issue が`ai-review-waiting`になった時点である。

## Required outcome

対象 Issue から作られた Pull Request には、少なくとも次の lineage が存在しなければならない。

- GitHub から直接取得し、digest で固定した Issue Revision
- base commit、dependency completion、authority content を固定した Context Manifest
- Issue の Acceptance Criteria に対応する test plan と test patch
- 対象機能が未実装であることを示す RED command evidence
- 実装と隔離された Review Worker による test validity approval
- 承認済み test を通す実装、refactor、GREEN command evidence
- 最終 head に対する独立した final implementation approval
- 実行した必須 check、その結果、残存 risk を含む Pull Request

artifact、Review Request、Review Result は immutable identity を持つ。Issue Revision、head SHA、artifact digest のいずれかが変われば、以前の review approval は再利用しない。

## Product behavior

Kudo は次を製品の標準動作とする。

- GitHub webhook を低遅延の通知経路、60秒ごとの polling を取りこぼし回復経路として併用する。
- 両経路を同じ冪等な`ReconcileIssue` operation へ集約する。
- `mrbaron3`が assign され、`ai-ready`が付いた open Task Issue だけを候補にする。
- dependency のない候補を、Issue/Run 単位の lease と専用 workspace で並行実行する。
- model を使う Operation ごとに Codex または Claude の fresh headless session を開始する。
- Issue/Review用providerはdeployment policyで明示し、選択したmodel/adapter policyをRunへ固定する。
- review は Issue と immutable artifact を受け取る新規 read-only session で行う。
- review finding は元の論理作業 lane へ返すが、provider session は再開せず、新しい session へ明示的に handoff する。
- workflow state を PostgreSQL に永続化し、GitHub label/comment はその投影として扱う。
- Docker Compose で Controller、Issue Worker、Review Worker、PostgreSQL を container として配備する。

詳細な順序は [End-to-end workflow](workflow.md)、技術構成は [Architecture](architecture.md) と [Runtime platform](runtime-platform.md) が定義する。

## Principles

- Explicit contract: 必須情報が欠落または曖昧なら、推測で実装せず claim を拒否または人間へ escalation する。
- Issue-rooted context: Task Issue を実行 context の root とし、親や依存 Issue の prose を暗黙に継承しない。
- Test-first gate: test validity approval の前に production implementation を開始しない。
- Independent review: implementation role は自身の test または実装を approve できない。
- Immutable handoff: session 間では transcript ではなく digest 付き artifact、commit、versioned result を渡す。
- Recoverable execution: process-local memory を正本にせず、再起動後に安全に retry または再開できる。
- Scoped concurrency: repository 全体を lock せず、Issue と Run の競合だけを排除する。
- Least authority: Controller、Issue Worker、Review Worker に必要最小限の filesystem/GitHub 権限だけを与える。
- Replaceable edges: GitHub、provider、process、artifact、telemetry は明示的な adapter boundary に置く。

## Product boundary

Kudo が担当するのは、実行依頼の検出から reviewable PR の handoff までである。次は標準 workflow に含めない。

- 人間の代わりに曖昧な Issue を完成させること
- Pull Request の merge、Issue close、deploy、release
- 人間の PR review comment に対する自動修正 loop
- pass@k、best-of-N、複数 candidate の競争実行
- model/provider のランキングや scoring dashboard
- 複数 host を跨ぐ distributed scheduler
- Kubernetes を必須とする deployment
- Servo の API、database schema、artifact layout との互換性

評価 harness は runtime telemetry と分離し、別の design decision まで保留する。

## Definition of product completion

完成とは、happy path の demo が一度動くことではない。少なくとも次が自動 test または明示した live verification で証明されている状態を指す。

- webhook を失っても polling が同じ候補を発見する
- duplicate、遅延、順不同 event が二重 Run または二重 PR を作らない
- Controller または Worker の再起動後に、lease と永続 state から処理を回復できる
- test review の`request_changes`が fresh session に handoff され、approve まで実装へ進まない
- RED、GREEN、refactor 後 checks、二つの review approval が対象 digest と一致する
- dependency のない複数 Issue は同時実行でき、同じ Issue は二重実行されない
- Review Worker は implementation worktree と write credential を持たない
- Issue 変更または head 変更が以前の approval を stale にする
- PR と`ai-review-waiting` projection が crash/retry 下でも一度だけ成立する
- Compose stack の health、migration、backup/restore、graceful shutdown 手順が検証されている
