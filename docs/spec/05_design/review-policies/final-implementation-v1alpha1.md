# Final Implementation Review Policy v1alpha1

## Purpose

`final_implementation` reviewで、承認済みテストとIssue Contractに対してfinal headが正しく、回帰、scope逸脱、重大なriskを残していないかを判定する標準観点を定義する。本policyはPRのready化（draft解除）とmergeの前に置く独立quality gateである。approveはmerge gateの品質側条件であり、live headの一致、required check、conflict、branch protectionという外形条件は[ADR-0005](../decisions/0005-auto-merge.md)に従いControllerとIssue Workerが判定する。reviewerにmergeの可否そのものを判断させない。release、deploy、merge後のrevert判断は代替しない。

## Applicability and binding

- Review Requestの`kind`が`final_implementation`の場合にだけ適用する。
- Controllerは`docs/spec/05_design/review-policies/final-implementation-v1alpha1.md`を`policyRefs`へ含める。
- repository固有policyを追加してよいが、このpolicyを省略または別policyから推測してはならない。
- required policy refが欠落し、または未対応のpath/versionを指すRequestはproviderへ渡さず、quality verdictではない入力不備として扱う。policy取得時のnetwork等の失敗はexecution failureとして分離する。
- 複数policyのauthorityが衝突し、人間の仕様判断なしに解消できない場合は`needs_human`とする。

## Authoritative inputs

Reviewerは次の明示された入力だけを使う。

- review開始時と完了時にlive sourceから再生成し、claim時の期待digestと一致したcanonical Task Contextとauthority content
- approved test validity Review Resultとapproved test-only head
- implementation patchまたはfinal source snapshot
- final headを再構築できるimmutable source artifact
- RED、GREEN、refactor後verification、Issue Verification、repository required checksのevidence
- performance観点にboundが宣言されている場合の`performance-evidence`内の測定evidence（command、実行条件、結果summary）
- test、fixture、helper、commandに対する変更と再承認のlineage
- Pull Request用summary、risk、manual verificationのdraft artifact

raw Issue body、Issue comment、親・依存Issueのprose、以前のprovider conversation、Issue Workerのmutable worktreeは評価根拠へ追加しない。
live Issueの取得とTask Context / Context Manifest identityの照合はfreshness検証に使い、raw bodyの内容を評価根拠へ追加しない。

## Review order

### 1. Deterministic prerequisites

Review handlerはsemantic reviewを開始する前に、少なくとも次を検証する。

- Review Request、Context Manifest、Execution Policy、Artifact Manifest、final headのbindingが一致する。
- live Issue/authorityから再生成したTask Context / Context Manifest identityがclaim時の期待digestと一致する。
- approved test validity Resultが同じIssue context、test-only head、test artifactへbindされている。
- approved test、fixture、helper、test commandに変更がある場合、その版に対するtest validity approvalが存在する。
- GREEN、refactor後verification、Issue Verification、required checksがfinal headにbindされ、必要なcommandが成功している。
- performance boundが宣言されている場合、`performance-evidence`に測定command、実行条件、結果summaryがあり、final headへbindされている。
- required artifactが欠落、破損、別head、staleではない。

これらのRequest integrityを検証できない場合はfindingで補わず、protocol、staleness、execution上の失敗として返す。成功evidenceの内容がIssueを十分に検証するかは後続の品質判断である。

### 2. Always-required perspectives

#### Functionality and correctness

- final behaviorが全Acceptance Criteria、Outcome、Constraintsを満たす。
- 正常系だけでなく、Issueが要求する境界、error、failure、concurrency、recovery behaviorが正しい。
- 実装とtestが同じ誤解を共有して形式的にGREENになっていない。
- implementation traceや参照されたsymbol、command、artifactがfinal headに実在する。

#### Regression and scope

- Included scopeの成果物が揃い、Excluded scopeやDecision Authorityを越えていない。
- 無関係なbehavior、public contract、configuration defaultを意図せず変更していない。
- 既存利用者、隣接component、failure pathに重大な回帰を持ち込んでいない。
- scope外の問題は本変更へ混入させず、ただし対象変更が直接作ったblocking riskを別Issueへ先送りしない。

#### Test integrity and quality

- approved testを削除、skip、弱体化、未検出化してGREENにしていない。
- test、fixture、helper、commandの変更版に対するapprovalの有無はDeterministic prerequisitesで検証し、ここでは変更がtestの検出力やtraceabilityを損なっていないかを評価する。
- 全Acceptance Criteriaへのtraceabilityと、退行を検出できる意味のあるassertionがfinal headでも維持されている。
- implementation中に判明したerror、boundary、regressionに必要なtestが不足していない。

#### Code quality

- naming、責務、構造が変更意図を明確に表し、不要なduplication、dead code、過剰な分岐を残していない。
- surrounding codeとrepository conventionに整合し、必要以上のabstractionやdependencyを追加していない。
- commentはコードの言い換えではなく、背景、制約、trade-off、不変条件を説明している。
- error handling、resource lifecycle、並行実行上のownershipが変更範囲に対して明確である。

#### Security

- untrusted inputのvalidation、injection、authentication/authorization、secret handling、権限境界に退行がない。
- unsafe default、過剰権限、credentialや機密情報のlog/artifact混入を追加していない。
- filesystem、network、process、GitHub mutation等の能力がleast authorityに保たれている。
- security surfaceを変更しないTaskでも、変更が新しい攻撃面や権限拡大を作っていないことを確認する。該当しない脅威を架空のblocking findingにしない。

#### Evidence and residual risk

- command、結果、environment、base/head、artifact referenceから判定理由を第三者が監査できる。
- summary、risk、manual verification draftが実際の変更と一致し、未確認事項を成功済みとして記載していない。
- deterministic evidenceの欠落をsemanticな自信やscoreで補っていない。

### 3. Conditional perspectives

適用可否はcanonical Task ContextのOutcome、Scope、Deliverables、Acceptance Criteria、Constraintsと、immutable diff/artifactから判定する。親・依存Issueのprose、会話履歴、file extensionだけから推測しない。安全な判定に必要なauthorityが不足し、適用有無で結論が変わる場合は`needs_human`とする。

| Perspective | 必須になる条件 | 主な確認事項 |
| --- | --- | --- |
| UX | 利用者が直接操作または観測するflow、state、message、defaultを変更する | clear state、error message、empty/loading state、sensible default、一貫した操作結果 |
| Accessibility | UI、interactive control、表示content、入力flowを変更する | semantics、keyboard operability、label/alt text、focus handling、contrast |
| Type design | public API、exported signature、domain model、state machine、schemaを変更する | illegal stateの表現防止、encapsulation、precise signature、versioning boundary |
| Performance | Issueまたはauthorityがlatency、throughput、capacity、resource boundを明示する。または利用者が性能を体感するfrontend surface、もしくはbatch/jobのような大量データ・長時間実行処理を変更する | bound宣言時はheadへbindされた測定evidenceの妥当性と上限充足。宣言がない場合はalgorithmic cost、memory/IO、明白な退行。いずれも負荷時のfailure behavior |

条件に該当しない観点を一律blockingにしない。該当する観点は常時必須観点と同じverdict semanticsで扱う。

performanceの測定はreviewの一部として実行しない。boundが宣言されたTaskでは、Issue WorkerがRED/GREENと同じ規律で固定した測定evidence（測定command、実行条件と環境identity、複数回実行の要約。web UIはLighthouse等の固定条件測定、batch/jobはbenchmarkまたは実行時間計測）を根拠に判定する。bound宣言がなく実行surfaceだけで適用された場合は、測定なしで判定できる明白な退行とalgorithmic costを評価し、budget整備の提案はadvisoryにできる。

## Verdict rules

- 全常時必須観点と全該当条件付き観点を満たし、blocking findingがない場合だけ`approve`とする。
- 同じIssue Contract内でproduction code、test review、evidenceを修正できる欠陥は`request_changes`とする。
- authority conflict、安全判断、scopeまたはproduct decisionが必要で修正方針を一意に選べない場合は`needs_human`とする。
- behavior、risk、保守性に影響しない改善提案は、verdictではなく`severity: advisory`のfindingとして残せる。
- 観点ごとのscoreや加重平均でblocking findingを相殺しない。一つのfresh sessionが全観点を評価してよく、観点ごとの別sessionを要求しない。

各findingは、違反した期待、実際の観測、対象ACまたはpolicy箇所、artifact evidenceを特定できる`expected`、`observed`、`evidenceRefs`を持つ。

## Versioning and provenance

本policyの意味を変える場合は新しいversioned pathを追加する。既存Requestは発行時のheadと`policyRefs`にbindされた内容で評価し、新しいheadのpolicyへ差し替えない。

Servoのfunctionality、code-quality、test-quality、UX、accessibility、security、type-designという分類を参考にしているが、Kudoの二段reviewとTaskごとの適用条件へ再定義した本文だけを正本とする。Servoの全観点一律panel、session fan-out、provider/effort profile、score、合議は本policyに含めない。
