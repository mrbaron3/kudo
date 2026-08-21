# Implementation–Review Protocol v1alpha1

## Purpose

Implementation と Review を、同じ repository と binary を利用しても、同じ mutable worktree、credential、model/provider session、conversation memory を共有しない独立 role として接続する。

Issue Worker は source、worktree、branch、commit、Pull Request mutation を所有する。Review Worker は immutable input と read-only checkout から verdict を作り、Implementation は自分の request を approve できない。Controller は request/result binding と state transition を検証するが、review verdict を上書きしない。

各 model-bearing Issue Operation と各 Review Request は fresh provider process/session で処理する。修正を同じ論理作業 lane へ差し戻す場合も、以前の session ID、resume token、conversation transcript、private memory を入力にしない。

## Review Request

Review Request は`kudo.review-request/v1alpha1`としてversion付けする。

```yaml
schema: kudo.review-request/v1alpha1
requestId: 01KUDOEXAMPLE
kind: test_validity
producerRunId: run-01
repository: github://owner/repository
issue: github://owner/repository/issues/42
pullRequest: github://owner/repository/pull/57
issueObservation:
  schema: kudo.issue-observation/v1alpha1
  digest: sha256:<digest>
pullRequestObservation:
  schema: kudo.pull-request-observation/v1alpha1
  digest: sha256:<digest>
headSha: <git-commit-sha>
contextManifest:
  schema: kudo.context-manifest/v1alpha1
  digest: sha256:<digest>
executionPolicy:
  schema: kudo.execution-policy/v1alpha1
  digest: sha256:<digest>
artifactManifest:
  schema: kudo.artifact-manifest/v1alpha1
  digest: sha256:<digest>
policyRefs:
  - docs/spec/05_design/review-policies/test-validity-v1alpha1.md
createdAt: 2026-08-11T00:00:00Z
```

`kind`はv1alpha1で次の2種類とする。

- `test_validity`: test plan、test-only head、test patch、RED証跡がIssue Contractを正しく検証するか
- `final_implementation`: approved testとIssue Contractに対し、final head、GREEN/refactor/check証跡が正しく、回帰や重大なriskがないか

全review roundはpublish済みdraft Pull Requestへ繋留される（[ADR-0002](../decisions/0002-pr-anchored-review.md)）。`pullRequest`はTask Issueと同じrepositoryのPRでなければならず、別repositoryのPRを指すrequestは受理しない。

Review Workerは`producerRunId`のstructured claim contextをPostgreSQLから読み、`issue`とauthorityをlive
sourceから取得する。claim時と同じCompiler versionでTask ContextとContext Manifestを再生成し、Requestの
期待identityと一致することをreview開始時と完了時に確認する。GitHub accessをfakeへ置き換えるtestでも、
同じIssue ReaderとCompiler contractを通す。model sessionへはraw bodyではなく、一致確認済みのin-memory
canonical Task Contextを渡す。

同様に、`pullRequest`が指すlive PRを取得し、open状態・headが`headSha`と一致・baseがstructured claim contextの`baseSha`と一致することを確認する。保存済みPR observationだけを現在のPRとして扱わない。live照合の結果は新しい`kudo.pull-request-observation/v1alpha1` artifact（PR ref、open/closed/merged、draft、head SHA、base ref、body digest、mergedのときはmerge commit SHA）としてlineageへ追記する。観測時刻はcanonical contentに含めず、Artifact Storeのmetadataが持つ。

Review Workerがsource treeを必要とする場合、artifact manifestに含まれるimmutable source bundle/snapshotから`headSha`を検証したdisposable checkoutを構築する。既にread-only remoteから同一commitを取得できる場合はそれを利用してよい。Issue Workerのworktree pathをRequestへ含めず、mutable worktreeをmountしない。

Request identityは、schema、kind、repository、Issue reference、Pull Request reference、head SHA、Context Manifest ref、Execution Policy ref、Artifact Manifest ref、policy refsから決まる。同じ`requestId`でもこれらが異なる入力を同一requestとして扱ってはならない。versioned refはschemaとdigestを組で比較し、digestが同じでもschemaが異なるrefを同一視しない。同じheadでも別PRへのrequestは別identityである。

`requestId`、`producerRunId`、`createdAt`、`issueObservation`、`pullRequestObservation`はidentityに含めない。observationはexact観測のaudit lineageであり、raw bodyの非意味的差分やPR body・draft/ready状態の変化だけでrequest identityと既存approvalをstaleにしない。意味のある変更はTask Context refを通じてContext Manifest refを変えるため、semantic stalenessはContext Manifest refの比較で判定できる。`policyRefs`は順序を持たない集合としてcanonical順へ正規化し、重複を拒否する。reviewerが評価基準を推測しないよう、`policyRefs`は1件以上必須とする。

### Required policy refs

kindごとに、対応するversioned review policyの現行pathを`policyRefs`へ含めなければならない。policy文書は標準policyの省略と別policyからの推測を禁じており、pathの形式検証だけでは任意の別policyだけを積んだrequestがproviderへ渡ってしまう。

| Review kind | 必須policy ref |
| --- | --- |
| `test_validity` | `docs/spec/05_design/review-policies/test-validity-v1alpha1.md` |
| `final_implementation` | `docs/spec/05_design/review-policies/final-implementation-v1alpha1.md` |

repository固有policyの追加は妨げない。欠落はrequestをrejectし、欠落したpathをすべてerrorへ載せて`protocol_kind_constraint`として分類する。policyの意味を変えるときは新しいversioned pathを追加し、この表を同じchangeで差し替える。

local path、provider session ID、会話履歴、application-private database record、credentialをreviewの必須入力にしない。

## Artifact Manifest

manifestは`kudo.artifact-manifest/v1alpha1`としてversion付けし、reviewに必要なartifactのlogical name、media type、byte length、SHA-256 digestを列挙する。各bytesはcontent-addressed storeのwrite-once objectであり、producerが後から上書きできない。

```yaml
schema: "kudo.artifact-manifest/v1alpha1"
entries:
  - name: "test-plan"
    mediaType: "text/markdown; charset=utf-8"
    length: "420"
    digest: "sha256:<digest>"
```

manifestはlogical nameで引くtableである。nameは`[a-z0-9]`で始まる小文字英数字と`-`、`.`、`/`、`_`だけを許可し、重複を拒否する。加えてnameはrelative pathとして正規形でなければならない。空segment、`.`、`..`、末尾`/`、改行やcontrol characterを含む形は受理しない（policy refやauthority refと同じpath規則を使う）。Review Workerはimmutable snapshotをdisposable checkoutへ展開するため、nameが展開先の名前として使われうる。traversal形状はmanifestの入口で拒否し、下流実装の規律に委ねない。canonical encodeではnameのlexicographic順へ並べ替えるため、producerの列挙順はmanifest identityを変えない。`length`はcanonical encodingの規則に従い、implicit intではなくdecimal stringとしてencodeする。payloadを持つartifactは、bytesとmetadataが食い違ったままreviewへ渡らないよう、length・media type・digestをpayloadから導出する。

`test_validity`では最低限、次を参照できるようにする。Task Contextとauthority contentはArtifact Manifestへ
含めず、live sourceから再生成・再取得して期待digestと比較する。

- 一致確認済みのin-memory canonical Task Contextとauthority content
- implementation briefとAcceptance Criteria mapping
- test plan
- test patchまたは固定済みtest-only source snapshot
- head SHAを再構築・検証できるsource bundleまたは同等のimmutable snapshot
- RED command、exit status、stdout/stderr、実行environment identity

`final_implementation`では次を追加する。

- approved test validity Review Result
- approved test-only head identity
- implementation patchまたはfinal source snapshot
- final head SHAを再構築・検証できるsource bundleまたは同等のimmutable snapshot
- GREEN command evidence
- refactor後のrequired checksとIssue Verification evidence
- Pull Requestに記載するsummary、risk、manual verificationのdraft artifact

### Required manifest entries

永続化するevidenceを次の固定logical nameへ正規化してrequest bindingで検証する。Task Context、Context
Manifest、raw Issue body、authority contentは再構築可能なのでArtifact Manifestへ入れない。implementation
briefとAcceptance Criteria mappingは再生成したTask Contextと`test-plan`、test/implementation patchまたは
snapshotは`source-bundle`、approved test-only head identityは参照先Review Requestへbindされた
`test-validity-result`が担う。語彙は`operation-protocol-v1alpha1.md`のArtifact logical names節に置く。

| Review kind | 必須logical name |
| --- | --- |
| `test_validity` | `test-plan`、`red-evidence`、`source-bundle`、`pull-request-observation` |
| `final_implementation` | 上記すべて、および`test-validity-result`、`green-evidence`、`check-evidence`、`pull-request-draft` |

`pull-request-observation`はpublish時点の観測recordであり、reviewerがlive PRとの照合の基準点として読む。加えて`final_implementation`には条件付きentry `performance-evidence`がある。Task Contextにperformance boundが宣言されている場合の測定証跡（測定command、固定した実行条件、環境identity、複数回実行の要約）であり、必須になる条件はTask Contextの内容に依存するため静的なkind別集合では強制しない。条件の判定と欠落の拒否はReview Workerのdeterministic prerequisiteが行う。

`source-bundle`はmodelの成果ではないが、headを生成する`author_tests`、`revise_tests`、`implement`、`repair_implementation`の必須outputである。Issue Workerがcheckpoint commitから固定する。ControllerはIssue Worker workspaceもartifact bytesもmountしないため、requestを組み立てる時点で欠落したbundleを補完しない。

必須集合は下限であって上限ではなく、語彙外のnameを追加してよい。欠落はrequestをrejectし、Review Workerへ渡さない。欠落したnameはすべてerrorへ載せ、`protocol_kind_constraint`として分類する。

binding検証はmanifestを再encodeしてrequestの`artifactManifest` refと照合したうえで行う。nameの充足だけを見てrefを照合しないと、必須entryを揃えた別のmanifestでgateを通せてしまい、検証したmanifestとreviewerが実際に読むmanifestがずれる。

artifactのbytesが変われば新しいdigest、Artifact Manifest、Review Requestを作る。test patchをimplementation phaseで変更した場合、以前のtest validity approvalを再利用せず、test review gateへ戻る。

## Review Result

Review Resultは`kudo.review-result/v1alpha1`としてversion付けする。

```yaml
schema: kudo.review-result/v1alpha1
requestDigest: sha256:<digest>
reviewRunId: review-01
verdict: request_changes
perspectives:
  - perspective: ux
    applicable: false
    reason: no-user-facing-surface-change
    evidenceRefs:
      - sha256:<digest>
  - perspective: accessibility
    applicable: false
    reason: no-ui-surface-change
    evidenceRefs:
      - sha256:<digest>
  - perspective: type-design
    applicable: true
    reason: exported-signature-changed
    evidenceRefs:
      - sha256:<digest>
  - perspective: performance
    applicable: false
    reason: no-bound-and-no-perf-surface
    evidenceRefs:
      - sha256:<digest>
findings:
  - id: F-1
    severity: blocking
    summary: AC-2を検証するtestがない
    expected: AC-1とAC-2の観測可能な結果を検証する
    observed: test planとpatchはAC-1のみを参照している
    evidenceRefs:
      - sha256:<digest>
createdAt: 2026-08-11T00:01:00Z
```

verdictは次のいずれかとする。

- `approve`: blocking findingがなく、同じrequest digestを次のgateへ進められる
- `request_changes`: Issue Workerの新しい修正Operationで対応可能なblocking findingがある
- `needs_human`: authority conflict、安全判断、仕様決定等、人の決定なしに修正方針を選べない

`severity`は`blocking`と`advisory`の2種類とする。`approve`はblocking findingを持てず、`request_changes`と`needs_human`はblocking findingを1件以上必要とする。verdictとfindingが矛盾するResultは、Controllerがfindingを読まずに誤ったgate判断をするため受理しない。

findingは`expected`、`observed`、`evidenceRefs`を持ち、単なる感想にしない。Review Resultはproducerのworktree、branch、PRを変更せず、新しいartifactとして保存する。

### Applicability宣言

`perspectives`は条件付き観点への観点別applicability宣言である。適用可否はreview sessionがpolicyの適用条件（正本は[Final Implementation Review Policy](../review-policies/final-implementation-v1alpha1.md)の条件付き観点表）から判断し、適用と判断した観点だけを深く評価する。事前のrule classifierや別のmodel呼び出しによる観点選択は行わない。

- 条件付き観点の語彙は`ux`、`accessibility`、`type-design`、`performance`の4つとする。
- `final_implementation`のResultは全条件付き観点への宣言をちょうど1件ずつ持たなければならない。宣言を欠くResultはbinding境界でrejectし、欠落した観点をすべてerrorへ載せて`protocol_kind_constraint`として分類する。`test_validity`は条件付き観点を持たないため`perspectives`を持てない。
- `reason`は機械判定可能なlowercase kebab-caseのcode値とする。自由記述の補足はfindingではなく`evidenceRefs`が指すartifactへ置く。
- 宣言は適用可否にかかわらず、判断根拠となったTask Context節やdiff範囲を指す`evidenceRefs`を1件以上持つ。
- 宣言はreviewer自身の判断であり、handlerやControllerが代筆・補完しない。宣言の欠落は品質verdictではなくprotocol violationである。

`summary`は1024 byte以内の単一行、`expected`と`observed`は65536 byte以内のcanonical textとする。上限と分類codeの規定は`operation-protocol-v1alpha1.md`のValidation節に置き、両protocolで同じ値を使う。上限を超えるfindingは受理せず、`protocol_field_too_long`として分類する。上限はReview Result identityの計算方法を変えない。

Review RequestとReview Resultのvalidation失敗も、Operation側と同じcode体系で分類する。Controllerは品質verdictとvalidation失敗を別経路で扱うため、validation失敗のcodeを`request_changes`や`needs_human`へ読み替えない。

Result identityは、schema、参照するrequest digest、verdict、applicability宣言、findingから決まる。`reviewRunId`と`createdAt`は含めないため、同じrequestへの同じ判断は同じcontent identityを持つ。findingは`id`、宣言はperspective名のlexicographic順へ正規化してencodeする。reviewerが列挙した順序は判断の一部ではなく、model providerは同じ判断でも順序を再現しないため、並びだけが違うResultを別identityにしない。`evidenceRefs`も同じ理由で順序を持たない集合として扱う。binding検証はResultが参照するrequest digestの一致と、kindに対する宣言の完全性で行う。

`request_changes`後の修正Operationには、Run input checkpoint、対象head、Review Result、必要なevidence
artifact referenceだけを渡す。修正Workerはlive Issue/authorityを再取得・再compileする。以前の
Implementation/Review sessionをresumeしない。修正後は新しいheadとrequest digestで再reviewする。

## Gate semantics

draft PRのpublish（`publish_head`）はreview approveをgateにしない。RED evidenceが固定された時点でIssue Workerはbranchをpushしdraft PRを冪等にensureする。draft状態のCIがREDになるのはTDDの位相の正直な表示であり、隠すためにpublishを遅らせない。

`test_validity`のapproveが、publish済みPRのlive headと一致するtest-only headとartifact manifestにbindされている場合だけ、Controllerは`implement` Operationを発行できる。

`final_implementation`のapproveが、publish済みPRのlive headと一致するfinal head、GREEN/refactor/check evidenceにbindされている場合だけ、Issue Workerは`finalize_pull_request`でrequired PR bodyを確定しdraftを解除できる。ready化とmergeがfinal approveをgateとする。finalize前後でheadが変わった場合はapproveをstaleにし、再publishと再reviewを要求する。PR bodyだけをartifactから決定論的に作成・更新してもsource headが変わらない場合、review bindingは維持できるが、required PR field validationは別途通さなければならない。

Review approveはそれ自体がmergeではない。approveはmerge gateの品質側条件であり、mergeにはさらにlive headの一致、required status checkのsuccess、mergeable、branch protectionという外形条件が要る。外形条件はControllerとIssue Workerが判定し、reviewerへ問い合わせない（[ADR-0005](../decisions/0005-auto-merge.md)）。final approve後にready化と`merge_pull_request`がdurableに記録され、Task Issueがcloseされて`ai-merged`へ投影された時点でKudo workflowはterminalになる。

## Failure and staleness

timeout、rate limit、network error、provider crash、invalid responseはtransport/execution failureであり、`request_changes`や`needs_human`という品質verdictに変換しない。Issueを取得できない場合もtransport failureであり、保存済み本文だけでreviewを続けない。execution failureはquality verdictのfieldを持たない別の型で表現し、1回のattemptの結末はverdictかfailureのどちらか一方だけを持つ。

Context Manifest ref、Execution Policy ref、commit SHA、Artifact Manifest ref、policy ref、Pull Request refのいずれかが変わった時点で既存Resultはstaleになる。Issue ObservationまたはPull Request Observationの変化だけではstaleにしない。PR bodyの編集やdraft/readyの状態遷移は観測の変化としてlineageへ追記される。

live PRのheadがrequestの`headSha`と一致しない場合、またはbaseがstructured claim contextの`baseSha`と一致しない場合は品質verdictを返さず、stale inputとしてControllerへ返す。Kudo自身のmerge intentに紐付かないcloseまたはmergeを観測した場合はRunを停止し、人間へescalateする。記録済みintentと一致するmerged観測は自分のmutationの再観測であり、干渉として扱わない。PR body編集またはdraft/ready遷移だけの差分はstaleにせず、観測をlineageへ追記する。同branchへの外部pushはIssue Workerのcompare-and-pushとこのlive照合の両方で検出する。

Review開始時または完了時に、live Issue/authorityから再生成したTask Context / Context Manifest identityが
Requestと一致しない場合は品質verdictを返さずstale inputとしてControllerへ返す。raw bodyの非意味的差分で
両identityが変わらない場合は、新しいIssue Observation ref/body digestをPostgreSQLのaudit lineageへ追記して
同じrequestを続行する。stale後は新しいReview Requestを発行し、古いResultを新しい入力のapprovalとして
再利用しない。review開始後の既存Requestへ最新refを上書きしない。

retry可能なexecution failureは同じlogical Review Requestに対する新しいattemptとして記録できるが、provider sessionはattemptごとに新規作成する。quality verdictとattempt failureを同じfieldで表現しない。
