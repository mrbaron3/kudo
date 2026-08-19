# 03. システム仕様

Kudo の完成形を、プロダクト全体の視点から示す中央仕様である。Kudo は、定型 GitHub Issue を
入力として、独立した test validity review を含む TDD を実行し、証跡付き Pull Request を人間へ
引き渡す、単一 host 向けの issue-to-PR runtime である。

本書は system-wide なアクター、要求、構成、不変条件を所有する。厳密な field や遷移を本書へ
複製せず、次の正本へ委譲する。

- protocol field と canonical identity: [contracts/](../../contracts/)
- workflow の順序と state transition: [End-to-end workflow](../../workflow.md)
- component の責務と権限: [Architecture](../../architecture.md)
- deployment / operations contract: [Runtime platform](../../runtime-platform.md)
- GitHub 上の candidate と status: [GitHub routing policy](../../github-routing.md)
- 実装状況と delivery order: [Implementation plan](../../implementation-plan.md)

## 1. プロジェクト概要

人間が完成した Task Issue に assignee `mrbaron3` と `ai-ready` label を付けると、Kudo は live
GitHub state を検証して Run を開始する。test を先に作り、その test の妥当性を独立 reviewer が
承認した後にだけ実装へ進む。最終実装も別の read-only review を通し、同じ head に対する証跡を
Pull Request へまとめて人間へ返す。

Kudo は webhook 欠落、重複 event、process restart、provider failure、複数 Issue の同時実行を
通常の運用条件として扱う。process-local memory や会話履歴を workflow の正本にしない。

## 2. アクター

| アクター | 責任 |
| --- | --- |
| Human Author | Issue Contract、authority、受け入れ条件を確定し、`ai-ready`で実行を依頼する |
| Human Reviewer | 完成した Pull Request を最終 review し、merge / release を判断する |
| Controller | candidate reconciliation、state transition、Operation dispatch、retry / escalation、status projection を行う |
| Issue Worker | claim、test、implementation、worktree、branch、Pull Request mutation を所有する唯一の writer |
| Review Worker | immutable input と独立 checkout から test validity / final implementation verdict を返す |
| GitHub | live Issue、repository、branch、Pull Request の source of truth |
| PostgreSQL | Run、Operation、lease、inbox / outbox、review binding の source of truth |

## 3. システム構成

Kudo は一つの Go module と一つの deployable binary を持つ modular monolith とする。同じ binary を
起動 mode により Controller、Issue Worker、Review Worker、migration job として実行し、Docker
Compose 上では権限の異なる container に分離する。

```mermaid
flowchart LR
    H[Human] -->|Issue / PR review| GH[GitHub]
    GH -->|webhook| C[Controller]
    C -->|fallback polling| GH
    C <--> DB[(PostgreSQL)]
    IW[Issue Worker] <--> DB
    RW[Review Worker] <--> DB
    C -->|label / comment projection| GH
    IW -->|branch / PR write| GH
    RW -->|Issue / PR / source read| GH
    IW -->|fresh session| IP[Codex or Claude]
    RW -->|fresh read-only session| RP[Codex or Claude]
    IW <--> AS[(Immutable artifacts)]
    RW <--> AS
    IW --> WS[(Run workspace)]
```

### 3.1. 採用構成

| レイヤー | 採用 | 役割 |
| --- | --- | --- |
| 言語 / binary | Go、単一 module / binary | domain、application、adapter を compile-time boundary で分ける |
| 実行基盤 | Docker Compose | 同一 binary を role 別 container として分離する正式 runtime |
| Workflow store / queue | PostgreSQL | durable state、queue、lease、inbox / outbox の正本 |
| Artifact Store | content-addressed named volume | digest 付きの write-once evidence を共有する |
| Workspace | Issue Worker 専用 named volume | Run ごとの clone / worktree / branch を保持する |
| Source / collaboration | GitHub / GitHub App | Issue、repository、Pull Request と role-scoped access |
| Model provider | Codex または Claude の child process | Operation ごとに fresh session で test、実装、review を行う |
| Telemetry | structured log、metric、trace | 診断と改善に使うが workflow state の正本にはしない |

採用理由と container / volume / secret の厳密な境界は
[ADR-0001](../../decisions/0001-compose-runtime.md) と [Runtime platform](../../runtime-platform.md) を正とする。

### 3.2. 権限境界

- Controller は model session と implementation worktree を持たない。
- Issue Worker だけが implementation worktree、branch、commit、Pull Request を変更できる。
- Review Worker は implementation workspace を mount せず、GitHub write credential を持たない。
- Controller と Review Worker へ Docker socket を mount しない。
- Worker 間では mutable state や conversation を共有せず、versioned message と immutable artifact を渡す。

## 4. 機能要件

### F-01. Issue の検出と reconciliation

- GitHub webhook を低遅延の通知経路として受け付ける。
- startup 時と既定 60 秒ごとの polling で webhook の欠落を回復する。
- webhook と polling を同じ冪等な `ReconcileIssue` operation へ集約する。
- open、non-PR、assignee `mrbaron3`、label `ai-ready`を満たす Task Issue だけを候補にする。

### F-02. Contract 検証と claim

- live GitHub API から Issue、relationship、dependency、authority、base commit を取得する。
- Issue Contract を strict parse し、Issue Observation、canonical Task Context、Context Manifest、
  Execution Policy を固定する。
- required input の欠落、曖昧さ、authority conflict を推測で補わない。
- dependency が未完了の Issue は readiness gate で待機させる。

### F-03. Test authoring と RED

- fresh provider session で Acceptance Criteria に対応する test plan と test code を作る。
- test-only head で対象機能の未実装に起因する RED を確認する。
- test plan、patch、command、exit status、出力、environment identity を immutable evidence にする。
- RED 固定後、test-only head を draft Pull Request へ publish する。

### F-04. Test validity review

- publish 済み draft Pull Request の exact head / base / open state を live に照合する。
- Review Worker が別 checkout と fresh read-only session で test の妥当性を評価する。
- `approve`まで production implementation を開始しない。
- `request_changes`は finding を immutable result として新しい `revise_tests` session へ渡す。
- `needs_human`または round 上限到達時は自動 loop を停止する。

### F-05. GREEN、refactor、検証

- test validity で承認された test を入力として fresh implementation session を開始する。
- 承認済み test を変更せずに production code を実装し、GREEN を固定する。
- behavior を保って refactor し、Issue Verification と repository required checks を再実行する。
- test 変更が必要なら test gate へ戻し、implementation 中に承認対象を差し替えない。

### F-06. Final implementation review

- final head を同じ draft Pull Request へ publish してから review を開始する。
- Review Worker が correctness、regression、scope、test quality、code quality、security、evidence を
  versioned policy に基づいて評価する。
- `request_changes`は fresh `repair_implementation` session へ渡し、変更後の head を再 review する。
- head、artifact、policy reference など semantic input が変われば、以前の approval を stale にする。

### F-07. Pull Request の確定と handoff

- final approval、required checks、live Pull Request head が同じ commit に bind された場合だけ draft を解除する。
- Pull Request body に Task Issue、Acceptance Criteria、RED / GREEN / checks、二つの Review Result、
  residual risk、Run / base / head identity を含める。
- ready 化を durable に記録した後、Issue を `ai-review-waiting`へ投影する。
- merge、Issue close、release は実行しない。

### F-08. Recovery と escalation

- transport / provider の一時障害は error class に応じて bounded retry する。
- process crash や lease expiry 後は、確定済み input から fresh attempt を開始する。
- human decision、authority conflict、外部干渉、review round 上限到達では `needs_human`へ遷移する。
- 停止理由、evidence、必要な対応を日本語の status comment と durable record に残す。
- 人間による `ai-ready`再付与後にだけ、安全な resume または supersede を行う。

### F-09. Dependency と並行実行

- `dependsOn`だけを readiness edge として扱い、parent や Issue 番号から暗黙の順序を作らない。
- dependency のない ready Issue は、独立した Run と workspace で同時実行できる。
- 同じ Issue に writer-capable な Run を二つ作らない。
- capacity 待ちを dependency blocked として扱わない。

## 5. 標準 workflow

```text
Issue ready
  -> claim
  -> author tests
  -> RED evidence
  -> draft PR publish
  -> test validity review
  -> implement + GREEN + refactor
  -> final head publish
  -> final implementation review
  -> PR ready
  -> human review waiting
```

review の `request_changes`は該当 gate 内で fresh correction session と再 review を繰り返す。
retry、stale、transport failure は review verdict と別に扱う。規範的な分岐と durable state は
[End-to-end workflow](../../workflow.md) を正とする。

## 6. 主要な情報と証跡

| 情報 | 役割 |
| --- | --- |
| Issue Observation | GitHub から取得した exact Issue body と取得時点の監査 lineage |
| Task Context | strict parse 済み Issue Contract の canonical representation |
| Context Manifest | Task Context、base、dependency completion、authority content の解決結果 |
| Execution Policy | provider、model、adapter version、tool / timeout policy の Run snapshot |
| Escalation Policy | gate ごとの review round 上限を固定する Controller policy |
| Artifact Manifest | test、patch、source snapshot、command evidence などの immutable reference |
| Review Request / Result | review input identity と versioned verdict / finding の binding |
| Pull Request Observation | live PR の head、base、open / draft state の監査 lineage |

schema と staleness rule は [contracts/](../../contracts/) を正とする。

## 7. 非機能要件

### 7.1. Recoverability

workflow の継続に必要な state を process-local memory に置かない。Controller、Worker、PostgreSQL の
restart 後に、Run、Operation、lease、commit、artifact から retry または再開できなければならない。

### 7.2. Idempotency

duplicate、遅延、順不同 event、ambiguous external response が、二重 Run、二重 branch、二重 Pull
Request、二重 comment を作らない。state transition と external projection intent を transaction で結ぶ。

### 7.3. Security と isolation

role ごとに filesystem と credential を最小化する。secret、非公開 Issue body、provider transcript、
source bytes を既定で telemetry へ送らない。Review Worker が implementation mutation を実行できない
ことを deployment boundary でも保証する。

### 7.4. Deterministic validation

GitHub、process、clock、filesystem、provider、telemetry の境界は fake で決定論的に test できるようにする。
live integration は opt-in とし、core correctness の唯一の証拠にしない。

### 7.5. Concurrency

repository 全体を global lock で直列化しない。Issue / Run scoped claim と lease により、独立 Issue の
同時実行と、同一 Issue / worktree の writer 排他を両立する。

### 7.6. Operability

Run ID、Operation ID、attempt、IssueRef、transition、duration、provider、artifact digest、verdict、
review round、escalation reason を構造化して観測できるようにする。ただし telemetry backend は
workflow correctness と復旧の正本にしない。

### 7.7. Deployability

Docker Compose を正式 runtime とし、同一 Go binary を role 別 container として実行する。
PostgreSQL migration、health / readiness、backup / restore、graceful shutdown を運用 contract に含める。

## 8. アーキテクチャ上の不変条件

- Task Issue だけを execution-context root とする。
- test validity approval より前に production implementation を開始しない。
- implementation role は自身の test または実装を approve できない。
- Issue Worker 以外は implementation worktree、branch、Pull Request を変更しない。
- model-bearing Operation ごとに fresh provider session を開始する。
- session 間では会話履歴ではなく immutable artifact と versioned result を渡す。
- transport failure と review verdict を同じ結果として扱わない。
- semantic input が変わった review approval を再利用しない。
- GitHub label / comment と telemetry を authoritative workflow state にしない。
- dependency のない Issue を repository global lock で直列化しない。

## 9. 対象外と実装状況

製品境界は [01. プロダクト設計](../01_product-design/) §5 を参照する。本書は完成形の仕様であり、
現在の repository status を表さない。実装済みの milestone と未実装 component は
[Implementation plan — Current status](../../implementation-plan.md) を正とする。
