# Implementation plan

## Target

本計画は bootstrap demo ではなく、[プロダクト設計](../01_product-design/README.md) の完成条件を満たす
issue-to-PR runtime（GitHubをSSOTとする単一processのstateless reconciler、
[ADR-0001](../../adr/0001-github-ssot-stateless-reconciler.md)）を完成させるための delivery order を
定義する。

各 milestone は外部 adapter を後回しにするだけの layer 実装ではなく、可能な範囲で一つの
recovery/idempotency behavior を end-to-end に証明する。fast deterministic test を先に実行し、live
GitHub/provider test は opt-in として最後に重ねる。

## Current status

現在repositoryに実装済みなのは、`kudo help`/`kudo version`のCLI bootstrap、Milestone 0のCompose開発
基盤（multi-stage Dockerfile、PostgreSQL 18.4付き開発用Compose、container内check/integration test
入口）、`kudo.issue/v1alpha1`のstrict parser、Issue Observation・canonical Task Context・Claim
Requirementsを作るpure Issue Compiler、Context Manifest・Execution Policyのcanonical core、Worker
Operation/Review protocolのcanonical identity・binding・semantic staleness判定、およびPostgreSQL
run store（`internal/adapter/postgres`）である。

このうち**PostgreSQL run storeと開発Composeのpostgres serviceは[ADR-0001](../../adr/0001-github-ssot-stateless-reconciler.md)により退役対象**であり、
reconcile coreの実装と入れ替えで削除する。target architectureは文書化されているが、次は未実装である。

- phase導出（観測 → phase → 次action）のpure core
- GitHub webhook/poller/reconciliation
- check run / comment / label のrecorderとmarker冪等化
- Run workspaceとchild process supervisor
- Codex/Claude provider adapter
- Issue Worker / Review Worker / Controller
- production single-container image（health/operations）

target document が完成形を定義していることと、code が完成していることを混同しない。

## Delivery order

**milestoneは「完成の定義」であって実行順序の単位ではない**（2026-08-19決定）。実行は貫通trackを
単位とし、各milestoneの貫通に必要な最小部分を横断して先に通し、残りを後から幅として戻す。

**Implementer worker（track A）とReviewer worker（track B）は並列で開発する**（2026-08-22決定）。
並列化を成立させるのはfixture PR seeder（[#71](https://github.com/mrbaron3/kudo/issues/71)）である。
claim形のPR（claim checkpoint付きbody、test-only head、evidence check run、test plan comment）を
合成し、ReviewerはImplementerの完成を待たずに実物と同形の入力で開発する。

| track | 到達点 | Epic | 対応するmilestone |
| --- | --- | --- | --- |
| F 共通基盤 | GitHub gateway、reconcile core、App identity、webhook / pollingが揃い、A / Bがこの上で並列に進める | [#63](https://github.com/mrbaron3/kudo/issues/63) | M2、M3 |
| A Implementer貫通 | live Issue 1件のclaim（branch CAS + claim checkpoint付きdraft PR）→ REDの固定 → publishとImplementer名義のevidence記録 | [#64](https://github.com/mrbaron3/kudo/issues/64) | M3、M4、M5 |
| B Reviewer貫通 | seederが合成したclaim形PRへの`test_validity` review 1 roundと、Reviewer名義のverdict / finding記録 | [#65](https://github.com/mrbaron3/kudo/issues/65) | M5 |
| I 統合貫通 | A×Bを結線したend-to-end 1 round（happy）と`request_changes`→`revise_tests`の1周 | [#69](https://github.com/mrbaron3/kudo/issues/69) | M5 |

AとBはFの上で並列に進み、Iで合流する。I以降のM6区間（`implement`〜merge）はIの実測を見て実行順を
確定する。人間の見えるdraft PRはtrack Aのclaim到達で現れる。

M0とM1は完了扱いのまま変わらない（M0のpostgres部分は退役に伴い縮小する）。M2はrun storeとして実装
されたが前提を失ったため、reconcile coreへ置き換える。M7とM8は貫通後、幅を戻し切ってから着手する。

各milestoneのexit criteriaは削らない。貫通の時点で未達のまま残るものは次のとおりであり、貫通が
通ったことをexit criteria達成と誤読しないため、この表を台帳として維持する。

| milestone | 貫通時点で未達のまま残るもの |
| --- | --- |
| M2 | phase導出の全域性（全観測組合せの網羅test）、attempt retry policyの全class |
| M3 | webhook、pagination網羅、4 label lifecycle、rate limit retry、`healthz` / `readyz` |
| M4 | secret redaction網羅、両provider adapter。**actor別App identityはtrack B着手までのReviewer分離を先行させ**、Coordinator分離と残りのdownscopeは幅で戻す |
| M5 | `revise_tests`、`needs_human` escalation / resumption、staleness全経路 |
| M6 | `repair_implementation`、test mutation detection、required checks統合、PR body validator |

### この順序にした理由

2026-08-19時点（`5815cdf`）で、非テストGo行数6,301のうち`internal/contract`が4,579行（73%）を占める
一方、GitHub adapter、provider adapter、Issue Worker、Review Worker、Controllerはいずれも未実装で
あり、**Kudoは自身のIssueを1件もPull Requestにしたことがなかった**。versioned protocolはv1alpha1と
して完成度高く作り込まれていたが、製品境界そのものはまだ動く可能性があった。層ごとに横へ厚く作る
順序が、「target documentの完成」と「codeの完成」の混同を構造的に許していた。

contract層を作ったこと自体は誤りではない。canonical encoding、digest規則、identity binding、
staleness判定は、後から入れると過去のpayloadとdigestが再現しなくなる種類の設計である。結論は
「**contract層はもう十分であり、これ以上磨かず動くものへ接続すべき**」の一点であり、これがcontract
feature freeze（Delivery rules）の根拠でもある。

**track Aのclaim到達（人間の見えるdraft PR）をもって、製品境界の疑義を実物に対して再評価する**ことが
この順序の主目的である。統合（I）以降は貫通後の話であり、F / A / Bの結果によって順序を組み直してよい。

決定に際して次を検討し、退けた。

- **層ごとのmilestone順（従来）**: 「Issueが1件もPRになっていない」という最大のリスクを解消する
  時点が最も遅い。文書とコードの完成の混同を構造的に許し続ける。
- **contract層の追加整備を先に完了させる**: 動きうる製品境界に対して、外部consumerを持たないalpha
  protocolを磨き続けることになる。貫通で「実際に詰まった箇所」を根拠に変更するほうが精度が高い。
- **claim→RED→reviewを1本の直列sliceとして通す**: Reviewer側の開発がImplementerの完成待ちになり、
  2つのworkerの開発が直列化する。fixture seederで入力を合成すれば、reviewerは実物と同形の入力で
  独立に開発できる。
- **F / A / B / Iを1つの大きな貫通として一気に通す**: 途中で製品境界を再評価する機会を失う。統合以降の
  内訳は実測を見てから決めるほうが手戻りが小さい。

引き継がれるのは実装コードとrecord surface上のpayloadであってRun instanceではない。provider adapter
実装でExecution Policy digestが変わると既存Runは`SemanticInputChanged`でsupersedeされ、再claimから
始まる。貫通で作るRunは捨てる前提であり、最初のRunが「本物の履歴」にならないことは受け入れる。

**この順序の最大のリスクは、milestone exit criteriaの一部が長期間未達のまま残ることである。** 上の
未達台帳の追跡が形骸化すると「動いたから完成」という誤読が起きる。あわせて層ごとの品質が非対称になる
（contract層はテスト充実、adapter層は薄いtestで開始）ため、reviewで「同じ厚さ」を期待しない合意が要る。

### この順序を見直す条件

次のいずれかが成立した場合、delivery orderを再検討する。

- track Aのclaimへ到達しても製品境界の疑義が解消しない。この場合、貫通の対象そのものを再定義する
  必要がある。
- F / A / Bの貫通が到達できず、原因が**contract層の不足**であると判明した。この場合はcontract feature
  freezeを解く。
- 並列開発の前提が崩れた——fixture seederの合成物と実Implementerの成果物が統合（I)で意味的に食い違い、
  Reviewer側の作り直しが並列化の節約を上回った。
- 貫通の過程で**使われないcontractが体系的に見つかった**。alphaで外部consumerを持たないため削除は
  可能であり、何を削るかを別途決める。
- provider CLIのheadless契約（structured output、project doc無効化flag、state directory env）が上流
  変更で壊れ、session isolationがCLI flagでは実現できなくなった。
- 幅を戻す段で、exit criteria未達台帳が追跡不能な規模へ膨らんだ。sliceの薄さが過剰であったことを
  意味する。

### 各trackの範囲

trackごとに「何を作るか」と「何を意図的に雑にするか」の両方を明示する。暗黙の手抜きを作らない。雑に
したものは実装PRの記述と、必要なら[Evaluation harness — deferred](04_evaluation-harness.md)へ記録し、
`05_design/01_architecture.md`や`05_design/contracts/`へは書かない。作業単位の実体はEpic
（[#63](https://github.com/mrbaron3/kudo/issues/63) / [#64](https://github.com/mrbaron3/kudo/issues/64) /
[#65](https://github.com/mrbaron3/kudo/issues/65) / [#69](https://github.com/mrbaron3/kudo/issues/69)）
配下のTask Issueであり、本節と食い違う場合は本節を先に直す。

#### F: 共通基盤（gateway・reconcile core・identity）

**作るもの**

- GitHub gateway（#16）: observer（観測snapshotの組み立て）、recorder（check run / comment / label、
  marker検索付き冪等記録）、marker / machine block形式のencode / parseとgolden fixture、transport
  failure分類の一点集約、pagination、rate limit handling。単一実装・actorごとのcapability instance。
- reconcile core（#70）: 観測model、Derived phases表のpure function化、未知の観測組合せを
  `needs_human`へ倒すfail-closed既定、round counter導出（Reviewer名義finding commentの計数）、
  in-process dispatchの単一flight排他、retry class / backoff / clock injection。
- GitHub認証（#59）: actorごとのApp identityとinstallation token発行。S字の立ち上がりはowner PATの
  単一identity（dev / test専用TokenSource）で開始してよく、track B着手までにImplementer / Reviewerの
  App分離を導入する。
- webhook（#18）とpolling / label lifecycle（#19）。webhookは貫通の必須経路ではなく、pollingだけで
  A / Bを開始できる。

**意図的に雑にするもの**

- claimはFに置かない（Aの#17）。gatewayは観測と記録のprimitiveまでを所有する。
- Coordinator identityの分離は推奨に留め、Implementer / Reviewer分離を先行させる。

#### A: Implementer worker貫通（claim→RED→publish / attest）

**作るもの**

- claim Operation（#17）: candidate filter（open / non-PR / assignee / `ai-ready`、`pull_request`
  keyでのPR除外）、`contract.Compile`による strict parse、authority解決（base SHA固定 +
  `contentDigest`）、`readiness: ready`のgate、branch `kudo/issue-<n>`のref create（CAS）、bootstrap
  commit、claim checkpoint（machine block）付きdraft PRのensure。成功Resultを受けたControllerが
  `ai-in-progress`を投影する。
- Run workspaceとchild process supervisor（#21）: Run専用clone、checkpoint commitのidentity固定、
  timeout / process group / bounded capture / 環境変数allowlist。
- fresh session runtimeとCodex adapter（#22）: operation-scoped state directory、project doc
  auto-discovery無効化、credential fileのみseed、schema非依存interface。
- `author_tests`とRED evidence（#24）: canonical YAML payload（`runs[]`、`headSha`、未切詰
  stdout/stderrのdigest / length）。
- `publish_head`とattest（#29）: compare-and-push、Implementer名義の`kudo/evidence-red` check runと
  test plan marker commentの記録。

**意図的に雑にするもの**

- native sub-issue / dependency relationshipとContract blockの照合をしない。
- dependency completionの証明をしない。`dependsOn`が非空なら`waiting_dependency`を返す。
- claim中の再取得（契約claim手順7）をしない。「1回fetch → 全部そこから解決 → mutation直前のbody
  digest比較」の形だけ保つ。
- provider adapterはcodexだけで足りる（claude #23は幅で戻す）。
- secret redactionはseamのみ。`toolPermissions`はroleごとに1組をhard-code。provider CLIのJSONL
  eventを細かくparseしない。MCP設定、rate limit専用backoff、adapter versionの自動検出をしない。

#### B: Reviewer worker貫通（test_validity 1 round）

**作るもの**

- fixture PR seeder（#71）: claim形PRをfake GitHub fixtureとopt-in live fixture repositoryの両方に
  合成するdev harness。record surface形式は#16のencode / parseを共用し、独自形式を作らない。
- `test_validity` Review Worker（#25）: protocol validation → live freshness照合 → read-only
  checkout → deterministic prerequisites → fresh session → Result構築とreport（Reviewer名義の
  `kudo/test-validity` check runとfinding comment）。required inputs（`test-plan`、`red-evidence`）の
  digest照合。

**意図的に雑にするもの**

- model providerはfakeで進める。live provider統合と実Implementer成果物への結線は統合（I）。
- `request_changes`後の`revise_tests` loopは統合（I、#26）。Bは1 roundのverdict記録まで。

#### I: 統合貫通（A×Bの結線）

**作るもの**

- Controllerのdispatch結線（#72）: 実repositoryでIssue → claim → `author_tests` → publish / attest →
  Review Request組み立て → Reviewer実行 → verdict記録 → phase導出が次actionを返すまで。live
  providerを両workerで使う最初の統合。
- `revise_tests`とneeds-human再開loop（#26）: round予算消費、ResumeIdentity、escalation commentと
  ledger、`ai-ready`再付与からのresume / supersede。

M6区間（#27 `implement`以降）の実行順は、Iの実測を見てから確定する。
### 貫通でも落とさないもの

「後から入れられない」ではなく、**後から入れると過去に作ったevidenceの意味が変わる／欠落を後から
観測できない**ものを落とさない。

#### provider session isolation

| 項目 | 後から入れると何が壊れるか |
| --- | --- |
| project doc auto-discoveryの無効化（codex `-c project_doc_max_bytes=0`、claude `--safe-mode`）。3 flagは効果が別物なので束ねず目的ごとに分けて書く | 対象repository自身の`AGENTS.md` / `CLAUDE.md`等がsessionへ黙って入る（実測: codex +8,897 token、claude cache_creation 3,769→75,263 token）。testでは検出できない。Context Manifestのdigest固定の外から未pin版が二重注入され、Operation digestと「sessionが実際に見た入力」の対応が崩れる |
| 環境変数のallowlistとmodelのCLI flag明示 | `os.Environ()`をそのまま渡すと、host側の`ANTHROPIC_MODEL`等がExecution Policyの`model`を黙って上書きし、policy digestが実行実態を表さなくなる |
| operation-scoped state directory（`CODEX_HOME` / `CLAUDE_CONFIG_DIR`をOperationごとのtemp dirへ） | cwdをキーにしたprovider側memoryがOperationをまたぐconversational carryover経路になる。credentialも同じdirectoryにあるため、seedはcredential fileのみをallowlistする |
| provider interfaceのschema非依存性 | `contract.TaskContext`を引数に取ると、schema versionごとにadapterが分岐し、旧schema併存要求（[Task Context Protocol](../05_design/contracts/task-context-v1alpha1.md)）の下で分岐が爆発する |
| `adapterVersion`と実CLIの起動時照合 | config定数のまま放置すると、host開発時とproduction imageで値が食い違ったままevidenceに載る |

#### content identityとdurability

| 項目 | 後から入れると何が壊れるか |
| --- | --- |
| checkpoint commitのidentity固定（author/committer name・emailと`GIT_AUTHOR_DATE` / `GIT_COMMITTER_DATE`） | 既定に任せるとhead SHAがwall clockとhost git configに依存する。head SHAはOperation Result、Review Request binding、check runへ焼き込まれるため、規則を後から変えると過去RunのSHAが再現しない |
| RED evidenceに`headSha`を含めること | 含めないと同一bytesのevidenceが別headへ流用可能になり、stalenessをdigest比較で検出できない |
| RED evidenceに未切り詰めstdout/stderrのdigestとlengthを含めること | inlineだけにすると、truncation上限の変更で同一実行のevidence identityが変わり、reviewerが「全部を見たか」を判定できない |
| RED evidenceの`runs[]`複数化と`exited` / `signaled` / `timed_out`の区別 | 「1 name = 1 command」は複数commandを後から表現できずschema bumpになる。exit codeだけではtimeoutとtest failureを区別できず[test-validity policy](../05_design/review-policies/test-validity-v1alpha1.md) §5を満たせない |
| 記録前のpayload size制約（record surfaceの64KiB上限に収まるcanonical構成） | 記録後のtruncationはdigestとbytesの対応を壊す。上限超過はprotocol境界で弾く形を最初から取る |
| GitHub bodyを正規化しないこと | `bodyDigest`は監査に使うidentityであり、正規化方針を後から変えると過去のdigestが再現しない |
| base commit SHAの固定とauthority pathのそのSHAでの解決 | baseと別時点でrefを解決するとmanifestが「base上のclosure」でなくなり、identityの意味が静かに変わる |
| marker形式の前方互換（kind、round、head、digestを含む機械可読block） | markerは冪等性とround導出の基盤としてPR上に永続する。形式を後から変えると過去Runの導出が壊れるか、複数形式のparserを恒久的に持つことになる |

#### 境界の位置

| 項目 | 後から入れると何が壊れるか |
| --- | --- |
| role次元のcredential分離（role-scoped clientをconstructor注入。package-levelのsingletonを作らない） | singletonにするとReview Workerのread-only化が全call site変更になる。PATは「dev / test専用のTokenSource実装」としてだけ持つ |
| transport failureの分類点の1箇所集約 | 403はpermission不足でもsecondary rate limitでも返る。分類点が散ると全call siteの修正になる |
| `ReconcileIssue`のresult enumを6値（`claimed` / `waiting_dependency` / `waiting_capacity` / `skipped_not_candidate` / `claim_rejected` / `failed_transport`）で最初から閉じること | 6値それぞれでlabelの扱いが違う。部分enumへの投影は、値を足したとき既定分岐が誤ったlabel操作をする |
| `ReconcileIssue`を唯一の入口にすること | M3のexit criterion「webhookを捨ててもpollingが同じRunを作る」は両者が同じ関数へ収束していないとtestできない。`Trigger`はclosed type（poll / startup / webhook delivery）にし、observabilityに限る |
| 記録（label / comment / check run）をmarker検索付きの冪等mutationにすること | inline実行のretryが重複commentや重複check runを作り、round導出とledgerが壊れる。marker規約は最初のcheck run / commentから守る |
| phase導出をpure functionにし、GitHub adapterから分離すること | 導出がadapterへ散ると全域性をtable-driven testで検証できず、観測組合せの穴がfail-openになる |
| pagination（Link header）を実装するか、黙って打ち切らないこと | 黙ったtruncationは「Issueが永遠にclaimされない」観測不能なfail-openになる |
| `pull_request` fieldによるPR除外 | issues list endpointはPRも返す。落とすと人間のPRへ`ai-needs-human`を投影する |
| authority referenceの解決をclaim（#17）で省略しないこと | routingのresult taxonomyに「未実装」を表す値が無く、唯一の受け皿`claim_rejected`は誤ったlabelを貼る。実装コストはendpoint 1本 |
| Runごとの独立clone | linked worktreeはobject DBとrefをrepository単位で共有し、複数Run並行時のmutableな共有資源になる。worktree共有はdisk最適化であり、測ってから入れる |
| claim use caseをController側packageに置かないこと | claimは**Issue WorkerのOperation**であり、branch / PR mutationを含む。`ReconcileIssue`の内側へ書くとwrite credentialがController側に生える。`ReconcileIssue`は薄いrouterに留める |

### 貫通で必ず踏むcontract空白

contract feature freeze（Delivery rules）の例外である。踏んだ実装PRの中で、文書・parser・fixture・
testを同時に更新する。

1. **`test-plan` / `red-evidence`のpayload kindが`artifactKindRules`に無い。** record surfaceへ記録する
   payloadのkind検証をどう通すかを#24の実装前に決める。
2. **checkpoint commitのidentity規則が[contracts/](../05_design/contracts/)に無い。** [Operation Protocol](../05_design/contracts/operation-protocol-v1alpha1.md)
   の「同じ入力から同じ結果を再生成したattemptは同じcontent identityを持つ」がhead SHA経由で壊れない
   よう、#21の実装PRと同じchangeで文書化する。
3. **marker / machine blockの具体format（#16で解消）。**
   [Operation Protocol](../05_design/contracts/operation-protocol-v1alpha1.md)のrecord surface envelopeとして
   確定し、claim checkpoint、evidence / verdict check runのoutput block、finding commentが同じ
   encoder / parserとgolden fixtureを使う。

### 後回しにするものと後から足せる根拠

| 後回しにするもの | 後から足せる根拠 |
| --- | --- |
| webhook（raw body signature検証、payload size limit） | `ReconcileIssue`が唯一の入口なら、webhook handlerは同じ関数を呼ぶproducerを1本足すだけ。healthz用HTTP serverを先に建てる場合、bodyを消費するJSON middlewareを挟まない（署名はraw body検証） |
| native relationship照合 | 契約上「adapterが取得できる場合」の条件付き要求。後入れはread 1本と比較 |
| dependency completion証明 | `waiting_dependency`で契約準拠のdegradationになっており、証明機構ができたら分岐を置き換えるだけ |
| claim中の再取得（契約手順7） | model-bearing Operationの直前検査であり、そのOperationがまだ無い |
| phase導出の全域表の網羅test | 導出がpure functionであれば、観測組合せのtable-driven testは後から拡充できる。未知の組合せを`needs_human`へ倒すfail-closed既定だけは最初から入れる |
| secret redactionの網羅性、graceful shutdown、MCP設定、rate limit専用backoff | M4 exit criteriaに含まれない。seamがあれば差し替えで足りる |
| production image hardening（M7） | 貫通trackはhostとM0のdevelopment imageで成立する。Issue Worker実行にはgit + provider CLI + credentialが要り、この差分がM7の実体である |

### 貫通の未決事項

- **M6区間（#27〜#62）の実行順**: 統合（I）の実測結果を見てから確定する。
- **operation-scoped state directoryへのcredential供給（track Aの実行前提）**: `CODEX_HOME` /
  `CLAUDE_CONFIG_DIR`を空のtemp dirへ向けると両CLIとも未認証になる。**credential fileのみをallowlist
  してseedする**方向で決着させる（global project docの巻き込みは二重注入の復活になる）。
- **record surface payloadのmissing / corruptに対応する`FailureClass`**: 現行6値で閉じている。(a)
  failure recordを作らず`needs_human`へescalate、(b) freeze例外としてclassを1つ足す、の二択をtrack A
  の#24着手前に決める。
- **`outputs`の長さ0チェック**: 長さ0の`red-evidence`もvalidationを通る。protocol層・recorder・
  handler検査のどこで弾くかを決めていない。
- **RED evidenceのversioned schema化**: 統合（I）までopaque bytesで実害は無いが、M6区間へ広げる前に
  schemaを決める。決めた時点で過去のevidenceは別identityになる。
- **run store退役の時期**: reconcile core（#70）とtrack Aのclaim（#17）が通った時点で
  `internal/adapter/postgres`と開発Composeのpostgres serviceを削除する。同一PRにするか分離するかは
  着手時に決める。

### Epic構成

作業単位はEpicであり、**GitHubのEpicは貫通trackに対応させる**（2026-08-22）。milestone Epicは
「完成の定義」と未達台帳として残すため、Epicとmilestoneは1対1ではない。milestoneの進捗は上の未達表を
正とする。

| Epic | 役割 | 配下 |
| --- | --- | --- |
| [F #63](https://github.com/mrbaron3/kudo/issues/63) | 共通基盤 | #16、#70、#59、#18、#19 |
| [A #64](https://github.com/mrbaron3/kudo/issues/64) | Implementer worker貫通 | #17、#21、#22、#24、#29 |
| [B #65](https://github.com/mrbaron3/kudo/issues/65) | Reviewer worker貫通 | #71、#25 |
| [I #69](https://github.com/mrbaron3/kudo/issues/69) | 統合貫通 | #72、#26 |
| [M2 #2](https://github.com/mrbaron3/kudo/issues/2)〜[M8 #8](https://github.com/mrbaron3/kudo/issues/8)、[M7 #36](https://github.com/mrbaron3/kudo/issues/36) | 未達台帳 | M4に#23、M6に#27・#28・#53・#60・#62、M7に#32・#37、M8に#33・#34・#35・#44 |

Task Issueは必ず1つのEpicに属する。Epic所属は実行順序を作らない——依存のgateは
[Issue Contract](../05_design/contracts/issue-contract-v1alpha1.md)のとおり`dependsOn`だけである。

## Delivery rules

- protocol、parser、fixture、test を同じ change で更新する。
- pure transition、fake boundary、targeted test を先に実装し、network/process/container test を後から追加する。
- GitHub、process、clock、filesystem、provider、telemetry は interface と deterministic fake を持つ。
- transport/execution failure と quality verdict を別 type として保つ。
- model-bearing Operation は常に fresh session factory を通す。
- 一つの milestone の temporary shortcut を target architecture として文書化しない。貫通slice中に意図
  して雑にしたものは実装PRと[Evaluation harness — deferred](04_evaluation-harness.md)へ記録し、
  `architecture.md`や`contracts/`へは書かない。
- `internal/contract`はfeature freezeする（根拠は Delivery order の「この順序にした理由」）。変更は
  「貫通で実際に詰まった箇所」だけを理由に行い、網羅性や対称性を理由に追加しない。
- Milestone 0以降の実装とtestは、host固有のdaemonではなくcontainer基盤で再現できる状態を維持する。
- 各 milestone の merge 前に`mise run check`を通す。

## Milestone 0 — Containerized development foundation

機能実装より先に、Go applicationを同じcontainer contractでbuild・testできる開発基盤を作る。

### Milestone 0 deliverables

- 現在の単一`kudo` binaryとtestをbuildできるreproducible multi-stage Dockerfile
- non-root development/test imageと`.dockerignore`
- container内で`mise run check`を実行する標準command
- Docker socket/Docker-in-Dockerを必要としないbuild/test path

（PostgreSQL関連のdeliverableは達成済みだが、[ADR-0001](../../adr/0001-github-ssot-stateless-reconciler.md)
により退役対象になった。）

### Milestone 0 exit criteria

- cleanなCompose-capable hostでimageをbuildできる。
- hostへGoやKudo daemonを直接installせず、container内で`mise run check`が成功する。
- image、configuration nameが後続のproduction imageへ拡張可能で、throwawayの別構成になっていない。
- macOS `linux/arm64`で検証し、`linux/amd64` buildを壊さないDockerfileになっている。

## Milestone 1 — Protocol core

IssueRef から Task の execution context と review identity を決定論的に構築できる pure core を作る。

### Milestone 1 deliverables

- `kudo.issue/v1alpha1`の fixed section と YAML block の strict parser
- unknown/duplicate field、不正 enum、欠落/重複 AC、曖昧 authority の validation
- Issue Observation、Task Context、Context Manifest の canonical encoding と SHA-256 identity
- claim checkpoint、Execution Policy / Escalation Policy snapshot、Operation envelope/resultの
  canonical identity
- Review Request / Result の validation と staleness rule
- claim/review/transport error taxonomy
- fixture corpus と canonicalization golden test

### Milestone 1 exit criteria

- 同じ input は常に同じ digest になり、whitespace と ordering rule が fixture で固定される。
- changed Context Manifest（Task Context または authority content の変化を含む）、Execution Policy、
  head SHA、input payload、policy ref が以前の review を stale にする。
- Issue Observation だけの変化は Operation identity と approval を stale にしない。
- malformed contract、human decision、transport failure、review finding が混同されない。
- GitHub/network/filesystem/provider なしで全 behavior を unit test できる。

## Milestone 2 — Reconcile core

観測からphaseを導出し、次actionを決めるpure coreと、marker / CASによる冪等化を実装する。crash後は
再観測だけで回復できるようにする。

### Milestone 2 deliverables

- Observation model（Issue、branch、PR、check run、comment、labelのtyped snapshot）
- phase導出のpure function（[workflow.md](../05_design/02_workflow.md)のDerived phases表）と、未知の
  観測組合せを`needs_human`へ倒すfail-closed既定
- 許可されたtransitionの検証と次actionの決定
- marker（kind、round、head、digest）のencode / parse / 検索契約
- round counterとattempt管理（round導出はmarker計数、attemptはprocess-local）
- retry class、backoff、jitter、clock injection
- in-processのdispatchと単一flight排他

### Milestone 2 exit criteria

- 同じ観測snapshotから常に同じphaseと次actionが導出される（table-driven test）。
- 中間状態（branchのみ、PRのみ、evidence欠落等）のすべてが「次action」か「escalation」のどちらかへ
  写像され、黙って進行しない。
- 記録のretryがmarkerにより重複を作らない。
- process再起動を模したtestで、同じ観測から同じ継続が再現される。
- dependency のない Run は並行に進み、repository global lock を使わない。

## Milestone 3 — GitHub discovery and claim

Webhook と必須 polling fallback を同じ`ReconcileIssue`へ接続し、実行可能な Issue を branch CAS で
claim する。

実行順序はtrack F / Aが先行し、webhookとlabel lifecycleの残りは幅を戻す段で満たす。

### Milestone 3 deliverables

- actorごとのGitHub App登録とinstallation token発行（Implementer / Reviewer分離必須、Coordinator分離推奨）
- `POST /webhooks/github`の raw-body signature verification、payload limit
- startup reconciliation と既定15分 polling、pagination、rate-limit handling
- candidate filter: open、non-PR、configured target assignee / ready label（既定`mrbaron3` / `ai-ready`）
- live Issue Reader、native relationship、dependency、repository content resolver
- claim時と各後続Operationでlive Issue/authorityを取得し、同じCompiler versionでTask Context /
  Context Manifest identityを再計算するcontext reconstruction handler
- branch `kudo/issue-<n>`のref create、bootstrap commit、draft PR ensure、claim checkpointのPR body記録
- ControllerがIssue / Review provider設定からimmutable Execution Policyを、attempt retry / review
  round設定からEscalation Policyを固定するresolver
- merged kudo PRの再claim防止（`skipped_already_merged`）
- 4 label lifecycleの記録
- `healthz`、`readyz`
- 後続roleも再利用するstructured logging contract / adapterと、webhook / reconciliation / claim の
  correlation field

### Milestone 3 exit criteria

- webhook を意図的に捨てても、polling が Issue を発見して同じ Run を作る。
- duplicate/遅延/順不同 webhook と poll overlap が二重 Run を作らない（branch CASで排他される）。
- candidate 外、dependency/capacity 待ち、contract rejection、transport failure が仕様どおり区別される。
- candidate のassignee / ready labelをconfigurationで上書きしても同じfilter ruleが適用される。
- claim checkpointがPR bodyへ記録されるまでclaim完了として扱わない。
- 後続Operationは開始時・完了時にlive contextを再構築し、意味的に同じなら継続、期待digestと異なれば
  staleになる。
- claim後にprocessを停止・再開しても、再観測が最終 label set と claim 状態を一貫させる。
- live GitHub test がなくても fake API で pagination、rate limit、mutation retry を検証できる。

## Milestone 4 — Workspace and process runtime

Worker が provider と repository command を安全に実行し、session 間をrecord surfaceのdigest検証済み
payload で handoff できる基盤を作る。

実行順序はtrack Aが先行する。provider session isolationとcheckpoint commit identityは後入れできないため、
最小形でも落とさない。

### Milestone 4 deliverables

- Run scoped clone/worktree/branch/checkpoint lifecycle（disposable、失われたら再構築）
- child process supervisor、process-group cancellation、timeout、bounded output、secret redaction
- fresh session factory と operation-scoped temp/config directory
- Codex headless adapter と Claude headless adapter
- Runに固定済みExecution Policyを各provider invocationへ適用するadapter boundary
- provider structured output schema と invalid response handling
- Issue/Review role ごとの credential policy

### Milestone 4 exit criteria

- model Operation を連続実行しても session ID、transcript、private state が再利用されない。
- timeout/crash後のattemptがclaim checkpoint、live GitHub/source、commit、record surfaceのevidence
  から再構築され、以前のprocessをresumeしない。
- workspace喪失後もbaseとpublished headから再構築できる。
- Review runtime は Issue workspace path を受け取らず、head SHA から別 checkout を作る。
- fake process/provider を使う deterministic test と、opt-in CLI smoke test の両方がある。

## Milestone 5 — RED and test review loop

Issue claim から test validity approval までの完全な TDD 前半を実装する。

実行順序はtrack A（RED evidenceとpublish）、track B（test validity review 1 round）、統合Iに分割される。

### Milestone 5 deliverables

- `author_tests`と`revise_tests` Issue Operation
- Acceptance Criteria と test plan/test case の traceability
- test-only checkpoint と RED command evidence（`kudo/evidence-red` check run）
- infrastructure failure と expected RED の classifier
- `publish_head`による compare-and-push
- `test_validity` Review Request/Result handlerと verdict check run / finding commentの記録
- `request_changes` finding の fresh revision session handoff
- `needs_human` comment と escalation/resumption

### Milestone 5 exit criteria

- expected failure の RED が固定され、head が push されて evidence check run が記録されるまで review
  request を作らない。
- reviewerはlive Issue/authorityを再compileしてTask Context / Context Manifest identityを、live PRで
  open/draft・head・baseを検証し、一致確認済みcanonical Task Context、digest検証済みevidence、
  read-only checkoutだけでverdictを返す。
- `request_changes`後は同じ worktree の新しい provider session が修正し、新しい request digest で再
  review する。
- test approval（verdict check run）なしに implementation Operation を dispatch できない。
- Task Context / Context Manifestを変える意味的なIssue edit、test head change、input payload change が
  approval を stale にする。Issue Observationだけの変化はapprovalを維持する。

## Milestone 6 — GREEN, refactor, final review, and PR

承認済み test から implementation を完成させ、承認済み head を merge して Task Issue を close する。

実行順序は統合（I）後のM6区間に対応し、実行順はIの実測後に確定する。

### Milestone 6 deliverables

- `implement`と`repair_implementation` Issue Operation
- GREEN、refactor 後 verification、repository required checks の evidence check run
- performance bound宣言時のTask固有command実行と`kudo/evidence-performance`
- test mutation detection、`test_revision_required`による rollback / 差し戻しと round 予算消費
- `final_implementation` Review Request/Result handler
- approved head binding と stale review prevention（新headにverdict check runが無いことによる構造的
  staleness）
- `finalize_pull_request`による required PR body 確定と draft 解除
- required PR body validator と `.github/pull_request_template.md` integration
- `merge_pull_request`による merge gate 評価、merge intent comment、SHA指定merge、head branch 削除
- 外部 close/merge との区別（intent commentの有無）
- Task Issue close と `ai-merged` の記録

### Milestone 6 exit criteria

- implementation は approved test validity digest を入力に持つ。
- refactor 後に同じ test/check を再実行し、evidence を最終 head の check run に bind する。
- performance bound宣言時は測定command、固定条件、環境identity、複数回実行の要約、bound比較を最終head
  へbindし、宣言がないTaskへ標準harnessを推測して要求しない。
- final`request_changes`は fresh repair session に渡り、head change 後に必ず再 review する。
- final approve verdict check run と required checks がない head では PR を ready 化できない。
- finalize / merge の開始時に live context を再構築し、final approve 後の Issue の意味的編集を stale
  として検出する。
- crash が publish/finalize/merge response の前後どちらで起きても PR は一つだけになり、merge は一度
  だけ成立し、Run は`merged`へ収束する。
- PR body が Issue、AC、RED/GREEN、二つの review、checks、risk、base/head を参照する。

## Milestone 7 — Production deployment and operations

Milestone 0のcontainer基盤を、完成したreconcilerを実行するproduction imageへ拡張し、
[Runtime platform](../05_design/03_runtime-platform.md)の運用contractを満たす。

### Milestone 7 deliverables

- provider CLI/toolchainを含むproduction image
- `linux/arm64` / `linux/amd64` production image buildとSBOM/provenance
- healthcheck、restart policy、resource limit、read-only root filesystem
- Compose secrets、GitHub App/provider credential setup
- versioned contractとrecord surface format（marker、machine block）の backward / forward
  compatibility policyとrelease boundary
- 導出phaseとrecord surfaceを診断し、期待stateとmarkerを確認してから安全にretryするoperator runbook
- graceful shutdown
- GHCR publish と pinned image update procedure

### Milestone 7 exit criteria

- clean host で documented setup から service が起動し、health/readiness が green になる。
- Milestone 0で確立したbuild/test commandとconfiguration contractがproduction imageでも維持される。
- host に Kudo daemon または provider GUI/session を必要としない。
- Review credential で write API を実行できない。
- application restart、process kill後のrecovery test（再観測からの収束）が通る。
- 全roleのlogがMilestone 3のstructured logging contractに従い、Run / Operation / attempt / IssueRefで
  相関できる。
- compatibility policyをfixtureで検証し、operator runbookのdiagnose / safe retryをdisposable環境で
  実行して二重OperationやGitHub mutationを作らない。
- Docker socket が mount されていない。
- pinned image と record surface format の互換境界が release note で追跡できる。

## Milestone 8 — Product acceptance and hardening

個別 component の完成ではなく、実運用に近い failure matrix で product completion を確認する。

### Milestone 8 deliverables

- Product completion criteriaと自動test / record surface / live verificationを対応付けるacceptance
  evidence matrix
- 下記failure matrixを決定論的に実行するheadless acceptance suite
- dedicated repository / sandbox credential、課金・外部mutation・cleanup境界を明示したopt-in live suite
- vendor / device boundaryに残るlive verificationと実行結果を記録するrelease checklist
- Milestone 7が所有するcompatibility policyとoperator diagnose / safe-retry runbookのacceptance scenario

### Automated acceptance matrix

- happy path: Issue -> claim（branch + draft PR）-> RED -> test approve -> GREEN/refactor -> final
  approve -> PR ready化 -> merge + branch削除 -> Issue close ->`ai-merged`
- merge gate: check pending の待機、check failure / conflict / protection 拒否の`merge_blocked`、
  merge 直前の head 変化
- test and final`request_changes`の複数 loop と round counter 導出
- `needs_human`、人間修正、`ai-ready`再付与、safe resume/supersede
- webhook loss、duplicate、reorder、invalid signature、poll overlap
- GitHub/provider の timeout、rate limit、temporary outage
- process kill 後の再観測からの収束
- record surface の tamper（comment編集・削除）検出と payload missing の escalation
- dependency graph、cycle、base 未統合、複数 independent Run
- PR/label/comment/check run mutation の ambiguous response と idempotent recovery

### Live verification

dedicated test repository と provider sandbox credential を使う opt-in suite で、GitHub webhook、
polling、branch/PR/check run、Codex/Claude CLI の実 boundary を検証する。課金、外部 mutation、
cleanup 対象を明示し、通常の`mise run check`には含めない。

headless test で同等の confidence が得られる部分は先に headless で検証する。GitHub delivery、
provider CLI lifecycle、macOS container runtime のような vendor boundary は fake だけを実機証明として
扱わず、残る live verification を release checklist に記録する。

### Milestone 8 exit criteria

- automated acceptance matrixの全scenarioがdeterministic suiteで成功し、failure注入後も一つのRun、
  Pull Request、statusへ収束する。
- product completion criteriaの各項目がtest result、record surface、runbook verification、または
  residual live verificationのいずれかへ一意に対応付く。
- opt-in live suiteがGitHub delivery / mutation、supported provider CLI lifecycle、reference macOS
  container runtimeの実boundaryを検証し、実行しない環境では残項目、理由、実行手順をrelease checklist
  へ記録する。
- compatibility fixtureとoperator runbook scenarioが成功し、直前のsupported releaseからの upgrade /
  recovery / safe retry boundaryを再現できる。
- live verificationの外部mutationと課金対象が記録され、cleanup後にtest repository、credential、
  record surfaceの残存状態を確認できる。

## Product-wide exit criteria

全 milestone 完了に加え、次が成立して初めて Kudo runtime を完成扱いにする。

- [プロダクト設計](../01_product-design/README.md) の完成条件を自動 evidence へ対応付けられる。
- [End-to-end workflow](../05_design/02_workflow.md) の全 transition、retry、escalation が実装されている。
- [Runtime platform](../05_design/03_runtime-platform.md) の deployment、security、recovery contract が
  検証されている。
- versioned contract と record surface format に backward/forward compatibility policy がある。
- operator が導出 phase と record surface を診断し、安全に retry できる runbook がある。
- live integration が opt-in でも、core correctness は deterministic tests だけで再現できる。
- merge/deploy、pass@k、multi-candidate evaluation を runtime completion と混同していない。
