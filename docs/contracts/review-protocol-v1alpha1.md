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
issueRevision: sha256:<digest>
headSha: <git-commit-sha>
contextManifest: sha256:<digest>
executionPolicy: sha256:<digest>
artifactManifest: sha256:<digest>
policyRefs:
  - docs/contracts/issue-contract-v1alpha1.md
createdAt: 2026-08-11T00:00:00Z
```

`kind`はv1alpha1で次の2種類とする。

- `test_validity`: test plan、test-only head、test patch、RED証跡がIssue Contractを正しく検証するか
- `final_implementation`: approved testとIssue Contractに対し、final head、GREEN/refactor/check証跡が正しく、回帰や重大なriskがないか

Review Workerは`issueRevision`が指すIssue Revision artifactを検証したうえで、`issue`をGitHubから直接取得し、現在bodyのdigestがartifact内の`bodyDigest`と一致することを確認する。保存済みIssue本文だけを現在のIssueであるかのように扱わない。GitHub accessをfakeへ置き換えるtestでも、同じIssue Reader contractを通す。

Review Workerがsource treeを必要とする場合、artifact manifestに含まれるimmutable source bundle/snapshotから`headSha`を検証したdisposable checkoutを構築する。既にread-only remoteから同一commitを取得できる場合はそれを利用してよい。Issue Workerのworktree pathをRequestへ含めず、mutable worktreeをmountしない。

Request identityは、schema、kind、repository、Issue reference、Issue Revision digest、head SHA、Context Manifest digest、Execution Policy digest、artifact manifest digest、policy refsから決まる。同じ`requestId`でもこれらが異なる入力を同一requestとして扱ってはならない。

local path、provider session ID、会話履歴、application-private database record、credentialをreviewの必須入力にしない。

## Artifact Manifest

manifestは、reviewに必要なartifactのlogical name、media type、byte length、SHA-256 digestを列挙する。各bytesはcontent-addressed storeのwrite-once objectであり、producerが後から上書きできない。

`test_validity`では最低限、次を参照できるようにする。

- Issue Revision evidence。取得時のIssue本文、取得identity、body digestを含む
- Context Manifestと解決済みauthority content
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

findingは`expected`、`observed`、`evidenceRefs`を持ち、単なる感想にしない。Review Resultはproducerのworktree、branch、PRを変更せず、新しいartifactとして保存する。

`request_changes`後の修正Operationには、Issue Revision、対象head、Review Result、必要なartifact referenceだけを渡す。以前のImplementation/Review sessionをresumeしない。修正後は新しいheadとrequest digestで再reviewする。

## Gate semantics

`test_validity`のapproveが同じtest-only headとartifact manifestにbindされている場合だけ、Controllerは`implement` Operationを発行できる。

`final_implementation`のapproveがfinal head、GREEN/refactor/check evidenceにbindされている場合だけ、Issue WorkerはPull Requestを作成または更新できる。PR mutation前後でheadが変わった場合はapproveをstaleにし、再reviewする。PR bodyだけを決定論的に作成・更新してもsource headが変わらない場合、review bindingは維持できるが、required PR field validationは別途通さなければならない。

Review approveはGitHub Issueの完了またはmergeを意味しない。final approve後にPRがdurableに記録され、Issueが`ai-review-waiting`へ投影された時点でKudo workflowは人間へhandoffする。

## Failure and staleness

timeout、rate limit、network error、provider crash、invalid responseはtransport/execution failureであり、`request_changes`や`needs_human`という品質verdictに変換しない。Issueを取得できない場合もtransport failureであり、保存済み本文だけでreviewを続けない。

Issue Revision、Context Manifest、Execution Policy、commit SHA、artifact manifest、policy refのいずれかが変わった時点で既存Resultはstaleになる。live Issueのbody digestがIssue Revision artifact内の`bodyDigest`と一致しない場合は品質verdictを返さず、stale inputとしてControllerへ返す。修正後は新しいReview Requestを発行し、古いResultを新しい入力のapprovalとして再利用しない。

retry可能なexecution failureは同じlogical Review Requestに対する新しいattemptとして記録できるが、provider sessionはattemptごとに新規作成する。quality verdictとattempt failureを同じfieldで表現しない。
