# Implementation plan

## Target

本計画は bootstrap demo ではなく、[プロダクト設計](../01_product-design/README.md) の完成条件を満たす
Compose-deployed issue-to-PR runtime を完成させるための delivery order を定義する。

各 milestone は外部 adapter を後回しにするだけの layer 実装ではなく、可能な範囲で一つの recovery/idempotency behavior を end-to-end に証明する。fast deterministic test を先に実行し、live GitHub/provider test は opt-in として最後に重ねる。

## Current status

現在repositoryに実装済みなのは、`kudo help`/`kudo version`のCLI bootstrap、Milestone 0のCompose開発基盤（multi-stage Dockerfile、PostgreSQL 18.4付き開発用Compose、container内check/integration test入口）、`kudo.issue/v1alpha1`のstrict parser、Issue Observation・canonical Task Context・Claim Requirementsを作るpure Issue Compiler、Context Manifest・Execution Policyのcanonical core、Worker Operation/Review protocolのcanonical identity・binding・semantic staleness判定、およびReview Result binding lineageを含むversioned PostgreSQL schemaとRunStoreである。target architectureは文書化されているが、次は未実装である。

- Operation queue、lease、inbox/outbox
- GitHub webhook/poller/reconciliation
- Artifact Store と Run workspace
- Codex/Claude provider adapter
- Issue Worker / Review Worker / Controller
- production Compose topology（role service、migration、health/operations）

target document が完成形を定義していることと、code が完成していることを混同しない。

## Delivery order

[ADR-0007](../../adr/0007-vertical-slice-delivery.md)により、**milestoneは「完成の定義」であって実行順序の単位ではない**。実行は縦の貫通sliceを単位とし、各milestoneの貫通に必要な最小部分を横断して先に通し、残りを後から幅として戻す。

| slice | 到達点 | 開始するmilestone |
| --- | --- | --- |
| S1 | live GitHub Issue 1件が`claimed` Runになる | M3 |
| S2 | `author_tests`がRED evidenceとsource-bundleを固定する | M4、M5 |
| S3 | test-only headがpushされdraft PRが1本できる | M5 |
| S4 | `test_validity` reviewが1 round通る | M5 |
| S5 | `implement`とfinal reviewを通しPRがready化する | M6 |

M0とM1は完了扱いのまま変わらない。M2はRunStoreまで到達しており、残るqueue / lease / inbox / outboxはslice順へ従属する。M7とM8は貫通後、幅を戻し切ってから着手する。

各milestoneのexit criteriaは削らない。貫通の時点で未達のまま残るものは次のとおりであり、貫通が通ったことをexit criteria達成と誤読しないため、この表を台帳として維持する。

| milestone | 貫通時点で未達のまま残るもの |
| --- | --- |
| M2 | Operation queue、lease、attempt recovery、delivery inbox。**status outbox producer は S1 で決着が要る**（ADR-0007 §1 S1） |
| M3 | webhook、pagination網羅、4 label lifecycle、rate limit retry、`healthz` / `readyz` |
| M4 | orphan detection、secret redaction網羅、両provider adapter。**role別credential / filesystem policy は S4 を例外とする**（最初の review verdict 時点で Review Worker は read-only／workspace 非 mount） |
| M5 | `revise_tests`、`needs_human` escalation / resumption、staleness全経路 |
| M6 | `repair_implementation`、test mutation detection、required checks統合、PR body validator |

各sliceで「何を作るか」と「何を意図的に雑にするか」、および後から入れられないため貫通でも落としてはならないものは、ADR-0007が定義する。

## Delivery rules

- protocol、parser、fixture、test を同じ change で更新する。
- pure transition、fake boundary、targeted test を先に実装し、network/process/container test を後から追加する。
- PostgreSQL、GitHub、process、clock、filesystem、provider、telemetry は interface と deterministic fake を持つ。
- transport/execution failure と quality verdict を別 type として保つ。
- model-bearing Operation は常に fresh session factory を通す。
- 一つの milestone の temporary shortcut を target architecture として文書化しない。貫通slice中に意図して雑にしたものは実装PRと[docs/deferred/](deferred/)へ記録し、`architecture.md`や`contracts/`へは書かない。
- `internal/contract`はfeature freezeする（[ADR-0007](../../adr/0007-vertical-slice-delivery.md) D2）。変更は「貫通で実際に詰まった箇所」だけを理由に行い、網羅性や対称性を理由に追加しない。
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
- structured claim context、Execution Policy / Escalation Policy snapshot、Operation envelope/resultのcanonical identity
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
- terminal Result / AttemptFailure / ProtocolError recordの排他とfail-closed routing
- delivery inbox と transactional status outbox
- Compiler/schema/digest/baseを持つstructured claim contextとExecution / Escalation Policyのdurable schema
- retry class、backoff、jitter、clock injection
- PostgreSQL integration test 用 disposable Compose profile

### Milestone 2 exit criteria

- transition と次 Operation/outbox が一つの transaction で commit される。
- Runとstructured claim context / policyが同じtransactionで固定され、欠落または不正なdigestを受理しない。
- duplicate event と concurrent claim が一つの active Run だけを作る。
- Worker crash を模した lease expiry 後、別 attempt が同じ logical Operation を取得する。
- dependency のない Run は並行に進み、repository global lock を使わない。
- PostgreSQL restart 後に process-local memory なしで queue/state を復元できる。

## Milestone 3 — GitHub discovery and claim

Webhook と必須 polling fallback を同じ`ReconcileIssue`へ接続し、実行可能な Issue を durable claim する。

実行順序は[ADR-0007](../../adr/0007-vertical-slice-delivery.md)のS1が先行し、webhookとlabel lifecycleの残りは幅を戻す段で満たす。

### Milestone 3 deliverables

- GitHub App authentication と role-scoped installation token
- `POST /webhooks/github`の raw-body signature verification、payload limit、delivery inbox
- startup reconciliation と既定15分 polling、pagination、rate-limit handling
- candidate filter: open、non-PR、configured target assignee / ready label（既定`mrbaron3` / `ai-ready`）
- live Issue Reader、native relationship、dependency、repository content resolver
- claim時と各後続Operationでlive Issue/authorityを取得し、同じCompiler versionでTask Context / Context
  Manifest identityを再計算するcontext reconstruction handler
- raw Issue bodyとcanonical YAMLを保存せず、Compiler/schema/digest/baseをPostgreSQLへ固定するstructured claim context
- ControllerがIssue / Review provider設定からimmutable Execution Policyを、attempt retry / review round設定から
  Escalation Policyを固定するresolver
- Issue/Run scoped claim leaseとactive Run validation、`merged` terminal Runの再claim防止
- status outbox consumer と4 label lifecycle
- `healthz`、`readyz`
- 後続roleも再利用するstructured logging contract / adapterと、Controllerの
  webhook / reconciliation / claim / outbox correlation field

### Milestone 3 exit criteria

- webhook を意図的に捨てても、polling が Issue を発見して同じ Run を作る。
- duplicate/遅延/順不同 webhook と poll overlap が二重 Run を作らない。
- candidate 外、dependency/capacity 待ち、contract rejection、transport failure が仕様どおり区別される。
- candidate のassignee / ready labelをconfigurationで上書きしても同じfilter ruleが適用される。
- required claim context fieldとExecution / Escalation Policy refが固定されるまでclaim successをcommitしない。
- 後続Operationは開始時・完了時にlive contextを再構築し、意味的に同じなら継続、期待digestと異なればstaleになる。
- claim commit 後に projection process を停止・再開しても、最終 label set が一貫する。
- live GitHub test がなくても fake API で pagination、rate limit、mutation retry を検証できる。

## Milestone 4 — Artifact, workspace, and process runtime

Worker が provider と repository command を安全に実行し、session 間を immutable artifact で handoff できる基盤を作る。

実行順序は[ADR-0007](../../adr/0007-vertical-slice-delivery.md)のS2が先行する。provider session isolation、Artifact Store のlayout / durability / streaming API、checkpoint commit identity は後入れできないため、最小形でも落とさない。

### Milestone 4 deliverables

- test / implementation / review evidence専用のnamed volume向けcontent-addressed Artifact Store。raw Issue
  body、Issue Observation、Task Context、Context Manifestは保存対象にしない
- 複数Workerのconcurrent append、corruption検出、orphan detection / cleanup
- Run scoped clone/worktree/branch/checkpoint lifecycle
- child process supervisor、process-group cancellation、timeout、bounded output、secret redaction
- fresh session factory と operation-scoped temp/config directory
- Codex headless adapter と Claude headless adapter
- Runに固定済みExecution Policyを各provider invocationへ適用するadapter boundary
- provider structured output schema と invalid response handling
- Issue/Review role ごとの credential/filesystem policy

### Milestone 4 exit criteria

- 同一 digest の異なる bytes を拒否し、corrupt/missing artifact を検出する。
- model Operation を連続実行しても session ID、transcript、private state が再利用されない。
- timeout/crash後のattemptがstructured claim context、live GitHub/source、commit/evidence artifactから再構築され、以前のprocessをresumeしない。
- Review runtime は Issue workspace path を受け取らず、head SHA から別 checkout を作る。
- fake process/provider を使う deterministic test と、opt-in CLI smoke test の両方がある。

## Milestone 5 — RED and test review loop

Issue claim から test validity approval までの完全な TDD 前半を実装する。

実行順序は[ADR-0007](../../adr/0007-vertical-slice-delivery.md)のS2（RED evidence）、S3（draft PR publish）、S4（test validity review 1 round）に分割される。S3到達が貫通の主目的である。

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
- reviewerはlive Issue/authorityを再compileしてTask Context / Context Manifest identityを、live PRでopen/draft・head・baseを検証し、一致確認済みcanonical Task Context、evidence artifact、read-only checkoutだけでverdictを返す。
- `request_changes`後は同じ worktree の新しい provider session が修正し、新しい request digest で再 review する。
- test approval なしに implementation Operation を enqueue できない。
- Task Context / Context Manifestを変える意味的なIssue edit、test head change、artifact change が approval を
  stale にする。Issue Observationだけの変化はaudit lineageへ追記し、approvalを維持する。

## Milestone 6 — GREEN, refactor, final review, and PR

承認済み test から implementation を完成させ、承認済み head を merge して Task Issue を close する。

実行順序は[ADR-0007](../../adr/0007-vertical-slice-delivery.md)のS5に対応する。

### Milestone 6 deliverables

- `implement`と`repair_implementation` Issue Operation
- GREEN、refactor 後 verification、repository required checks の evidence
- performance bound宣言時のTask固有command実行と`performance-evidence`
- test mutation detection、`test_revision_required`による rollback / 差し戻しと round 予算消費
- `final_implementation` Review Request/Result handler
- approved head binding と stale review prevention
- `finalize_pull_request`による required PR body 確定と draft 解除
- required PR body validator と `.github/pull_request_template.md` integration
- `merge_pull_request`による merge gate 評価、compare-and-merge、head branch 削除
- merge intent の idempotency identity と、外部 close/merge との区別
- Task Issue close と `ai-merged` projection

### Milestone 6 exit criteria

- implementation は approved test validity digest を入力に持つ。
- refactor 後に同じ test/check を再実行し、evidence を最終 head に bind する。
- performance bound宣言時は測定command、固定条件、環境identity、複数回実行の要約、bound比較を最終headへbindし、宣言がないTaskへ標準harnessを推測して要求しない。
- final`request_changes`は fresh repair session に渡り、head change 後に必ず再 review する。
- final approval と required checks がない head では PR を ready 化できない。draft の publish は approve を gate にしない。
- finalize / merge の開始時に live context を再構築し、final approve 後の Issue の意味的編集を stale として検出する。
- crash が publish/finalize/merge response の前後どちらで起きても PR は一つだけになり、merge は一度だけ成立し、Run は`merged`へ収束する。
- PR body が Issue、AC、RED/GREEN、二つの review、checks、risk、Run/base/head を参照する。

## Milestone 7 — Production Compose deployment and operations

Milestone 0のCompose基盤を、完成したController/Worker use caseを実行するproduction topologyへ拡張し、[Runtime platform](../05_design/03_runtime-platform.md)の全serviceと運用contractを満たす。

### Milestone 7 deliverables

- `kudo controller`、`kudo worker issue`、`kudo worker review`、`kudo migrate up`のrole command
- controller imageとprovider CLI/toolchainを含むworker image flavor
- `linux/arm64` / `linux/amd64` production image buildとSBOM/provenance
- Milestone 0の`compose.yaml`を拡張するproduction profile
- Controller、Issue Worker、Review Worker、PostgreSQL、migration service
- healthcheck、dependency ordering、restart policy、resource limit、read-only root filesystem
- Compose secrets、GitHub App/provider credential setup
- PostgreSQL/artifact backup と restore command/runbook
- versioned contract、queue payload、artifact / review protocol、database migration の
  backward / forward compatibility policyとrelease boundary
- Run / Operation / attempt / outboxを診断し、期待stateとidempotency identityを確認してから
  retryするoperator runbook / command
- graceful shutdown と lease drain
- GHCR publish と pinned image update procedure

### Milestone 7 exit criteria

- clean host で documented setup から stack が起動し、health/readiness が green になる。
- Milestone 0で確立したbuild/test commandとvolume/configuration contractがproduction profileでも維持される。
- host に Kudo daemon または provider GUI/session を必要としない。
- Controller/Review container から Issue workspace が見えず、Review credential で write API を実行できない。
- PostgreSQL/application restart、Worker kill、volume restore の recovery test が通る。
- 全roleのlogがMilestone 3のstructured logging contractに従い、Run / Operation / attempt / IssueRefで
  相関できる。
- compatibility policyをfixture / migration testで検証し、operator runbookのdiagnose / safe retryを
  disposable Compose projectで実行して二重OperationやGitHub mutationを作らない。
- Docker socket がどの service にも mount されていない。
- pinned image と migration/rollback boundary が release note で追跡できる。

## Milestone 8 — Product acceptance and hardening

個別 component の完成ではなく、実運用に近い failure matrix で product completion を確認する。

### Milestone 8 deliverables

- Product completion criteriaと自動test / artifact / live verificationを対応付けるacceptance evidence matrix
- 下記failure matrixを決定論的に実行するheadless acceptance suite
- dedicated repository / sandbox credential、課金・外部mutation・cleanup境界を明示したopt-in live suite
- vendor / device boundaryに残るlive verificationと実行結果を記録するrelease checklist
- Milestone 7が所有するcompatibility policyとoperator diagnose / safe-retry runbookのacceptance scenario

### Automated acceptance matrix

- happy path: Issue -> RED -> draft PR publish -> test approve -> GREEN/refactor -> final approve -> PR ready化 -> merge + branch削除 -> Issue close ->`ai-merged`
- merge gate: check pending の待機、check failure / conflict / protection 拒否の`merge_blocked`、merge 直前の head 変化
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

### Milestone 8 exit criteria

- automated acceptance matrixの全scenarioがdeterministic suiteで成功し、failure注入後も一つのRun、Pull
  Request、status projectionへ収束する。
- product completion criteriaの各項目がtest result、immutable artifact、runbook verification、または
  residual live verificationのいずれかへ一意に対応付く。
- opt-in live suiteがGitHub delivery / mutation、supported provider CLI lifecycle、reference macOS container
  runtimeの実boundaryを検証し、実行しない環境では残項目、理由、実行手順をrelease checklistへ記録する。
- compatibility fixture / migration testとoperator runbook scenarioが成功し、直前のsupported releaseからの
  upgrade / recovery / safe retry boundaryを再現できる。
- live verificationの外部mutationと課金対象が記録され、cleanup後にtest repository、credential、artifactの
  残存状態を確認できる。

## Product-wide exit criteria

全 milestone 完了に加え、次が成立して初めて Kudo runtime を完成扱いにする。

- [プロダクト設計](../01_product-design/README.md) の完成条件を自動 evidence へ対応付けられる。
- [End-to-end workflow](../05_design/02_workflow.md) の全 transition、retry、escalation が実装されている。
- [Runtime platform](../05_design/03_runtime-platform.md) の deployment、security、backup/recovery contract が検証されている。
- versioned contract と migration に backward/forward compatibility policy がある。
- operator が Run/Operation/attempt/outbox を診断し、安全に retry できる runbook がある。
- live integration が opt-in でも、core correctness は deterministic tests だけで再現できる。
- merge/deploy、pass@k、multi-candidate evaluation を runtime completion と混同していない。
