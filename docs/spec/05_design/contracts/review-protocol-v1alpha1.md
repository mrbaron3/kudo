# Implementation–Review Protocol v1alpha1

## Purpose

Implementation と Review を、同じ repository と binary を利用しても、同じ mutable worktree、
credential、model/provider session、conversation memory を共有しない独立 role として接続する。

Issue Worker は source、worktree、branch、commit、Pull Request mutation を所有する。Review Worker は
immutable input と read-only checkout から verdict を作り、verdict check run と finding comment を
自分の App identity で記録する。Implementation は自分の request を approve できない——verdict は
Reviewer App にしか作れず、この分離は identity で構造的に強制される。Controller は request/result
binding と gate 判定を検証するが、verdict を代筆も上書きもしない。Review Worker は判定対象（source、
branch、PR の状態・本文）に不可侵である。

各 model-bearing Issue Operation と各 Review Request は fresh provider process/session で処理する。
修正を同じ論理作業 lane へ差し戻す場合も、以前の session ID、resume token、conversation transcript、
private memory を入力にしない。

Review の判断内容は provider 固有定義ではなく
[Agent Package Protocol](agent-package-protocol-v1alpha1.md) の repository 所有 package を正本とする。
Review Worker は deterministic prerequisite を完了してから package input を構築し、Codex/Claude の薄い
launcherを通して実行し、package output schemaと本protocolの両方でResultを検証する。

## Review Request

Review Request は`kudo.review-request/v1alpha1`として version 付けする。

### v1alpha1 compatibility note

Issue #85時点ではReview Requestのproduction producer/consumerとdurable review recordがまだ存在しないため、
`agentPackage`必須化と`inputs`から`artifactManifest`への実装済み表現の同期をpre-releaseのv1alpha1へ
取り込む。旧shapeはstrict validatorで拒否し、fieldを推測して補わない。productionで発行済みRequestとの
互換が必要になった後のrequired field変更は新しいschema versionを作る。

```yaml
schema: kudo.review-request/v1alpha1
requestId: 01KUDOEXAMPLE
kind: test_validity
producerRunId: run-01
repository: github://owner/repository
issue: github://owner/repository/issues/42
pullRequest: github://owner/repository/pull/57
issueObservation:
  schema: kudo.issue-observation/v1alpha1
  digest: sha256:<digest>
pullRequestObservation:
  schema: kudo.pull-request-observation/v1alpha1
  digest: sha256:<digest>
headSha: <git-commit-sha>
contextManifest:
  schema: kudo.context-manifest/v1alpha1
  digest: sha256:<digest>
executionPolicy:
  schema: kudo.execution-policy/v1alpha1
  digest: sha256:<digest>
agentPackage:
  schema: kudo.agent-package/v1alpha1
  digest: sha256:<digest>
artifactManifest:
  schema: kudo.artifact-manifest/v1alpha1
  digest: sha256:<digest>
policyRefs:
  - agent-packages/test_validity/v1alpha1/instructions.md
createdAt: 2026-08-11T00:00:00Z
```

`kind`は v1alpha1 で次の2種類とする。

- `test_validity`: test plan、test-only head、test diff、RED 証跡が Issue Contract を正しく検証するか
- `final_implementation`: approved test と Issue Contract に対し、final head、GREEN/refactor/check
  証跡が正しく、回帰や重大な risk がないか

全 review round は claim 時に作成された draft Pull Request へ繋留される。`pullRequest`は Task Issue と
同じ repository の PR でなければならず、別 repository の PR を指す request は受理しない。

Review Worker は claim checkpoint（PR body の machine block）を読み、`issue`と authority を live
source から取得する。claim 時と同じ Compiler version で Task Context と Context Manifest を再生成し、
Request の期待 identity と一致することを review 開始時と完了時に確認する。GitHub access を fake へ
置き換える test でも、同じ Issue Reader と Compiler contract を通す。model session へは raw body では
なく、一致確認済みの in-memory canonical Task Context を渡す。

同様に、`pullRequest`が指す live PR を取得し、open 状態・head が`headSha`と一致・base が claim
checkpoint の`baseSha`と一致することを確認する。保存済みの観測だけを現在の PR として扱わない。

Review Worker が source tree を必要とする場合、read-only clone から`headSha`を検証した disposable
checkout を構築する。Issue Worker の worktree path を Request へ含めず、mutable worktree を参照しない。

`artifactManifest`は logical name で引く immutable artifact table のversioned refであり、名前の語彙・
形式規則・記録先は
[Worker Operation Protocol](operation-protocol-v1alpha1.md) の Record surface vocabulary を正とする。
Review Worker はmanifest自体のschema/digestを照合してから、各entryのpayloadをrecord surface（check
run output、marker comment）から取得し、name、media type、length、digest、bytesを照合する。digest の
一致しないpayloadは改竄または欠落であり、品質verdictを返さずprotocol violationとして返す。

`agentPackage`はreview観点、instructions、input/output schema、tool profile、fixturesのclosure identity
である。Review kindとpackageの`operation`が一致しなければならない。Package refの解決・component
digest検証をproviderに任せず、Kudo runtimeが開始前に行う。

Request identity は、schema、kind、repository、Issue reference、Pull Request reference、head SHA、
Context Manifest ref、Execution Policy ref、Agent Package ref、Artifact Manifest ref、policy refs から
決まる。同じ`requestId`でも
これらが異なる入力を同一 request として扱ってはならない。versioned ref は schema と digest を組で
比較し、digest が同じでも schema が異なる ref を同一視しない。同じ head でも別 PR への request は別
identity である。

`requestId`、`producerRunId`、`createdAt`、`issueObservation`、`pullRequestObservation`は identity に
含めない。observation は
exact 観測の audit 情報であり、raw body の非意味的差分や PR body・draft/ready 状態の変化だけで
request identity と既存 approval を stale にしない。意味のある変更は Task Context ref を通じて
Context Manifest ref を変えるため、semantic staleness は Context Manifest ref の比較で判定できる。
`policyRefs`は順序を持たない集合として canonical 順へ正規化し、重複を拒否する。reviewer が評価基準を
推測しないよう、`policyRefs`は1件以上必須とする。Artifact Manifest entryはnameのlexicographic順へ
正規化し、nameの重複を拒否する。

### Required policy refs

kind ごとに、対応する versioned review policy の現行 path を`policyRefs`へ含めなければならない。
policy 文書は標準 policy の省略と別 policy からの推測を禁じており、path の形式検証だけでは任意の別
policy だけを積んだ request が provider へ渡ってしまう。

| Review kind | 必須 policy ref |
| --- | --- |
| `test_validity` | `agent-packages/test_validity/v1alpha1/instructions.md` |
| `final_implementation` | `docs/spec/05_design/review-policies/final-implementation-v1alpha1.md` |

repository 固有 policy の追加は妨げない。欠落は request を reject し、欠落した path をすべて error へ
載せて`protocol_kind_constraint`として分類する。policy の意味を変えるときは新しい versioned path を
追加し、この表を同じ change で差し替える。

local path、provider session ID、会話履歴、application-private state、credential を review の必須
入力にしない。

### Required inputs

kind ごとに、次の logical name をすべて`artifactManifest.entries`へ含めなければならない。Task Context
とauthority contentはmanifestへ含めず、live sourceから再生成・再取得して期待digestと比較する。
`source-bundle`はhead SHAを再構築できるimmutable payloadであり、checkout時にSHAを検証する。

| Review kind | 必須 logical name |
| --- | --- |
| `test_validity` | `test-plan`、`red-evidence`、`source-bundle`、`pull-request-observation` |
| `final_implementation` | 上記すべて、および`test-validity-result`、`green-evidence`、`check-evidence`、`pull-request-draft` |

`test-validity-result`は approved test head の`kudo/test-validity` verdict check run に記録された
Review Result を指す。加えて`final_implementation`には条件付き input `performance-evidence`がある。
Task Context に performance bound が宣言されている場合の測定証跡であり、必須になる条件は Task
Context の内容に依存するため静的な kind 別集合では強制しない。条件の判定と欠落の拒否は Review
Worker の deterministic prerequisite が行う。

必須集合は下限であって上限ではなく、語彙外の name を追加してよい。欠落は request を reject し、
Review Worker へ渡さない。欠落した name はすべて error へ載せ、`protocol_kind_constraint`として分類
する。

payload の bytes が変われば新しい digest と新しい Review Request を作る。test を implementation
phase で変更した場合、以前の test validity approval を再利用せず、test review gate へ戻る。

## Review Result

Review Result は`kudo.review-result/v1alpha1`として version 付けする。

```yaml
schema: kudo.review-result/v1alpha1
requestDigest: sha256:<digest>
reviewRunId: review-01
verdict: request_changes
perspectives:
  - perspective: ux
    applicable: false
    reason: no-user-facing-surface-change
    evidenceRefs:
      - sha256:<digest>
  - perspective: accessibility
    applicable: false
    reason: no-ui-surface-change
    evidenceRefs:
      - sha256:<digest>
  - perspective: type-design
    applicable: true
    reason: exported-signature-changed
    evidenceRefs:
      - sha256:<digest>
  - perspective: performance
    applicable: false
    reason: no-bound-and-no-perf-surface
    evidenceRefs:
      - sha256:<digest>
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

verdict は次のいずれかとする。

- `approve`: blocking finding がなく、同じ request digest を次の gate へ進められる
- `request_changes`: Issue Worker の新しい修正 Operation で対応可能な blocking finding がある
- `needs_human`: authority conflict、安全判断、仕様決定等、人の決定なしに修正方針を選べない

`severity`は`blocking`と`advisory`の2種類とする。`approve`は blocking finding を持てず、
`request_changes`と`needs_human`は blocking finding を1件以上必要とする。verdict と finding が矛盾
する Result は、Controller が finding を読まずに誤った gate 判断をするため受理しない。

finding は`expected`、`observed`、`evidenceRefs`を持ち、単なる感想にしない。Review Result は
producer の worktree、branch、PR を変更しない。Review Worker は verdict と request identity を
`kudo/test-validity`または`kudo/final-implementation` check run の output（machine block）として対象
head へ、finding 本文を marker 付き PR comment として、いずれも自分の App identity で記録してから
Result を in-process で Controller へ返す。Controller は記録の存在・binding・作成 identity を検証
する。gate 判定は Reviewer App 所有の check run に記録された machine block を正とし、comment は人間と
修正 session が読む表現である。両者の digest が食い違う場合は comment 側の改竄または記録失敗であり、
check run 側を正として comment を再記録する。

### Applicability 宣言

`perspectives`は条件付き観点への観点別 applicability 宣言である。適用可否は review session が policy
の適用条件（正本は [Final Implementation Review Policy](../review-policies/final-implementation-v1alpha1.md)
の条件付き観点表）から判断し、適用と判断した観点だけを深く評価する。事前の rule classifier や別の
model 呼び出しによる観点選択は行わない。

- 条件付き観点の語彙は`ux`、`accessibility`、`type-design`、`performance`の4つとする。
- `final_implementation`の Result は全条件付き観点への宣言をちょうど1件ずつ持たなければならない。
  宣言を欠く Result は binding 境界で reject し、欠落した観点をすべて error へ載せて
  `protocol_kind_constraint`として分類する。`test_validity`は条件付き観点を持たないため
  `perspectives`を持てない。
- `reason`は機械判定可能な lowercase kebab-case の code 値とする。自由記述の補足は finding ではなく
  `evidenceRefs`が指す payload へ置く。
- 宣言は適用可否にかかわらず、判断根拠となった Task Context 節や diff 範囲を指す`evidenceRefs`を
  1件以上持つ。
- 宣言は reviewer 自身の判断であり、handler や Controller が代筆・補完しない。宣言の欠落は品質
  verdict ではなく protocol violation である。

`summary`は1024 byte 以内の単一行、`expected`と`observed`は65536 byte 以内の canonical text とする。
上限と分類 code の規定は`operation-protocol-v1alpha1.md`の Validation 節に置き、両 protocol で同じ
値を使う。上限を超える finding は受理せず、`protocol_field_too_long`として分類する。上限は Review
Result identity の計算方法を変えない。

Review Request と Review Result の validation 失敗も、Operation 側と同じ code 体系で分類する。
Controller は品質 verdict と validation 失敗を別経路で扱うため、validation 失敗の code を
`request_changes`や`needs_human`へ読み替えない。

Result identity は、schema、参照する request digest、verdict、applicability 宣言、finding から
決まる。`reviewRunId`と`createdAt`は含めないため、同じ request への同じ判断は同じ content identity を
持つ。finding は`id`、宣言は perspective 名の lexicographic 順へ正規化して encode する。reviewer が
列挙した順序は判断の一部ではなく、model provider は同じ判断でも順序を再現しないため、並びだけが違う
Result を別 identity にしない。`evidenceRefs`も同じ理由で順序を持たない集合として扱う。binding 検証は
Result が参照する request digest の一致と、kind に対する宣言の完全性で行う。

`request_changes`後の修正 Operation には、対象 head、Review Result、必要な evidence への参照だけを
渡す。修正 Worker は live Issue / authority を再取得・再 compile する。以前の Implementation /
Review session を resume しない。修正後は新しい head と request digest で再 review する。

## Gate semantics

draft Pull Request は claim 時に作成され、review approve を publish の gate にしない。RED evidence が
固定された時点で Issue Worker は test head を compare-and-push し、RED evidence check run を自分の
名義で記録する。draft 状態の CI が RED になるのは TDD の位相の正直な表示であり、隠すために publish を
遅らせない。

`test_validity`の approve が、publish 済み PR の live head と一致する test-only head に verdict check
run として記録されている場合だけ、Controller は`implement` Operation を発行できる。

`final_implementation`の approve が、publish 済み PR の live head と一致する final head に verdict
check run として記録され、GREEN/refactor/check evidence check run も同じ head に存在する場合だけ、
Issue Worker は`finalize_pull_request`で required PR body を確定し draft を解除できる。ready 化と
merge が final approve を gate とする。finalize 前後で head が変わった場合、新 head には verdict
check run が存在しないため approve は構造的に stale であり、再 publish と再 review を要求する。PR
body だけを決定論的に作成・更新しても source head が変わらない場合、review binding は維持できるが、
required PR field validation は別途通さなければならない。

Review approve はそれ自体が merge ではない。approve は merge gate の品質側条件であり、merge には
さらに live head の一致、required status check の success、mergeable、branch protection という外形
条件が要る。外形条件は Controller と Issue Worker が判定し、reviewer へ問い合わせない
（[workflow.md](../02_workflow.md)）。final approve 後に ready 化と`merge_pull_request`の merge
intent comment・merged 観測が揃い、Task Issue が close されて`ai-merged`が記録された時点で Kudo
workflow は terminal になる。

## Failure and staleness

timeout、rate limit、network error、provider crash、invalid response は transport/execution failure
であり、`request_changes`や`needs_human`という品質 verdict に変換しない。Issue を取得できない場合も
transport failure であり、保存済み本文だけで review を続けない。execution failure は quality verdict
の field を持たない別の型で表現し、1回の attempt の結末は verdict か failure のどちらか一方だけを持つ。

Context Manifest ref、Execution Policy ref、Agent Package ref、commit SHA、Artifact Manifest ref、policy
ref、Pull Request ref のいずれかが変わった時点で既存 Result は stale になる。Issue の raw body だけの
非意味的差分、PR body の編集、draft/ready の状態遷移は stale にせず、観測を telemetry に残す。

live PR の head が request の`headSha`と一致しない場合、または base が claim checkpoint の`baseSha`と
一致しない場合は品質 verdict を返さず、stale input として Controller へ返す。Kudo 自身の merge
intent に紐付かない close または merge を観測した場合は Run を停止し、人間へ escalate する。記録済み
intent と一致する merged 観測は自分の mutation の再観測であり、干渉として扱わない。同 branch への
外部 push は Issue Worker の compare-and-push とこの live 照合の両方で検出する。

Review 開始時または完了時に、live Issue / authority から再生成した Task Context / Context Manifest
identity が Request と一致しない場合は品質 verdict を返さず stale input として Controller へ返す。
raw body の非意味的差分で両 identity が変わらない場合は、観測を telemetry に残して同じ request を
続行する。stale 後は新しい Review Request を発行し、古い Result を新しい入力の approval として再利用
しない。review 開始後の既存 Request へ最新 ref を上書きしない。

retry 可能な execution failure は同じ logical Review Request に対する新しい attempt として記録
できるが、provider session は attempt ごとに新規作成する。quality verdict と attempt failure を同じ
field で表現しない。
