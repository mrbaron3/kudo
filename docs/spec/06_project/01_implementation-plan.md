# Implementation plan

## Target

本計画は bootstrap demo ではなく、[プロダクト設計](../01_product-design/README.md) の完成条件を満たす
Compose-deployed issue-to-PR runtime を完成させるための delivery order を定義する。

各 milestone は外部 adapter を後回しにするだけの layer 実装ではなく、可能な範囲で一つの recovery/idempotency behavior を end-to-end に証明する。fast deterministic test を先に実行し、live GitHub/provider test は opt-in として最後に重ねる。

## Current status

現在repositoryに実装済みなのは、`kudo help`/`kudo version`のCLI bootstrap、Milestone 0のCompose開発基盤（multi-stage Dockerfile、PostgreSQL 18.4付き開発用Compose、container内check/integration test入口）、`kudo.issue/v1alpha1`のstrict parser、Issue Observation・canonical Task Context・Claim Requirementsを作るpure Issue Compiler、Context Manifest・Execution Policyのcanonical core、Worker Operation/Review protocolのcanonical identity・binding・semantic staleness判定、およびReview Result binding lineageを含むversioned PostgreSQL schemaとRunStoreである。target architectureは文書化されているが、次は未実装である。

- Operation queue、lease、inbox/outbox
- structured claim contextのdurable schema（RunStoreは保存先が無い間、値を持つRunを受理せず拒否する）
- GitHub webhook/poller/reconciliation
- Artifact Store と Run workspace
- Codex/Claude provider adapter
- Issue Worker / Review Worker / Controller
- production Compose topology（role service、migration、health/operations）

target document が完成形を定義していることと、code が完成していることを混同しない。

## Delivery order

**milestoneは「完成の定義」であって実行順序の単位ではない**（2026-08-19決定）。実行は縦の貫通sliceを単位とし、各milestoneの貫通に必要な最小部分を横断して先に通し、残りを後から幅として戻す。

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
| M2 | Operation queue、lease、attempt recovery、delivery inbox。**status outbox producer は S1 で決着が要る**（下記「貫通の未決事項」） |
| M3 | webhook、pagination網羅、4 label lifecycle、rate limit retry、`healthz` / `readyz` |
| M4 | orphan detection、secret redaction網羅、両provider adapter。**role別credential / filesystem policy は S4 を例外とする**（最初の review verdict 時点で Review Worker は read-only／workspace 非 mount） |
| M5 | `revise_tests`、`needs_human` escalation / resumption、staleness全経路 |
| M6 | `repair_implementation`、test mutation detection、required checks統合、PR body validator |

各sliceの範囲は次のとおり定義する（この順序を選んだ理由は「この順序にした理由」節）。

### この順序にした理由

2026-08-19時点（`5815cdf`）で、非テストGo行数6,301のうち`internal/contract`が4,579行（73%）を占める一方、GitHub adapter、provider adapter、Artifact Store、Run workspace、Issue Worker、Review Worker、Controllerはいずれも未実装であり、**Kudoは自身のIssueを1件もPull Requestにしたことがなかった**。11個のversioned protocolはv1alpha1として完成度高く作り込まれていたが、製品境界そのものはまだ動く可能性があった。層ごとに横へ厚く作る順序が、「target documentの完成」と「codeの完成」の混同を構造的に許していた。

contract層を作ったこと自体は誤りではない。canonical encoding、digest規則、identity binding、staleness判定は、後から入れると過去のartifactとdigestが再現しなくなる種類の設計である。結論は「**contract層はもう十分であり、これ以上磨かず動くものへ接続すべき**」の一点であり、これがcontract feature freeze（Delivery rules）の根拠でもある。

**S3到達（人間の見えるdraft PR）をもって、製品境界の疑義を実物に対して再評価する**ことがこの順序の主目的である。S4以降は貫通後の話であり、S3の結果によって順序を組み直してよい。

決定に際して次を検討し、退けた。

- **層ごとのmilestone順（従来）**: 「Issueが1件もPRになっていない」という最大のリスクを解消する時点が最も遅い。文書とコードの完成の混同を構造的に許し続ける。
- **contract層の追加整備を先に完了させる**: 動きうる製品境界に対して、外部consumerを持たないalpha protocolを磨き続けることになる。貫通で「実際に詰まった箇所」を根拠に変更するほうが精度が高い。
- **S1〜S5を1つの大きな貫通として一気に通す**: S3で製品境界を再評価する機会を失う。S4以降の内訳はS3の実測を見てから決めるほうが手戻りが小さい。

引き継がれるのは実装コードとartifactであってRun instanceではない。provider adapter実装でExecution Policy digestが変わるとS1のRunは`SemanticInputChanged`でsupersedeされるため、S2はS1のRunを再開せず再claimから始まる。貫通で作るRunは捨てる前提であり、最初のRunが「本物の履歴」にならないことは受け入れる。

**この順序の最大のリスクは、milestone exit criteriaの一部が長期間未達のまま残ることである。** 上の未達台帳の追跡が形骸化すると「動いたから完成」という誤読が起きる。あわせて層ごとの品質が非対称になる（contract層はテスト充実、adapter層は薄いtestで開始）ため、reviewで「同じ厚さ」を期待しない合意が要る。

### この順序を見直す条件

次のいずれかが成立した場合、delivery orderを再検討する。

- S3へ到達しても製品境界の疑義が解消しない。この場合、貫通の対象そのものを再定義する必要がある。
- 貫通がS1〜S3で到達できず、原因が**contract層の不足**であると判明した。この場合はcontract feature freezeを解く。
- 貫通の過程で**使われないcontractが体系的に見つかった**。alphaで外部consumerを持たないため削除は可能であり、何を削るかを別途決める。
- provider CLIのheadless契約（structured output、project doc無効化flag、state directory env）が上流変更で壊れ、session isolationがCLI flagでは実現できなくなった。
- 幅を戻す段で、exit criteria未達台帳が追跡不能な規模へ膨らんだ。sliceの薄さが過剰であったことを意味する。


### 各sliceの範囲

各sliceで「何を作るか」と「何を意図的に雑にするか」の両方を明示する。暗黙の手抜きを作らない。雑にしたものは実装PRの記述と、必要なら[Evaluation harness — deferred](04_evaluation-harness.md)へ記録し、`05_design/01_architecture.md`や`05_design/contracts/`へは書かない。

#### S1: Issue 1件を`claimed` Runにする

**作るもの**

- 認証を持たない薄いread client。対象repositoryはpublicであり、S1〜S2のGitHub readは未認証で行う。write認証（branch push、PR作成が始まるS3の直前）で設計し、S4までにReview Worker側をread-only tokenへdownscopeする。未認証APIは60 req/hour（IP単位）のため、polling間隔は製品既定の15分とする（[github-routing.md](../05_design/04_github-routing.md)）。
- read client: Issue list、Issue get、repository content、base commit SHA。
- candidate filter。`GET /repos/{o}/{r}/issues?state=open&assignee=<login>&labels=ai-ready&per_page=100`で3条件をquery parameterで満たし、non-PRは`pull_request` keyの有無で判定する。4条件とも落とさない。
- `ReconcileIssue(repositoryRef, issueNumber, Trigger)`。pollerはIssueRefを流すだけの薄いproducerにする。
- claim use case。`contract.Compile(body, IssueRef)`が[Issue Contract](../05_design/contracts/issue-contract-v1alpha1.md)のclaim手順1〜2を完全に埋める。adapter側にparseを書かない。
- authority referenceの解決。`GET /repos/{o}/{r}/contents/{path}?ref=<baseSha>`でcontentを取り、`contract.SHA256(bytes)`を`contentDigest`にする。
- `readiness: ready`のgate。`Compile`はdraft / blockedのIssueも成功して返すため、claim use caseが`req.Readiness == contract.ReadinessReady`を明示的に書く。

**意図的に雑にするもの**

- GitHub native sub-issue / dependency relationshipとContract blockの照合をしない（[Issue Contract](../05_design/contracts/issue-contract-v1alpha1.md)は「adapterが取得できる場合」の条件付き要求であり、取得しない構成は契約違反にならない）。
- dependency completionの証明をしない。省略の仕方は一つだけで、**`dependsOn`が非空ならclaimせず`waiting_dependency`を返す**。`ai-ready`を消費せず`needs_human`にもせず、pollingで再評価させる。
- claim中の再取得（契約claim手順7）をしない。「1回fetch → 全部そこから解決 → commit直前に再readしてbody digest比較」の形だけ保つ。
- webhookを作らない。signature検証、payload size limit、delivery inboxはS3以降。
- Operation queue、lease、heartbeat、reaper（#14）を作らない。claim後の`DispatchOperation`はS2でinline実行し、queueは後から包む。
- artifact bytesを保存しない（refs-only）。ただし`ArtifactWriter` interfaceの呼び出し口だけS1で確定させる。

#### S2: `author_tests`でRED evidenceを固定する

**作るもの**

- content-addressed Artifact Store。`objects/sha256/aa/bb/<hex>`、同一filesystemの`<root>/tmp`経由でfile fsync → link/rename → 親dir fsync、read時のdigest再検証、missingとcorruptの別error。
- Run workspace。Run専用clone → `baseSha` checkout → Run専用branch → provider実行 → checkpoint commit → source-bundle化。worktree共有ではなくRunごとの独立cloneにする。
- child process supervisor。timeout、process group kill、exit / timeout / invalid outputの分類、環境変数allowlist、bounded output capture。
- provider adapter（codexとclaudeのどちらか一方で足りる）。schema非依存のinterfaceにし、`OutputSchema []byte`を受けて`FinalMessage []byte`と実行evidenceを返す。
- RED evidence artifact（canonical YAML）。`runs[]`、各runのargv、`workingDir`、`exited|signaled|timed_out`と`exitCode`/`signal`、stdout/stderrの`(inline, truncated, fullDigest, fullLength)`、environment identity、`headSha`、source-bundle digest。観測時刻と実行時間はcanonical contentに入れない。

**意図的に雑にするもの**

- orphan scan、GC、参照カウント、delete / overwrite API、圧縮・pack、artifact metadata table、作成時刻index。
- secret redactionは`func([]byte) []byte`のseamだけ用意し、初版は環境変数由来の値の走査に留める。
- `toolPermissions`はroleごとに1組をhard-codeし、それ以外の値をrejectする。
- provider CLIのJSONL eventを細かくparseしない。structured outputには「計画と主張」だけを載せ、test patchをJSONに埋め込ませない（コード変更はworktreeからgitで取る）。
- MCP設定、rate limit専用backoff、adapter versionの自動検出（version照合はする）。

#### S3: draft PRを1本publishする

**作るもの**

- `publish_head` Operation。branch pushとPR ensureの冪等な組。
- `pull-request-observation` artifactの固定。
- `ai-in-progress`のstatus outbox consumer。

**意図的に雑にするもの**

- PR body生成はTask Issue link、Run ID、phaseだけの最小形。test plan要約の決定論的生成はS4以降。
- 外部干渉（人間push、close、base変更）のreconciliationは検出だけにし、復旧経路はS4以降。
- 4 label lifecycleのうちS3で投影するのは`ai-in-progress`だけ。

#### S4 / S5

S3の実測結果を見てから内訳を確定する。それまで該当Taskはmilestone Epicに置く。

### 貫通でも落とさないもの

「後から入れられない」ではなく、**後から入れると過去に作ったevidenceの意味が変わる／欠落を後から観測できない**ものを落とさない。

#### provider session isolation

| 項目 | 後から入れると何が壊れるか |
| --- | --- |
| project doc auto-discoveryの無効化（codex `-c project_doc_max_bytes=0`、claude `--safe-mode`）。3 flagは効果が別物なので束ねず目的ごとに分けて書く | 対象repository自身の`AGENTS.md` / `CLAUDE.md`等がsessionへ黙って入る（実測: codex +8,897 token、claude cache_creation 3,769→75,263 token）。testでは検出できない。Context Manifestのdigest固定の外から未pin版が二重注入され、Operation digestと「sessionが実際に見た入力」の対応が崩れる |
| 環境変数のallowlistとmodelのCLI flag明示 | `os.Environ()`をそのまま渡すと、host側の`ANTHROPIC_MODEL`等がExecution Policyの`model`を黙って上書きし、policy digestが実行実態を表さなくなる |
| operation-scoped state directory（`CODEX_HOME` / `CLAUDE_CONFIG_DIR`をOperationごとのtemp dirへ） | cwdをキーにしたprovider側memoryがOperationをまたぐconversational carryover経路になる。credentialも同じdirectoryにあるため、seedはcredential fileのみをallowlistする |
| provider interfaceのschema非依存性 | `contract.TaskContext`を引数に取ると、schema versionごとにadapterが分岐し、旧schema併存要求（[Task Context Protocol](../05_design/contracts/task-context-v1alpha1.md)）の下で分岐が爆発する |
| `adapterVersion`と実CLIの起動時照合 | config定数のまま放置すると、host開発時とworker imageで値が食い違ったままevidenceに載る |

#### content identityとdurability

| 項目 | 後から入れると何が壊れるか |
| --- | --- |
| checkpoint commitのidentity固定（author/committer name・emailと`GIT_AUTHOR_DATE` / `GIT_COMMITTER_DATE`） | 既定に任せるとhead SHAがwall clockとhost git configに依存する。head SHAはOperation Result、Review Request binding、PR observationへ焼き込まれるため、規則を後から変えると過去RunのSHAが再現しない |
| Artifact Store layoutのalgorithm segmentと2段fanout | named volumeはupgradeを跨いで残るdurable formatであり、後から変えると既存objectの移行が要る |
| durability手順（同一FSのtemp、fileと親dirのfsync、write-onceを保つlink、EEXISTは既存検証で成功収束、read時digest再検証、missing/corruptの別error） | bytes消失・破損上書き・誤bytesでのapproveを後から検出も修復もできない |
| store APIをstreaming + store測定descriptorにすること | `Data []byte`で型付けすると必須3本がkind未登録で弾かれ、source-bundle（数十MB）が全量on-memoryになる。API形状は最も広く波及するretrofit |
| RED evidenceに`headSha`を含めること | 含めないと同一bytesのevidenceが別headのArtifact Manifestへ載り、stalenessをdigest比較で検出できない |
| RED evidenceに未切り詰めstdout/stderrのdigestとlengthを含めること | inlineだけにすると、truncation上限の変更で同一実行のevidence identityが変わり、reviewerが「全部を見たか」を判定できない |
| RED evidenceの`runs[]`複数化と`exited` / `signaled` / `timed_out`の区別 | 「1 name = 1 command」は複数commandを後から表現できずschema bumpになる。exit codeだけではtimeoutとtest failureを区別できず[test-validity policy](../05_design/review-policies/test-validity-v1alpha1.md) §5を満たせない |
| source-bundleをgit bundleにすること | tarはcommit objectを含まず`headSha`を再構築・検証できない。違反はReview Workerのhead検証まで表面化しない |
| GitHub bodyを正規化しないこと | `bodyDigest`は永続化されるaudit lineageであり、正規化方針を後から変えると過去のdigestが再現しない |
| base commit SHAの固定とauthority pathのそのSHAでの解決 | baseと別時点でrefを解決するとmanifestが「base上のclosure」でなくなり、identityの意味が静かに変わる |

#### 境界の位置

| 項目 | 後から入れると何が壊れるか |
| --- | --- |
| role次元のcredential分離（role-scoped clientをconstructor注入。package-levelのsingletonを作らない） | singletonにするとReview Workerのread-only化が全call site変更になる。PATは「dev / test専用のTokenSource実装」としてだけ持つ |
| transport failureの分類点の1箇所集約 | 403はpermission不足でもsecondary rate limitでも返る。分類点が散ると全call siteの修正になる |
| `ReconcileIssue`のresult enumを6値（`claimed` / `waiting_dependency` / `waiting_capacity` / `skipped_not_candidate` / `claim_rejected` / `failed_transport`）で最初から閉じること | 6値それぞれでlabelの扱いが違う。部分enumへの投影は、値を足したとき既定分岐が誤ったlabel操作をする |
| `ReconcileIssue`を唯一の入口にすること | M3のexit criterion「webhookを捨ててもpollingが同じRunを作る」は両者が同じ関数へ収束していないとtestできない。`Trigger`はclosed type（poll / startup / webhook delivery）にし、observabilityとdedupに限る |
| `ProjectStatus`のoutbox化（Run transitionと同一transaction） | commit後のinline label APIは、label失敗をclaim失敗として返す誘因になる。retrofitはtransaction境界の変更になる |
| pagination（Link header）を実装するか、黙って打ち切らないこと | 黙ったtruncationは「Issueが永遠にclaimされない」観測不能なfail-openになる |
| `pull_request` fieldによるPR除外 | issues list endpointはPRも返す。落とすと人間のPRへ`ai-needs-human`を投影する |
| authority referenceの解決をS1で省略しないこと | routingのresult taxonomyに「未実装」を表す値が無く、唯一の受け皿`claim_rejected`は誤ったlabelを貼る。実装コストはendpoint 1本 |
| Artifact Store packageをControllerからimportさせないこと | Controllerはartifacts volumeをmountしない（[runtime-platform.md](../05_design/03_runtime-platform.md)）。read APIを呼べる形は「hostでは通るがCompose上は必ず失敗する依存」を作る。interfaceは利用側（issueworker / reviewworker）に置く |
| storeのkeyをdigestのみにすること | Run scopeやlogical nameで引かせると、content identityの一意性とvolume境界が同時に崩れる |
| Runごとの独立clone | linked worktreeはobject DBとrefをrepository単位で共有し、複数Run並行時のmutableな共有資源になる。worktree共有はdisk最適化であり、測ってから入れる |
| claim use caseをController側packageに置かないこと | claimは**Issue WorkerのOperation**である。`ReconcileIssue`の内側へ書くとartifact書き込み口がController側に生え、volume契約に反する。`ReconcileIssue`は薄いrouterに留め、package境界は#14が入る前に確定させる |
| `ArtifactWriter`（利用側package所有）の呼び出し口をS1で確定させること | retrofitが難しいのは保存先ではなく呼び出し口の位置である。bytesをPostgreSQLへ一時退避する近道は取らない |

### 貫通で必ず踏むcontract空白

contract feature freeze（Delivery rules）の例外である。踏んだ実装PRの中で、文書・parser・fixture・testを同時に更新する。

1. **`test-plan` / `red-evidence` / `source-bundle`が`artifactKindRules`に無い。** `requiredOperationOutputs[author_tests]`はこの3本を要求するが、`ArtifactPayload.Validate()`は`protocol_kind_unknown`で弾く。S2の実装前に「opaque kindとして追加する」か「Artifact Storeが`ArtifactPayload`を経由しない別経路を持つ」かを決める。
2. **checkpoint commitのidentity規則が[contracts/](../05_design/contracts/)に無い。** [Operation Protocol](../05_design/contracts/operation-protocol-v1alpha1.md)の「同じ入力から同じ結果を再生成したattemptは同じcontent identityを持つ」がhead SHA経由で壊れないよう、S2の実装PRと同じchangeで文書化する。

### 後回しにするものと後から足せる根拠

| 後回しにするもの | 後から足せる根拠 |
| --- | --- |
| webhook（raw body signature検証、payload size limit、delivery inbox） | `ReconcileIssue`が唯一の入口なら、webhook handlerは同じ関数を呼ぶproducerを1本足すだけ。healthz用HTTP serverを先に建てる場合、bodyを消費するJSON middlewareを挟まない（署名はraw body検証） |
| native relationship照合 | 契約上「adapterが取得できる場合」の条件付き要求。後入れはread 1本と比較 |
| dependency completion証明 | `waiting_dependency`で契約準拠のdegradationになっており、証明機構ができたら分岐を置き換えるだけ |
| claim中の再取得（契約手順7） | model-bearing Operationの直前検査であり、そのOperationがまだ無い |
| Operation queue、lease、heartbeat、reaper（#14） | envelopeが「GitHubを触る前にpolicyとRun IDが決まっている」順序を強制しており、queueは後から包める |
| Artifact Storeのorphan scan / GC / 参照カウント | append-onlyな既存objectを読むだけのread-only reportであり、書かれたbytesの意味を変えない |
| worktree共有によるdisk節約 | 独立cloneが正しい側の選択。共有は測ってから入れる最適化 |
| remote pushとPR mutation（S3まで） | `author_tests`必須outputにbranch pushもPRも含まれない。`publish_head`は別Operation |
| secret redactionの網羅性、graceful shutdownのlease drain、MCP設定、rate limit専用backoff | M4 exit criteriaに含まれない。seamがあれば差し替えで足りる |
| production Compose topology（M7） | S1〜S5はhostとM0のdevelopment Composeで成立する。ただしIssue Worker imageはgit + provider CLI + credential mountを抱える別base imageになり、この差分がM7の実体である |
| Issue WorkerとReview Workerのprocess分離 | mutable worktree、provider session、conversational memory、application-private stateを共有しなければ、process分離は配備の問題になる |

### 貫通の未決事項

- **S4 / S5の内訳**: S3の実測結果を見てから確定する。
- **status outbox producerの位置**: S1で「outbox producerだけ先に作る」か「inline実行で穴（commitとlabel投影の間のcrashで自己修復経路が無い）を明示的に受け入れる」かをS1着手前に決める。producerはtransitionと同一transactionのINSERT 1本であり、consumer（S3）やlease / heartbeat（#14）とは分離できる。
- **operation-scoped state directoryへのcredential供給（S2の実行前提）**: `CODEX_HOME` / `CLAUDE_CONFIG_DIR`を空のtemp dirへ向けると両CLIとも未認証になる。**credential fileのみをallowlistしてseedする**方向で決着させる（global project docの巻き込みは二重注入の復活になる）。
- **`artifact_refs`の用途**: semantic input refの受け皿に限るか、artifact metadata一般へ広げるかをS2の前に決める。広げる場合にのみschema制約の扱いが論点になる。
- **artifactのmissing / corruptに対応する`FailureClass`**: 現行6値で閉じており、`AttemptFailure.TerminalOutcome`は`Validate()`を通すため流用もできない。(a) failure recordを作らず`needs_human`へescalate、(b) freeze例外としてclassを1つ足す、の二択をS2の前に決める。
- **`ArtifactEntry`の長さ0チェック**: 長さ0の`red-evidence`もmanifest validationを通る。protocol層・store側Put・handler検査のどこで弾くかを決めていない。
- **authority contentをmanifest entryとして載せる場合のlogical name規則**: `validArtifactName`は大文字を許さず、`AGENTS.md`をそのままnameにできない。命名規則を場当たりで決めない。
- **RED evidenceのversioned schema化**: S3までopaque bytesで実害は無いが、S4へ広げる前にschemaを決める。決めた時点で過去のevidenceは別identityになる。

### Epic構成

作業単位はEpicであり、**GitHubのEpicは貫通sliceに対応させる**（2026-08-21）。milestone Epicは「完成の定義」と未達台帳として残すため、Epicとmilestoneは1対1ではない。milestoneの進捗は上の未達表を正とする。

| Epic | 役割 | 配下 |
| --- | --- | --- |
| [S1](https://github.com/mrbaron3/kudo/issues/63) | 貫通の実行単位 | #16、#17 |
| [S2](https://github.com/mrbaron3/kudo/issues/64) | 貫通の実行単位 | #20、#21、#22、#24 |
| [S3](https://github.com/mrbaron3/kudo/issues/65) | 貫通の実行単位（本ADRの主目的） | #29 |
| [M2](https://github.com/mrbaron3/kudo/issues/2) | 未達台帳 | #14、#15、#44 |
| [M3](https://github.com/mrbaron3/kudo/issues/3) | 未達台帳 | #18、#19、#59 |
| [M4](https://github.com/mrbaron3/kudo/issues/4) | 未達台帳 | #23 |
| [M5](https://github.com/mrbaron3/kudo/issues/5) | 未達台帳／S4予定 | #25、#26 |
| [M6](https://github.com/mrbaron3/kudo/issues/6) | 未達台帳／S5予定 | #27、#28、#53、#60、#62 |
| [M7](https://github.com/mrbaron3/kudo/issues/36)、[M8](https://github.com/mrbaron3/kudo/issues/8) | 貫通の影響を受けない | 変更なし |

S4とS5のEpicは作らない。S4 / S5の内訳はS3の実測結果を見てから確定するため、S3到達後に切る。それまで該当Taskはmilestone Epicに置く。

Task Issueは必ず1つのEpicに属する。Epic所属は実行順序を作らない——依存のgateは[Issue Contract](../05_design/contracts/issue-contract-v1alpha1.md)のとおり`dependsOn`だけである。

## Delivery rules

- protocol、parser、fixture、test を同じ change で更新する。
- pure transition、fake boundary、targeted test を先に実装し、network/process/container test を後から追加する。
- PostgreSQL、GitHub、process、clock、filesystem、provider、telemetry は interface と deterministic fake を持つ。
- transport/execution failure と quality verdict を別 type として保つ。
- model-bearing Operation は常に fresh session factory を通す。
- 一つの milestone の temporary shortcut を target architecture として文書化しない。貫通slice中に意図して雑にしたものは実装PRと[Evaluation harness — deferred](04_evaluation-harness.md)へ記録し、`architecture.md`や`contracts/`へは書かない。
- `internal/contract`はfeature freezeする（根拠は Delivery order の「この順序にした理由」）。変更は「貫通で実際に詰まった箇所」だけを理由に行い、網羅性や対称性を理由に追加しない。
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

実行順序はS1が先行し、webhookとlabel lifecycleの残りは幅を戻す段で満たす。

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

実行順序はS2が先行する。provider session isolation、Artifact Store のlayout / durability / streaming API、checkpoint commit identity は後入れできないため、最小形でも落とさない。

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

実行順序はS2（RED evidence）、S3（draft PR publish）、S4（test validity review 1 round）に分割される。S3到達が貫通の主目的である。

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

実行順序はS5に対応する。

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
