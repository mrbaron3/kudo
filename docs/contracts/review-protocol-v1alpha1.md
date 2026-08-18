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
issueObservation:
  schema: kudo.issue-observation/v1alpha1
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
  - docs/contracts/issue-contract-v1alpha1.md
createdAt: 2026-08-11T00:00:00Z
```

`kind`はv1alpha1で次の2種類とする。

- `test_validity`: test plan、test-only head、test patch、RED証跡がIssue Contractを正しく検証するか
- `final_implementation`: approved testとIssue Contractに対し、final head、GREEN/refactor/check証跡が正しく、回帰や重大なriskがないか

Review Workerは`issueObservation`が指すIssue Observation artifactを検証したうえで、`issue`をGitHubから直接取得し、現在bodyのdigestがartifact内の`bodyDigest`と一致することを確認する。保存済みraw bodyだけを現在のIssueであるかのように扱わない。GitHub accessをfakeへ置き換えるtestでも、同じIssue Reader contractを通す。model sessionへはraw bodyではなく、Context Manifestの`TaskContextRef`が指すcanonical Task Contextを渡す。

Review Workerがsource treeを必要とする場合、artifact manifestに含まれるimmutable source bundle/snapshotから`headSha`を検証したdisposable checkoutを構築する。既にread-only remoteから同一commitを取得できる場合はそれを利用してよい。Issue Workerのworktree pathをRequestへ含めず、mutable worktreeをmountしない。

Request identityは、schema、kind、repository、Issue reference、head SHA、Context Manifest ref、Execution Policy ref、Artifact Manifest ref、policy refsから決まる。同じ`requestId`でもこれらが異なる入力を同一requestとして扱ってはならない。versioned refはschemaとdigestを組で比較し、digestが同じでもschemaが異なるrefを同一視しない。

`requestId`、`producerRunId`、`createdAt`、`issueObservation`はidentityに含めない。`issueObservation`と`bodyDigest`はexact観測のaudit lineageであり、raw bodyの非意味的差分だけでrequest identityと既存approvalをstaleにしない。意味のある変更はTask Context refを通じてContext Manifest refを変えるため、semantic stalenessはContext Manifest refの比較で判定できる。`policyRefs`は順序を持たない集合としてcanonical順へ正規化し、重複を拒否する。reviewerが評価基準を推測しないよう、`policyRefs`は1件以上必須とする。

local path、provider session ID、会話履歴、application-private database record、credentialをreviewの必須入力にしない。

## Artifact Manifest

manifestは`kudo.artifact-manifest/v1alpha1`としてversion付けし、reviewに必要なartifactのlogical name、media type、byte length、SHA-256 digestを列挙する。各bytesはcontent-addressed storeのwrite-once objectであり、producerが後から上書きできない。

```yaml
schema: "kudo.artifact-manifest/v1alpha1"
entries:
  - name: "task-context"
    mediaType: "application/yaml; charset=utf-8"
    length: "1647"
    digest: "sha256:<digest>"
```

manifestはlogical nameで引くtableである。nameは`[a-z0-9]`で始まる小文字英数字と`-`、`.`、`/`、`_`だけを許可し、重複を拒否する。加えてnameはrelative pathとして正規形でなければならない。空segment、`.`、`..`、末尾`/`、改行やcontrol characterを含む形は受理しない（policy refやauthority refと同じpath規則を使う）。Review Workerはimmutable snapshotをdisposable checkoutへ展開するため、nameが展開先の名前として使われうる。traversal形状はmanifestの入口で拒否し、下流実装の規律に委ねない。canonical encodeではnameのlexicographic順へ並べ替えるため、producerの列挙順はmanifest identityを変えない。`length`はcanonical encodingの規則に従い、implicit intではなくdecimal stringとしてencodeする。payloadを持つartifactは、bytesとmetadataが食い違ったままreviewへ渡らないよう、length・media type・digestをpayloadから導出する。

`test_validity`では最低限、次を参照できるようにする。

- Issue Observation evidenceとraw body artifact。取得identity、exact body digestを含む
- canonical Task Context、Context Manifest、解決済みauthority content
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

上記のartifactに対応するlogical nameの必須集合をrequest bindingで検証するかは、命名規約を確定させる別change（[#43](https://github.com/mrbaron3/kudo/issues/43)）で決める。現時点のvalidatorはmanifestが非空であることと、各entryのname・media type・length・digestの形だけを検証する。

artifactのbytesが変われば新しいdigest、Artifact Manifest、Review Requestを作る。test patchをimplementation phaseで変更した場合、以前のtest validity approvalを再利用せず、test review gateへ戻る。

## Review Result

Review Resultは`kudo.review-result/v1alpha1`としてversion付けする。

```yaml
schema: kudo.review-result/v1alpha1
requestDigest: sha256:<digest>
reviewRunId: review-01
verdict: request_changes
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

`summary`は1024 byte以内の単一行、`expected`と`observed`は65536 byte以内のcanonical textとする。上限と分類codeの規定は`operation-protocol-v1alpha1.md`のValidation節に置き、両protocolで同じ値を使う。上限を超えるfindingは受理せず、`protocol_field_too_long`として分類する。上限はReview Result identityの計算方法を変えない。

Review RequestとReview Resultのvalidation失敗も、Operation側と同じcode体系で分類する。Controllerは品質verdictとvalidation失敗を別経路で扱うため、validation失敗のcodeを`request_changes`や`needs_human`へ読み替えない。

Result identityは、schema、参照するrequest digest、verdict、findingから決まる。`reviewRunId`と`createdAt`は含めないため、同じrequestへの同じ判断は同じcontent identityを持つ。findingは`id`のlexicographic順へ正規化してencodeする。reviewerが列挙した順序は判断の一部ではなく、model providerは同じ判断でも順序を再現しないため、並びだけが違うResultを別identityにしない。`evidenceRefs`も同じ理由で順序を持たない集合として扱う。binding検証はResultが参照するrequest digestの一致で行う。

`request_changes`後の修正Operationには、Issue Observation、Context Manifestが指すTask Context、対象head、Review Result、必要なartifact referenceだけを渡す。以前のImplementation/Review sessionをresumeしない。修正後は新しいheadとrequest digestで再reviewする。

## Gate semantics

`test_validity`のapproveが同じtest-only headとartifact manifestにbindされている場合だけ、Controllerは`implement` Operationを発行できる。

`final_implementation`のapproveがfinal head、GREEN/refactor/check evidenceにbindされている場合だけ、Issue WorkerはPull Requestを作成または更新できる。PR mutation前後でheadが変わった場合はapproveをstaleにし、再reviewする。PR bodyだけを決定論的に作成・更新してもsource headが変わらない場合、review bindingは維持できるが、required PR field validationは別途通さなければならない。

Review approveはGitHub Issueの完了またはmergeを意味しない。final approve後にPRがdurableに記録され、Issueが`ai-review-waiting`へ投影された時点でKudo workflowは人間へhandoffする。

## Failure and staleness

timeout、rate limit、network error、provider crash、invalid responseはtransport/execution failureであり、`request_changes`や`needs_human`という品質verdictに変換しない。Issueを取得できない場合もtransport failureであり、保存済み本文だけでreviewを続けない。execution failureはquality verdictのfieldを持たない別の型で表現し、1回のattemptの結末はverdictかfailureのどちらか一方だけを持つ。

Context Manifest ref、Execution Policy ref、commit SHA、Artifact Manifest ref、policy refのいずれかが変わった時点で既存Resultはstaleになる。Issue Observationの変化だけではstaleにしない。

live Issueのbody digestがIssue Observation artifact内の`bodyDigest`と一致しない場合は品質verdictを返さず、Controllerへ返す。raw bodyの非意味的差分でTask Context/Context Manifestが変わらない場合も、このlive freshness判定は省略しない。Controllerは最新のcompile結果と解決済みrefでsemantic comparisonを行い、`SameSemanticInput`なら新しいIssue Observationをaudit lineageへ追記して同じrequestを続行し、`ChangedSemanticInput`ならstale inputとして扱う。stale後は新しいReview Requestを発行し、古いResultを新しい入力のapprovalとして再利用しない。review開始後の既存Requestへ最新refを上書きしない。

retry可能なexecution failureは同じlogical Review Requestに対する新しいattemptとして記録できるが、provider sessionはattemptごとに新規作成する。quality verdictとattempt failureを同じfieldで表現しない。
