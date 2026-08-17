# Worker Operation Protocol v1alpha1

## Purpose

Controller が Issue Worker へ指示する run-once Operation と、Worker が返す Result の durable handoff を定義する。Controller は concrete Worker や provider process を直接呼び出さず、この protocol を PostgreSQL queue へ記録する。

Review Worker への品質評価依頼は [Implementation–Review Protocol](review-protocol-v1alpha1.md) の Review Request / Result を使う。本 protocol の execution failure と review の quality verdict を混在させない。

## Operation envelope

```yaml
schema: kudo.worker-operation/v1alpha1
operationId: op-01
runId: run-01
kind: author_tests
repository: github://owner/repository
issue: github://owner/repository/issues/42
issueObservation:
  schema: kudo.issue-observation/v1alpha1
  digest: sha256:<digest>
contextManifest:
  schema: kudo.context-manifest/v1alpha1
  digest: sha256:<digest>
executionPolicy:
  schema: kudo.execution-policy/v1alpha1
  digest: sha256:<digest>
baseSha: <git-commit-sha>
headSha: <git-commit-sha>
inputArtifacts:
  - sha256:<digest>
policyRefs:
  - docs/github-routing.md
causationId: transition-01
createdAt: 2026-08-11T00:00:00Z
```

`operationId`はlogical Operationのstable identity、database上のexecution attemptは別identityとする。retryのたびにOperationを複製せず、同じOperationへattemptを追加する。

Operation digestは、schema、kind、Run、repository、Issue、Context Manifest ref、Execution Policy ref、base/head SHA、input artifact digest、policy refs、causation identityから計算するcontent identityである。`operationId`、`createdAt`、lease owner、attempt number、provider session ID、workspace pathをdigestへ含めない。

`issueObservation`と`bodyDigest`はexact観測のaudit lineageであり、Operation digestへ含めない。raw bodyの非意味的差分でTask ContextとContext Manifestが変わらない場合、Operation identityは変わらず、新しい観測をlineageへ追記する。意味のある変更はTask Context refを変え、Task Context refはContext Manifestに含まれるため、semantic stalenessはContext Manifest refの比較だけで判定できる。validatorはContext Manifest refをschemaとdigestのopaque valueとして扱い、Task Context YAMLやIssue Contractをparseしない。

`repository`はIssue referenceから導出し、同じenvelopeへ食い違いうるidentityを二重に持たせない。`inputArtifacts`と`policyRefs`は順序を持たない集合として扱い、canonical順（lexicographic）へ正規化したうえで重複を拒否する。並び順の違いだけでOperation identityを変えない。

Issue Observation、Task Context、Context Manifest、Execution Policyのschema、canonicalization、refは[Task Context Protocol](task-context-v1alpha1.md)を正とする。Execution PolicyはIssue Worker/Review Workerのprovider adapter、model identifier、adapter version、tool permission、timeout policyを固定し、credential、secret path、session IDを含めない。同じRunの途中でdeployment defaultが変わっても、既存Operationへ暗黙に適用しない。

## Issue Worker operation kinds

| Kind | Model session | Required input | Output |
| --- | --- | --- | --- |
| `claim` | no | repository、IssueRef、candidate policy | Issue Observation、Task Context、ClaimRequirements、Context Manifest、base SHAまたはstructured rejection |
| `author_tests` | fresh | claimed context、base/head | test plan、test-only head、RED evidence |
| `revise_tests` | fresh | current test head、blocking Review Result、prior artifacts | revised test head、new RED evidence |
| `implement` | fresh | approved test validity Result、test head、Issue context | implementation head、GREEN/refactor/check evidence |
| `repair_implementation` | fresh | current implementation head、blocking final Review Result | repaired head、new evidence |
| `create_pull_request` | no | final approved head、PR body artifact、idempotency identity | GitHub PR number、URL、observed head |

`claim`ではControllerがRun IDを予約するが、claim成功まではactive Runとして公開しない。また、Issue Observation、Context Manifest、base/headはまだ存在しないため、envelope上の該当fieldを省略する。Execution Policyはclaim成功時にRunへ固定する。それ以外のOperationではkindに必要なfieldを省略しない。空文字や直前Runの値から推測しない。Task ContextはContext Manifest内の`TaskContextRef`から取得し、digestだけでschemaを推測しない。

kindごとのfield要件は次のとおりとする。validatorは省略だけでなく、kindが持てないfieldの混入もrejectする。

| Kind | `issueObservation`、`contextManifest`、`baseSha`、`headSha` | `inputArtifacts` |
| --- | --- | --- |
| `claim` | 持たない | 持たない |
| `author_tests` | 必須 | 任意 |
| `revise_tests`、`implement`、`repair_implementation`、`create_pull_request` | 必須 | 1件以上必須 |

model-bearing Operationは、同じRun/worktreeを扱う場合もfresh provider process/sessionを作る。継続に必要な情報はcurrent commit、input artifact、versioned Review Resultとして渡し、resume tokenやconversation transcriptを渡さない。

## Operation Result

```yaml
schema: kudo.worker-result/v1alpha1
operationDigest: sha256:<digest>
attemptId: attempt-01
outcome: succeeded
headSha: <git-commit-sha>
outputArtifacts:
  - sha256:<digest>
externalRefs: []
completedAt: 2026-08-11T00:01:00Z
```

terminalな`outcome`は次のいずれかとする。

- `succeeded`: Operation contractを満たすoutputがimmutableに固定された
- `stale_input`: Issue Observation、authority、base/head、input artifactが開始時の期待値と一致しない
- `needs_human`: Issue Workerだけでは選べないauthority、安全、仕様判断が必要
- `failed_terminal`: retry policyを適用しても自動継続できないexecution failure

retry可能なtimeout、rate limit、network/process failureはterminal Resultにせず、attempt failureとしてerror class、evidence、次回eligible timeを記録する。bounded retryを使い切った場合だけpolicyに従って`failed_terminal`またはoperator escalationへ進む。attempt failureはquality verdictのfieldを持たない別の型で表現し、`approve`、`request_changes`、`needs_human`へ変換しない。

`request_changes`と`approve`は本Resultのoutcomeにしない。Review WorkerだけがReview Resultとして返す。

Result digestは、schema、参照するOperation digest、outcome、head SHA、output artifact、external refから計算する。`attemptId`と`completedAt`は含めないため、同じ入力から同じ結果を再生成したattemptは同じcontent identityを持つ。

`headSha`はOperationが新しく固定または観測したheadであり、入力`headSha`と一致するとは限らない。したがってbinding検証はResultが参照するOperation digestの一致だけで行い、head一致を要求しない。`claim`のResultはheadを返さず、それ以外のkindの`succeeded` Resultはheadを省略できない。

## Attempt and lease

Workerはroleとkindが一致するqueued Operationだけをleaseする。attemptには少なくともattempt ID、worker instance、lease expiry、heartbeat、start/end time、provider、exit/error classを記録する。

- lease取得前にprovider processやworkspace mutationを開始しない。
- heartbeat更新失敗時は外部mutation前にownershipを再確認する。
- lease expiry後に別attemptが開始できるが、以前のprocess/sessionは再利用しない。
- late ResultはOperationの現在versionとlease ownershipが一致しなければcommitしない。
- Result commitと次transitionはControllerがbindingを検証して行う。WorkerがRun stateを直接進めない。

## Freshness and mutation

各Operationは開始時にinput artifactのdigest/lengthとschemaを検証する。model-bearing Operationの直前にはlive Issue body digestを期待Issue Observationと照合する。sourceを変更するOperationは専用worktreeのcurrent headが`headSha`と一致することを確認する。

live Issue body digestが一致しない場合、それだけではstaleと判定しない。Issueを再compileして得た最新のIssue Observation ref、Context Manifest ref、Execution Policy ref、head SHA、input artifact、policy refをsemantic comparisonへ渡し、結果に従う。

- `SameSemanticInput`: Operation identityとquality approvalを維持し、新しいIssue Observationをaudit lineageへ追記して続行する
- `ChangedSemanticInput`: `stale_input`として返し、どのfieldが変わったかを記録する

comparisonはpure functionであり、最新入力を既存Operation、approval、reviewへ書き戻さない。書き戻すと、古いapprovalが新しい入力のapprovalとして黙って再利用される。新しい入力で続けるには新しいOperationを作る。

GitHub mutationを伴う`create_pull_request`は、repository、Run、Issue、headを含むstable idempotency markerをPR bodyまたは検索可能なmetadataへ記録する。responseを受け取る前にprocessが停止した場合、retry時は既存PRを検索・照合してからcreateする。

Issue Workerだけがimplementation worktree、branch、commit、Pull Requestを変更できる。ControllerはOperationをenqueueできてもmutationを代行しない。

## Validation

unknown schema/version、unknown kind、欠落required field、kindに許されないfield combination、digest/bytes不一致をrejectする。invalid payloadをproviderへ渡さず、retry可能なtransport failureにも変換しない。

versioned refはschemaとdigestの組で検証・比較する。digestが同じでもschemaが異なるrefを同一視しない。ref schemaのversion部分はopaqueに扱い、既知versionでなくても組として保持・比較できるが、envelope自身のschemaは既知versionだけを受理する。

Operation/Resultのcanonicalizationとvalidation ruleはfixtureで固定する。protocolを変更する場合、本書、parser、fixture、testを同じchangeで更新する。
