# Runtime platform

## Decision summary

Kudo の deployment unit は単一の`kudo` process を動かす単一の OCI container とする。Docker Compose
は volume、secret、restart policy の宣言に使う packaging であり、service は一つである。macOS host 上へ
Kudo daemon を直接常駐させない。

Kudo は自前の database も artifact volume も持たない（[ADR-0001](../../adr/0001-github-ssot-stateless-reconciler.md)）。
workflow 状態は GitHub の観測から導出するため、platform が守るべき永続資産は GitHub credential と
設定だけである。Controller、Issue Worker、Review Worker は同一 process 内の role であり、権限境界は
container ではなく role-scoped installation token と operation ごとの fresh provider child process で作る。

## Technology stack

| Concern | Standard |
| --- | --- |
| Application | Go 1.26.5、single module、single `kudo` binary、single process |
| Image | OCI image、`linux/arm64`と`linux/amd64` |
| Orchestration | Compose Specification、`docker compose`（単一 service） |
| macOS reference host | Docker Desktop 上の Compose |
| Linux reference host | Docker Engine と Compose plugin |
| Durable state | GitHub（branch、PR、check run、comment、label）。local には持たない |
| Source control | Git と GitHub App |
| Model providers | Codex CLI または Claude Code CLI の headless child process |
| Logs | JSON structured logs to stdout/stderr |
| Metrics/traces | OpenTelemetry-compatible export。任意であり workflow state ではない |
| CI / image distribution | GitHub Actions と GHCR を標準候補とする |

Kudo image と provider CLI version は immutable version で固定し、更新手順を持つ。

## Delivery order

Compose は完成後に付加する packaging ではなく、最初の implementation milestone から開発・test の
実行境界として使う。最初の foundation は次に限定する。

- 現在の Go binary と test を build・実行する non-root application/test image
- container 内の`mise run check`入口
- 後続で共有する configuration、workspace volume、network naming

workflow の実装が進んでも service は増えない。end-to-end workflow 完成後、provider CLI を含む
image、resource/security policy、GHCR release を production hardening として完了する。

## Images and toolchains

image は Kudo binary、Git、選択した provider CLI、managed repository の required checks に必要な
toolchain を含む。対象 repository ごとに toolchain が異なる場合は、共通 image を無制限に肥大化させず、
Kudo base image を継承した deployment-specific image を build し、一つの deployment に互換 toolchain の
repository だけを割り当てる。

runtime に Docker socket を mount して sibling build container を作る設計にはしない。
Docker-in-Docker も標準にしない。container build 自体が Task の必須要件になる repository は、
限定された remote builder または専用 runner boundary を別途設計する。

## Filesystem

| Filesystem | Rule |
| --- | --- |
| issue workspaces | Run scoped clone/worktree。disposable であり、失われたら base と published head から再構築する |
| review checkout | head SHA から毎回再構築し、Operation 終了時に破棄する ephemeral checkout |
| operation temp | provider session/state、command temp。handoff に使用しない |

workspace volume は再起動をまたぐ cache として named volume に置いてよいが、checkpoint にはしない。
重要な段階は commit（compare-and-push）と record surface（check run / comment）へ固定する。backup は
不要である。GitHub 側に正本があるため、disaster recovery は「新しい host で起動して再観測する」である。

## Network

- host へ publish する port は webhook/health endpoint だけ。
- provider session endpoint を公開しない。
- process は GitHub、provider API、language package registry など必要な outbound access を持つ。

Kudo は既定で`:8080`を listen し、次の endpoint を持つ。

- `POST /webhooks/github`: GitHub signature 検証後に reconcile を trigger する。application work の
  完了を待たずに応答する。webhook は配送保証がないため、fallback polling が取りこぼしを回収する。
- `GET /healthz`: process liveness。
- `GET /readyz`: required configuration と GitHub App 認証を確認する readiness。

public deployment では TLS termination と request size/rate limit を reverse proxy または managed
ingress に置く。Webhook secret の検証を TLS termination の代わりにしない。

## Secrets and credentials

secret は image、Git repository、plain environment file に埋め込まない。Compose secrets または
権限を制限した read-only file として mount し、`*_FILE` configuration から読む。

最低限、次を分離する。

- GitHub App ID、private key、webhook secret（actor ごとの App それぞれ。private key は分離して保管する）
- Codex provider credential
- Claude provider credential

GitHub は PAT ではなく GitHub App を標準とする。App を採用する理由は identity の調達である: actor
ごとの複数 identity を ToS 制約・seat 課金なしに持て、installation token は短命（自動失効）で、複数
repository への一括 install ができる。App 専用機能である check run（App 所有で改竄不能）はこの選択の
帰結として利用する。

actor ごとに別の GitHub App を登録する。最低限 Implementer と Reviewer の分離は必須である
（[architecture.md](01_architecture.md) Actor model）。installation token は短命にし、actor ごとに
必要な permission subset だけを要求して operation 単位で発行する。

| Actor | GitHub authority |
| --- | --- |
| Controller（Coordinator） | metadata read、issues read/write、pull requests read、checks read。label / status comment の記録と gate の外形条件評価用。contents と checks への write は持たない |
| Issue Worker（Implementer） | metadata read、issues write、contents write、pull requests write、checks write（自分の evidence check run）。merge intent comment、PR の merge、head branch 削除を含む |
| Review Worker（Reviewer） | metadata/issues/contents/pull requests read、checks write（自分の verdict check run）、PR comment write（finding） |

開発の立ち上がり（実装計画の S1〜S2）は owner PAT による単一 identity で開始してよい。PAT は
dev / test 専用の TokenSource 実装としてだけ持ち、production への経路にしない。verdict の記録が
始まる時点（S3）までに Reviewer App の分離を導入する。

最低限 Implementer / Reviewer 間では credential と provider config directory を共有しない。
Coordinator も別 App にする場合は同じ分離境界を適用する。同一 process 内でも、各 operation へ渡す
token はその actor の subset に限定し、provider child process の環境には該当 actor の credential だけを
注入する。installation token は operation の Task repository 1件だけを`repositories`で指定して発行し、
response の`repositories[].full_name`がその1件と一致することを確認してから利用する。これにより、同じ
installation が許可された別 repository や、別 owner の同名 repository へ provider child がアクセス
できないようにする。log と record surface に token、private key、credential path を含めない。

## Configuration contract

configuration は command flag より environment / mounted config を基本とし、起動時に strict
validation する。少なくとも次を持つ。

| Key | Meaning |
| --- | --- |
| `KUDO_REPOSITORIES` | 許可された`owner/repository`一覧 |
| `KUDO_TARGET_ASSIGNEE` | 既定`mrbaron3` |
| `KUDO_READY_LABEL` | 既定`ai-ready` |
| `KUDO_POLL_INTERVAL` | 既定`15m`。正数かつ最低値を検証する |
| `KUDO_WORKSPACE_ROOT` | 既定`/var/lib/kudo/workspaces` |
| `KUDO_PROVIDER_ALLOWLIST` | `codex`、`claude`の許可集合 |
| `KUDO_GITHUB_<ACTOR>_APP_ID_FILE` | actor ごとの GitHub App ID を読む file。`<ACTOR>`は`COORDINATOR`、`IMPLEMENTER`、`REVIEWER` |
| `KUDO_GITHUB_<ACTOR>_PRIVATE_KEY_FILE` | actor ごとの GitHub App private key PEM を読む file |
| `KUDO_GITHUB_<ACTOR>_INSTALLATION_ID_FILE` | actor ごとの GitHub App installation ID を読む file |
| `KUDO_ISSUE_PROVIDER` | Issue Worker Operation に使う required provider。`codex`または`claude` |
| `KUDO_REVIEW_PROVIDER` | Review Request に使う required provider。`codex`または`claude` |
| `KUDO_MAX_CONCURRENCY` | 同時 Operation 上限（in-process semaphore） |
| `KUDO_OPERATION_TIMEOUT` | Operation kind ごとの deadline policy 参照 |
| `KUDO_ATTEMPT_RETRIES` | 一つの logical Operation で初回後に許す追加 attempt 数。既定`3`、許容範囲`1`〜`10` |
| `KUDO_REVIEW_ROUNDS_TEST_VALIDITY` | `test_validity` gate の review round 上限。既定`3`、許容範囲`1`〜`10` |
| `KUDO_REVIEW_ROUNDS_FINAL_IMPLEMENTATION` | `final_implementation` gate の review round 上限。既定`3`、許容範囲`1`〜`10` |

Issue と Review に同じ provider を指定してもよいが、session、credential、filesystem、context は
role ごとに分離する。選択した provider/model/adapter version/tool/timeout policy は Run 開始時に
Execution Policy として固定し、その digest を claim checkpoint へ記録する。attempt retry 上限と
review round 上限は Escalation Policy として同時に pin するが、semantic input ではないため値の変更は
進行中 Run を supersede せず、次の claim から有効になる。

タスク種別ごとの model 割り当てなど、設定が構造化されて environment 変数で表現しきれなくなった
場合は、versioned configuration file または設定用 database の導入を検討する。設定は正本性を持つ
workflow state ではないため、database の利用は [ADR-0001](../../adr/0001-github-ssot-stateless-reconciler.md)
と矛盾しない。

unknown key、欠落した required key、不正 duration を warning だけで継続しない。

GitHub App の production 設定は3 actor 分をまとめて検証する。Implementer と Reviewer は App ID、
private key、installation ID のいずれも共有してはならない。Coordinator の App 分離は推奨であり、
共有する場合も発行 token の permission subset は Coordinator 用へ downscope する。Reviewer の finding は
PR conversation comment endpoint を使うため、GitHub REST permission では`issues:write`へ写像するが、
`contents`と`pull_requests`は`read`のままにする。Implementer の merge intent comment も同じ endpoint を
使うため`issues:write`へ写像する。provider child process に渡してよい GitHub credential は
operation 用の短命 token（`GH_TOKEN`）だけで、App private key と`*_FILE` path は渡さない。

## Process supervision

Worker role は provider CLI と test command を direct child process として起動し、次を保証する。

- Operation ごとに fresh process、working directory、temporary state を作る。
- stdout/stderr を bounded streaming capture し、evidence には secret redaction 後の抜粋を記録する。
- deadline 超過または shutdown 時は process group へ graceful signal を送り、猶予後に終了させる。
- child exit と record surface への書き込みが完了するまで Operation を succeeded にしない。
- invalid structured output と non-zero exit を分類し、review verdict に偽装しない。

Kudo service は`restart: unless-stopped`相当の lifecycle policy を持つ。SIGTERM を受けると新規
Operation の開始を停止し、実行中 Operation を設定済み grace period 内で完了または中断してから
終了する。中断された Operation は再起動後の再観測で新しい attempt として再実行される。

## Observability and operations

すべての log に Run（PR 番号）、Operation、attempt、IssueRef を可能な範囲で含める。health は
process、readiness は設定と認証、workflow alert は導出 phase の滞留から判定する。

最低限、次を監視対象とする。

- reconcile の実行時刻、対象件数、失敗
- webhook signature failure
- GitHub API の rate-limit remaining と conditional request の hit 率
- phase 滞留時間と`needs_human`件数
- provider duration、exit class、rate limit。品質 score とは分離する
- workspace filesystem capacity

## Scaling boundary

同一 repository への Kudo instance は一つとする。並行度は`KUDO_MAX_CONCURRENCY`と host resource の
増強で上げる。複数 instance、複数 host、外部 queue が必要になった場合は
[ADR-0001](../../adr/0001-github-ssot-stateless-reconciler.md) の revisit conditions に該当するため、
新しい ADR で設計する。Kubernetes や external broker を先回りして導入しない。

## Apple Container compatibility

Apple Container は正式 orchestrator には採用しない。ただし Kudo image は OCI-compliant な
`linux/arm64` image として build し、基本 command の compatibility test 対象にできる。将来、macOS
固有の per-Operation sandbox として使う場合も、workflow へ runtime 固有 command を埋め込まず、
Worker 内の Sandbox Runner adapter として追加する。
