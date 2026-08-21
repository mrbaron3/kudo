# ADR-0003: Review round 上限と Escalation Policy

- Status: accepted（2026-08-19、attempt retry / resume決定を2026-08-20追記、`test_revision_required`のround消費と保存境界の修正を2026-08-21追記）
- 関連Issue: 未起票（Kudoの概念設計レビューで発見）
- 関連ADR: [ADR-0002](0002-pr-anchored-review.md)（全review roundをPRへ繋留する判断を前提とする）

## Context

`request_changes`は自動修正loopへroutingされるが、loopを止める上限がどこにも定義されていなかった。retry policyが定義していたのはtransport/execution failureに対する`bounded retry`だけであり、reviewのquality verdict `request_changes`には回数の記載が無い。reviewerが「まだ直せる」と判断し続ける限り、Runは`authoring_tests ⇄ awaiting_test_review`または`implementing ⇄ awaiting_final_review`を無限に往復しうる。

期待される挙動は「規定回数でgateを通らなければ、理由とともに`needs_human`として人間へ差し戻す」ことであり、その機構が存在しなかった。`bounded retry`の予算値そのものにも置き場所が無かった。round上限は単発の欠落ではなく、「Controllerがどれだけ自動継続してから人間へ渡すか」という予算category全体が未設計であることの最初の症状である（D8で同じEscalation Policyへ統合して解消する）。

## Decision

### D1. round上限はControllerのgate判断であり、quality verdictではない

Review Workerの契約を変更しない。verdictは3種のままで、reviewerへround数・上限・過去roundの結果を渡さず、「上限だから`needs_human`を返せ」と判断させない。上限はreviewの品質基準ではなく、Controllerが自動継続をやめる条件である。上限到達はRun phaseを`needs_human`へ送ることで表現し、Review ResultにもOperation Resultにも新しい値を追加しない。Controllerが行うのは「修正Operationを発行するか、人間へ渡すか」の選択だけであり、`request_changes`という判断自体は上書きしない。

### D2. counterはgateごとに独立、Run scope、単調増加

- `test_validity`と`final_implementation`は別々のcounterを持つ。2つのgateは失敗理由も修正Operationも異なる独立した収束過程であり、通算にすると片方のgateが荒れただけでもう片方の予算を食い潰す。
- counterはRun aggregateが持ち、同じgateへ再入してもresetしない。
- 数えるのはquality verdictが確定したroundである。attempt failure、stale input、transport failure、protocol validation errorはroundを消費しない。
- （2026-08-21追記）`test_validity`のcounterは、implement laneが返す`test_revision_required`の確定でも1を消費する。reviewerのverdictではないが、test gateを再び開いて無人loopを継続させる差し戻しであり、予算が縛る対象（無人区間のtest gate churn）は同じである。消費しないと、implement→revise→approve→implementの往復がどの予算にも数えられず、無人区間が有限にならない。

### D3. 予算の単位は無人区間であり、escalationごとにresetする

上限が縛るのは「人間が次にこのRunを見るまでに何round回すか」であって、Runの生涯合計ではない。人間へ差し戻した時点で区間が終わり、counterは0へ戻る。resume（`ai-ready`再付与）は満額の予算から始まる——resetしないと人間が直した後のreviewが予算0になり、gateが「1回だけ動く仕組み」に退化する。

当初はresumeでcounterを継続する案を採ったが、根拠にしたanti-bypass論法が弱い。`needs_human` → `ai-ready` → resumeのloopは1周ごとに人間がlabelを貼りescalation commentを読む必要があり、これは無人の暴走ではなく人間が明示的に予算を追加する行為である。round上限が防ぐべきなのは無人区間の暴走であり、人間が関与する再実行ではない。resetの実装位置はresume時ではなくescalation時とする。

生涯round数（`TotalRounds`）に対する第二段の上限は置かない。人間が介入してもgateを通らないRunは、round予算が足りないのではなく、Issue Contract、authority、分割の粒度、provider/model選択のどれかが誤っているというsignalである。上限を置くとそのsignalを読む前に数字が判断を代行する。無思考な再labelへの抑止は、gateを締めることではなく`TotalRounds`と差し戻し回数をescalation commentへ出すことで担う。各無人区間は必ず有限で終わるため、これは無制限に回すこととは違う。

### D4. 上限値は`kudo.escalation-policy/v1alpha1`としてRunへ固定する

deployment configurationからControllerが解決し、claim時にRunへpinするversioned artifactを新設する。検討した3案:

| 案 | 採否 | 理由 |
| --- | --- | --- |
| protocol coreへ定数として固定 | 不採用 | round上限は「Controllerがどれだけ自動継続するか」というpatience budgetで、deploymentごとに正当に異なる。定数にするとtuningがGo source変更になる |
| Execution Policyへfieldを追加 | 不採用 | `ExecutionPolicyRef`は`InputIdentity`の一部であり、予算を3から4へ変えるだけで進行中の全Runがsupersedeされ承認済みtestが破棄される。加えてExecution Policyはprovider実行境界というWorker側が読む値の集合であり、Controllerしか読まないgate予算を混ぜると1つのartifactが2つの意味と2つの消費者を持つ |
| 第三の場所を作る | **採用** | 消費者がControllerだけで、`InputIdentity`へ含めないためstalenessを起こさず、Runへpinするため実行中に暗黙に変わらない |

Escalation PolicyはControllerのdeployment configurationからだけ解決し、Task Issue本文・`authorityRefs`・対象repository・Worker Resultからは読まない。gateされる側がgate条件を供給できる経路を作らない。許容範囲（1〜10）だけはprotocol coreが固定する——「0で常に即escalate」も「10000で事実上無制限」もgateの意味を失わせる。

### D5. counting semantics

counterはquality verdictが確定するたびに加算し、「verdictが`request_changes`かつ加算後のround数が上限以上」なら修正Operationを発行せずescalateする。`approve`は上限到達後でも次のgateへ進む——上限は差し戻しだけを止める。既定は`test_validity` 3、`final_implementation` 3。（2026-08-21追記）`test_revision_required`の確定も同じ規則で加算・判定する。

### D6. escalationは全roundを集約し、同一性の自動判定はしない

最終roundのfindingだけでは人間が対処を選べない（同じfindingの反復=実装の問題、毎回違うfinding=契約の問題、で差し戻しmessageが根本的に変わる）。escalation commentには全roundのfindingをround順に並べたledgerを載せる。

一方で「同じ指摘か違う指摘か」の判定はKudoが行わない。reviewerに前roundのfindingを渡す案は[ADR-0002](0002-pr-anchored-review.md) D3とfresh session isolationを壊し、Controllerのfuzzy一致は「Controllerはreview判断を代替しない」境界を侵す。代わりに各findingのcanonical fingerprintをledgerへ併記する。完全一致は曖昧さのない証拠だが、**不一致は「違う指摘である」ことの証拠にはならない**片側のsignalであり、判断は人間が行う。

### D7. escalation reason codeを語彙として固定する

`ai-needs-human` commentの理由codeに語彙が無かったため、`review_round_limit_exceeded`を含む語彙を[github-routing.md](../spec/05_design/04_github-routing.md)のHuman escalation節を正本として確定する。state machineが自ら導出するcodeは外部からの明示的escalation eventでは指定できない。指定できると、上限に達していないRunを「上限到達」として停止させる等、lineageとcodeが食い違う偽装ができる。

### D8. attempt retry budgetもEscalation Policyへ固定する

attempt retryはprovider実行設定ではなく、Controllerが同じlogical Operationを無人で何回再実行するかという自動継続gateである。Execution Policyへ置くと値の変更がsemantic stalenessを起こすため、review round上限と同じ`kudo.escalation-policy/v1alpha1`のrequired field `attemptRetries`（既定3、範囲1〜10）としてRun開始時にpinする。

- counterはlogical Operationかつ無人区間単位で持ち、failure class間で共有する。途中でclassが変わっても上限を迂回できない。
- `provider_invalid_response`（raw outputをversioned Resultへdecodeできない失敗）だけをbounded retryし、構築済みimmutable inputの`ProtocolError`は受理せず`failed_terminal` / `protocol_validation_failed`として同じinputをretryしない。
- quality verdict、stale input、protocol validation errorはcounterを消費しない。escalationで無人区間counterを0へ戻すが、Attempt lineageと生涯Attempt数は戻さない。

### D9. `needs_human`からのresumeは複合identityで判定する

`ContextManifestRef`だけでは、停止後にExecution Policy、published head、artifact、policy、PR、approval bindingが変わっても同じRunを再開できてしまう。停止時に`InputIdentity`（Context Manifest / Execution Policy ref）と`CheckpointIdentity`（`StoppedAt`、有効なhead、Artifact Manifest ref、policyRefs、PR ref、approval binding）の二層を`ResumeIdentity`としてdurableに固定し、resume時のlive照合で「同一なら保存済み`StoppedAt`へ戻す / `InputIdentity`が変わればsupersede / `CheckpointIdentity`だけが変われば以前のapprovalをstaleにして同じRunはresumeしない」を判定する。Observationだけの差分はaudit lineageへ追記しidentityを維持する。resumeは自動retryではなく、毎回人間の`ai-ready`を必要とする。

## 設計の現在形

本ADRが導入した規範は仕様側と実装を正本とする。

- Escalation Policy schema、counting規則、保存境界（Artifact Storeに置かずPostgreSQLのtyped dataとしてRunへ固定）: [task-context-v1alpha1.md](../spec/05_design/contracts/task-context-v1alpha1.md)「Escalation Policy」「Canonical payload and persistence boundary」
- configuration key: [runtime-platform.md](../spec/05_design/03_runtime-platform.md)
- round上限・escalation・resumeのworkflow遷移: [workflow.md](../spec/05_design/02_workflow.md)、[architecture.md](../spec/05_design/01_architecture.md)、[04_features/05](../spec/04_features/05_recovery-and-escalation/)
- reason code語彙: [github-routing.md](../spec/05_design/04_github-routing.md)「Human escalation」
- PostgreSQL schema: `internal/adapter/postgres/migrations/`が正本。本ADRの旧版に置いていたSQL草案は実装確定時に細部が変わっており（列名の簡素化、`attempt_retry_limit`列は置かず範囲検証をprotocol coreへ一本化）、草案は削除した。

## Consequences

### 影響を受ける文書

workflow.md、architecture.md、github-routing.md、contracts/task-context-v1alpha1.md、runtime-platform.md、implementation-plan.md（上記「設計の現在形」の各正本）。

### 利点

- 自動loopが必ず有限時間で終わり、収束しないIssueがprovider予算とrate limitを無制限に消費しない。
- 止まった理由が「reviewerの判断」と「Controllerの予算切れ」に分離され、reason codeで区別できる。
- 全roundのledgerにより、差し戻しを受けた人間が「実装の問題か契約の問題か」を判断できる材料が揃う。
- 予算値の変更が進行中Runをsupersedeせず、tuningとworkflow correctnessが独立する。

### 代償・リスク

- Run aggregate、Run store schema、claim pathにfieldが増える。
- 上限が低すぎると、本来自動収束できたIssueを人間へ返す。
- Run全体のround/Attempt数に静的な上限が無く、差し戻しを繰り返せば総コストは増え続ける。これはD3/D8の意図した帰結であり、人間が数字を読まずに`ai-ready`を貼り直し続けた場合、Kudoは止めない。
- fingerprintの完全一致は稀にしか起きず、多くの場合ledgerは人間の読解に委ねられる。自動判定を持ち込まない選択の代償である。

### 未決事項（deferred）

- **base churnによるsupersede loop**: 追従先baseが動き続けるとRunがsupersedeを繰り返しcounterもresetし続ける。round上限とは別のlivelockであり、base pinning policyの問題として別に扱う。
- **findingの意味的同一性判定**: fingerprint完全一致以上の判定を行う場合は、reviewerの継続性宣言（ADR-0002 D3の見直しを伴う）か、Controller外のoffline分析として設計する。

## Revisit conditions

- 実運用のAttempt/round分布で既定値3が不適合（自動収束できたIssueの差し戻しが常態化、または上限到達がほぼ起きない）と実測された場合、既定値をdeployment configurationの見直しとして扱う。それでも解けない場合のみ本ADRの再検討とする。
- base churnによるsupersede loopが実際に観測された場合、base pinning policyを別ADRで設計する。
- 差し戻しledgerが人間の判断材料として機能していない（読まれずに再labelされる）ことが繰り返し観測された場合、D6の「自動判定しない」選択を再検討する。
