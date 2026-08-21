# Task Context Protocol v1alpha1

## Purpose

strict parse済みのTask Issueから、GitHub上で観測したexactな本文と、AIへ渡す意味的な実行入力を分離する。Issue Compilerは外部I/Oを持たないpure boundaryであり、raw bodyとGitHubから検証済みのIssue identityを入力に、次を決定論的に生成する。

- `kudo.issue-observation/v1alpha1`: live変更検知と監査に使うexact observation
- `kudo.task-context/v1alpha1`: model sessionへ渡すcanonical Task表現
- `ClaimRequirements`: claim時に解決するrelationshipとauthorityのstable projection

authority、dependency completion、base commitはCompilerの外で解決し、`kudo.context-manifest/v1alpha1`へ固定する。provider/model/adapter/tool/timeoutの選択は`kudo.execution-policy/v1alpha1`へ、Controllerが自動継続をやめるまでの予算は`kudo.escalation-policy/v1alpha1`へ固定する。

## Issue Compiler boundary

Compilerの入力は次の二つだけである。

- GitHub APIから得たraw Issue body string
- GitHub APIまたは検証済みevent envelopeから得た`github://owner/repository/issues/<number>` identity

Compilerは`kudo.issue/v1alpha1`のContract field、H2 section title、Acceptance Criterionを解釈する唯一のapplication-facing boundaryである。claim以降のController、Issue Worker、Review Workerはparserの`Task`、`Contract`、section titleを読み直さず、`ClaimRequirements`とversioned artifact/refだけを受け取る。

CompilerはGitHub、repository、filesystem、clock、provider、Artifact Storeへ接続しない。bodyがUTF-8でない、bodyがcontrol characterを含む、Issue identityが欠落・不正、またはIssue Contractがstrict parseに失敗した場合はartifactを返さず、構造化されたvalidation errorを返す。

body中のcontrol characterは信頼境界で拒否する。許可するのは改行（LFまたはCRLF）とTABだけであり、NUL、ESC、単独のCR、DELを含むその他のC0 controlは`body_control_character`として拒否する。これらはcanonical YAMLとPostgreSQLのtext / jsonbへ格納できないため、compile後の保存時点まで失敗を遅らせない。

Issue identityのowner / repositoryはGitHubに合わせて大文字小文字を区別しない。CompilerはcallerのIssue identityと本文中のreferenceの双方を小文字へ正規化してからcanonical encodeする。表記のcase差分は意味の差分ではないため、refを変えてはならない。

Compiler algorithmは`kudo.issue-compiler/v1alpha1`として識別する。同じraw body、Issue identity、Compiler versionは、別processでも同じcanonical bytes、ref、`ClaimRequirements`を生成しなければならない。

## Issue Observation

Issue Observationは取得したTask Issueのidentityとexact body digestだけを表す。

```yaml
schema: "kudo.issue-observation/v1alpha1"
issue: "github://owner/repository/issues/42"
bodyDigest: "sha256:<digest>"
```

`bodyDigest`はraw body stringを追加の改行・空白正規化なしにUTF-8 bytesへ変換して計算する。raw body bytesは同じdigestのcontent-addressed artifactとして別に保存し、Observationへ本文を複製しない。Observation artifact自身のSHA-256 digestとschemaを組にした`IssueObservationRef{schema,digest}`を公開する。

取得時刻、delivery ID、Run ID、label、assignee、commentはObservation identityへ含めない。同じIssue identityとraw bodyは同じObservation refを持ち、CRLF/LF、template HTML comment、末尾改行を含むraw bodyのbyte差分はObservation refを変える。

## Task Context

Task ContextはAI実行入力の正本であり、raw Markdown本文の代わりにmodel sessionへ渡す。

```yaml
schema: "kudo.task-context/v1alpha1"
compiler: "kudo.issue-compiler/v1alpha1"
issue: "github://owner/repository/issues/42"
contract:
  schema: "kudo.issue/v1alpha1"
  kind: "task"
  readiness: "ready"
  parent: null
  dependsOn: []
  acceptanceCriteriaIds:
    - "AC-1"
  authorityRefs: []
outcome: "外部から観測できる結果"
scope: "### Included\n\n- 対象\n\n### Excluded\n\n- 対象外"
deliverables: "- 成果物"
acceptanceCriteria:
  preamble: ""
  criteria:
    - id: "AC-1"
      body: "- Given: 前提\n- When: 操作\n- Then: 期待結果"
verificationAndEvidence: "- `mise run check`"
constraintsAndInvariants: "- 不変条件"
decisionAuthority: "- このIssue内で決めてよい: 内部構成"
stopAndEscalationConditions: "- 停止条件"
advisoryHints: null
```

`TaskContextRef{schema,digest}`のdigestは上記canonical YAML bytesへ計算する。schemaを省略したdigest単体を公開refとして扱わない。

Compilerは次をTask Contextから除外する。

- raw bodyのline ending差分。canonical contentはLFを使う
- code fence外にある、Issue Contractで許可済みの行全体HTML comment
- Markdown headingやContract fieldのsource line number
- Contract fenced blockの空行、comment、表記上のlayout。strict parse後のtyped valueだけを固定順でencodeする
- GitHub API response、Issue comment、label、Project field、assignee、native relationship metadata

各section contentはcomment除去後に先頭・末尾の空行を除き、内部のMarkdownと空行を保持する。code fence内のcomment記号は通常のcontentとして保持する。`Acceptance Criteria`はcriterion前のpreambleと、Contractの宣言順に並ぶ`{id,body}`へCompilerが分解する。Issue Contractはsection内のcriterion順が`acceptanceCriteriaIds`と一致することを要求するため、Compilerは並べ替えを行わない。任意の`Advisory Hints`が無い場合は`null`、存在する空sectionは空stringとして区別する。

Outcome、Scope、Deliverables、Acceptance Criteria、Verification、Constraints、Decision Authority、Stop Conditions、Advisory Hintsまたはtyped Contractの意味が変わればTask Context refも変わる。raw bodyだけの非意味的差分ではIssue Observation refだけが変わり、Task Context refは変わらない。

## ClaimRequirements

`ClaimRequirements`は次のfieldだけを保持する。

- `readiness`
- `parent`
- `dependsOn`
- `authorityRefs`

これはclaimでlive stateとcontentを解決するためのprojectionであり、Task Contextの代替ではない。parentはidentityとrelationship検証だけに使い、parent本文を暗黙にcontextへ加えない。dependencyはreadiness gateであり、本文や会話履歴を暗黙に継承しない。parentまたはdependencyの本文が実装authorityならTask自身の`authorityRefs`にも明示しなければならない。

## Context Manifest

Context ManifestはTask Contextから解決した実装入力のclosureである。

```yaml
schema: "kudo.context-manifest/v1alpha1"
taskContext:
  schema: "kudo.task-context/v1alpha1"
  digest: "sha256:<digest>"
baseSha: "<git-commit-sha>"
parent: null
dependencies:
  - issue: "github://owner/repository/issues/101"
    completionDigest: "sha256:<digest>"
authorityRefs:
  - ref: "docs/spec/05_design/01_architecture.md"
    contentDigest: "sha256:<digest>"
```

親が無い場合は`parent: null`、dependencyまたはauthorityが無い場合は`[]`を明示する。配列順はIssue Contractの宣言順を保ち、同じidentityを重複させない。repository pathは固定した`baseSha`から、Issue authorityはGitHubから直接解決し、取得bytesのdigestを記録する。

manifestが解決結果であることは呼び出し側の規律ではなくencode境界で強制する。manifestのencodeには対応する`ClaimRequirements`を渡し、`parent`、`dependencies`、`authorityRefs`のidentityと件数と順序がTask Contextの宣言と一致しない場合は拒否する。manifestが自由に決められるのは解決結果、すなわち`baseSha`、`completionDigest`、`contentDigest`だけである。`authorityRefs`の順序はauthorityの優先順位を表すため、集合一致では不十分である。

Context Manifestは`TaskContextRef`、base、parent identity、dependency completion、authority content digestだけを持つ。`IssueObservationRef`、`bodyDigest`、raw body、authority本文、Run ID、claim timestamp、workspace path、provider session IDを含めない。したがって、Task Contextと解決済み入力が同じなら、raw bodyだけの非意味的差分でContext Manifest refは変わらない。

## Execution Policy

Execution PolicyはRun開始時にIssue WorkerとReview Workerのprovider実行境界を固定する。

```yaml
schema: "kudo.execution-policy/v1alpha1"
issueWorker:
  provider: "codex"
  model: "gpt-5.5"
  adapter: "codex-cli"
  adapterVersion: "1.2.3"
  toolPermissions:
    - "github:write"
    - "repository:write"
  timeout: "45m0s"
reviewWorker:
  provider: "claude"
  model: "claude-opus"
  adapter: "claude-cli"
  adapterVersion: "2.0.0"
  toolPermissions:
    - "repository:read"
  timeout: "30m0s"
```

tool permissionは順序を持たない集合としてlexicographic順にencodeし、重複を拒否する。timeoutは正のGo duration canonical stringとする。credential、token、secret path、provider session ID、workspace pathはfieldとして持たない。provider、model、adapter、adapter version、tool permission、timeoutのいずれかが変われば`ExecutionPolicyRef{schema,digest}`だけが変わり、Task ContextまたはContext Manifestへ波及させない。

## Escalation Policy

Escalation PolicyはRun開始時に、Controllerが自動継続をやめて人間へ渡すまでの予算を固定する。

```yaml
schema: "kudo.escalation-policy/v1alpha1"
attemptRetries: "3"
reviewRounds:
  testValidity: "3"
  finalImplementation: "3"
```

`attemptRetries`は、一つのlogical Operationについて初回Attemptの後に作成できる追加Attempt数である。既定は`3`、許容範囲は`1`以上`10`以下で、既定では初回を含め最大4 Attemptになる。retryableなAttempt failureをdurableに確定して次のAttemptを作成するたびに1を消費し、timeout、rate limit、一時network failure、provider crash、GitHub transport failure、providerのinvalid structured outputで同じ予算を共有する。failure classが途中で変わってもcounterを分けない。quality verdict、stale input、immutableなprotocol validation errorは消費対象ではない。protocol validation errorはWorker Result / AttemptFailureとして受理せず`ProtocolError`を別記録し、同じinputをretryせずOperation queue stateを`failed_terminal`、Runを`protocol_validation_failed`の`needs_human`へ送る。

attempt retryのcounterはlogical Operationかつ無人区間ごとに独立して持つ。初回Attemptは予算を消費せず、escalation時に区間counterを0へ戻すが、Attempt lineageと生涯Attempt数は戻さない。人間が`ai-ready`を再付与して同じRunをresumeした場合は、新しい無人区間の初回Attemptから開始する。

`reviewRounds`はreview gateごとに`request_changes`の自動修正loopを続けるround数の上限である。`test_validity`と`final_implementation`は独立した上限と独立したcounterを持ち、通算しない。各値は`1`以上`10`以下でなければならず、範囲はprotocol coreが固定してconfigurableにしない。`0`は「最初の`request_changes`で必ずescalate」、過大な値は事実上の無制限であり、どちらもgateとしての意味を失わせる。`attemptRetries`を含む整数はArtifact Manifestの`length`と同じくdecimal stringとしてencodeする。

Escalation PolicyはControllerのdeployment configurationからだけ解決する。Task Issue本文、`authorityRefs`、変更対象repositoryの内容、Worker Resultからは読まない。gateされる側がgate条件を供給できる経路を作らない。

`EscalationPolicyRef{schema,digest}`はRunへ記録するが、Runのsemantic input identityには含めない。attempt retry上限とreview round上限はController側の自動継続判断だけに使い、Workerまたはreviewerの判断入力へ渡さないため、値の変更は既存のOperation identity、Review Request、approvalをstaleにしない。deployment configurationの変更は次のclaimから有効になり、進行中のRunはpin済みの値を使い切る。判断の背景は[ADR-0003](../decisions/0003-review-round-limit.md)を正とする。

provider、model、adapter version、tool permission、timeout、credential、secret path、session IDをfieldとして持たない。実行境界はExecution Policyが固定し、両者の役割を重ねない。

## Canonical encoding

Issue Observation、Task Context、Context Manifest、Execution Policy、Escalation Policyのcanonical YAMLは次の規則を共有する。

- encodingはUTF-8、line endingはLF、末尾は1個のLF
- key順はschemaごとに固定し、Go map iterationへ依存しない
- indentはspace 2個。tabを使わない
- string scalarはすべてdouble quoteし、改行やcontrol characterをescapeする
- optionalな単一値は省略せず`null`、空listは省略せず`[]`と書く
- YAML anchor、alias、tag、implicit type、document marker、flow-style mappingを生成しない
- SHA-256は`sha256:`に64桁のlowercase hexを続ける

byte-levelの正本は`internal/contract/testdata/canonical/`のgolden fixtureである。key、表現、除外fieldを変更する場合は、該当schema version、本書、encoder、fixture、testを同じchangeで更新する。

golden fixtureはencoder自身の出力から生成するため、変化検出器ではあってもYAMLとしての正しさの検出器ではない。canonical bytesが実際にYAMLとしてparseでき、string scalarがescape前の値へ戻ることは、独立実装のYAML parserを差分オラクルとして検証する。CIでは`KUDO_YAML_ORACLE=required`を設定し、oracleが見つからない場合にskipせず失敗させる。

## Artifact payload contract

Artifact Storeへ渡すpayloadは少なくとも次を持つ。

| Field | Meaning |
| --- | --- |
| `kind` | `raw-issue-body`、`issue-observation`、`task-context`、`context-manifest`、`execution-policy`、`escalation-policy`、`artifact-manifest`、`pull-request-observation` |
| `schema` | versioned YAML artifactのschema。raw bodyは空 |
| `mediaType` | raw bodyは`text/markdown; charset=utf-8`、canonical artifactは`application/yaml; charset=utf-8` |
| `digest` | `data`のSHA-256 |
| `data` | write-onceで保存するexact bytes |

store前とread時にdigest/data、kind、schema/refの一致を検証する。同じdigestへ異なるbytesを保存せず、既存payloadをin-place更新しない。

Escalation Policy payloadのkindは`escalation-policy`、この版のschemaは`kudo.escalation-policy/v1alpha1`である。`execution-policy` kindへ保存したり、kindからschemaを推測してrefのschemaを省略したりしない。encoderはこの組を生成し、readerは`EscalationPolicyRef{schema,digest}`、payload kind、payload schema、payload digest、bytesをすべて照合する。Artifact kindは保存用途、schemaはversioned payloadの解釈を表す別の値空間である。

## Versioning and compatibility

Issue Contract、Issue Compiler、Issue Observation、Task Context、Context Manifest、Execution Policy、Escalation Policyは独立したschema/version identityを持つ。

- Issue Contractのfield/Markdown semanticを変更する場合は`kudo.issue/...`をversion upする。Task Contextの表現が互換ならTask Context schemaを連動変更しない
- Task Contextのkey順、field、scalar/list/null表現等に非互換変更を加える場合は`kudo.task-context/...`を新しくし、旧schemaと併存させる
- exact observation、resolved input、execution settingの表現変更はそれぞれ対応するschemaだけをversion upする
- refは常にschemaとdigestの組で比較し、digestだけからschemaを推測しない
- readerはref/payloadのschemaとdigestを検証した後、保存bytesをdecode/re-encodeせずそのまま読む。active Runが参照する旧artifactを新encoderの出力で上書きしない

新しいCompilerは旧Task Context artifactを移行済み最新版へ見せかけない。新しいschemaが必要な入力は新しいartifact/refを生成し、既存Runはclaim時に固定した旧schema/refを継続して参照する。
