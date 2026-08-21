# ADR-0007: 縦の貫通sliceをdelivery orderの単位とする

- Status: accepted（2026-08-19）
- 関連Issue: [#13](https://github.com/mrbaron3/kudo/issues/13)、[#14](https://github.com/mrbaron3/kudo/issues/14)、[#15](https://github.com/mrbaron3/kudo/issues/15)、[#16](https://github.com/mrbaron3/kudo/issues/16)、[#17](https://github.com/mrbaron3/kudo/issues/17)、[#20](https://github.com/mrbaron3/kudo/issues/20)、[#21](https://github.com/mrbaron3/kudo/issues/21)、[#22](https://github.com/mrbaron3/kudo/issues/22)、[#24](https://github.com/mrbaron3/kudo/issues/24)、[#29](https://github.com/mrbaron3/kudo/issues/29)
- Supersede対象: なし。[implementation-plan.md](../spec/06_project/01_implementation-plan.md)のMilestone 3〜6を**完成の定義としては維持し、実行順序の単位としては置き換える**（「Milestone計画との関係」節）。
- 前提となるADR: [ADR-0001](0001-compose-runtime.md)（Compose runtime）、[ADR-0002](0002-pr-anchored-review.md)（PR-anchored review）

## Context

### 実測した現状

2026-08-19時点（`5815cdf`）の非テストGo行数は次のとおりである。

| package | 非テスト行数 | 割合 | テスト行数 |
| --- | ---: | ---: | ---: |
| `internal/contract` | 4,579 | 73% | 4,857 |
| `internal/workflow` | 768 | 12% | 584 |
| `internal/adapter/postgres` | 920 | 15% | 84 |
| `cmd/kudo` | 36 | 1% | 60 |

コードの73%が「canonical encodingとdigestとvalidation」に費やされている。一方でGitHub adapter、provider adapter、Artifact Store、Run workspace、Issue Worker、Review Worker、Controllerはいずれも未実装であり、**Kudoは自身のIssueを1件もPull Requestにしたことがない**。

11個のversioned protocol（Issue Contract、Task Context、Operation Protocol、Review Protocol、2つのreview policy、およびそれらが内包するartifact schema群）はv1alpha1として完成度高く作り込まれている。しかし製品境界そのものはまだ動く可能性がある——「execution コンテキストだけを切り出したものは望むものではない」という指摘が未決着である。動きうる境界に対して、外部consumerを持たないalpha protocolを先に完成させた状態にある。

[implementation-plan.md](../spec/06_project/01_implementation-plan.md)は「target document が完成形を定義していることと、code が完成していることを混同しない」と自ら警告しているが、実際にはその混同が起きている。層ごとに横へ厚く作るdelivery orderが、その混同を構造的に許している。

### contract層は無駄ではない

この判断はcontract層への否定ではない。canonical encoding、digest規則、identity binding、staleness判定、error taxonomyは、**後から入れると過去のartifactとdigestが再現しなくなる**種類の設計である。先に作ったこと自体は合理的である。

正確な結論は次の一つだけである。

> **contract層はもう十分である。これ以上磨かず、動くものへ接続すべきである。**

以後、contract層への変更は「貫通で実際に詰まった箇所」だけを理由に行う。網羅性、対称性、将来の拡張余地を理由にした追加はしない。

### 3つの事前調査

本ADRは次の調査結果を根拠にしている。いずれも実測とコード確認を伴う。

- **GitHub adapter最小形**: claim貫通に新規で必要なのは未認証の薄いread client、将来の認証を注入する`TokenSource` seam、candidate filter、claim use caseの4つだけで、parse・canonical artifact・identity・Run永続化・state machineは既存3 packageで揃っている。
- **provider adapterとRun workspace最小形**: この端末でcodex 0.147.0とclaude 2.1.226の両方がheadless + JSON Schema制約付きstructured outputで動作することを実測した。同時に、project doc auto-discoveryを無効化しないと対象repositoryの`AGENTS.md` / `CLAUDE.md`がsessionへ黙って入ることを実測した（codexで+8,897 input token、claudeで`cache_creation`が3,769→75,263 token）。
- **Artifact StoreとRED evidence最小形**: S1〜S3が踏む3 kind（claim 4本 + `author_tests` 3本 + `publish_head` 1本）で`requiredOperationOutputs`が要求するlogical nameの和は8本である（全kindの和は11本）。なお`init()`のpanic guardは各operation kindがkeyを持つことしか検査せず、要求集合を空にすると素通りする——3本を守るのはguardではなくD2のcontract disciplineであり、機械が守ってくれるわけではない。一方で`test-plan` / `red-evidence` / `source-bundle`は`artifactKindRules`に登録が無く、現状の`ArtifactPayload.Validate()`は`protocol_kind_unknown`で弾く。

## Decision

### D1. delivery orderの単位を層ではなく縦の貫通sliceにする

Milestoneごとに層を完成させるのをやめ、「Issue 1件がPull Requestになる」という1本の経路を、各層の最小部分だけを繋いで先に通す。

sliceは次の5本とし、この順で実行する。各sliceは前のsliceが作った実物の上に載る。**ただし引き継がれるのは実装コードとartifactであってRun instanceではない**——`InputIdentity`はContext ManifestとExecution Policyの組であり、provider adapterの実装でExecution Policy digestが変わるとS1のRunは`SemanticInputChanged`でsupersedeされる。`reject_run_input_update` triggerがRunのsemantic inputをimmutableにしているため、S2はS1のRunを再開するのではなく再claimから始まる。

| slice | 到達点 | 主に触るIssue |
| --- | --- | --- |
| S1 | live GitHub Issue 1件が`claimed` Runになる | #16、#17 |
| S2 | `author_tests`がRED evidenceとsource-bundleを固定する | #20、#21、#22、#24 |
| S3 | test-only headがpushされdraft PRが1本できる | #29 |
| S4 | `test_validity` reviewが1 round成立する（verdictが返り`onTestReviewCompleted`がtransitionを成立させる。approve / request_changes / needs_humanのどれでもよい——approveの獲得はS4の条件ではない） | #25 |
| S5 | `implement`とfinal reviewを通しPRがready化する | #27、#28、#29 |

**S3が本ADRの主目的である。** S3到達をもって「Issueが人間の見えるPRになった」と宣言し、そこで製品境界の疑義を実物に対して再評価する。S4以降は貫通後の話であり、S3の結果によっては順序を組み直してよい。

### D2. contract層をfeature freezeする

`internal/contract`への変更は次の2条件のいずれかを満たすときだけ行う。

1. 貫通の実装が実際に詰まった（既存の型・語彙で表現できない成果物がある）。
2. 実装と同時に文書・parser・fixture・testを更新できる（[AGENTS.md](../../AGENTS.md)のContract discipline）。

「まだ足りていない気がする」「対称性が欠けている」を理由にした追加はしない。既知のcontract空白は2つあり、いずれもD1のsliceで必ず踏むため、踏んだ実装PRの中で埋める（「設計詳細 §3」）。

### D3. 「雑にする」ことを各stepで明示的に宣言する

各sliceで**何を作るか**と**何を意図的に雑にするか**の両方を書く。暗黙の手抜きを作らない。

雑にしたものは実装PRの記述と、必要なら[Evaluation harness — deferred](../spec/06_project/04_evaluation-harness.md)へ記録する。[implementation-plan.md](../spec/06_project/01_implementation-plan.md)のdelivery rule「一つのmilestoneのtemporary shortcutをtarget architectureとして文書化しない」をここでも守る——雑にしたものは`05_design/01_architecture.md`や`05_design/contracts/`へ書かない。

### D4. sliceを跨ぐIssueは分割し、dependency宣言をsliceの順序へ合わせる

`dependsOn`はKudo自身が読むreadiness gateであり、実作業順と食い違ったまま放置すると、Kudoが自分自身を動かす段階で必ず矛盾する。S1は「`dependsOn`が非空ならclaimせず`waiting_dependency`を返す」と決めているため（§1）、この整合は代償ではなく**貫通のhard prerequisite**である。

食い違いには2種類あり、直し方が違う。

**(a) 後回しにするIssueへの依存** — 宣言を付け替える。

| Issue | 現在 | 問題 | 変更後 |
| --- | --- | --- | --- |
| #16 | #15 | S1の起点が、後回しにするinbox / outboxに依存している | #13 |
| #19 | #18 | S1はpollingだけで足り、webhookは幅を戻す段に置く | #17 |
| #20 | #15 | 同上（#15後回し） | #17 |
| #21 | #15 | 同上（#15後回し） | #17 |
| #24 | #19、#23 | #23はClaude adapterで、本ADRは「codexとclaudeのどちらか一方で足りる」として除外している | #21、#22 |

**(b) 1つのIssueが2つのsliceにまたがっている** — Issueを分割する。依存の付け替えでは解けない。

| Issue | 現在 | 問題 | 変更後 |
| --- | --- | --- | --- |
| #16 | — | S1の live read adapter とS4のGitHub App実装が同居している | S1側（read adapter + `TokenSource` seam）とS4側（GitHub App + role-scoped token）へ分割 |
| #29 | #28、#49 | S3の`publish_head`とS5の`finalize_pull_request`が同居している。**#29 → #28は嘘ではなく、finalizeは本当にfinal approveを要する** | S3側（`publish_head` / draft PR publish、dependsOn #24）とS5側（`finalize_pull_request` / `ai-review-waiting`投影、dependsOn #28）へ分割 |

#29 → #28の反転を「順序の誤り」として`dependsOn`だけ付け替えるのは誤りである。実在する依存を隠すことになり、finalizeがfinal approveなしに走りうる宣言になる。**slice定義とIssueのどちらかが誤っているのではなく、Issueの粒度がsliceより粗い**というのが正しい診断である。

- `dependsOn`を実装側から黙って書き換えない。分割と付け替えはIssue所有者が行う。
- 分割後のIssueは、それぞれが1つのsliceに収まることを条件とする。

## 設計詳細

### 1. 各sliceで作るものと雑にするもの

#### S1: Issue 1件を`claimed` Runにする

**作るもの**

- 認証を持たない薄いread client。対象repositoryはpublicであり、Issue read、repository content read、commit readはいずれも未認証で200を返す（実測）。認証の設計自体を後回しにする（上記「決着済み (1)」）。
- 薄いread client。Issue list、Issue get、repository content、base commit SHA。
- candidate filter。`GET /repos/{o}/{r}/issues?state=open&assignee=<login>&labels=ai-ready&per_page=100`で3条件がquery parameterだけで満たせ、non-PRはresponseの`pull_request` keyの有無1つで判定できる。4条件とも落とさない（合計10行程度で、削っても最小化にならない）。
- `ReconcileIssue(repositoryRef, issueNumber, Trigger)`。pollerはここへIssueRefを流すだけの薄いproducerにする。
- claim use case。`contract.Compile(body, IssueRef)`が[Issue Contract](../spec/05_design/contracts/issue-contract-v1alpha1.md)のclaim手順1〜2を完全に埋める。adapter側にparseを1行も書かない。
- authority referenceの解決。`GET /repos/{o}/{r}/contents/{path}?ref=<baseSha>`でcontentを取り、`contract.SHA256(bytes)`を`contentDigest`にする。
- `readiness: ready`のgate。`Compile`はdraft / blockedのIssueも成功して返すため、claim use caseが`req.Readiness == contract.ReadinessReady`を明示的に書く。Issue templateの既定値が`readiness: draft`なので、落とすと実際に踏む。

**意図的に雑にするもの**

- GitHub native sub-issue / dependency relationshipとContract blockの照合をしない。[Issue Contract](../spec/05_design/contracts/issue-contract-v1alpha1.md)は「adapterが取得できる場合」の条件付き要求であり、取得しない構成は契約違反にならない。
- dependency completionの証明をしない。ただし省略の仕方は一つだけで、**`dependsOn`が非空ならclaimせず`waiting_dependency`を返す**。`ai-ready`を消費せず、`needs_human`にもせず、pollingで再評価させる。契約が「Issueがclosedであることだけをcompletedとして推測しない」と定めているため、証明機構が無い＝証明できない、であり、これは契約準拠のdegradationである。
- claim中の再取得（契約claim手順7）をしない。「1回fetch → 全部そこから解決 → commit直前に再readしてbody digestを比較」という形だけ保つ。手順7はmodel-bearing Operationの直前の検査であり、そのOperationがまだ存在しない。
- webhookを作らない。signature検証、payload size limit、delivery inboxはS3以降。
- Operation queue、lease、heartbeat、reaper（#14）を作らない。claim後の`DispatchOperation`はS2でinline実行し、queueは後から包む。
- artifact bytesを保存しない（refs-only）。ただし`ArtifactWriter` interfaceの呼び出し口だけS1で確定させる（「§2 落とさないもの」）。

**決着済み (1): S1〜S2のGitHub readは未認証で行い、write認証はS3直前に設計する**

対象repositoryはpublicである。Issue read、repository content read、commit readはいずれも未認証で成立することを実測で確認した。S1〜S2で必要なGitHub readは未認証で通し、この2 sliceではGitHub App / PATいずれの認証機構も作らない。branch pushとPR作成が始まるS3の直前にwrite認証を設計する。

GitHub Appが解いている問題は、role別のpermission downscopeと短命tokenである。S1〜S2にはReview Workerもwrite操作も存在せず、credentialを置かないことがこの段階では最も強い権限分離になる。PATも同様に作らない——public readに不要なcredentialを置くと、rotate対象と漏洩面が理由なく増える。最初のReview Workerが動くS4までには、S3で導入する認証をread-only tokenへdownscopeし、GitHub側でもwrite APIを実行できない構成にする。

この決着により、S1〜S2からJWT署名 / 鍵管理 / token cache / 期限判定と`TokenSource`実装、およびそれらのtestが消える。将来の認証を注入する`TokenSource` seamだけをS1に残し、S3着手時に実装を追加する。

**代償: polling間隔を15分にする。** 未認証のGitHub APIは60 req/hourで、しかもIP単位である。60秒間隔ではpollingだけで枠を使い切り、claimに必要なIssue get / content取得 / base SHAの分が残らない。15分間隔なら消費は4 req/hourで、残り56をclaimへ回せる。

この変更を貫通限定の特例にせず、[github-routing.md](../spec/05_design/04_github-routing.md)以下の製品既定として15分にした。pollingは低遅延経路ではなく取りこぼし回復経路であり（低遅延はwebhookが担う）、60秒である必然性が元から薄いためである。貫通の都合とproduction仕様を二重管理しない。

**認証が必要になる条件（このときに設計する）:**

- private repositoryを対象にする
- write操作（branch push、PR作成、label投影）を実装する — S3以降で必ず到達する
- 15分間隔でもrate limitに当たる（複数repository、または同一IPからの他利用との競合）

したがって認証は「不要」ではなく「S3の直前まで不要」である。write が入る時点で必ず必要になるため、S3着手時に改めてIssueを起票する。権限分離の実効化期限は、それが最初のreview verdictより前になるようS4を上限とする。

**S1で決着させる必要がある未決事項 (2): label投影**

`workflow.Decide`の`onClaimSucceeded`はS1の到達点でいきなり`ProjectStatus{ai-in-progress}`と`DispatchOperation{author_tests}`の2つのActionを返す。したがってS1は「このActionをどう実行するか」を避けて通れない。本ADRはここを決め切っていない。

- §2は`ProjectStatus`のoutbox化（Run transitionと同一transaction）を「後から入れるとtransaction境界の変更になる」として落とさないものに挙げている。
- 一方で影響表は#14 / #15をS3まで後回しにしている。outbox *producer*（transitionと同一transactionでprojection intentを記録する側）をどのsliceで作るかが書かれていない。

commit後にinlineでlabel APIを叩く形にすると、commitとlabel呼び出しの間のcrashで「Runは`runs_one_writer_per_issue`によりwriter確定済みなのにIssueは`ai-ready`のまま」という状態が残り、pollingが再発見しても同indexが2つ目のRunを拒むため自己修復経路が無い。逆にlabel失敗をclaim失敗として返すと[github-routing.md](../spec/05_design/04_github-routing.md)の「GitHub API failureで確定済みRun stateを巻き戻さない」に反する。

**この二択（S1でoutbox producerだけ先に作るか、inlineで進めて上記の穴を明示的に受け入れるか）はS1着手前に人間が決める。** producerはtransitionと同一transactionでのINSERT 1本であり、consumer（S3）やlease / heartbeat（#14）とは分離できる。

#### S2: `author_tests`でRED evidenceを固定する

**作るもの**

- content-addressed Artifact Store。named volume上の`objects/sha256/aa/bb/<hex>`、同一filesystemの`<root>/tmp`経由でfile fsync → link/rename → 親dir fsync、read時のdigest再検証、missingとcorruptの別error。
- Run workspace。Run専用clone → `baseSha`をcheckout → Run専用branch作成 → provider実行 → checkpoint commit → source-bundle化。worktree共有ではなくRunごとの独立cloneにする（実測: linked worktreeの`git rev-parse --git-common-dir`は元の`.git`を返し、object DBとrefが全worktreeで共有される）。
- child process supervisor。timeout、process group kill、exit / timeout / invalid outputの分類、環境変数allowlist、bounded output capture。
- provider adapter（codexとclaudeのどちらか一方で足りる。両方は不要）。schema非依存のinterfaceにし、`OutputSchema []byte`を受けて`FinalMessage []byte`と実行evidenceを返す。
- RED evidence artifact（canonical YAML）。`runs[]`、各runのargv（文字列list。shell文字列1本にしない）、`workingDir`、`exited|signaled|timed_out`のenumと`exitCode`または`signal`、stdout/stderrそれぞれの`(inline, truncated, fullDigest, fullLength)`、environment identity、`headSha`、source-bundle digest。観測時刻と実行時間はcanonical contentに入れない（[ADR-0002](0002-pr-anchored-review.md)のPull Request Observationが確立した先例に従う）。

**意図的に雑にするもの**

- orphan scan、GC、参照カウント、delete / overwrite API、圧縮・pack、PostgreSQL側のartifact metadata table、作成時刻index。いずれもappend-onlyな既存objectを読むだけで後から構築でき、既に書かれたbytesの意味を変えない。
- secret redactionは`func([]byte) []byte`のseamだけ用意し、初版は環境変数由来の値の走査に留める。
- `toolPermissions`はroleごとに1組をhard-codeし、それ以外の値をrejectする。自由集合として受理すると、adapterが強制していない権限境界をExecution Policy digestが主張することになり、evidenceが嘘をつく。
- provider CLIのJSONL eventを細かくparseしない。codexは`--output-schema <file>`に書き出して`-o <file>`から最終メッセージを読み、claudeは`--json-schema`と`--output-format json`の`.structured_output`を読む。
- structured outputには「計画と主張」だけを載せる。`author_tests`のstructured outputはAC ID → test caseのmappingに限定し、test patchをJSONに埋め込ませない。コード変更はworktreeの状態からgitで取る。両CLIともファイル編集agentであり、patchをstructured outputで往復させるのは能力の後退である。
- MCP設定、rate limit専用backoff、adapter versionの自動検出はしない（ただしversion照合はする。§2）。

#### S3: draft PRを1本publishする

**作るもの**

- `publish_head` Operation。branch pushとPR ensureの冪等な組（[ADR-0002](0002-pr-anchored-review.md) §1）。
- `pull-request-observation` artifactの固定。
- `ai-in-progress`のstatus outbox consumer。

**意図的に雑にするもの**

- PR bodyの生成はTask Issue link、Run ID、phaseだけの最小形にする。test plan要約の決定論的生成はS4以降。
- 外部干渉（人間push、close、base変更）のreconciliationは検出だけにし、復旧経路はS4以降。
- 4 label lifecycleのうち、S3で投影するのは`ai-in-progress`だけにする。

#### S4 / S5

S3到達後に、[ADR-0002](0002-pr-anchored-review.md)のhandler pipelineと[implementation-plan.md](../spec/06_project/01_implementation-plan.md) Milestone 5〜6のdeliverableへ戻る。S3の実測結果を見てから順序を確定するため、本ADRではsliceの存在だけを宣言し、内訳は固定しない。

### 2. 落とさないもの（後から入れるのが本当に難しいもの）

「後から入れられない」ではなく、**後から入れると過去に作ったevidenceの意味が変わる／欠落していたことを後から観測できない**ものを落とさない。いずれも今なら数十行で入る。

#### provider session isolation

| 項目 | 後から入れると何が壊れるか |
| --- | --- |
| project doc auto-discoveryの無効化（codex `-c project_doc_max_bytes=0`、claude `--safe-mode`）。**3 flagは効果が別物なので束ねない**——project docを実際に止めるのは`project_doc_max_bytes=0`単独であり、`--ignore-user-config`は`$CODEX_HOME/config.toml`を読まないだけ（authは`CODEX_HOME`を使い続ける）、`--ignore-rules`はexecpolicyの`.rules`を読まないだけでproject docとは無関係である。どれか1つが落ちたときに気付けるよう、目的ごとに分けて書く | 対象repository自身の`AGENTS.md` / `CLAUDE.md`（および親ディレクトリのCLAUDE.md連鎖、skill、plugin、hook、settings）がsessionへ黙って入る。実測でcodex +8,897 token、claudeは`cache_creation`が3,769→75,263 token。**testでは絶対に検出できず**、症状は「なぜか妙に文脈を知っているprovider」としてしか現れない。Kudoは同じ文書をContext Manifestでcontent digest固定して渡す設計なので、auto-discoveryは未pinなworking tree版をdigestの外から二重注入する。Operation digestと「sessionが実際に見た入力」の対応が崩れる。 |
| 環境変数のallowlistとmodelのCLI flag明示 | この端末には既に`ANTHROPIC_MODEL`が設定されており、素の`claude -p`は実際にそれへ従った。`os.Environ()`をそのまま渡すと、Execution Policyの`model`が黙って上書きされ、policy digestが実行実態を表さなくなる。 |
| operation-scoped state directory（`CODEX_HOME` / `CLAUDE_CONFIG_DIR`をOperationごとのtemp dirへ） | claudeは`--no-session-persistence`を付けても`~/.claude/projects/<cwdスラッグ>/memory/`を作る。同一Runの複数Operationは同じworktree = 同じcwdを使うため、cwdをキーにしたmemoryがOperationをまたぐconversational carryover経路になる。transcriptだけ潰してmemoryを残すと、要件の文言は満たすが[AGENTS.md](../../AGENTS.md)の原則は破れる。**ただしcredentialも同じdirectoryに入るため、空のtemp dirを指すと両CLIとも未認証になる**（下の未決事項）。 |
| provider interfaceのschema非依存性 | `contract.TaskContext`を引数に取る形にすると、Task Context schemaのversionが変わるたびにadapterが分岐する。[Task Context Protocol](../spec/05_design/contracts/task-context-v1alpha1.md)のVersioning節が旧schemaの併存を要求しているため、この分岐は爆発する。後から直すと全呼び出し側に波及する。型を先に正しくするコストはほぼゼロである。 |
| `adapterVersion`と実CLIの起動時照合 | config定数のまま放置すると、host開発時とCompose worker imageで値が食い違ったままevidenceに載る。照合は1行。 |

#### content identityとdurability

| 項目 | 後から入れると何が壊れるか |
| --- | --- |
| checkpoint commitのidentity固定（author/committerのname・emailと`GIT_AUTHOR_DATE` / `GIT_COMMITTER_DATE`） | 既定に任せるとhead SHAがwall clockとhostのgit configに依存する。head SHAはOperation Result、Review Request binding、PR observationへ焼き込まれるため、後から規則を変えると過去RunのSHAが再現しない。なお[docs/contracts/](../spec/05_design/contracts/)にこの規則の記述は現状無く、実装と同時に文書化が要る。 |
| Artifact Store layoutにalgorithm segmentと2段fanoutを含めること | named volumeはupgradeを跨いで残るためlayoutはdurable formatである。後から変えると既存objectの移行が要る。 |
| durability手順（同一FSのtemp、fileと親dirのfsync、write-onceを保つlink、EEXISTは既存を検証して成功へ収束、read時のdigest再検証、missingとcorruptの別error） | 「Resultをcommitしたのにbytesが消えている」「同一digestへ破損上書き」「reviewerが誤ったbytesでapproveする」のいずれも、後から検出も修復もできない。 |
| store APIをstreaming + store測定descriptorにすること | `ArtifactPayload`（`Data []byte`）で型付けすると、必須3本（test-plan / red-evidence / source-bundle）がkind未登録で`Validate()`に弾かれ、かつsource-bundle（git bundleは容易に数十MB）が全量on-memoryになる。`NewArtifactEntry`のコメントが守っている不変条件（producerにlength/digestを自己申告させない）を、opaque側でも守る必要がある。API形状は最も広く波及するretrofitである。 |
| RED evidenceに`headSha`を含めること | 含めないと、同一bytesのevidenceが別headのArtifact Manifestへそのまま載る。Review Request側の`headSha`は変わるのにevidence側は不変という状態が作れ、review stalenessはdigest比較で判定されるためprotocol validationでは検出できない。 |
| RED evidenceに未切り詰めstdout/stderrのdigestとlengthを含めること | inlineだけにすると、truncation上限を変えた瞬間に同一実行のevidence identityが変わり、しかもreviewerは「全部を見たか」を判定できない。`MaxCanonicalTextBytes` = 64 KiBは意図的な制約なので緩めない。全文は別objectにする。 |
| RED evidenceの`runs[]`複数化と`exited` / `signaled` / `timed_out`の区別 | manifestはlogical name重複を拒否するため「1 name = 1 command」にするとbuild + testのような複数commandを後から表現できずschema bumpになる。exit codeだけではtimeoutとtest failureを区別できず、[test-validity policy](../spec/05_design/review-policies/test-validity-v1alpha1.md) §5の「timeoutやenvironment failureをREDとしない」を満たせない。 |
| source-bundleをgit bundleにすること（`git archive` / tarにしない） | tarはcommit objectを含まないため`headSha`を再構築・検証できず、契約を静かに満たさない。違反はReview Workerがheadを検証する段階（数milestone先）まで表面化しない。media typeも`application/octet-stream`ではなく識別可能な値にする。 |
| GitHub bodyを正規化しないこと | `bodyDigest`はIssue Observationとして永続化されRunのaudit lineageになる。後から正規化方針を変えると過去のdigestが再現しない。CRLFは`Compile`が許容し、単独CRやNULは`CodeBodyControlCharacter`で拒否されるので、adapter側の正規化は一切不要である。 |
| base commit SHAの固定と、repository-relative authority pathをそのSHAで解決すること | `validateContextManifest`が`baseSha`に40または64桁lowercase hexを要求するため技術的に省略不可。加えてbaseと別時点でrefを解決するとmanifestが「base上のclosure」でなくなり、identityの意味が静かに変わる。 |

#### 境界の位置

| 項目 | 後から入れると何が壊れるか |
| --- | --- |
| role次元のcredential分離（role-scoped clientをconstructor引数で渡す。package-levelのsingletonやpackage変数のtokenを作らない） | singletonにするとReview Workerのread-only化が全call siteの変更になり、[architecture.md](../spec/05_design/01_architecture.md)の「Issue Workerのwrite credentialを共有しない」を後から満たすのが高くつく。PATは「dev / test専用のTokenSource実装」としてだけ持ち、既定にもproduction compose profileにも入れない。 |
| transport failureの分類点を1箇所に集約すること | 403はpermission不足でもsecondary rate limitでも返る。`x-ratelimit-remaining`と`retry-after`を見ずに全403をpermission扱いにすると`ai-needs-human`へescalateし、「transport failureをcontract rejectionに変換しない」に反する。分類点が散ると全call siteの修正になる。 |
| `ReconcileIssue`のresult enumを6値（`claimed` / `waiting_dependency` / `waiting_capacity` / `skipped_not_candidate` / `claim_rejected` / `failed_transport`）で最初から閉じること | 6値それぞれでlabelの扱いが違う。部分enumに対して投影を書くと、後から値を足したとき既定分岐が誤ったlabel操作をする。列挙を先に閉じるのは型定義1つで済む。S1で到達不能な`waiting_capacity`も含める。 |
| `ReconcileIssue`を唯一の入口にすること | Milestone 3のexit criterion「webhookを意図的に捨ててもpollingが同じRunを作る」は、両者が同じ関数へ収束していないとtestできない。分岐させたままwebhookを後から足すと、収束性の証明が構造的に不可能になる。`Trigger`はclosed type（poll / startup / webhook delivery）にし、observabilityとdedup用に限る——candidate判定にもIssue Contract入力にも使わない。 |
| `ProjectStatus`のoutbox化（Run transitionと同一transaction） | commit後にinlineでlabel APIを叩くと、label失敗をclaim失敗として返す誘因になる。retrofitはtransaction境界の変更になる。 |
| pagination（Link header）を最初から実装するか、少なくとも黙って打ち切らないこと | 黙ったtruncationは「Issueが永遠にclaimされない」という観測不能なfail-openになる。実装しないなら「next linkがあるのに読まなかった」ことを明示的にtransport failureとして失敗させる。 |
| `pull_request` fieldによるPR除外 | issues list endpointはPull Requestも返す。落とすとPR bodyをCompileしてstrict parseに失敗し、`claim_rejected` → `ai-needs-human`を人間のPRへ投影する。 |
| authority referenceの解決（S1で省略しない） | routingのresult taxonomyに「未実装」を表現する値が無い。唯一の受け皿である`claim_rejected`は「人間が直すべきcontract/authority不備」を意味し`ai-needs-human`を投影するため、正しいIssueに誤ったlabelを貼る。実装コストはendpoint 1本。 |
| Artifact Store packageをControllerからimportさせないこと | [runtime-platform.md](../spec/05_design/03_runtime-platform.md)の volume契約でControllerはartifacts volumeをmountしない。read APIを呼べる形にすると、コード上は通るがCompose上は必ず失敗する依存ができる。interfaceは利用側（issueworker / reviewworker）に置き、Controllerはrefだけを扱う。 |
| storeのkeyをdigestのみにすること（Run scopeやlogical nameで引かせない） | content identityの一意性とvolume境界が同時に崩れる。 |
| Runごとの独立clone | linked worktreeはobject DBとrefをrepository単位で共有する。同一repositoryの複数Runを同時に走らせると、mutableな共有資源ができる。独立cloneならこの問題自体が存在しない。worktree共有はdisk最適化であり、必要になってから測って入れる。 |
| claim use caseをController側packageに置かないこと | `ReconcileIssue`は[architecture.md](../spec/05_design/01_architecture.md)上Controllerの責務だが、claimは**Issue WorkerのOperation**である。queueを作らないS1でclaim use caseを`ReconcileIssue`の内側へ書くと、body取得・`contract.Compile`・authority解決・`ArtifactWriter`の呼び出し口がすべてController側packageに生える。`requiredOperationOutputs[claim]`は4本のoutputを要求するので、この呼び出し口は必ずArtifact Storeへ繋がり、[runtime-platform.md](../spec/05_design/03_runtime-platform.md)のvolume契約（Controllerはartifactsをmountしない）に反する——本表が1行下で禁じている「hostでは通るがCompose上は必ず失敗する依存」をS1の構成そのものが作る。`ReconcileIssue`は薄いrouterに留め、claim use caseはissueworker側packageに置く。inline呼び出しでよいが、package境界だけは#14が入る前に確定させる。 |
| `ArtifactWriter`（利用側package所有）の呼び出し口をS1で確定させること | retrofitが難しいのは保存先ではなく呼び出し口の位置である。S2でArtifact Storeを差し替えるとき、ここが決まっていればsignature変更にならない。bytesをPostgreSQLへ一時退避する近道は取らない（[architecture.md](../spec/05_design/01_architecture.md)の「bytesはvolumeに置く」と衝突し、data migrationと二重write pathを生む）。 |

### 3. 貫通で必ず踏む2つのcontract空白

D2のfeature freezeの例外である。いずれも踏んだ実装PRの中で、文書・parser・fixture・testを同時に更新する。

1. **`test-plan` / `red-evidence` / `source-bundle`が`artifactKindRules`に無い。** `requiredOperationOutputs[author_tests]`はこの3本を要求するが、`ArtifactPayload.Validate()`は`protocol_kind_unknown`で弾く。現状のtestは`ArtifactEntry`リテラルを直接組むopaque entryとして扱っており、lengthとmedia typeがproducerの自己申告になっている。S2の実装に入る前に「opaque kindとして追加する（`raw-issue-body`と同じくschemaPrefix空、media typeだけ固定）」か「Artifact Storeが`ArtifactPayload`を経由しない別経路を持つ」かを決める。
2. **checkpoint commitのidentity規則が[docs/contracts/](../spec/05_design/contracts/)に無い。** [Operation Protocol](../spec/05_design/contracts/operation-protocol-v1alpha1.md)は「同じ入力から同じ結果を再生成したattemptは同じcontent identityを持つ」と述べており、head SHAを含むResult digestがその対象である。commit identityが非決定的だとこの性質がhead経由で壊れる。

副次的な論点として、`artifact_refs`（PR #57 で`kudo_`prefixを除去した後の名前）は`(schema, digest)`をPRIMARY KEYにし`schema`に`NOT NULL CHECK (schema <> '')`を課している。ただし現状この tableへ書くのは`insertArtifactRef`だけで、呼び出し元はContext ManifestとExecution Policyのref（いずれもschema必須）に限られる。すなわちこれはRunのsemantic inputに対するFKの受け皿であって汎用のartifact metadata tableではない。したがって「schemaを持たないartifact（`raw-issue-body`、`source-bundle`）をここへ登録する」という前提自体が未確認であり、S2の前に決めるべきは制約の緩和ではなく**そもそもこの tableの用途をsemantic input refに限るのか、artifact metadata一般へ広げるのか**である。広げると決めた場合にのみschema制約の扱いが問題になる。

### 4. 後回しにするものと、後から足せる根拠

| 後回しにするもの | 後から足せる根拠 |
| --- | --- |
| webhook（raw body signature検証、payload size limit、delivery inbox） | `ReconcileIssue`を唯一の入口にしてあれば、webhook handlerは同じ関数を呼ぶproducerを1本足すだけでadditiveに収まる。ただしhealthz用にHTTP serverを先に建てるなら、bodyを消費するJSON middlewareを挟まない（署名はraw bodyに対して検証する必要がある）。 |
| native relationship照合 | [Issue Contract](../spec/05_design/contracts/issue-contract-v1alpha1.md)が「adapterが取得できる場合」の条件付き要求。後入れはread 1本と比較だけ。 |
| dependency completion証明 | `waiting_dependency`で契約準拠のdegradationになっており、証明機構ができたらその分岐を置き換えるだけ。 |
| claim中の再取得（契約手順7） | model-bearing Operationの直前の検査であり、そのOperationがまだ無い。後入れは再GETとdigest比較の10行。 |
| Operation queue、lease、heartbeat、reaper（#14） | `ValidateWorkerOperation`がkind=claimでも`ExecutionPolicyRef`と`RunID`を必須にしており、envelopeの形が既に「GitHubを触る前にpolicyとRun IDが決まっている」ことを強制している。同じ順序を保てば、queueは後から包むだけで入る。 |
| Artifact Storeのorphan scan / GC / 参照カウント | append-onlyな既存objectを読むだけのread-only reportであり、既に書かれたbytesの意味を変えない。 |
| worktree共有によるdisk節約 | 独立cloneは正しい側の選択であり、共有は測ってから入れる最適化である。 |
| remote pushとPR mutation（S3まで） | [Operation Protocol](../spec/05_design/contracts/operation-protocol-v1alpha1.md)の`author_tests`必須outputはtest-plan / red-evidence / source-bundleの3つだけで、branch pushもPRも含まない。`publish_head`は別Operationである。 |
| secret redactionの網羅性、graceful shutdownのlease drain、MCP設定、rate limit専用backoff | Milestone 4のexit criteriaに含まれない。seamだけ用意しておけば実装は差し替えになる。 |
| production Compose topology（Milestone 7） | S1〜S5はhostとMilestone 0のdevelopment Compose上で成立する（PostgreSQL portをhostへ出す場合は`infra/compose.debug.yaml`のoverlayが要る）。role container分離は[ADR-0001](0001-compose-runtime.md)で決着済み。ただし「単一binaryのmode分岐に閉じる」は正確ではない——runtime stageは`gcr.io/distroless/static-debian12:nonroot`でshellもgitもnodeも無く、S2が要求する独立clone、checkpoint commit、`git bundle`、Node製provider CLIはこのimageでは動かない。**Issue Worker imageはgit + provider CLI + credential mountを抱える別のbase imageになる**。この差分がM7の実体であり、後回しにする判断自体は変わらないが、作業量を「構成の問題」と見積もらない。 |
| Issue WorkerとReview Workerのprocess分離 | [AGENTS.md](../../AGENTS.md)は「同じOS processで走ってよいが、mutable worktree、provider session、conversational memory、application-private stateを共有してはならない」と定める。後者を守っていればprocess分離は配備の問題になる。 |

## Milestone計画との関係

**Milestone 3〜6をsupersedeしない。順序の単位を変えるだけである。**

- Milestone 3〜6の**deliverableとexit criteriaは完成の定義として維持する**。本ADRはそれらを削らない。
- 変わるのは**実行順序の単位**である。従来は「Milestone Nを完成させてからN+1へ」だったものを、「各Milestoneの貫通に必要な最小部分を横断して先に通し、残りを後から幅として戻す」に変える。
- したがって各Milestoneは、あるsliceで**開始**され、後続のsliceまたは幅を戻す段で**完了**する。「Milestone 3が完了した」と言えるのはexit criteriaを全部満たしたときだけであり、S1到達はそれを意味しない。
- Milestone 0（Compose開発基盤）とMilestone 1（Protocol core）は完了扱いのまま変えない。Milestone 2はRunStore（#13）まで到達しており、queue / lease / inbox / outbox（#14、#15）が残る——これはD4のとおりslice順へ従属する。
- Milestone 7（production Compose）とMilestone 8（acceptance / hardening）は本ADRの影響を受けない。貫通後に幅を戻し切ってから着手する。

対応関係は次のとおりである。

| Milestone | どのsliceで開始するか | 貫通時点で未達のまま残るもの |
| --- | --- | --- |
| M2 Durable control plane | 完了済み部分あり（#13） | Operation queue、lease、attempt recovery、inbox / outbox |
| M3 GitHub discovery and claim | S1 | webhook、pagination網羅、4 label lifecycle、rate limit retry、`healthz` / `readyz` |
| M4 Artifact / workspace / process runtime | S2 | orphan detection、secret redaction網羅、両provider adapter。**role別credential / filesystem policyはS4を例外とする**——S4は最初の本物のreview verdictが出るsliceなので、その時点でReview Workerはread-only tokenとworkspace非mountで構築されていなければならない（[runtime-platform.md](../spec/05_design/03_runtime-platform.md)、[AGENTS.md](../../AGENTS.md)）。constructor注入のseamがあるためretrofit自体は安いが、「後で入れるもの」として運用に定着させない |
| M5 RED and test review loop | S2（RED）、S3（publish）、S4（review） | `revise_tests`、`needs_human` escalation / resumption、staleness全経路 |
| M6 GREEN / final review / PR | S5 | `repair_implementation`、test mutation detection、required checks統合、PR body validator |

**未達の台帳を持つ。** 貫通が通ったことでMilestone exit criteriaが満たされたと誤読しないよう、上表の右列を[implementation-plan.md](../spec/06_project/01_implementation-plan.md)側で追跡する。

## Consequences

### 影響を受ける文書・Issue

| 対象 | 変更 |
| --- | --- |
| implementation-plan.md | Delivery order節を追加し、Milestoneが「完成の定義」であって実行順序の単位ではないことを明記する。Milestone 3〜6の冒頭へ本ADRの参照を置く |
| #16 / #17 | S1のscopeへ絞り、雑にする範囲（native relationship、dependency証明、claim中の再取得、webhook）をIssue本文へ明示する。#16はGitHub App部分をS4のIssueへ分離する（D4-b） |
| #16 / #29 | sliceを跨ぐためD4-bに従って分割する。分割後の各Issueは1つのsliceに収まる |
| #16 / #19 / #20 / #21 / #24の`dependsOn` | D4-aの表に従って付け替え、slice順へ合わせる |
| #13（PostgreSQL migration / RunStore） | 実装が先行して確定した部分（migration runnerにgooseを採用、table名からのapplication prefix除去、Escalation Policy refとreview round counterの列）へIssue本文を合わせる。実装側は変更しない |
| #20 / #21 / #22 / #24 | S2のscopeへ絞る。#20は「§2 落とさないもの」のlayout / durability / streaming APIを必須とし、GC / orphan scanを外す |
| #14（queue / lease / heartbeat） | slice順へ従属させる。S1〜S2の間は`DispatchOperation`をinline実行し、queueは後から包む |
| #15（inbox / outbox） | inboxはwebhookと同時に入るためS3以降で足りる（additive）。**outbox producerはS1で決着が要る**（§1 S1の未決事項）。同一Issueにまとめたまま両方を後回しにしない |
| docs/contracts/operation-protocol-v1alpha1.md | checkpoint commit identity規則を、S2の実装PRと同じchangeで追記する |
| internal/contract/artifact.go | `test-plan` / `red-evidence` / `source-bundle`のkind語彙の扱いを、S2の実装PRと同じchangeで決める |
| docs/deferred/ | 貫通中に作ったshortcutのうち、Issue化するには早いものを記録する |

### 利点

- 「Issueが1件もPRになっていない」という現在の最大のリスクが、5 sliceのうち3本目で解消する。
- 製品境界の疑義を、文書ではなく**動く実物**に対して評価できるようになる。境界が動いた場合の破棄コストは、薄いadapter層のほうがcontract層より小さい。
- どのcontractが実際に使われ、どれが使われていないかが実測でわかる。以後のcontract変更が「網羅性」ではなく「詰まった箇所」を根拠にできる。
- provider CLIのheadless契約とstructured outputが実測で動くことを既に確認済みであり、S2の主要な不確実性は残っていない。
- 各sliceが小さいため、`mise run check`と review の回転が速くなる。

### 代償・リスク

- **貫通で作るRunは捨てる前提になる。** Execution Policyの`provider` / `model` / `adapter` / `adapterVersion`はprovider adapter未実装のうちは暫定値であり、実装後に`ExecutionPolicyRef`のdigestが変わる。S1で作ったRunは`SemanticInputChanged` → supersedeの対象になる。仕様どおりの挙動だが、最初のRunが「本物の履歴」にならないことは受け入れる。
- **Milestone exit criteriaの一部が長期間未達のまま残る。** 並行性、crash recovery、pagination網羅、rate limit retryは貫通時点で未証明である。上の台帳で追跡するが、追跡が形骸化すると「動いたから完成」という誤読が起きる。これは本ADRが導入する最大のリスクである。
- **層ごとの品質が非対称になる。** contract層はテスト行数が非テスト行数を上回る一方、adapter層は薄いtestで始まる。reviewで「同じ厚さ」を期待しない合意が要る。逆に、この非対称を放置すると幅を戻す段のコストが読めなくなる。
- **`dependsOn`の編集は貫通の前提条件である。** D4は自動化しないと決めたため、6件のIssue編集が貫通開始前に必要になる（D4の表）。readiness gateの意味が一時的に弱まる。
- **live検証には専用のTask Issueが要る。** 現在open なIssueはすべてlabelが`kudo-task`で`ai-ready`を持たず、#16 / #17は`readiness: draft`である。既存Issueに`ai-ready`を付けると本番相当のclaimが走るため、`assignee` + `ai-ready` + `readiness: ready`を満たす検証用Issueを別途用意する。
- **S1完了時点でRunは`claimed` phaseで停止する。** `DispatchOperation{author_tests}`を受けるqueue（#14）が無いためである。これを埋めるためにstate machineを迂回する近道（claim use caseから直接`author_tests`を呼ぶ等）を作らない誘惑がある。S2ではinline実行でよいが、必ず`workflow.Decide`が返したActionを経由する。
- **「雑にする」判断が暗黙の負債になりうる。** D3で明示宣言を義務づけるが、宣言のない手抜きは検出できない。実装PRのreviewでここを見る必要がある。
- GitHub contents APIのJSON base64レスポンスは1MB上限であり、authority fileがこれを超える場合はraw media typeまたはblob APIが必要になる。またresponseの`sha`はgit blob SHA-1であり`contract.Digest`（`sha256:`前置）ではないため、そのまま`contentDigest`へ入れると`Digest.Valid()`で落ちる（fail-closedなので事故にはならない）。

### 未決事項（deferred）

- **S4 / S5の内訳**: S3の実測結果を見てから確定する。本ADRはsliceの存在だけを宣言する。
- **operation-scoped state directoryへのcredential供給（S2の実行前提）**: `CODEX_HOME` / `CLAUDE_CONFIG_DIR`をOperationごとのtemp dirへ向ける方針は、認証情報がまさにそのdirectory配下にあるため（`~/.codex/auth.json`、`~/.claude/.credentials.json`）、空のtemp dirを指すと両CLIとも未認証になりS2が1回も動かない。開発環境の両providerはsubscription / OAuth認証で`ANTHROPIC_API_KEY` / `OPENAI_API_KEY`を持たないため、API keyへの切り替えも即座には取れない。credentialだけをseedする（copy / symlink）方針を決める必要があり、seedの仕方によっては`~/.codex/AGENTS.md`のようなglobal project docを巻き込んで、この節が防ごうとしている二重注入を自分で復活させうる。**seedはcredential fileのみをallowlistする**方向で決着させる。
- **`artifact_refs`の用途**: semantic input refの受け皿に限るのか、artifact metadata一般へ広げるのかをS2の前に決める（§3）。広げる場合にのみschema制約の扱いが論点になる。
- **artifactのmissing / corruptに対応する`FailureClass`**: 現行語彙は`timeout` / `rate_limit` / `network` / `provider_crash` / `provider_invalid_response` / `github_transport`の6値で閉じている。**「terminalと決めて先送り」は現行コードでは実行できない**——`AttemptFailure.TerminalOutcome`は先に`Validate()`を通すため、`failed_terminal`を記録するには必ず6値のいずれかを名乗る必要がある。したがって選べるのは (a) failure recordを作らず`needs_human`へescalateする、(b) D2のfreeze例外としてclass を1つ足す、の二択であり、どちらかをS2の前に決める。既存classへ流用すると、audit recordに嘘のclassがdurableに残って後から訂正できない。
- **`ArtifactEntry`の長さ0チェック**: 現行の検証は`Length >= 0`しか見ないため、長さ0の`red-evidence`もmanifest validationを通る。protocol層で弾くか、store側のPutで弾くか、`author_tests` handlerが固定前に検査するかを決めていない。
- **authority contentをmanifest entryとして載せる場合のlogical name規則**: `validArtifactName`は小文字英数字と`- . / _`しか許さないが、authority path側（`validAuthorityPath`）は大文字を許すため、この repository自身の`AGENTS.md`をそのままnameにできない。必須集合には入っていないため貫通では回避できるが、命名規則を場当たりで決めない。
- **RED evidenceのversioned schema化**: 貫通がS3で止まる（reviewを含まない）間はopaque bytesでも実害が無いが、S4へ広げる前にschemaを決める必要がある。決めた時点で過去のevidenceは別identityになる。この境界を跨ぐ判断を暗黙にしない。
- **review round上限**: [ADR-0003](0003-review-round-limit.md)で決着済みである。`ReconcileIssue`のresult taxonomyとlabel投影を実装するときは、同ADRのescalation reasonと既存enumを再利用し、別の語彙を作らない。

## Revisit conditions

次のいずれかが成立した場合、本ADRを新しいADRで再検討する。

- S3（draft PR publish）へ到達しても製品境界の疑義（「execution コンテキストだけを切り出したものは望むものではない」）が解消しない。この場合、貫通の対象そのものを再定義する必要がある。
- 貫通がS1〜S3の3 sliceで到達できず、その原因が**contract層の不足**であると判明した。この場合はD2のfeature freezeを解く。
- 逆に、貫通の過程で**使われないcontractが体系的に見つかった**。この場合はv1alpha1のうち何を削るかを別ADRで決める（alphaであり外部consumerを持たないため削除は可能である）。
- provider CLIのheadless契約（structured output、project doc無効化のflag、state directoryのenv）が上流の変更で壊れ、§2で「落とさない」としたisolationがCLI flagでは実現できなくなった。
- 幅を戻す段で、Milestone exit criteriaの未達台帳が追跡不能な規模へ膨らんだ。この場合はsliceの薄さが過剰であったことを意味する。

## References

- [Implementation plan](../spec/06_project/01_implementation-plan.md)
- [Architecture](../spec/05_design/01_architecture.md)
- [GitHub routing](../spec/05_design/04_github-routing.md)
- [Runtime platform](../spec/05_design/03_runtime-platform.md)
- [Issue Contract v1alpha1](../spec/05_design/contracts/issue-contract-v1alpha1.md)
- [Task Context Protocol v1alpha1](../spec/05_design/contracts/task-context-v1alpha1.md)
- [Operation Protocol v1alpha1](../spec/05_design/contracts/operation-protocol-v1alpha1.md)
- [Review Protocol v1alpha1](../spec/05_design/contracts/review-protocol-v1alpha1.md)
- [Test Validity Review Policy v1alpha1](../spec/05_design/review-policies/test-validity-v1alpha1.md)
