# Runtime platform

## Decision summary

Kudo の正式な deployment unit は Docker Compose application とする。すべての Kudo application process と PostgreSQL を OCI container で動かし、macOS host 上へ Kudo daemon を直接常駐させない。

同じ Go binary を mode 別に起動する。role ごとに container、filesystem mount、credential、resource limit を分けるが、別 repository や別 Go module には分割しない。

## Technology stack

| Concern | Standard |
| --- | --- |
| Application | Go 1.26.5、single module、single `kudo` binary |
| Image | OCI image、`linux/arm64`と`linux/amd64` |
| Orchestration | Compose Specification、`docker compose` |
| macOS reference host | Docker Desktop 上の Compose |
| Linux reference host | Docker Engine と Compose plugin |
| Durable state / queue | PostgreSQL 18.4。18系minorを計画的に更新 |
| Artifact bytes | local content-addressed filesystem on a named volume |
| Source control | Git と GitHub App |
| Model providers | Codex CLI または Claude Code CLI の headless child process |
| Logs | JSON structured logs to stdout/stderr |
| Metrics/traces | OpenTelemetry-compatible export。任意であり workflow state ではない |
| CI / image distribution | GitHub Actions と GHCR を標準候補とする |

PostgreSQL の image は`18.4`を起点とし、release tag だけで浮動させず実際のCompose fileではmanifest digestもpinする。Kudo imageとprovider CLI versionもimmutable versionで固定し、更新手順を持つ。

## Delivery order

Composeは完成後に付加するpackagingではなく、最初のimplementation milestoneから開発・testの実行境界として使う。ただし、まだ存在しないControllerやWorkerをdummy processとして先に実装しない。

最初のCompose foundationは次に限定する。

- 現在のGo binaryとtestをbuild・実行するnon-root application/test image
- PostgreSQL 18.4、healthcheck、named volume、internal network
- container内の`mise run check`とPostgreSQL integration test入口
- 後続serviceが共有するconfiguration、volume、network naming

Controller、Issue Worker、Review Worker、migration commandが実装されるたびに同じCompose applicationへserviceを追加する。end-to-end workflow完成後、role別credential、provider CLIを含むworker image、resource/security policy、backup/restore、GHCR releaseをproduction hardeningとして完了する。

この二段階は別runtimeを作ることを意味しない。development foundationとproduction profileは同じDockerfile、Compose project、PostgreSQL/artifact contractを継続利用し、host native daemonへのfallbackを作らない。

## Compose services

正式な Compose model は次の service からなる。

| Service | Command | Replicas | Responsibility |
| --- | --- | --- | --- |
| `controller` | `kudo controller` | 通常1、設計上は複数可 | HTTP ingress、polling、reconcile、state transition、dispatch、outbox projection |
| `issue-worker` | `kudo worker issue` | 1以上 | claim、test、implementation、command、worktree、branch、PR mutation |
| `review-worker` | `kudo worker review` | 1以上 | test validity / final implementation review |
| `postgres` | official PostgreSQL 18.4 entrypoint | 1 | workflow state、queue、lease、inbox/outbox、artifact metadata |
| `migrate` | `kudo migrate up` | one-shot | backward-compatible database migration |

Controller と Worker 間に private HTTP API や message broker を追加しない。PostgreSQL queue と versioned payload が通信境界である。`migrate`の成功と PostgreSQL health を、application service の readiness 条件にする。

概念上の Compose topology は次のとおりである。実際の`compose.yaml`は implementation milestone でこの contract に従って追加する。

```yaml
services:
  migrate:
    image: ghcr.io/mrbaron3/kudo:<immutable-version>
    command: ["kudo", "migrate", "up"]

  controller:
    image: ghcr.io/mrbaron3/kudo:<immutable-version>
    command: ["kudo", "controller"]
    ports:
      - "8080:8080"

  issue-worker:
    image: ghcr.io/mrbaron3/kudo-worker:<immutable-version>
    command: ["kudo", "worker", "issue"]
    volumes:
      - issue-workspaces:/var/lib/kudo/workspaces
      - artifacts:/var/lib/kudo/artifacts

  review-worker:
    image: ghcr.io/mrbaron3/kudo-worker:<immutable-version>
    command: ["kudo", "worker", "review"]
    volumes:
      - artifacts:/var/lib/kudo/artifacts

  postgres:
    image: postgres:18.4
    volumes:
      - postgres-data:/var/lib/postgresql
```

この snippet は topology を示す仕様例であり、そのまま実行可能であることを主張しない。実際のimage referenceにはdigestを追加する。healthcheck、secret、network、read-only root filesystem、resource limit、backupは実装時のcompose fileに必須である。PostgreSQL 18 の official image は PGDATA を`/var/lib/postgresql/18/<dir>`に置き`VOLUME /var/lib/postgresql`を宣言するため、named volume は`/var/lib/postgresql`全体へ mount する。`/var/lib/postgresql/data`を mount すると実データが anonymous volume へ落ちる。

## Images and toolchains

Controller image は Kudo binary と CA certificate だけを中心にした小さい image とする。Worker image は、Kudo binary、Git、選択した provider CLI、managed repository の required checks に必要な toolchain を含む。

対象 repository ごとに toolchain が異なる場合は、共通 worker image を無制限に肥大化させない。Kudo worker base image を継承した deployment-specific image を build し、一つの Compose project に互換 toolchain の repository だけを割り当てる。

runtime に Docker socket を mount して、Issue Worker が sibling build container を自由に作る設計にはしない。Docker-in-Docker も標準にしない。container build 自体が Task の必須要件になる repository は、限定された remote builder または専用 runner boundary を別途設計する。

## Volumes and filesystem authority

| Volume / filesystem | Mount | Rule |
| --- | --- | --- |
| `postgres-data` | PostgreSQLのみ read/write | durable workflow state。定期 backup の対象 |
| `artifacts` | Issue/Review Workerがappend/read。ControllerはmountせずDB metadataだけ参照 | digest key の write-once bytes。既存内容の変更禁止 |
| `issue-workspaces` | Issue Workerのみ read/write | Run scoped clone/worktree。Controller/Review Workerはmount禁止 |
| Review checkout | Review Worker containerのephemeral filesystem | head SHAから毎回再構築し、Operation終了時に破棄 |
| operation temp | 各Workerのephemeral filesystem | provider session/state、command temp。handoffに使用しない |

workspace は crash recovery のため named volume に置くが、唯一の checkpoint にはしない。重要な段階は commit、source bundle/snapshot artifact、Operation Result として固定する。workspace を失った場合に、immutable checkpoint artifactと、存在する場合はremote branchから安全に再構築できる状態を目指す。

Artifact Store は複数 Worker から append されるため、temporary file への書き込み、fsync、digest/length 検証、同一 filesystem 内の atomic rename を使う。metadata を PostgreSQL に登録する前に bytes の durability を確認し、orphan cleanup は digest reference を検証してから行う。

## Network

Compose では external ingress と internal service network を分ける。

- host へ publish する application port は Controller の webhook/health endpoint だけ。
- PostgreSQL port は host へ公開しない。
- Worker は GitHub、provider API、language package registry など必要な outbound access を持つ。
- Review Worker は GitHub read-only token を使い、Issue Worker の write token を共有しない。
- service 間で provider session endpoint を公開しない。

Controller は既定で`:8080`を listen し、次の endpoint を持つ。

- `POST /webhooks/github`: GitHub signature 検証後に inbox へ記録する。application work の完了を待たず、durable acceptance 後に応答する。
- `GET /healthz`: process liveness。
- `GET /readyz`: migration version、PostgreSQL、required configuration を確認する readiness。

public deployment では TLS termination と request size/rate limit を reverse proxy または managed ingress に置く。Webhook secret の検証を TLS termination の代わりにしない。

## Secrets and credentials

secret は image、Git repository、plain environment file に埋め込まない。Compose secrets または権限を制限した read-only file として mount し、`*_FILE` configuration から読む。

最低限、次を分離する。

- GitHub App ID、private key、webhook secret
- PostgreSQL application/migration credential
- Codex provider credential
- Claude provider credential

GitHub は PAT ではなく GitHub App を標準とする。installation token は短命にし、role ごとに必要な permission subset を要求する。

| Role | GitHub authority |
| --- | --- |
| Controller | metadata read、Issues read/write。label/comment projection用 |
| Issue Worker | metadata/issues read、contents write、pull requests write、required check read |
| Review Worker | metadata/issues/contents/pull requests read-only |

credential と provider config directory を Worker 間で共有しない。log、artifact、Review Result に token、private key、credential path を含めない。

## Configuration contract

configuration は command flag より environment / mounted config を基本とし、起動時に strict validation する。少なくとも次を持つ。

| Key | Meaning |
| --- | --- |
| `KUDO_DATABASE_URL_FILE` | role別 PostgreSQL connection string を読む file |
| `KUDO_REPOSITORIES` | 許可された`owner/repository`一覧 |
| `KUDO_TARGET_ASSIGNEE` | 既定`mrbaron3` |
| `KUDO_READY_LABEL` | 既定`ai-ready` |
| `KUDO_POLL_INTERVAL` | 既定`60s`。正数かつ最低値を検証する |
| `KUDO_ARTIFACT_ROOT` | 既定`/var/lib/kudo/artifacts` |
| `KUDO_WORKSPACE_ROOT` | Issue Worker専用。既定`/var/lib/kudo/workspaces` |
| `KUDO_PROVIDER_ALLOWLIST` | `codex`、`claude`の許可集合 |
| `KUDO_ISSUE_PROVIDER` | Issue Worker Operationに使うrequired provider。`codex`または`claude` |
| `KUDO_REVIEW_PROVIDER` | Review Requestに使うrequired provider。`codex`または`claude` |
| `KUDO_MAX_CONCURRENCY` | role containerごとの同時Operation上限 |
| `KUDO_OPERATION_TIMEOUT` | Operation kindごとのdeadline policy参照 |
| `KUDO_ATTEMPT_RETRIES` | Controller専用。一つのlogical Operationで初回後に許す追加Attempt数。既定`3`、許容範囲`1`〜`10` |
| `KUDO_REVIEW_ROUNDS_TEST_VALIDITY` | Controller専用。`test_validity` gateのreview round上限。既定`3`、許容範囲`1`〜`10` |
| `KUDO_REVIEW_ROUNDS_FINAL_IMPLEMENTATION` | Controller専用。`final_implementation` gateのreview round上限。既定`3`、許容範囲`1`〜`10` |

IssueとReviewに同じproviderを指定してもよいが、session、credential、filesystem、contextはroleごとに分離する。選択したprovider/model/adapter version/tool/timeout policyはRun開始時にExecution Policy artifactとして固定する。attempt retry上限とreview round上限は同じくRun開始時にEscalation Policy artifactとして固定するが、semantic inputではないため値の変更は進行中Runをsupersedeせず、次のclaimから有効になる。

secret-specific key と provider-specific setting は adapter 実装時に versioned configuration reference を追加する。unknown key、欠落した required key、不正 duration を warning だけで継続しない。

## Process supervision

Worker は provider CLI と test command を direct child process として起動し、次を保証する。

- Operation ごとに fresh process、working directory、temporary state を作る。
- stdout/stderr を bounded streaming capture し、完全版は secret redaction 後に artifact 化する。
- deadline 超過または shutdown 時は process group へ graceful signal を送り、猶予後に終了させる。
- child exit と artifact flush が完了するまで Operation を succeeded にしない。
- invalid structured output と non-zero exit を分類し、review verdict に偽装しない。
- heartbeat が止まった attempt は lease expiry 後に recovery 対象にする。

長時間動くCompose serviceは`restart: unless-stopped`相当のlifecycle policyを持つ。one-shotの`migrate`にはrestart policyを適用しない。applicationはSIGTERMを受けると新規leaseを停止し、実行中Operationを設定済みgrace period内でcheckpointまたは中断し、leaseを安全に解放する。

## Database migration and compatibility

schema migration は`migrate` service が application 起動前に行う。migration は少なくとも直前の released version から forward upgrade でき、rolling しない単一 host deployment でも backup、migration、application start の順序を固定する。

- destructive migration を application startup に暗黙実行しない。
- migration 実行前に PostgreSQL backup を取得できる手順を持つ。
- binary は対応 schema version 範囲を readiness で検証する。
- queue payload、artifact manifest、Review protocol は schema version を持ち、DB migration だけで無断変換しない。

## Backup and recovery

authoritative backup set は PostgreSQL と artifact volume である。issue workspace volume も短期 recovery を速めるため backup 対象にできるが、長期的な正本にはしない。

運用手順は次を検証する。

1. new Operation lease を停止する。
2. PostgreSQL の整合した backup と artifact snapshot を取得する。
3. 別の disposable Compose project へ restore する。
4. artifact digest と metadata reference を検証する。
5. expired lease を recovery し、二重 PR/projection が発生しないことを確認する。

backup が存在するだけで完了とせず、定期 restore test を行う。

## Observability and operations

すべての log に service role、instance、Run ID、Operation ID、attempt、IssueRef を可能な範囲で含める。health は process、readiness は依存関係、workflow alert は durable queue/state から判定する。

最低限、次を監視対象とする。

- queued Operation の age と件数
- lease expiry、retry、terminal execution failure
- webhook signature failure と inbox lag
- polling success time、GitHub rate-limit remaining
- outbox backlog と label/PR projection failure
- Run phase duration と`needs_human`件数
- PostgreSQL/volume capacity
- provider duration、exit class、rate limit。品質 score とは分離する

## Scaling boundary

Compose の同一 host 上で Issue Worker と Review Worker を複数 replica にできる。PostgreSQL queue、scoped lease、workspace ownership が replica 数に依存しない設計にする。

複数 host へ広げる場合は、local named volume と Run workspace ownership が境界になる。その時点で object storage、remote workspace、scheduler を別 ADR で設計する。Kubernetes や external broker を先回りして導入しない。

## Apple Container compatibility

Apple Container は正式 orchestrator には採用しない。ただし Kudo image は OCI-compliant な`linux/arm64` image として build し、基本 command の compatibility test 対象にできる。将来、macOS 固有の per-Operation sandbox として使う場合も、Controller workflow へ runtime 固有 command を埋め込まず、Worker 内の Sandbox Runner adapter として追加する。

判断の根拠と再検討条件は [ADR-0001](decisions/0001-compose-runtime.md) を参照する。
