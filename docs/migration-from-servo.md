# Servo からの移行判断

## Strategy

Kudo は Servo の次 version や source-compatible rewrite ではなく、独立した greenfield product である。Servo の source、database、runtime を移植せず、Kudo で必要性を確認した invariant と workflow concept だけを再定義する。

Kudo の product、protocol、ADR が以後の source of truth である。Servo 側の draft が更新されても自動的に取り込まない。

## Concepts retained

Servo の`docs/requirements/lightweight-tdd-issue-to-pr-run/`にあった draft から、次の概念を採用した。

- machine-readable な Issue Contract と claim 前の strict validation
- Task Issue を execution-context root とする考え方
- 1 active Issue / 1 Run / 1専用worktree / 1branch / 1reviewable PR
- test 作成、RED evidence、独立 test validity review、実装、GREEN evidence の順序
- Implementation だけが mutable workspace と PR mutation を所有する権限分離
- versioned Review Request / Result と immutable artifact handoff
- changed Issue/head/artifact に対する review result の staleness
- transport/execution failure と quality verdict の分離

これらは主に次の draft を参照し、Kudo namespace と現在の product boundary へ書き直した。

- `issue-contract.md`
- `implementation-review-protocol.md`
- `requirements.md`のcore workflow部分

## New Kudo decisions

次は Servo からの移植ではなく、Kudo で改めて決めた。

- `mrbaron3` assignment と`ai-ready`をcandidate条件にする簡潔なrouting
- Webhookをprimary通知、60秒pollingを必須fallbackとするunified reconciliation
- `ai-ready`、`ai-in-progress`、`ai-review-waiting`、`ai-needs-human`のstatus projection
- model-bearing Operation ごとのfresh Codex/Claude sessionと、Orca handoffに似た明示artifact handoff
- test reviewに加え、PR作成前のfinal implementation review gate
- PostgreSQLをRun state、Operation queue、lease、inbox/outboxの正本にする構成
- 一つのGo binaryをrole別containerとして起動するDocker Compose deployment
- content-addressed artifact volumeとIssue Worker専用workspace volume

Compose と PostgreSQL を採用していても、Servo の process topology、table、queue、artifact naming を流用したことを意味しない。Kudo の boundary から独立に設計・testする。

## Deliberately not migrated

- Servo の Go/TypeScript source code、database schema、migration、API
- `roadmap.yaml`と旧ADR群
- Servo 固有のtmux/process supervisor/container orchestration
- provider session resume、conversation transcript handoff、provider private database共有
- provider固有のeffort/review profileをIssue Contractへ埋め込む設計
- repair supervisor、domain alignment、複数review sessionの合議
- best-of-N execution、pass@k、scoring、benchmark、evaluation harness
- 旧artifact namingと旧schema namespace
- draftの包括的な`acceptance.yaml`

必要になった項目は機械的にcopyせず、Kudoで観測した制約、選択肢、結果を新しいADRまたはversioned contractとして追加する。

## Provenance note

最初の選別は2026-08-11時点のServo未コミットdraftを基に行った。draftはcommitで固定されていなかったため、実装時はKudo repository内の文書だけをauthorityとする。
