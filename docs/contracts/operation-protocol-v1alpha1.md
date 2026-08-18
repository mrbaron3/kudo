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
headSha: <git-commit-sha>
inputArtifacts:
  - sha256:<digest>
policyRefs:
  - docs/github-routing.md
causationId: transition-01
createdAt: 2026-08-11T00:00:00Z
```

`operationId`はlogical Operationのstable identity、database上のexecution attemptは別identityとする。retryのたびにOperationを複製せず、同じOperationへattemptを追加する。

Operation digestは、schema、kind、Run、repository、Issue、Context Manifest ref、Execution Policy ref、head SHA、input artifact digest、policy refs、causation identityから計算するcontent identityである。`operationId`、`createdAt`、lease owner、attempt number、provider session ID、workspace pathをdigestへ含めない。

`issueObservation`と`bodyDigest`はexact観測のaudit lineageであり、Operation digestへ含めない。raw bodyの非意味的差分でTask ContextとContext Manifestが変わらない場合、Operation identityは変わらず、新しい観測をlineageへ追記する。意味のある変更はTask Context refを変え、Task Context refはContext Manifestに含まれるため、semantic stalenessはContext Manifest refの比較だけで判定できる。validatorはContext Manifest refをschemaとdigestのopaque valueとして扱い、Task Context YAMLやIssue Contractをparseしない。

`repository`はIssue referenceから導出し、同じenvelopeへ食い違いうるidentityを二重に持たせない。`baseSha`も同じ理由でenvelopeに置かない。baseはclaimがContext Manifestへpinする解決結果であり、validatorはmanifest refをopaqueに扱うため、envelopeへ複製するとcross-check不能な二重identityになる。baseの変更はContext Manifest refの変化として検出する。`inputArtifacts`と`policyRefs`は順序を持たない集合として扱い、canonical順（lexicographic）へ正規化したうえで重複を拒否する。並び順の違いだけでOperation identityを変えない。

Issue Observation、Task Context、Context Manifest、Execution Policyのschema、canonicalization、refは[Task Context Protocol](task-context-v1alpha1.md)を正とする。Execution PolicyはIssue Worker/Review Workerのprovider adapter、model identifier、adapter version、tool permission、timeout policyを固定し、credential、secret path、session IDを含めない。同じRunの途中でdeployment defaultが変わっても、既存Operationへ暗黙に適用しない。

## Issue Worker operation kinds

| Kind | Model session | Required input | Output |
| --- | --- | --- | --- |
| `claim` | no | repository、IssueRef、candidate policy | Issue Observation、Task Context、ClaimRequirements、base SHAをpinしたContext Manifestまたはstructured rejection |
| `author_tests` | fresh | claimed context（baseはContext Manifestがpin）、head | test plan、test-only head、RED evidence |
| `revise_tests` | fresh | current test head、blocking Review Result、prior artifacts | revised test head、new RED evidence |
| `implement` | fresh | approved test validity Result、test head、Issue context | implementation head、GREEN/refactor/check evidence |
| `repair_implementation` | fresh | current implementation head、blocking final Review Result | repaired head、new evidence |
| `create_pull_request` | no | final approved head、PR body artifact、idempotency identity | GitHub PR number、URL、observed head |

`claim`ではControllerがRun IDを予約するが、claim成功まではactive Runとして公開しない。また、Issue Observation、Context Manifest、headはまだ存在しないため、envelope上の該当fieldを省略する。Execution Policyはclaim成功時にRunへ固定する。それ以外のOperationではkindに必要なfieldを省略しない。空文字や直前Runの値から推測しない。Task ContextはContext Manifest内の`TaskContextRef`から取得し、digestだけでschemaを推測しない。

kindごとのfield要件は次のとおりとする。validatorは省略だけでなく、kindが持てないfieldの混入もrejectする。

| Kind | `issueObservation`、`contextManifest`、`headSha` | `inputArtifacts` |
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
changedInputFields: []
outputArtifacts:
  - sha256:<digest>
externalRefs: []
completedAt: 2026-08-11T00:01:00Z
```

terminalな`outcome`は次のいずれかとする。

- `succeeded`: Operation contractを満たすoutputがimmutableに固定された
- `stale_input`: 再取得したContext Manifest ref、Execution Policy ref、head、input artifact、policy refが開始時の期待値と一致しない（Issue Observationだけの差分はstaleにしない）
- `needs_human`: Issue Workerだけでは選べないauthority、安全、仕様判断が必要
- `failed_terminal`: retry policyを適用しても自動継続できないexecution failure

retry可能なtimeout、rate limit、network/process failureはterminal Resultにせず、attempt failureとしてerror class、evidence、次回eligible timeを記録する。bounded retryを使い切った場合だけpolicyに従って`failed_terminal`またはoperator escalationへ進む。attempt failure recordがrequired fieldを欠く、あるいはclassが未知の場合は、retry継続にもterminal outcomeにも倒さずvalidation errorとして返す。判定不能なrecordをretry継続へ倒すと、bounded retryが無効化されたままattemptが積み続けられ、caller側からは検知できない。attempt failureはquality verdictのfieldを持たない別の型で表現し、`approve`、`request_changes`、`needs_human`へ変換しない。

`request_changes`と`approve`は本Resultのoutcomeにしない。Review WorkerだけがReview Resultとして返す。

1回のattemptの結末はterminal Resultかattempt failureのどちらか一方だけを持つ。両方を持てる表現にすると、失敗したattemptがterminal Resultとしてcommitされうる。この制約はIssue Worker側とReview Worker側の双方で型として表現する。

`changedInputFields`は`stale_input`の根拠であり、semantic comparisonが返した変更field名をそのまま載せる。`stale_input`では1件以上必須、それ以外のoutcomeでは空とする。値はcomparisonと同じ語彙（`contextManifest`、`executionPolicy`、`headSha`、`inputArtifacts`、`policyRefs`）に限り、自由文字列を許さない。語彙を共有しないと、Controllerは受け取った記録をcomparison結果と突き合わせられない。

Result digestは、schema、参照するOperation digest、outcome、head SHA、changed input fields、output artifact、external refから計算する。`attemptId`と`completedAt`は含めないため、同じ入力から同じ結果を再生成したattemptは同じcontent identityを持つ。

`headSha`はOperationが新しく固定または観測したheadであり、headを進めるkindでは入力`headSha`と一致するとは限らない。したがってbinding検証は、Resultが参照するOperation digestの一致と、次のkindごとの`succeeded`要件で行う。

| Kind | `headSha` | `outputArtifacts` | `externalRefs` |
| --- | --- | --- | --- |
| `claim` | 返さない | 1件以上必須 | 任意 |
| `author_tests`、`revise_tests`、`implement`、`repair_implementation` | 必須 | 1件以上必須 | 任意 |
| `create_pull_request` | 入力`headSha`と一致 | 任意 | 同じrepositoryのPR referenceを1件以上必須 |

外部referenceは`github://owner/repository/pull/<number>`形式のcanonicalなPull Request referenceとする。非空判定だけでは、成功と主張するResultからPR numberとURLを復元できず、retry時の既存PR照合とdurable handoffが成立しない。Issue referenceと同じく、numberは先頭0や符号を含まない十進表記だけを許可し、owner/repositoryはcase-insensitiveに正規化する。Operationが対象とするrepository以外のPR referenceは成功の根拠にしない。

`succeeded`は「Operation contractを満たすoutputがimmutableに固定された」ことを意味する。outputを残さない成功を受理すると、後続Operationとreviewが存在しないartifactを前提に進む。`create_pull_request`はsource headを進めずapproved headを観測してPRを作るOperationなので、Result headが入力headと一致しないことは、reviewしていないheadへ外部mutationを行ったことを意味する。binding境界で拒否する。

kindごとに必要なartifactの中身（test plan、RED evidence等）は本書のkind表が定めるが、binding境界が検証するのは「kindが要求するoutputを残したか」までである。logical nameの必須集合をvalidatorへ固定するかは、artifact命名規約を確定させる別change（[#43](https://github.com/mrbaron3/kudo/issues/43)）で決める。

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

### Validation error classification

拒否理由は機械可読なcodeで分類する。Controllerはerror文字列の一致で分岐しない。文字列に依存すると、message表現を変えただけでControllerの分岐が壊れ、逆に分岐を保つためにmessageを固定する必要が生じる。

| code | 意味 |
| --- | --- |
| `protocol_schema_unknown` | envelope自身またはversioned refのschemaが既知versionでない |
| `protocol_kind_unknown` | Operation kindまたはReview kindが語彙に無い |
| `protocol_field_missing` | required fieldが空 |
| `protocol_field_invalid` | fieldの値が形式規則を満たさない |
| `protocol_field_duplicate` | 集合として扱うfieldに重複がある |
| `protocol_field_too_long` | canonical textの上限を超えている |
| `protocol_kind_constraint` | 個々のfieldは妥当だが、そのkindでは持てないか省略できない |
| `protocol_identity_mismatch` | 再計算したdigestと参照されたidentityが一致しない |
| `protocol_outcome_conflict` | 結末の排他規則違反（verdictとfailureの同時保持、verdictとfindingの矛盾等） |

この分類はprotocol validation専用であり、Issue Contract parserのclaim rejection codeとは別の値空間である。parserのcodeはIssue bodyの行・section・fieldを指すtext由来の診断であり、protocol validationは解決済みの値に対する判定で行番号を持たない。

いずれのcodeもretry可能なtransport failureではない。protocol validationの失敗はimmutableな入力に対するpermanentな契約違反であり、同じ入力でretryしても結果は変わらない。分類できない失敗をretry可能側の既定へ倒すと、契約違反を無限にretryしうるため、既定はretryしない側とする。

### Canonical text limits

canonical textには上限を定める。これらの値はmodel providerの出力に由来し、canonical bytesとPostgreSQL textの両方へそのまま載る。上限が無いと、単一のattemptがrow sizeとdigest計算量を通じて後続の全Operationへ影響しうる。

| 対象 | 上限 |
| --- | --- |
| 複数行本文（attempt failureの`evidence`、findingの`expected`／`observed`） | 65536 byte |
| 単一行の値（findingの`summary`、artifact manifestの`mediaType`、`externalRefs`の各要素、Execution Policyの各field、repository-relative path） | 1024 byte |

上限は受理可否だけに影響し、canonical bytesの構成方法とdigestの計算方法を変えない。上限ちょうどの値は受理する。上限超過は`protocol_field_too_long`として分類し、control character混入や空文字による`protocol_field_invalid`と区別する。producer側が本文を切り詰めれば通る失敗と、値そのものが不正な失敗は対処が違う。

`policyRefs`と`authorityRefs`のrepository-relative pathは、canonicalな単一行であることを要求する。改行やcontrol characterを含む値はcanonical bytesとPostgreSQL textの両方へ載るため、protocol層で拒否する。

`operationId`、`runId`、`attemptId`、`causationId`等のidentifierは、英数字で始まり、英数字と`-`、`_`、`.`だけを含む128文字以内の値とする。Runはworkspaceを持つためidentifierはpath segmentへも載りうる。`.`や`..`のような値をprotocol層で通すと、拒否がfilesystem層まで遅れる。

versioned refはschemaとdigestの組で検証・比較する。digestが同じでもschemaが異なるrefを同一視しない。ref schemaのversion部分はopaqueに扱い、既知versionでなくても組として保持・比較できるが、envelope自身のschemaは既知versionだけを受理する。

Operation/Resultのcanonicalizationとvalidation ruleはfixtureで固定する。protocolを変更する場合、本書、parser、fixture、testを同じchangeで更新する。
