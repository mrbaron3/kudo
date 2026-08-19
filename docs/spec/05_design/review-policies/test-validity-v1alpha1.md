# Test Validity Review Policy v1alpha1

## Purpose

`test_validity` reviewで、テストがIssue Contractを正しく検証するかを判定するための標準観点を定義する。本policyはReview Request / Resultのschemaを変更せず、Review Workerが明示された入力から判断する内容と境界を固定する。

テストが存在することやRED commandが失敗したことだけではapproveしない。実装が誤っても失敗できるテストであること、失敗が未実装の対象挙動に起因すること、production実装を先行させていないことまで確認する。

## Applicability and binding

- Review Requestの`kind`が`test_validity`の場合にだけ適用する。
- Controllerは`docs/spec/05_design/review-policies/test-validity-v1alpha1.md`を`policyRefs`へ含める。
- repository固有policyを追加してよいが、このpolicyを省略または別policyから推測してはならない。
- required policy refが欠落し、または未対応のpath/versionを指すRequestはproviderへ渡さず、quality verdictではない入力不備として扱う。policy取得時のnetwork等の失敗はexecution failureとして分離する。
- 複数policyのauthorityが衝突し、人間の仕様判断なしに解消できない場合は`needs_human`とする。

## Authoritative inputs

Reviewerは次の明示された入力だけを使う。

- canonical Task ContextのOutcome、Scope、Deliverables、Acceptance Criteria、Verification、Constraints、Decision Authority、Stop conditions
- Context Manifestが固定したauthority content
- implementation briefとAcceptance Criteria mapping
- test plan、test patchまたはtest-only source snapshot
- test-only headを再構築できるimmutable source artifact
- RED command、exit status、stdout/stderr、environment identity
- baseからtest-only headまでの変更範囲を確認できるevidence

raw Issue body、Issue comment、親・依存Issueのprose、以前のprovider conversation、Issue Workerのmutable worktreeは評価根拠へ追加しない。

## Review order

### 1. Deterministic prerequisites

Review handlerはsemantic reviewを開始する前に、Request binding、artifactのdigest・length、head、live Issue freshnessを検証する。欠落、破損、別head、stale inputはfindingで補わず、protocol、staleness、execution上の失敗を区別して返す。

保存されたRED evidenceの内容が期待するREDを示すかは品質判断である。commandが実行済みというmetadataだけを理由にapproveしない。

### 2. Contract traceability and scope

- 全Acceptance Criteria IDが少なくとも一つのtest caseへ対応している。
- test planからtest case、test code、expected resultを双方向に追跡できる。
- 各test caseのbehavior、test level、setup、action、expected result、予定RED理由が判断可能である。
- IssueのOutcome、Scope、Constraints、Verificationにないproduct behaviorを新しい必須仕様として追加していない。
- Excluded scopeや、人間へ留保されたDecision Authorityをtestで既成事実化していない。

AC IDをtitleへ埋め込む等の具体的な追跡形式はrepository policyに従う。形式が未指定の場合も、mapping artifactから一意に追跡できなければならない。

### 3. Behavioral validity

- 対象behaviorが壊れた場合にtestが実際に失敗する。
- assertionは実装内部の値を言い換えるだけでなく、契約上の観測可能な結果を検証する。
- 常に真になるassertion、実装と同じalgorithmの再記述、過度に緩い比較、無条件skipや無効化を含まない。
- Acceptance CriteriaまたはConstraintsが要求する正常系、境界、error、安全性のcaseを落としていない。
- test名やcommentだけでなく、実行されるassertionが主張するbehaviorと一致する。

### 4. Isolation and test design

- fakeやmockはGitHub、process、clock、filesystem、provider、telemetry等の外部境界に置き、production実装のprivate構造を不必要に固定しない。
- test順序、wall clock、network、共有mutable stateに暗黙依存せず、通常suiteで決定論的に再現できる。
- live integrationが必要な場合はopt-inであり、それだけをbehaviorの唯一の証明にしていない。
- fixture、helper、snapshotは意味のある失敗を隠さず、変更対象より広い出力を無批判にapproveしていない。

### 5. Discovery and RED causality

- repositoryの標準commandが追加testを発見し、意図したtest caseを実行している。
- REDは対象production behaviorが未実装であるために発生している。
- syntax error、dependency不足、toolchain不備、timeout、environment failure、無関係なbaseline failureをREDとしていない。
- exit statusだけでなくstdout/stderrとtest identityから、期待したfailureであることを確認できる。
- production sourceまたはproduction configを先に変更していない。test実行に必要なtest-only fixture、helper、configの変更はmappingとscopeへ明示されている。

## Verdict rules

- 全必須観点を満たし、blocking findingがない場合だけ`approve`とする。
- 同じIssue Contract内でtest、plan、fixture、RED evidenceを修正できる欠陥は`request_changes`とする。
- Issueまたはauthorityの曖昧さ、安全判断、scope決定が必要で修正方針を一意に選べない場合は`needs_human`とする。
- 命名や説明の改善だけでテストの検出力、追跡性、RED妥当性に影響しない指摘は、verdictではなく`severity: advisory`のfindingとして残せる。
- 観点ごとのscoreや加重平均でblocking findingを相殺しない。一つのfresh sessionが全観点を評価してよく、観点ごとの別sessionを要求しない。

各findingは、違反した期待、実際の観測、対象ACまたはpolicy箇所、artifact evidenceを特定できる`expected`、`observed`、`evidenceRefs`を持つ。

## Versioning and provenance

本policyの意味を変える場合は新しいversioned pathを追加する。既存Requestは発行時のheadと`policyRefs`にbindされた内容で評価し、新しいheadのpolicyへ差し替えない。

Servoのtest-qualityおよびTDD reviewの知見を参考にしているが、Kudoの二段review、fresh session、immutable handoffへ再定義した本文だけを正本とする。Servoのsession fan-out、provider/effort profile、score、合議は本policyに含めない。
