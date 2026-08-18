# Implementation plan

## Target

本計画は bootstrap demo ではなく、[Product vision](vision.md) の completion criteria を満たす Compose-deployed issue-to-PR runtime を完成させるための delivery order を定義する。

各 milestone は外部 adapter を後回しにするだけの layer 実装ではなく、可能な範囲で一つの recovery/idempotency behavior を end-to-end に証明する。fast deterministic test を先に実行し、live GitHub/provider test は opt-in として最後に重ねる。

## Current status

現在repositoryに実装済みなのは、`kudo help`/`kudo version`のCLI bootstrap、Milestone 0のCompose開発基盤（multi-stage Dockerfile、PostgreSQL 18.4付き開発用Compose、container内check/integration test入口）、`kudo.issue/v1alpha1`のstrict parser、Issue Observation・canonical Task Context・Context Manifest・Execution Policyを作るpure compiler core、およびWorker Operation/Review protocolのcanonical identity・binding・semantic staleness判定である。target architectureは文書化されているが、次は未実装である。

- PostgreSQL schema、Operation queue、lease、inbox/outbox
- GitHub webhook/poller/reconciliation
- Artifact Store と Run workspace
- Codex/Claude provider adapter
- Issue Worker / Review Worker / Controller
- production Compose topology（role service、migration、health/operations）

target document が完成形を定義していることと、code が完成していることを混同しない。

## Delivery rules

- protocol、parser、fixture、test を同じ change で更新する。
- pure transition、fake boundary、targeted test を先に実装し、network/process/container test を後から追加する。
- PostgreSQL、GitHub、process、clock、filesystem、provider、telemetry は interface と deterministic fake を持つ。
- transport/execution failure と quality verdict を別 type として保つ。
- model-bearing Operation は常に fresh session factory を通す。
- 一つの milestone の temporary shortcut を target architecture として文書化しない。
- Milestone 0以降の実装とintegration testは、host固有のdaemonではなくCompose基盤で再現できる状態を維持する。
- 各 milestone の merge 前に`mise run check`を通す。

## Milestone 0 — Containerized development foundation

機能実装より先に、Go applicationとPostgreSQLを同じCompose contractでbuild・testできる開発基盤を作る。未実装のController/Workerをdummy processとして常駐させず、以後のmilestoneが継続利用するimage、network、volume、configuration boundaryを固定する。

### Milestone 0 deliverables

- 現在の単一`kudo` binaryとtestをbuildできるreproducible multi-stage Dockerfile
- non-root development/test imageと`.dockerignore`
- PostgreSQL 18.4をdigest pinしたdevelopment`compose.yaml`
- application/test service、PostgreSQL healthcheck、named volume、internal network
- local/test overrideとnon-secret example configuration
- container内で`mise run check`とPostgreSQL integration testを実行する標準command
- Docker socket/Docker-in-Dockerを必要としないbuild/test path

### Milestone 0 exit criteria

- cleanなCompose-capable hostでimageをbuildし、PostgreSQLがhealthyになる。
- hostへGo、PostgreSQL、Kudo daemonを直接installせず、container内で`mise run check`が成功する。
- PostgreSQL portをhostへ常時公開せず、test serviceから接続できる。
- image、volume、network、configuration nameが後続のproduction Composeへ拡張可能で、throwawayの別構成になっていない。
- macOS `linux/arm64`で検証し、`linux/amd64` buildを壊さないDockerfileになっている。

## Milestone 1 — Protocol core

IssueRef から Task の execution context と review identity を決定論的に構築できる pure core を作る。

### Milestone 1 deliverables

- `kudo.issue/v1alpha1`の fixed section と YAML block の strict parser
- unknown/duplicate field、不正 enum、欠落/重複 AC、曖昧 authority の validation
- Issue Observation、Task Context、Context Manifest の canonical encoding と SHA-256 identity
- Execution Policy snapshot と Operation envelope/result の canonical identity
- Review Request / Result / Artifact Manifest の validation と staleness rule
- claim/review/transport error taxonomy
- fixture corpus と canonicalization golden test

### Milestone 1 exit criteria

- 同じ input は常に同じ digest になり、whitespace と ordering rule が fixture で固定される。
- changed Context Manifest（Task Context または authority content の変化を含む）、Execution Policy、head SHA、artifact manifest、policy ref が以前の review を stale にする。
- Issue Observation だけの変化は audit lineage へ追記され、Operation identity と approval を stale にしない。
- malformed contract、human decision、transport failure、review finding が混同されない。
- GitHub/network/filesystem/provider なしで全 behavior を unit test できる。

## Milestone 2 — Durable control plane

PostgreSQL に authoritative Run state、Operation queue、lease、inbox/outbox を実装し、crash 後も state machine を回復できるようにする。

### Milestone 2 deliverables

- versioned SQL migration と`kudo migrate up`
- Run aggregate と pure transition function
- Run version の optimistic concurrency control
- 1 IssueRef に active Run 最大一つの database constraint
- role/kind ごとの Operation queue、attempt、lease、heartbeat、reaper
- delivery inbox と transactional status outbox
- retry class、backoff、jitter、clock injection
- PostgreSQL integration test 用 disposable Compose profile

### Milestone 2 exit criteria

- transition と次 Operation/outbox が一つの transaction で commit される。
- duplicate event と concurrent claim が一つの active Run だけを作る。
- Worker crash を模した lease expiry 後、別 attempt が同じ logical Operation を取得する。
- dependency のない Run は並行に進み、repository global lock を使わない。
- PostgreSQL restart 後に process-local memory なしで queue/state を復元できる。

## Milestone 3 — GitHub discovery and claim

Webhook と必須 polling fallback を同じ`ReconcileIssue`へ接続し、実行可能な Issue を durable claim する。

### Milestone 3 deliverables

- GitHub App authentication と role-scoped installation token
- `POST /webhooks/github`の raw-body signature verification、payload limit、delivery inbox
- startup reconciliation と既定60秒 polling、pagination、rate-limit handling
- candidate filter: open、non-PR、assignee`mrbaron3`、label`ai-ready`
- live Issue Reader、native relationship、dependency、repository content resolver
- Issue/Run scoped claim lease と active Run validation
- status outbox consumer と4 label lifecycle
- `healthz`、`readyz`、structured log

### Milestone 3 exit criteria

- webhook を意図的に捨てても、polling が Issue を発見して同じ Run を作る。
- duplicate/遅延/順不同 webhook と poll overlap が二重 Run を作らない。
- candidate 外、dependency/capacity 待ち、contract rejection、transport failure が仕様どおり区別される。
- claim commit 後に projection process を停止・再開しても、最終 label set が一貫する。
- live GitHub test がなくても fake API で pagination、rate limit、mutation retry を検証できる。

## Milestone 4 — Artifact, workspace, and process runtime

Worker が provider と repository command を安全に実行し、session 間を immutable artifact で handoff できる基盤を作る。

### Milestone 4 deliverables

- named volume 向け content-addressed Artifact Store
- atomic write、digest/length verification、orphan detection
- Run scoped clone/worktree/branch/checkpoint lifecycle
- child process supervisor、process-group cancellation、timeout、bounded output、secret redaction
- fresh session factory と operation-scoped temp/config directory
- Codex headless adapter と Claude headless adapter
- Issue/Review provider設定からimmutable Execution Policyを固定するresolver
- provider structured output schema と invalid response handling
- Issue/Review role ごとの credential/filesystem policy

### Milestone 4 exit criteria

- 同一 digest の異なる bytes を拒否し、corrupt/missing artifact を検出する。
- model Operation を連続実行しても session ID、transcript、private state が再利用されない。
- timeout/crash 後の attempt が commit/artifact から再構築され、以前の process を resume しない。
- Review runtime は Issue workspace path を受け取らず、head SHA から別 checkout を作る。
- fake process/provider を使う deterministic test と、opt-in CLI smoke test の両方がある。

## Milestone 5 — RED and test review loop

Issue claim から test validity approval までの完全な TDD 前半を実装する。

### Milestone 5 deliverables

- `author_tests`と`revise_tests` Issue Operation
- Acceptance Criteria と test plan/test case の traceability
- test-only checkpoint と RED command evidence
- infrastructure failure と expected RED の classifier
- `publish_head`による draft PR publish と pull request observation
- `test_validity` Review Request/Result handler
- `request_changes` finding の fresh revision session handoff
- `needs_human` comment と escalation/resumption

### Milestone 5 exit criteria

- expected failure の RED が固定され、head が draft PR へ publish されるまで review request を作らない。
- reviewer はIssue ObservationとPR observationでlive freshness（Issue body digest、PR の open/draft・head・base）を検証し、canonical Task Context、artifact、read-only checkoutだけでverdictを返す。
- `request_changes`後は同じ worktree の新しい provider session が修正し、新しい request digest で再 review する。
- test approval なしに implementation Operation を enqueue できない。
- Issue edit、test head change、artifact change が approval を stale にする。

## Milestone 6 — GREEN, refactor, final review, and PR

承認済み test から implementation を完成させ、人間 review 用 PR へ handoff する。

### Milestone 6 deliverables

- `implement`と`repair_implementation` Issue Operation
- GREEN、refactor 後 verification、repository required checks の evidence
- test mutation detection と test review gate への rollback
- `final_implementation` Review Request/Result handler
- approved head binding と stale review prevention
- `finalize_pull_request`による required PR body 確定と draft 解除
- required PR body validator と `.github/pull_request_template.md` integration
- `ai-review-waiting` projection

### Milestone 6 exit criteria

- implementation は approved test validity digest を入力に持つ。
- refactor 後に同じ test/check を再実行し、evidence を最終 head に bind する。
- final`request_changes`は fresh repair session に渡り、head change 後に必ず再 review する。
- final approval と required checks がない head では PR を ready 化できない。draft の publish は approve を gate にしない。
- crash が publish/finalize response の前後どちらで起きても PR は一つだけになり、Run は`awaiting_human_review`へ収束する。
- PR body が Issue、AC、RED/GREEN、二つの review、checks、risk、Run/base/head を参照する。

## Milestone 7 — Production Compose deployment and operations

Milestone 0のCompose基盤を、完成したController/Worker use caseを実行するproduction topologyへ拡張し、[Runtime platform](runtime-platform.md)の全serviceと運用contractを満たす。

### Milestone 7 deliverables

- `kudo controller`、`kudo worker issue`、`kudo worker review`、`kudo migrate up`のrole command
- controller imageとprovider CLI/toolchainを含むworker image flavor
- `linux/arm64` / `linux/amd64` production image buildとSBOM/provenance
- Milestone 0の`compose.yaml`を拡張するproduction profile
- Controller、Issue Worker、Review Worker、PostgreSQL、migration service
- healthcheck、dependency ordering、restart policy、resource limit、read-only root filesystem
- Compose secrets、GitHub App/provider credential setup
- PostgreSQL/artifact backup と restore command/runbook
- graceful shutdown と lease drain
- GHCR publish と pinned image update procedure

### Milestone 7 exit criteria

- clean host で documented setup から stack が起動し、health/readiness が green になる。
- Milestone 0で確立したbuild/test commandとvolume/configuration contractがproduction profileでも維持される。
- host に Kudo daemon または provider GUI/session を必要としない。
- Controller/Review container から Issue workspace が見えず、Review credential で write API を実行できない。
- PostgreSQL/application restart、Worker kill、volume restore の recovery test が通る。
- Docker socket がどの service にも mount されていない。
- pinned image と migration/rollback boundary が release note で追跡できる。

## Milestone 8 — Product acceptance and hardening

個別 component の完成ではなく、実運用に近い failure matrix で product completion を確認する。

### Automated acceptance matrix

- happy path: Issue -> RED -> draft PR publish -> test approve -> GREEN/refactor -> final approve -> PR ready化 ->`ai-review-waiting`
- test and final`request_changes`の複数 loop
- `needs_human`、人間修正、`ai-ready`再付与、safe resume/supersede
- webhook loss、duplicate、reorder、invalid signature、poll overlap
- GitHub/provider/PostgreSQL の timeout、rate limit、temporary outage
- Controller/Worker kill と expired lease recovery
- artifact corruption、workspace loss、Issue/head/authority staleness
- dependency graph、cycle、base 未統合、複数 independent Run
- PR/label/comment mutation の ambiguous response と idempotent recovery

### Live verification

dedicated test repository と provider sandbox credential を使う opt-in suite で、GitHub webhook、polling、branch/PR、Codex/Claude CLI の実 boundary を検証する。課金、外部 mutation、cleanup 対象を明示し、通常の`mise run check`には含めない。

headless test で同等の confidence が得られる部分は先に headless で検証する。GitHub delivery、provider CLI lifecycle、macOS container runtime のような vendor boundary は fake だけを実機証明として扱わず、残る live verification を release checklist に記録する。

## Product-wide exit criteria

全 milestone 完了に加え、次が成立して初めて Kudo runtime を完成扱いにする。

- [Product vision](vision.md) の completion criteria を自動 evidence へ対応付けられる。
- [End-to-end workflow](workflow.md) の全 transition、retry、escalation が実装されている。
- [Runtime platform](runtime-platform.md) の deployment、security、backup/recovery contract が検証されている。
- versioned contract と migration に backward/forward compatibility policy がある。
- operator が Run/Operation/attempt/outbox を診断し、安全に retry できる runbook がある。
- live integration が opt-in でも、core correctness は deterministic tests だけで再現できる。
- merge/deploy、pass@k、multi-candidate evaluation を runtime completion と混同していない。
