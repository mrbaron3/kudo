# Issue Contract v1alpha1

## Purpose

GitHub Issueを、人とKudoの双方が解釈できる実行契約として扱う。Kudoが実行するtaskは、repositoryとIssue番号だけを起点に、実装に必要なauthority、scope、完了条件、検証方法を明示的に解決できなければならない。

「Issue単体で実装可能」とは、Issue本文へ全資料を複製することではない。Task Issue自身が実行境界を完結させ、必要な外部contextを安定したreferenceとして列挙していることを意味する。Controllerが会話履歴や独自の要約で不足を補ってはならない。

Issue WorkerはTask開始時にGitHubからIssueを直接取得する。pureなIssue Compilerは取得したraw bodyと検証済みIssue identityから、exactな[Issue Observationとcanonical Task Context](task-context-v1alpha1.md)を分離して生成する。解決済みreferenceはContext Manifestへ固定する。これらはGitHub Issueの代替となる別の正本ではなく、実際に観測・解釈・参照した版の証拠である。

## Executability

Kudoがclaimできるのは、[GitHub routing policy](../04_github-routing.md)のcandidate条件を満たしたうえで、Contract blockが`schema: kudo.issue/v1alpha1`、`kind: task`、`readiness: ready`を満たし、依存関係とcontext referenceをすべて解決できるIssueだけである。Issue番号とrepositoryはGitHub APIまたは検証済みevent envelopeのidentityを正とし、body内に自己申告させない。

次のH2 sectionを、この順序でそれぞれ1回だけ含める。

1. `Contract`
2. `Outcome`
3. `Scope`
4. `Deliverables`
5. `Acceptance Criteria`
6. `Verification and Evidence`
7. `Constraints and Invariants`
8. `Decision Authority`
9. `Stop and Escalation Conditions`

`Advisory Hints`は任意であり、使う場合は最後に置く。required sectionは空にできない。HTML commentだけのsectionは内容が無いものとして扱い、claim rejectionとする（`Advisory Hints`は空でもよい）。Issue comment、Project field、label、会話履歴は、上記契約へ明示的に取り込まれない限り実装authorityではない。

### 本文の書き方

人が読む描画とKudoのparseで解釈が分かれないように、本文の構造は次の規則に従う。Kudoは描画を推測して補わず、解釈が一意に定まらない書き方をclaim rejectionとする。

- heading、code fence、HTML commentは列0から書く。1〜3 space indentしたこれらのmarkerは、GitHubでは前後の文脈によりheadingにもcode blockにもなるため受理しない。
- code fenceはbacktickまたはtildeを3個以上使って開き、同じ文字を同じ長さ以上並べたinfo stringを持たない列0の行だけが閉じる。
- HTML commentは行全体がcommentである行だけに書く。可視内容と同一行に混在させない。inline code spanも可視内容であり、`` `AGENTS.md` <!-- 補足 --> ``のように可視内容がcode spanだけの行も混在として扱う。
- inline code spanとHTML commentは、同一行では先に現れた側が優先される。code spanが先ならその内側の`<!--`はHTML commentではなく通常の本文として扱い、`<!--`が先ならその内側のbacktickはcode spanを開かない（commentは`-->`で閉じる）。
- 本文にcontrol characterを含めない。改行（LFまたはCRLF）とTABだけを許可し、NUL、ESC、単独のCR、DELを含むその他のC0 controlはclaim rejectionとする。これらはcanonical artifactとPostgreSQLのtext / jsonbへ格納できず、受理すると失敗がcompile後の保存時点まで遅れる。

## Contract block

`Contract` sectionには、YAML fenced blockを1つ置く。

```yaml
schema: kudo.issue/v1alpha1
kind: task
readiness: ready
parent: github://owner/repository/issues/100
dependsOn:
  - github://owner/repository/issues/101
acceptanceCriteriaIds:
  - AC-1
authorityRefs:
  - docs/spec/05_design/01_architecture.md
```

親を持たないtaskは、`parent: null`を明示する。

| Field | Required | Meaning |
| --- | --- | --- |
| `schema` | yes | このcontractのversion |
| `kind` | yes | v1alpha1では`task`のみ実行可能 |
| `readiness` | yes | `draft`、`ready`、`blocked`。実行可能なのは`ready`のみ |
| `parent` | yes | 直接の親Issue reference。親がなければ`null` |
| `dependsOn` | yes | 先に完了し、成果物がclaim対象baseへ統合済みである必要があるIssue references。空配列可 |
| `acceptanceCriteriaIds` | yes | このTask自身の`Acceptance Criteria`に定義するID。1件以上 |
| `authorityRefs` | yes | 実装時に読むrepository内pathまたはGitHub Issueのsource-of-truth references。優先順位の高い順に列挙する。空配列可 |

未知のfield、重複key、不正なenum、重複reference、解決不能なreferenceはclaim rejectionとする。将来のschema追加を暗黙に解釈しない。

v1alpha1では`parent`、`dependsOn`、GitHub Issue形式の`authorityRefs`をTaskと同じrepositoryに限定する。cross-repository hierarchyまたはdependencyは別の設計判断なしに解釈しない。

`acceptanceCriteriaIds`の各IDはTask自身の`Acceptance Criteria` sectionに一度だけ存在し、section内の全criterion IDがこの配列に列挙されなければならない。さらに、section内のcriterionはこの配列と同じ順序で並べなければならない。順序が食い違う本文を受理してCompiler側で並べ替えると、人が読むIssueの順序とAIへ渡る順序が黙って食い違うため、H2 sectionの順序規則と同じくclaim rejectionとする。

`parent`、`dependsOn`、`authorityRefs`のGitHub Issue referenceは、GitHubに合わせてowner / repositoryを大文字小文字非区別に解釈する。同じIssueを指すreferenceが表記差分だけで別identityにならないよう、parse時にowner / repositoryを小文字へ正規化してから重複検出とcanonical encodeを行う。

authority間の優先順位は`authorityRefs`の配列順で表す。先頭が最優先であり、矛盾した場合はより前のreferenceを正とする。優先順位を散文で重複定義せず、順序を変える場合はContract blockを変更する。

## Hierarchy and reference semantics

### Task Issue

Task Issueが唯一の実行単位である。1 Task IssueにactiveなRunは最大1つであり、成功したRunは1専用worktree、1branch、1reviewable PRに対応する。staleまたはsuperseded Runの履歴は残してよいが、同時に複数Runをwriterとして動かさない。EpicやInitiative自体には実装PRを作らない。

Taskは、自身のOutcome、Scope、Deliverables、Acceptance Criteria、Verification、Constraints、Decision Authority、停止条件を完結させる。親Issueの本文を暗黙に継承して、Taskに欠けた情報を補ってはならない。

### Parent Issue

`parent`は成果scope、progress、traceabilityを表す。親の全本文、Acceptance、comment、他の子IssueをTask sessionへ自動的に投入しない。Epicの進捗はsub-issueの完了状態から計算できるが、Epic自体を実装Taskとして扱わない。

親Issueが`kudo.issue/v1alpha1`を実装する必要はない。Kudoは親の存在とrelationship identityを検証するが、親をclaimしたり、親本文の独自formatを実装契約として解釈したりしない。

親の横断的な制約がTaskへ適用される場合は、Taskの`Constraints and Invariants`へ明記するか、親Issueを`authorityRefs`へ明示する。適用範囲を限定する必要があれば`Constraints and Invariants`へ書く。Initiative等の上位Issueも、Taskが`authorityRefs`へ列挙しない限り再帰的に継承しない。

### Dependencies

`dependsOn`は実行順序のgateであり、依存Issueの会話履歴や作業sessionを引き継ぐ指定ではない。claim時には各依存がcompletedで、その成果物が選択したbase commitへ統合済みであることを検証する。未merge branch、draft PR、provider session内だけに存在する成果物を入力とみなさない。

二つのready Task Issue間に`dependsOn` edgeがなければ、同じparentまたはrepositoryに属していても互いを待たずにclaimできる。parent、phase、Issue番号順、Epic内の記載順から暗黙のdependencyを作らない。

dependency completion identityには、少なくともIssue reference、completed state、baseへ統合されたcommit identityを含める。linked PRまたは明示的なcompletion artifactからbase統合を証明できない場合、Issueがclosedであることだけをcompletedとして推測しない。

依存成果物を実装入力として読む必要がある場合、Taskはbase commit上のpathを`authorityRefs`で指定する。依存Issue本文全体を暗黙のcontextにしない。依存Issue本文そのものがauthorityなら、そのIssue referenceも`authorityRefs`へ明示する。

### Authority references

v1alpha1の`authorityRefs`は、対象repository内のrelative pathと、同じrepositoryの`github://.../issues/<number>`だけを許可する。repository-relative pathはcanonicalな単一行かつ1024 byte以内とし、上限超過はIssue本文の行を指すclaim rejectionとして扱う。この値は同じ上限を持つContext Manifestへ載るため、artifact生成まで拒否を遅らせない。repository-relative referenceはclaim対象base commitで解決し、content digestを記録する。GitHub Issue referenceはIssue本文を直接取得してbody digestを記録する。cross-repository reference、mutableな一般URL、versionを固定できないreferenceは実装authorityとして扱わない。

実装authorityは`authorityRefs`だけを正とする。`parent`と`dependsOn`はrelationshipとreadiness gateであり、それ自体はauthorityではない。Contract blockにないreferenceをproseから推測して取得しない。referenceが解決できない場合はclaimを拒否する。

GitHub native sub-issueまたはdependency relationshipをadapterが取得できる場合、Contract blockの`parent`および`dependsOn`と一致しなければならない。二つの表現が競合した場合に片方を推測で採用しない。

`Enables`、priority、phase、Project status、assignee、label等はroutingや人間向け計画に利用できるが、v1alpha1 Issue Contractの実装入力ではない。

## Section semantics

### Outcome

完了後に利用者または外部systemから観測できる結果を書く。ファイル名、関数名、利用library、実装手順だけでOutcomeを代用しない。

### Scope

IncludedとExcludedを分け、変更してよいsystem boundaryを示す。親Epicのscopeを参照するだけでTask固有の境界を省略しない。

### Deliverables

作成または変更する成果物と、外部から確認できる役割を書く。内部構造を過度に固定する必要はないが、何が完成すればTaskの成果物が揃うかを判断できなければならない。

### Acceptance Criteria

各criterionはIssue内で一意かつ変更されないIDを持ち、Given / When / Thenで観測可能な振る舞いを定義する。内部実装だけを確認するcriterionは不可とする。

### Verification and Evidence

完了を証明するcommand、test surface、artifact、外部境界を記載する。実装者が選べる内部手順と、必須の検証入口を区別する。実行不能なcommandや取得不能な証跡を必須化しない。

### Constraints and Invariants

security、compatibility、data integrity、performance、authority ownershipなど、全criterionにまたがる不変条件を書く。

### Decision Authority

Issue WorkerがTask内で決めてよい事項、別のADRやIssueを必要とする事項、人間判断が必要な事項を分ける。実装詳細をすべて事前指定するのではなく、安全に前進できる裁量の境界を与える。

### Stop and Escalation Conditions

不足authority、仕様矛盾、危険なmutation、必要なcredential不足、Issueまたは参照先の変更など、自動実行を止める条件を書く。停止は失敗を隠して推測で続行することを意味しない。

### Advisory Hints

非拘束の実装上の助言を書く。Authority、Acceptance Criteria、Constraintsと競合するhintは無効であり、Workerはhintを根拠に契約を拡張しない。

## Claim and context resolution

claim operationはrepository identityとIssue numberを入力にし、次を行う。

1. Issue WorkerがIssueをGitHubから直接取得する
2. raw bodyと検証済みIssue identityをIssue Compilerへ渡し、strict parse済みTask Context、Issue Observation、ClaimRequirementsを生成する
3. native relationshipとContract blockを照合する
4. parent identity、dependency completion、authority referenceを解決する
5. base commitを固定し、referenceごとのdigestを計算する
6. raw body、Issue Observation、Task Context、Context Manifestをimmutable artifactとして保存する
7. model-bearing Worker Operationを開始する直前にIssueを再取得し、Issue Observationのbody digestと一致することを検証する

### Issue Observation and Task Context

canonical表現、SHA-256 identity、payload contract、versioning ruleは[Task Context Protocol](task-context-v1alpha1.md)を正とする。

- Issue Observationはverified Issue identityとexact raw body digestを持ち、live変更検知と監査に使う。
- Task Contextはstrict parse後のContract、section、criterionを固定fieldへ投影したcanonical YAMLであり、model sessionへ渡すIssue表現である。
- raw bodyのline endingまたはtemplate HTML commentだけが変わった場合、Issue Observation refは変わるがTask Context refは変わらない。
- claim以降はparserのTask/Contract/section titleを再解釈せず、ClaimRequirementsとversioned artifact/refを渡す。

### Context Manifest

Context Manifestは、Task Contextから解決した実装入力のclosureを表す。

```yaml
schema: "kudo.context-manifest/v1alpha1"
taskContext:
  schema: "kudo.task-context/v1alpha1"
  digest: "sha256:<digest>"
baseSha: "<git-commit-sha>"
parent: "github://owner/repository/issues/100"
dependencies:
  - issue: "github://owner/repository/issues/101"
    completionDigest: "sha256:<digest>"
authorityRefs:
  - ref: "docs/spec/05_design/01_architecture.md"
    contentDigest: "sha256:<digest>"
```

親を持たない場合は`parent: null`、依存またはauthority referenceがない場合は空配列を使う。配列順はIssue Contractの宣言順を保ち、重複を許可しない。

Context ManifestにはIssue Observation ref、body digest、Issue本文、authority本文、会話履歴を含めない。各contentはdigestから取得できるimmutable artifactとして保存する。parent本文は`authorityRefs`にも指定された場合だけcontent artifactへ含める。

Context Manifestのidentityにrun ID、claim timestamp、workspace path、provider session IDを含めない。同じTask Context、base SHA、relationship、dependency completion、authority contentは同じmanifest identityを持たなければならない。Task Contextが同じなら、raw bodyだけの非意味的差分でmanifest identityを変えない。

claim成功時に、少なくとも次を固定する。

- repository identityとIssue number
- Issue Observation ref、取得したraw body、body digest
- Task Context refとcanonical YAML artifact
- base commit SHA
- parent identity
- dependency identities、completion identities、baseへの統合確認
- 解決済みauthority referencesとcontent digests
- Task自身のAcceptance Criteria IDs
- Context Manifest digest
- run IDとclaim timestamp

model sessionはraw Issue本文ではなくcanonical Task Contextと、Context Manifestが明示するauthority contentを読む。Controllerが契約内容を自然言語で再解釈してpromptへ置き換えたり、parserのsection titleを読み直したりしてはならない。raw bodyとIssue Observationは監査とlive変更検知に使う。

実行中にIssue本文、dependency completion、base commit、authority referenceの内容が変わった場合は、進行中runへ暗黙に取り込まない。Task ContextまたはContext Manifestが変わる差分はstaleとして記録し、新しいclaimまたは人間判断へ戻す。raw bodyの非意味的差分でTask Context/Context Manifestが変わらない場合は、Issue Observationのlive freshness不一致を検出したうえで、新しい観測をaudit lineageへ追記して進行中runを維持する。実装入力ではない親Issue本文の編集だけではrunをstaleにしない。

## Claim rejection

少なくとも次を構造化されたclaim rejectionとして区別する。

- contractの構文またはsemantic rule違反
- `readiness`が`ready`でない
- 必須contextの欠落
- `acceptanceCriteriaIds`とTask本文のcriterionが一致しない
- dependencyが未完了またはbaseへ未統合
- native relationshipとContract blockの不一致
- authority referenceの解決不能または内容の矛盾
- claim中のIssueまたはreference変更

GitHub API timeout、rate limit、network error等はclaim rejectionへ変換せず、transport failureとして扱う。
