# ADR-0003: Review round 上限と Escalation Policy

- Status: accepted（2026-08-19、attempt retry / resume決定を2026-08-20追記）
- 関連Issue: 未起票（Kudoの概念設計レビューで発見）
- 関連ADR: [ADR-0002](0002-pr-anchored-review.md)（全review roundをPRへ繋留する判断を前提とする）

## Context

`request_changes`は自動修正loopへroutingされるが、loopを止める上限がどこにも定義されていない。

- [architecture.md](../01_architecture.md)のretry policyは、`bounded retry`をtimeout、rate limit、network failure、invalid provider outputというtransport/execution failureに対してだけ定義する。
- [operation-protocol-v1alpha1.md](../contracts/operation-protocol-v1alpha1.md)のattempt failureも同じくexecution failure側の概念である。
- 一方でreviewのquality verdict `request_changes`には回数の記載が無い。[github-routing.md](../04_github-routing.md)は「test/final reviewの`request_changes`は自動修正loopなので`ai-in-progress`を保つ」とだけ述べ、[workflow.md](../02_workflow.md)のstate図は`awaiting_test_review → authoring_tests`と`awaiting_final_review → implementing`を無条件の辺として描く。

したがってreviewerが「まだ直せる」と判断し続ける限り、Runは`authoring_tests ⇄ awaiting_test_review`または`implementing ⇄ awaiting_final_review`を無限に往復しうる。現行設計で自動loopを止められるのは、reviewer自身がauthorityまたは安全判断が必要だと判断して`needs_human`を返した場合だけである。

期待される挙動は「規定回数でgateを通らなければ、理由とともに`needs_human`として人間へ差し戻す」ことであり、その機構が存在しない。

`bounded retry`の予算値そのものにも置き場所が無かった。round上限は単発の欠落ではなく、「Controllerがどれだけ自動継続してから人間へ渡すか」という予算category全体が未設計であることの最初の症状である。この欠落はD8で同じEscalation Policyへ統合して解消する。

## Decision

### D1. round上限はControllerのgate判断であり、quality verdictではない

- Review Workerの契約を変更しない。verdictは`approve`、`request_changes`、`needs_human`の3種のままで、新しいverdictを追加しない。
- reviewerへround数、上限、過去roundの結果を渡さない。reviewerに「上限だから`needs_human`を返せ」と判断させない。上限はreviewの品質基準ではなく、Controllerが自動継続をやめる条件である。
- Run phase、Operation outcome、Review verdictの3つの値空間を混ぜない。上限到達は`Decide`がRun phaseを`needs_human`へ送ることで表現し、Review ResultにもOperation Resultにも新しい値を追加しない。

Controllerが行うのは「reviewerが返した`request_changes`に対して修正Operationを発行するか、人間へ渡すか」の選択だけであり、`request_changes`という判断自体は上書きしない（[architecture.md](../01_architecture.md)「Controllerはreviewerの品質verdictをapproveに変更しない」を維持する）。

### D2. counterはgateごとに独立、Run scope、単調増加

- `test_validity`と`final_implementation`は別々のcounterを持つ。2つのgateは失敗理由も修正Operation（`revise_tests` / `repair_implementation`）も異なる独立した収束過程であり、通算にすると片方のgateが荒れただけでもう片方の予算を食い潰す。
- counterはRun aggregateが持ち、同じgateへ再入してもresetしない。
- 数えるのはquality verdictが確定したroundだけである。attempt failure、stale input、transport failure、protocol validation errorはroundを消費しない。いずれもverdictではなく、attempt/transport failureはretry budget、protocol validation errorはResult非受理・即時terminal routingの別経路で扱う。

### D3. 予算の単位は無人区間であり、escalationごとにresetする

上限が縛るのは「人間が次にこのRunを見るまでに何round回すか」であって、Runの生涯合計ではない。したがって人間へ差し戻した時点で区間が終わり、counterは0へ戻る。理由codeによらず、あらゆるescalationが区間の終わりである。

- **resume**（停止したRunの再開）: 満額の予算から始まる。resetしないと、人間が直した後のreviewが予算0になり、次の`request_changes`で即座に再escalateする。1roundで収束する修正にもautomationが追従できず、gateを付けたことでloopが「1回だけ動く仕組み」に退化する。
- **supersede**（semantic inputが変わり新しいRunを作る）: 新しいRun identityなので当然0から始まる。
- **生涯counter**（`TotalRounds`）はresetしない。ledgerとtelemetryが「このIssueに通算何round費やし、何回差し戻したか」を示すために使う。

当初はresumeでcounterを継続する案を採ったが、根拠にしたanti-bypass論法が弱い。`needs_human` → `ai-ready` → resumeのloopは1周ごとに人間がlabelを貼る必要があり（`ai-ready`は人間所有のtriggerである）、しかも1周ごとにescalation commentを読ませる。これは無人の暴走ではなく、人間が明示的に予算を追加する行為そのものである。round上限が防ぐべきなのは無人区間の暴走であり、人間が関与する再実行ではない。

無思考な再labelに対する抑止は、gateを締めることではなく`TotalRounds`と差し戻し回数をescalation commentへ出すことで担う。数字が見えていれば、同じ差し戻しを4回目に受けた人間はloopを回し直すのではなくIssueを直す。

resetの実装位置はresume時ではなく**escalation時**とする。どの理由のescalationも同じ停止経路を通るため、そこでresetすればD9のresumeは満額から始まる。停止したRunがどのphaseで何round使ったかは、escalation actionと`TotalRounds`が保持する。

生涯round数（`TotalRounds`）に対する第二段の上限は**置かない**。人間が介入してもgateを通らないRunは、round予算が足りないのではなく、Issue Contract、authority、分割の粒度、あるいはExecution Policyのprovider/model選択のどれかが誤っているという signal である。上限を置くとその signal を読む前に数字が判断を代行し、しかも「これ以上は無理」という結論を機械が出すことになる。どの前提が誤っているかは差し戻しのたびに人間が判断する。

回数で自動停止させないことは、無制限に回すこととは違う。各無人区間は必ず有限で終わり、区間の終わりごとに人間へledgerが提示される。Kudoが保証するのは「無人で回り続けないこと」と「判断材料を毎回渡すこと」であり、「何回で諦めるか」は判断そのものなので自動化しない。

supersedeの既知の穴として、base追従でContext Manifest refが変わった場合も新しいRunになるため生涯counterごとresetする。base churnによるsupersede自体が既にloop源であり、round上限とは別問題として「未決事項」へ置く。

### D4. 上限値は`kudo.escalation-policy/v1alpha1`としてRunへ固定する

deployment configurationからControllerが解決し、claim時にRunへpinするversioned artifactを新設する。

検討した3案:

| 案 | 採否 | 理由 |
| --- | --- | --- |
| protocol coreへ定数として固定 | 不採用 | 必須logical name集合は「Resultが契約を満たすか」というprotocol conformance条件であり、緩めるとprotocolの意味が壊れる。round上限は「Controllerがどれだけ自動継続するか」というpatience budgetで、安価で速いmodelなら5、高価なmodelなら2というようにdeploymentごとに正当に異なる。定数にするとtuningがGo source変更になる |
| Execution Policyへfieldを追加 | 不採用 | `ExecutionPolicyRef`はRunの`InputIdentity`の一部であり、変化は`SemanticInputChanged`すなわちsupersedeを起こす。自動継続予算を3から4へ変えるだけで進行中の全Runがsupersedeされ、承認済みtestが破棄される。attempt retry/review round上限はWorkerまたはreviewerの判断入力ではないため、stalenessを引き起こしてはならない。加えてExecution Policyはprovider実行境界（provider/model/adapter version/tool permission/timeout）というWorker側のadapterが読む値の集合であり、Controllerしか読まないgate予算を混ぜると1つのartifactが2つの意味と2つの消費者を持つ |
| 第三の場所を作る | **採用** | 消費者がControllerだけで、reviewerにもWorkerにも渡らない。`InputIdentity`へ含めないためstalenessを起こさない。Runへpinするため実行中に暗黙に変わらない |

規則:

- Escalation PolicyはControllerのdeployment configurationからだけ解決する。Task Issue本文、`authorityRefs`、変更対象repositoryの内容、Worker Resultからは読まない（[operation-protocol-v1alpha1.md](../contracts/operation-protocol-v1alpha1.md)の「Issue本文からproviderを推測しない」と同じ規律）。gateされる側がgate条件を供給できる経路を作らない。
- `EscalationPolicyRef{schema,digest}`をRunへ記録する。人間が「なぜ3回で止まったのか」を問うたとき、答えがdigestで確定する。
- `InputIdentity`には含めない。Escalation Policyが変わっても既存のOperation identity、Review Request、approvalはstaleにならない。Worker/reviewerがこのpolicyを判断入力として読まないためである。
- deployment configurationの変更は次のclaimから有効になる。進行中のRunはpin済みの値を使い切る。
- 許容範囲だけはprotocol coreが固定する。`1 <= attemptRetries <= 10`かつ`1 <= reviewRounds.* <= 10`を満たさないpolicyはencode境界でrejectする。値そのものはdeployment判断だが、「0にして常に即escalate」も「10000にして事実上無制限」もgateの意味を失わせるため、範囲はconfigurableにしない。

### D5. counting semantics

- 上限はそのgateで実行するreview roundの最大数である。
- counterはquality verdictが確定するたびに加算し、gate判定は「verdictが`request_changes`かつ加算後のround数が上限以上」なら修正Operationを発行せずescalateする。
- 自動修正Operationの回数は`上限 - 1`になる。`1`は「最初の`request_changes`で即escalate、自動修正は行わない」を意味する。
- 既定は`test_validity` 3、`final_implementation` 3とする。
- counterは無人区間ごとの値である。差し戻しを挟めば同じgateで再び3roundまで回せる。

例（上限3）:

| round | verdict | 結果 |
| --- | --- | --- |
| 1 | `request_changes` | 1 < 3 なので修正Operationをdispatch |
| 2 | `request_changes` | 2 < 3 なので修正Operationをdispatch |
| 3 | `request_changes` | 3 >= 3 なので`needs_human`へescalate |
| 3 | `approve` | 上限に達していても次のgateへ進む。上限は`request_changes`だけを止める |

### D6. escalationは全roundを集約し、同一性の自動判定はしない

最終roundのfindingだけでは、人間が対処を選べない。

- 同じfindingの反復 = 実装が指摘を直せていない。実装能力・context・provider選択の問題。
- 毎回違うfinding = 何を作るべきかが決まっていない。Issue Contractまたはauthorityの問題。

差し戻しmessageが根本的に変わるため、escalation commentには**全roundのfindingをround順に並べたledger**を載せる。

一方で、「同じ指摘か違う指摘か」の判定はKudoが行わない。

- reviewerに前roundのfindingを渡して継続性を宣言させる案は、[ADR-0002](0002-pr-anchored-review.md) D3「前roundの観点別結果を持ち越さない」とfresh session isolationを壊す。
- Controllerがfuzzy一致で判定する案は、control planeにheuristicを持ち込み、「Controllerはreview判断を代替しない」という境界を侵す。model由来のfinding `id`はround間で安定する保証が無いため、id一致による判定も成立しない。

代わりに、各findingのcanonical fingerprint（`severity`、`summary`、`expected`、`observed`から計算するdigest）をledgerへ併記する。完全一致は「reviewerが字義どおり同じことを再度述べた」という曖昧さのない証拠になる。**不一致は「違う指摘である」ことの証拠にはならない**片側のsignalであり、その旨をledgerに明記する。判断そのものは人間が行う。

Run aggregateはfinding本文もfingerprintも保持しない。findingはimmutableなReview Result artifactとして既に永続化され、round順序はReview binding recordが持つ。Controllerはescalation投影時にlineageを読んでledgerを組み立てる。Run aggregateはcounterとreason codeだけを持つ。

### D7. escalation reason codeを語彙として固定する

[github-routing.md](../04_github-routing.md)は`ai-needs-human` commentに「理由code」を含めることを要求しているが、語彙が定義されていなかった。round上限のescalationを識別可能にするため、ここで語彙を確定する。

`review_round_limit_exceeded`を含む語彙は[github-routing.md](../04_github-routing.md)の Human escalation 節を正本とする。state machineが自ら導出するcode（`review_needs_human`、`review_round_limit_exceeded`、`retry_budget_exhausted`、`protocol_validation_failed`）は、外部からの明示的escalation eventでは指定できない。指定できると、counterが上限に達していないRunを「上限到達」として停止させたり、検証済みResultをprotocol違反として偽装したりでき、lineageとcodeが食い違う。

### D8. attempt retry budgetもEscalation Policyへ固定する

attempt retryはprovider実行設定ではなく、Controllerが同じlogical Operationを無人で何回再実行するかという自動継続gateである。Execution Policyへ置くと、値を変えただけで`ExecutionPolicyRef`が変わり、semantic stalenessによってOperation identityとapprovalを無関係に失効させる。したがって、review round上限と同じ`kudo.escalation-policy/v1alpha1`へrequired field `attemptRetries`として置き、Run開始時にpinする。

- `attemptRetries`は初回Attemptを含まず、その後に作成できる追加Attempt数である。既定は`3`、許容範囲は`1`〜`10`とする。既定では一つの無人区間につき初回を含め最大4 Attemptになる。
- 消費単位は、retryableなAttempt failureをdurableに確定した後、同じlogical Operationへ次のAttemptを一つ作成することとする。schedulerの再起動、重複delivery、同じAttemptの再観測では二重消費しない。
- counterはlogical Operationかつ無人区間単位で持つ。failure classごとに別budgetを持たず、`timeout`、`rate_limit`、`network`、`provider_crash`、`github_transport`、`provider_invalid_response`で共有する。途中でclassが変わっても上限を迂回できない。
- `provider_invalid_response`はproviderのraw outputをversioned Resultへdecodeできない失敗である。unsupported schema、missing required field、unknown kind、ref/digest不一致等、既に構築されたimmutable input/envelopeの`ProtocolError`とは区別する。前者だけをbounded retryする。後者はWorker Result / AttemptFailureとして受理せず、`ProtocolError`をdurableに記録してOperation queue stateを`failed_terminal`、Runを`protocol_validation_failed`の`needs_human`へ送り、同じinputをretryしない。
- quality verdict、stale input、protocol validation errorはcounterを消費しない。retry上限到達時は新しいAttemptを作らず、reason code `retry_budget_exhausted`で`needs_human`へ送る。
- escalationで無人区間counterを0へ戻すが、Attempt numberと生涯Attempt数は戻さない。人間が`ai-ready`を再付与したresumeは、新しい無人区間へ明示的に予算を与える行為である。

既定値`3`は、このADRで既に採用済みのController自動継続budgetの既定と揃える。retryとreviewはcounterも消費事象も共有せず、同じ値であることを相互変換の根拠にはしない。

### D9. `needs_human`からのresumeは複合identityで判定する

`ContextManifestRef`だけでは、停止後にExecution Policy、published head、artifact、policy、PR、approval bindingが変わっても同じRunを再開できてしまう。停止時に次の二層を`ResumeIdentity`としてdurableに固定する。

| 層 | field |
| --- | --- |
| `InputIdentity` | `ContextManifestRef`、`ExecutionPolicyRef` |
| `CheckpointIdentity` | `StoppedAt`、phaseで有効なfixed/published/checks head、`ArtifactManifestRef`、ordered `policyRefs`、Pull Request ref、test/final approvalのReview Result binding |

Issue ObservationとPull Request Observationはaudit lineageなので含めない。Escalation Policyと無人区間counterもsemantic inputではなく、resume時に現在のdeployment値へ差し替えないため含めない。optionalなcheckpoint fieldは停止phaseで未作成なら「欠落」として比較し、callerの都合で省略して一致扱いにしない。

人間による`ai-ready`再付与を確認した再reconciliationは、live Issueから現在の`InputIdentity`を構築し、durable checkpointとlive PRから`CheckpointIdentity`を照合する。次をpaused Runのversion確認、writer排他、次Operationのenqueueと同じtransactionで決める。

- 全fieldが同一なら、同じRunを`needs_human`から保存済み`StoppedAt`へ戻す。active phaseでは対応するOperation、publish/finalize phaseではidempotent mutation recovery、review待ちphaseでは同じimmutable inputに対するfresh review Attemptをdispatchする。
- `InputIdentity`が変わりvalidな新規inputを構築できる場合は旧Runをsupersededにして新Runをclaimする。以前のapprovalは移さない。
- `CheckpointIdentity`だけが変わった場合は以前のapprovalをstaleとし、同じRunをresumeしない。validな新規inputとして安全にclaimできる場合だけsupersedeし、PR close/merge等でできない場合は`needs_human`を維持する。
- Observationだけの差分はaudit lineageへ追記し、identityを維持する。

`StoppedAt`はcomment生成用の一時値ではなくresume先を決めるdurable checkpointである。resumeは自動retryではなく、毎回人間の`ai-ready`を必要とする。

## 設計詳細

### 1. Escalation Policy schema

```yaml
schema: "kudo.escalation-policy/v1alpha1"
attemptRetries: "3"
reviewRounds:
  testValidity: "3"
  finalImplementation: "3"
```

- `attemptRetries`と`reviewRounds`の整数はArtifact Manifestの`length`と同じくdecimal stringとしてencodeする（[task-context-v1alpha1.md](../contracts/task-context-v1alpha1.md)のcanonical encoding規則を共有する）。
- provider、credential、timeout、tool permissionのfieldを持たない。Execution Policyと役割を重ねない。
- Artifact Store上のkindは`escalation-policy`、schemaは`kudo.escalation-policy/v1alpha1`とする。`execution-policy` kindへ格納せず、ref/payloadのkind、schema、digest、bytesをすべて照合する。

### 2. Run aggregateの変更

| field | 内容 |
| --- | --- |
| `EscalationPolicy` | claim時にpinした`EscalationPolicyRef`。監査用であり`InputIdentity`には入らない |
| `AttemptRetries` | pin済みpolicyから解決した、初回後に許す追加Attempt数。retry schedulerがlogical Operationごとの区間counterと比較する |
| `RoundLimits` | pin済みpolicyから解決したgateごとの上限。pure transitionはartifactをdecodeしないため、解決済みの値をRunが運ぶ |
| `Rounds` | gateごとに確定したreview round数 |
| `ResumeCheckpoint` | `StoppedAt`と停止時の複合`ResumeIdentity`。同じRunを再開できるかを再reconciliationが比較する |

`Decide`はpureのまま維持する。上限はRun stateとして運ぶのであって、transitionへ外部policyを注入しない。

claim時に範囲外の上限を持つRunを作らせない。`ClaimSucceeded`のgateで`1 <= attemptRetries <= 10`かつ`1 <= reviewRounds.* <= 10`を検証し、満たさない場合は`transition_gate_unsatisfied`とする。

### 3. Transition

```text
awaiting_test_review + review_completed(request_changes)
  rounds.testValidity + 1 <  limit.testValidity → authoring_tests    + dispatch revise_tests
  rounds.testValidity + 1 >= limit.testValidity → needs_human        + escalate(review_round_limit_exceeded)

awaiting_final_review + review_completed(request_changes)
  rounds.finalImplementation + 1 <  limit.finalImplementation → implementing + dispatch repair_implementation
  rounds.finalImplementation + 1 >= limit.finalImplementation → needs_human  + escalate(review_round_limit_exceeded)

active_phase + attempt_failed(retryable)
  next retry count <= attemptRetries → same phase   + schedule same logical Operation / new Attempt
  next retry count >  attemptRetries → needs_human  + escalate(retry_budget_exhausted)

needs_human + resume_requested(ai-ready, current ResumeIdentity)
  current == saved → saved StoppedAt + dispatch phase-specific fresh Attempt
  InputIdentity changed and new input valid → superseded + claim new Run
  CheckpointIdentity mismatch / unsafe live state → needs_human + no dispatch
```

比較を`>=`にすることで、上限が未設定（0）のRunはloopを続けずに停止する。信頼境界の既定は「止まる」側に倒す。

新しいaction `EscalateHuman{Reason, StoppedAt}`を追加する。`ProjectStatus{ai-needs-human}`がlabelを、`EscalateHuman`がstatus commentの理由codeと停止phaseを担う。停止phaseは`Decision`のRun（既に`needs_human`）から復元できないためactionが運び、storeは複合`ResumeIdentity`とともにdurable checkpointとして保存する。

### 4. Escalation Policy budgetのPostgreSQL schema

Run storeがEscalation Policy snapshotとreview counterに必要とするcolumnと制約は次のとおり。`kudo_runs`へ折り込む。`ResumeCheckpoint`のdurable表現は同じRun storeが担うが、このbudget schemaとは別の値として保存する。

```sql
-- kudo_runs へ追加する column。
-- BETWEEN の範囲は contract.Min/MaxAttemptRetries と Min/MaxReviewRounds の写しであり、
-- 片方だけを変えない。protocol core が範囲を固定する意味が消える。
    escalation_policy_schema text NOT NULL,
    escalation_policy_digest text NOT NULL,
    attempt_retry_limit integer NOT NULL
        CHECK (attempt_retry_limit BETWEEN 1 AND 10),
    review_round_limit_test_validity integer NOT NULL
        CHECK (review_round_limit_test_validity BETWEEN 1 AND 10),
    review_round_limit_final_implementation integer NOT NULL
        CHECK (review_round_limit_final_implementation BETWEEN 1 AND 10),
    -- 無人区間 counter。escalation で 0 へ戻るため単調増加ではない。
    review_rounds_test_validity integer NOT NULL DEFAULT 0
        CHECK (review_rounds_test_validity >= 0),
    review_rounds_final_implementation integer NOT NULL DEFAULT 0
        CHECK (review_rounds_final_implementation >= 0),
    -- 生涯 counter。reset しない audit lineage。
    review_rounds_total_test_validity integer NOT NULL DEFAULT 0
        CHECK (review_rounds_total_test_validity >= review_rounds_test_validity),
    review_rounds_total_final_implementation integer NOT NULL DEFAULT 0
        CHECK (review_rounds_total_final_implementation >= review_rounds_final_implementation),
    CONSTRAINT kudo_runs_escalation_policy_ref_fkey
        FOREIGN KEY (escalation_policy_schema, escalation_policy_digest)
        REFERENCES kudo_artifact_refs (schema, digest)
        DEFERRABLE INITIALLY DEFERRED,
```

```sql
-- gate 予算は claim 後 immutable、counter は単調増加。
-- semantic input の immutability trigger（kudo_reject_run_input_update）へ相乗りさせない。
-- Escalation Policy は staleness 判定に参加しないため、「semantic input は変更できません」
-- という診断で拒否すると、本 ADR が引いた区別が SQL 側の message に上書きされる。
CREATE FUNCTION kudo_reject_run_gate_budget_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF ROW(OLD.escalation_policy_schema, OLD.escalation_policy_digest,
           OLD.attempt_retry_limit,
           OLD.review_round_limit_test_validity, OLD.review_round_limit_final_implementation)
        IS DISTINCT FROM
       ROW(NEW.escalation_policy_schema, NEW.escalation_policy_digest,
           NEW.attempt_retry_limit,
           NEW.review_round_limit_test_validity, NEW.review_round_limit_final_implementation) THEN
        RAISE EXCEPTION 'Run へ固定した gate 予算は変更できません'
            USING ERRCODE = '23514', CONSTRAINT = 'kudo_runs_gate_budget_immutable';
    END IF;
    -- 単調増加を強制するのは生涯 counter だけである。無人区間 counter は
    -- escalation で 0 へ戻るため、同じ制約を課すと停止そのものが拒否される。
    IF NEW.review_rounds_total_test_validity < OLD.review_rounds_total_test_validity
        OR NEW.review_rounds_total_final_implementation < OLD.review_rounds_total_final_implementation THEN
        RAISE EXCEPTION 'review round の生涯 counter は単調増加です'
            USING ERRCODE = '23514', CONSTRAINT = 'kudo_runs_review_rounds_total_monotonic';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER kudo_runs_reject_gate_budget_mutation
BEFORE UPDATE ON kudo_runs
FOR EACH ROW
EXECUTE FUNCTION kudo_reject_run_gate_budget_mutation();
```

attempt retryの消費数はOperation/Attempt storeのdurable lineageと無人区間markerから数える。新しいAttempt rowの作成とcounter消費を同じtransactionにし、schedulerのprocess-local counterやdelivery回数を正本にしない。escalationで区間markerを進めても既存Attempt rowを削除・採番し直さない。

生涯counterの単調増加をpure transitionだけに委ねない。transitionが加算しか書かなくても、store層のbugや手動UPDATEで巻き戻せると差し戻し履歴が消え、抑止の材料が失われる。無人区間counterは生涯counter以下というCHECKだけで縛り、escalationによる0への復帰を許す。

このschemaは`0001_run_store.sql`（Milestone 2 / Run store）と同じchangeで入れる。本ADR時点のmainには`internal/adapter/postgres/`が存在しないため、参照先の無いmigrationを先行して置かない。`0001`が未mergeの間は`0001`へ折り込み、mergeされていれば`0002`としてALTERで追加する。

## Consequences

### 影響を受ける文書

| 対象 | 変更 |
| --- | --- |
| workflow.md | state図の`request_changes`guard、test mutation rollback、retry規則、複合Resume Identityと`needs_human`からの遷移 |
| architecture.md | retry policy表へreview round行、Durable model表へEscalation Policyとround counter、Controller責務へgate予算 |
| github-routing.md | Human escalation節にreason code語彙とledger規則、Transition rulesの`request_changes`記述へ上限の但し書き |
| contracts/task-context-v1alpha1.md | `attemptRetries`を含むEscalation Policy schema、artifact kind/schema境界、canonical encoding規則 |
| runtime-platform.md | `KUDO_ATTEMPT_RETRIES`と`KUDO_REVIEW_ROUNDS_*` configuration key |
| implementation-plan.md | Milestone 1 / Milestone 2 deliverablesへ反映 |

### 利点

- 自動loopが必ず有限時間で終わる。収束しないIssueがprovider予算とGitHub rate limitを無制限に消費しない。
- 止まった理由が「reviewerの判断」と「Controllerの予算切れ」に分離され、reason codeで区別できる。
- 全roundのledgerにより、差し戻しを受けた人間が「実装の問題か契約の問題か」を自分で判断できる材料が揃う。
- 予算値の変更が進行中Runをsupersedeしない。tuningとworkflow correctnessが独立する。
- protocol validation errorをprovider invalid outputから分離するため、immutableな契約違反を同じ入力で再試行し続けない。
- 差し戻し後のRunが満額の予算で再開するため、人間の修正または外部設定復旧後にautomationがretry/reviewを継続できる。

### 代償・リスク

- Run aggregate、Run store schema、claim pathにfieldが増える。
- 上限が低すぎると、本来自動収束できたIssueを人間へ返す。retry/reviewとも既定3は初期値であり、実運用のAttempt/round分布で見直す。
- Run全体のround/Attempt数に静的な上限が無く、差し戻しを繰り返せば総コストは増え続ける。これはD3/D8の意図した帰結であり、判断材料は生涯counterと差し戻し回数の表示が担う。人間が数字を読まずに`ai-ready`を貼り直し続けた場合、Kudoは止めない。
- fingerprintの完全一致は稀にしか起きず、多くの場合ledgerは人間の読解に委ねられる。自動判定を持ち込まない選択の代償である。

### 未決事項（deferred）

- **base churnによるsupersede loop**: 追従先baseが動き続けるとContext Manifest refが変わり続け、Runがsupersedeを繰り返してcounterもresetし続ける。round上限とは別の livelock であり、base pinning policyの問題として別に扱う。
- **findingの意味的同一性判定**: fingerprintの完全一致以上の判定を行う場合は、reviewerの継続性宣言（ADR-0002 D3の見直しを伴う）か、Controller外のoffline分析として設計する。runtime のgate判断には入れない。
