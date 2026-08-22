# Worker Operation Protocol v1alpha1

## Purpose

Controller が Issue Worker へ指示する run-once Operation と、Worker が返す Result の handoff を定義
する。transport は in-process call だが、envelope は versioned protocol として strict に検証し、
unknown version や field を暗黙に解釈しない。

Review Worker への品質評価依頼は [Implementation–Review Protocol](review-protocol-v1alpha1.md) の
Review Request / Result を使う。本 protocol の execution failure と review の quality verdict を
混在させない。

本 protocol における**artifact**とは、canonical encoding と SHA-256 digest で identity が決まる
versioned payload である。bytes の永続表現は専用 store ではなく GitHub 上の record surface（check
run output、marker 付き comment、PR body の machine block）であり、Controller だけが記録する
（[ADR-0001](../../../adr/0001-github-ssot-stateless-reconciler.md)）。record surface のうち comment
と PR body は repository write 権限者が編集できるため、gate 判定は App 所有 check run に記録した
digest との照合を正とする。

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
  - docs/spec/05_design/04_github-routing.md
causationId: transition-01
createdAt: 2026-08-11T00:00:00Z
```

`operationId`は logical Operation の stable identity、execution attempt は別 identity とする。retry の
たびに Operation を複製せず、同じ Operation へ attempt を追加する。`runId`は claim が確定した後は
kudo Pull Request の番号で識別される Run を指す。

Operation digest は、schema、kind、Run、repository、Issue、Context Manifest ref、Execution Policy
ref、head SHA、input artifact digest、policy refs、causation identity から計算する content identity
である。`operationId`、`createdAt`、attempt number、provider session ID、workspace path を digest へ
含めない。

`issueObservation`は exact 観測の audit 情報であり、Operation digest へ含めない。raw body の非意味的
差分で Task Context と Context Manifest が変わらない場合、Operation identity は変わらず、新しい観測は
telemetry に残す。意味のある変更は Task Context ref を変え、Task Context ref は Context Manifest に
含まれるため、semantic staleness は Context Manifest ref の比較だけで判定できる。validator は Context
Manifest ref を schema と digest の opaque value として扱い、Task Context YAML や Issue Contract を
parse しない。Worker は claim checkpoint（PR body の machine block）を読み、live source から canonical
input を再構築する。

`repository`は Issue reference から導出し、同じ envelope へ食い違いうる identity を二重に持たせない。
`baseSha`も同じ理由で envelope に置かない。base は claim が Context Manifest へ pin する解決結果で
あり、validator は manifest ref を opaque に扱うため、envelope へ複製すると cross-check 不能な二重
identity になる。base の変更は Context Manifest ref の変化として検出する。`inputArtifacts`と
`policyRefs`は順序を持たない集合として扱い、canonical 順（lexicographic）へ正規化したうえで重複を
拒否する。並び順の違いだけで Operation identity を変えない。

Issue Observation、Task Context、Context Manifest、Execution Policy の schema、canonicalization、ref
は [Task Context Protocol](task-context-v1alpha1.md) を正とする。Execution Policy は Issue Worker /
Review Worker の provider adapter、model identifier、adapter version、tool permission、timeout policy
を固定し、credential、secret path、session ID を含めない。同じ Run の途中で deployment default が
変わっても、既存 Operation へ暗黙に適用しない。

## Issue Worker operation kinds

| Kind | Model session | Required input | Output |
| --- | --- | --- | --- |
| `claim` | no | repository、IssueRef、candidate policy | branch + draft PR の作成、claim checkpoint（structured claim context）または structured rejection |
| `author_tests` | fresh | claimed context（base は Context Manifest が pin）、head | test plan、test-only head、RED evidence |
| `revise_tests` | fresh | current test head、blocking Review Result または`test-revision-report`、prior payloads | revised test plan、revised test head、new RED evidence |
| `implement` | fresh | approved test validity Result、test head、Issue context | implementation head、GREEN/refactor/check evidence、Pull Request draft |
| `repair_implementation` | fresh | current implementation head、blocking final Review Result | repaired head、new GREEN/refactor/check evidence、revised Pull Request draft |
| `publish_head` | no | 固定済み head、idempotency identity | compare-and-push による branch 反映、observed head |
| `finalize_pull_request` | no | final approved head、approved final Review Result、PR body draft、idempotency identity | required PR body の確定、draft 解除、observed head |
| `merge_pull_request` | no | final approved head、approved final Review Result、finalize 済み Pull Request reference、期待 head SHA、idempotency identity | merge intent comment、期待 head へ束縛した merge commit、head branch 削除の結果、merged 状態の観測 |

`claim`では branch `kudo/issue-<n>`の ref create が排他を確定し、成功時に draft PR を ensure して
claim checkpoint を PR body の machine block へ記録する。Issue Observation、Context Manifest、head は
claim 開始時点では存在しないため、envelope 上の該当 field を省略する。Execution Policy は claim 成功
時に Run へ固定する。それ以外の Operation では kind に必要な field を省略しない。空文字や直前 Run の
値から推測しない。Task Context の期待 identity は claim checkpoint から取得し、実体は live Issue から
再 compile する。digest だけで schema を推測しない。

`ClaimRequirements`は Issue Compiler が返し、Issue Worker が claim の中で readiness、dependency、
authority を解決して Context Manifest を構築するために使う中間 projection である。Worker Result の
field として process 間へ渡さない。claim 成功後の durable handoff は claim checkpoint が担う。
Controller は raw Issue body から`ClaimRequirements`を再構築せず、Issue Worker が返した versioned
identity と structured な claim 結果だけを使う。

kind ごとの field 要件は次のとおりとする。validator は省略だけでなく、kind が持てない field の混入も
reject する。

| Kind | `issueObservation`、`contextManifest`、`headSha` | `inputArtifacts` |
| --- | --- | --- |
| `claim` | 持たない | 持たない |
| `author_tests` | 必須 | 任意 |
| `revise_tests`、`implement`、`repair_implementation`、`publish_head`、`finalize_pull_request`、`merge_pull_request` | 必須 | 1件以上必須 |

`publish_head`は RED evidence 固定後と GREEN/refactor evidence 固定後の両方で使い、同じ draft PR へ
head を再 publish する。publish は review approve を gate にしない。`finalize_pull_request`は final
approve に bind された head に対してだけ発行され、required PR body の確定と ready 化を行う。
`merge_pull_request`は ready 化の後、final approve に加えて live head の一致、required status check の
success、mergeable が揃った場合だけ発行される（gate 規則は [workflow.md](../02_workflow.md) を正と
する）。いずれも mutation 前に期待 head と live branch head を照合し（compare-and-push、SHA 指定
merge）、外部 push による head 不一致を検出した場合は blind mutation せず`stale_input`として返す。
`merge_pull_request`は期待 head SHA を merge 要求自体にも載せ、照合と merge の間に入る外部 push を
GitHub 側でも弾く。

model-bearing Operation は、同じ Run / worktree を扱う場合も fresh provider process/session を作る。
継続に必要な情報は current commit、input payload、versioned Review Result として渡し、resume token や
conversation transcript を渡さない。

## Operation Result

```yaml
schema: kudo.worker-result/v1alpha1
operationDigest: sha256:<digest>
attemptId: attempt-01
outcome: succeeded
headSha: <git-commit-sha>
claimContext: null
changedInputFields: []
outputs:
  - name: test-plan
    digest: sha256:<digest>
  - name: red-evidence
    digest: sha256:<digest>
externalRefs: []
completedAt: 2026-08-11T00:01:00Z
```

`claim`の`succeeded` Result だけは`claimContext`を必須とし、`headSha`と Issue 由来の`outputs`を
持たない。

```yaml
claimContext:
  compiler: kudo.issue-compiler/v1alpha1
  issueObservation:
    schema: kudo.issue-observation/v1alpha1
    digest: sha256:<digest>
  bodyDigest: sha256:<digest>
  taskContext:
    schema: kudo.task-context/v1alpha1
    digest: sha256:<digest>
  contextManifest:
    schema: kudo.context-manifest/v1alpha1
    digest: sha256:<digest>
  baseSha: <git-commit-sha>
```

`issueObservation`は Issue identity と`bodyDigest`から再計算した ref と一致しなければならない。
`claimContext`は claim checkpoint として PR body の machine block へ記録される typed data であり、
canonical YAML file を保存する指示ではない。claim 以外、または`succeeded`以外の Result は
`claimContext`を持てない。workflow event へ Context Manifest digest だけを投影して、Compiler
version、Task Context ref、body digest、base SHA を失ってはならない。

`outputs`は digest の列ではなく logical name で引く table である。name が無いと、Controller は
「この Operation が何を残したか」を digest からしか判断できず、kind が要求する成果物を残したかを
検証できない。name は後述の語彙・形式規則を使い、重複を拒否する。canonical encode では name の
lexicographic 順へ並べ替えるため、producer の列挙順は Result identity を変えない。各 output の
payload は Result と同時に in-process で Controller へ渡り、Controller が record surface へ記録する。
記録が確認されるまで Controller は次の transition を発行しない。

terminal な`outcome`は次のいずれかとする。

- `succeeded`: Operation contract を満たす output が固定された
- `stale_input`: 開始時または完了時に再取得・再 compile した Context Manifest ref、Execution Policy
  ref、head、input payload、policy ref が期待値と一致しない（Issue Observation だけの差分は stale に
  しない）
- `needs_human`: Issue Worker だけでは選べない authority、安全、仕様判断が必要
- `test_revision_required`: `implement`または`repair_implementation`が「承認済み test の変更が必要」と
  判断して停止した
- `failed_terminal`: retry policy を適用しても自動継続できない execution failure

`test_revision_required`を返せるのは`implement`と`repair_implementation`だけである。test を所有する
`author_tests` / `revise_tests`は自分の test へ差し戻しを返せず、publish 系と`claim`は test を評価
しない。他 kind からのこの outcome は binding 境界で`protocol_kind_constraint`として reject する。
この Result の`headSha`は、未承認の test/production 変更を最後に承認された test checkpoint へ
rollback した後の head であり、省略できない。Controller はこの head が承認済み test head と一致する
ことを verdict check run の binding から検証したうえで、`test-revision-report`を入力に`revise_tests`
を dispatch する。差し戻しは quality verdict でも execution failure でもなく、attempt retry budget を
消費しない。無人区間の予算は`test_validity` gate の round counter が担い、差し戻しの確定ごとに1を
消費する（[workflow.md](../02_workflow.md)）。

retry 可能な timeout、rate limit、network/process failure は terminal Result にせず、attempt failure
として error class、evidence、次回 eligible time を記録する。bounded retry を使い切った場合だけ
policy に従って`failed_terminal`または operator escalation へ進む。attempt failure record が required
field を欠く、あるいは class が未知の場合は、retry 継続にも terminal outcome にも倒さず validation
error として返す。判定不能な record を retry 継続へ倒すと、bounded retry が無効化されたまま attempt
が積み続けられ、caller 側からは検知できない。attempt failure は quality verdict の field を持たない
別の型で表現し、`approve`、`request_changes`、`needs_human`へ変換しない。

`request_changes`と`approve`は本 Result の outcome にしない。Review Worker だけが Review Result と
して返す。

1回の attempt の結末は terminal Result か attempt failure のどちらか一方だけを持つ。両方を持てる
表現にすると、失敗した attempt が terminal Result として commit されうる。この制約は Issue Worker 側
と Review Worker 側の双方で型として表現する。

`changedInputFields`は`stale_input`の根拠であり、semantic comparison が返した変更 field 名をそのまま
載せる。`stale_input`では1件以上必須、それ以外の outcome では空とする。値は comparison と同じ語彙
（`contextManifest`、`executionPolicy`、`headSha`、`inputArtifacts`、`policyRefs`）に限り、自由文字列
を許さない。語彙を共有しないと、Controller は受け取った記録を comparison 結果と突き合わせられない。

Result digest は、schema、参照する Operation digest、outcome、head SHA、claim context、changed input
fields、outputs、external refs から計算する。`attemptId`と`completedAt`は含めないため、同じ入力から
同じ結果を再生成した attempt は同じ content identity を持つ。

`headSha`は Operation が新しく固定または観測した head であり、head を進める kind では入力`headSha`と
一致するとは限らない。したがって binding 検証は、Result が参照する Operation digest の一致と、次の
kind ごとの`succeeded`要件で行う。

| Kind | `headSha` | `outputs` | `externalRefs` |
| --- | --- | --- | --- |
| `claim` | 返さない | 空。代わりに`claimContext`必須 | 同じ repository の PR reference を1件必須 |
| `author_tests`、`revise_tests` | 必須 | 必須 logical name を満たす | 任意 |
| `implement`、`repair_implementation` | 必須 | 必須 logical name を満たす | 任意 |
| `publish_head`、`finalize_pull_request`、`merge_pull_request` | 入力`headSha`と一致 | 必須 logical name を満たす | 同じ repository の PR reference を1件以上必須 |

外部 reference は`github://owner/repository/pull/<number>`形式の canonical な Pull Request reference
とする。非空判定だけでは、成功と主張する Result から PR number と URL を復元できず、retry 時の既存
PR 照合と durable handoff が成立しない。Issue reference と同じく、number は先頭0や符号を含まない
十進表記だけを許可し、owner/repository は case-insensitive に正規化する。Operation が対象とする
repository 以外の PR reference は成功の根拠にしない。

`succeeded`は「Operation contract を満たす output が固定された」ことを意味する。kind が要求する
logical name を欠いた成功を受理すると、後続 Operation と review が存在しない payload を前提に進む。
必須集合は後述の Record surface vocabulary 節が定める。`publish_head`、`finalize_pull_request`、
`merge_pull_request`は source head を進めず固定済み head を branch、PR、base へ反映する Operation
なので、Result head が入力 head と一致しないことは、review していない head へ外部 mutation を行った
ことを意味する。binding 境界で拒否する。merge commit SHA は base 側に生まれる新しい commit であり、
`headSha`へ載せない。

## Record surface vocabulary

payload は content address で一意になるが、digest だけでは「その bytes が何であるか」を表せない。
Operation Result の`outputs`は logical name で引く table であり、name ごとに記録先の record surface
が決まる。

| logical name | 内容 | 記録先 | 主な producer |
| --- | --- | --- | --- |
| `test-plan` | Acceptance Criteria と test の対応 | marker 付き PR comment | `author_tests`、`revise_tests` |
| `red-evidence` | 実装前に test が失敗したことの実行証跡（command、exit status、抜粋、environment identity） | `kudo/evidence-red` check run（test head） | `author_tests`、`revise_tests` |
| `green-evidence` | 実装後に test が通ったことの実行証跡 | `kudo/evidence-green` check run（final head） | `implement`、`repair_implementation` |
| `check-evidence` | refactor 後の required checks と Issue Verification の証跡 | `kudo/evidence-checks` check run（final head） | `implement`、`repair_implementation` |
| `pull-request-draft` | PR へ載せる summary、risk、manual verification の草稿 | marker 付き PR comment | `implement`、`repair_implementation` |
| `performance-evidence` | performance bound 宣言時の測定証跡（command、実行条件、環境 identity、複数回実行の要約） | `kudo/evidence-performance` check run（final head） | `implement`、`repair_implementation` |
| `test-revision-report` | `test_revision_required`の根拠。どの test / Acceptance Criteria がなぜ誤りかを expected / observed 形式で示し、`revise_tests` session の入力になる | marker 付き PR comment | `implement`、`repair_implementation` |

source snapshot はこの語彙に無い。head を再構築・検証できる snapshot は git commit そのものであり、
compare-and-push された branch と PR が正本である。review verdict もこの語彙に無い。verdict は
[Implementation–Review Protocol](review-protocol-v1alpha1.md) の Review Result として返り、Controller
が verdict check run へ記録する。

name の形式規則は`[a-z0-9]`で始まる小文字英数字と`-`、`.`、`/`、`_`、relative path として正規形、
128 byte 以内とする。

この語彙は payload の`kind`とは別の値空間である。`kind`は bytes 自体の規則（versioned schema と
media type）を決めるが、logical name は table の中での役割を決める。Issue 由来の raw body、Issue
Observation、Task Context、Context Manifest は live source から再構築するため、この語彙へ含めない。
`raw-issue-body`、`issue-observation`、`task-context`、`context-manifest`を追加の logical name として
指定した Result も binding 境界で reject する。

必須集合は protocol の一部として core 実装へ固定し、Execution Policy のような配備側 payload へ
持たせない。Execution Policy は producer が作って Operation へ添える payload であり、そこへ必須集合を
置くと、producer が自分に課される gate 条件を自分で緩められる。

### Required outputs

kind ごとの`succeeded` Result は、次の logical name をすべて`outputs`に持たなければならない。必須
集合は下限であって上限ではなく、語彙外の name を追加してよい。kind 別集合とは別に、
`test_revision_required`の Result は outcome に紐付く必須集合として`test-revision-report`を持たなければ
ならない。差し戻しは blocking Review Result を作らないため、report を欠くと修正 session へ渡す根拠が
存在しない。

| Kind | 必須 logical name |
| --- | --- |
| `claim` | なし。`claimContext`を structured field として必須にする |
| `author_tests`、`revise_tests` | `test-plan`、`red-evidence` |
| `implement`、`repair_implementation` | `green-evidence`、`check-evidence`、`pull-request-draft` |
| `publish_head`、`finalize_pull_request`、`merge_pull_request` | なし。`externalRefs`の PR reference と live 観測が成功の根拠になる |

`performance-evidence`は performance bound が宣言された Task でのみ要求される条件付き output であり、
静的な必須集合には含めない。条件の判定は review の deterministic prerequisite が行う。

非空判定では足りない。protocol は kind ごとに「何を」残すかまで定めており、test plan の代わりに任意の
1件を置いた Result も非空判定なら通る。binding 境界は欠落した logical name をすべて error へ載せ、
`protocol_kind_constraint`として分類する。

### Payload size

record surface には上限がある（check run output と comment はそれぞれ 64KiB）。canonical payload は
記録先の上限に収まるよう producer 側で構成し、収まらない payload は protocol 境界で
`protocol_field_too_long`として reject する。command の stdout/stderr は capture 時に bounded な抜粋へ
redaction とともに縮め、全文は telemetry にのみ流す。記録後の truncation は digest と bytes の対応を
壊すため行わない。

## Attempt rules

- 同じ logical Operation の実行は process 内で単一 flight とする。
- attempt には少なくとも attempt ID、start/end time、provider、exit/error class を telemetry へ記録する。
- 新しい attempt は以前の provider process / session を再利用しない。
- Result の commit（record surface への記録と次 transition）は Controller が binding を検証して行う。
  Worker が phase を直接進めない。
- process 再起動で進行中の attempt は消える。再観測が同じ phase を導出すれば、新しい fresh attempt と
  して再実行される。

## Freshness and mutation

各 Operation は開始時に input payload の digest と schema を検証する。Task Context を必要とする
Operation は、開始時と完了時に live Issue / authority を再取得し、claim checkpoint に固定した
Compiler version で Task Context と Context Manifest を再生成する。source を変更する Operation は
専用 worktree の current head が`headSha`と一致することも確認する。

`finalize_pull_request`と`merge_pull_request`は model session を持たないが、開始時に同じ live 再構築
と Context Manifest 照合を必須とする。final review 完了から merge までの間に Issue が意味的に編集
される窓を検出できるのはこの2つの Operation だけであり、merge は取り消せない mutation だからである。
照合は開始時のみでよい。完了時には mutation が確定済みで、stale を検出しても取り消せない。不一致は
他の Operation と同じく`stale_input`として返す。`publish_head`にはこの照合を要求しない。publish 後の
staleness は、次の Review Request が開始時の再構築で検出する。

再生成した最新の Issue Observation ref、Context Manifest ref、Execution Policy ref、head SHA、input
payload、policy ref を semantic comparison へ渡し、結果に従う。live body digest の不一致だけでは
stale と判定しない。

- `SameSemanticInput`: Operation identity と quality approval を維持し、新しい観測を telemetry に
  残して続行する
- `ChangedSemanticInput`: `stale_input`として返し、どの field が変わったかを記録する

comparison は pure function であり、最新入力を既存 Operation、approval、review へ書き戻さない。
書き戻すと、古い approval が新しい入力の approval として黙って再利用される。新しい入力で続けるには
新しい Operation を作る。一致した canonical bytes はその Attempt の model input にだけ使い、完了後に
破棄する。

GitHub mutation を伴う`publish_head`、`finalize_pull_request`、`merge_pull_request`は、repository、
Run、Issue、head を含む stable idempotency marker を PR body または marker comment へ記録する。
response を受け取る前に process が停止した場合、retry 時は既存 PR / comment を検索・照合してから
create/update/merge する。`merge_pull_request`は mutation 前に merge intent comment（対象 head SHA を
含む marker）を記録し、retry では記録済み intent と merge commit の親が期待 head であることを照合して
自分の merge を再確認する。intent の無い merged 観測を自分の成功として扱わない。

Issue Worker だけが implementation worktree、branch、commit、Pull Request を変更できる。Controller は
Operation を dispatch できても mutation を代行しない。

## Validation

unknown schema/version、unknown kind、欠落 required field、kind に許されない field combination、
digest/bytes 不一致を reject する。invalid payload を provider へ渡さず、retry 可能な transport
failure にも変換しない。

### Validation error classification

拒否理由は機械可読な code で分類する。Controller は error 文字列の一致で分岐しない。文字列に依存
すると、message 表現を変えただけで Controller の分岐が壊れ、逆に分岐を保つために message を固定する
必要が生じる。

| code | 意味 |
| --- | --- |
| `protocol_schema_unknown` | envelope 自身の schema が既知 version でない、または versioned ref の schema namespace／形式が不正（ref の version 部分は opaque として保持する） |
| `protocol_kind_unknown` | Operation kind または Review kind が語彙に無い |
| `protocol_field_missing` | required field または ref が欠落している |
| `protocol_field_invalid` | field の値が形式規則を満たさない（空の canonical text を含む） |
| `protocol_field_duplicate` | 集合として扱う field に重複がある |
| `protocol_field_too_long` | canonical text の上限を超えている |
| `protocol_kind_constraint` | 個々の field は妥当だが、その kind では持てないか省略できない |
| `protocol_identity_mismatch` | 再計算した digest と参照された identity が一致しない |
| `protocol_outcome_conflict` | 結末の排他規則違反（verdict と failure の同時保持、verdict と finding の矛盾等） |

この分類は protocol validation 専用であり、Issue Contract parser の claim rejection code とは別の
値空間である。parser の code は Issue body の行・section・field を指す text 由来の診断であり、
protocol validation は解決済みの値に対する判定で行番号を持たない。

いずれの code も retry 可能な transport failure ではない。protocol validation の失敗は immutable な
入力に対する permanent な契約違反であり、同じ入力で retry しても結果は変わらない。分類できない失敗を
retry 可能側の既定へ倒すと、契約違反を無限に retry しうるため、既定は retry しない側とする。

### Canonical text limits

canonical text には上限を定める。これらの値は model provider の出力に由来し、canonical bytes と
record surface の両方へそのまま載る。上限が無いと、単一の attempt が記録先の容量制限と digest 計算量を
通じて後続の全 Operation へ影響しうる。

| 対象 | 上限 |
| --- | --- |
| 複数行本文（attempt failure の`evidence`、finding の`expected`／`observed`） | 65536 byte |
| 単一行の値（finding の`summary`、`mediaType`、`externalRefs`の各要素、Execution Policy の各 field、repository-relative path） | 1024 byte |

上限は受理可否だけに影響し、canonical bytes の構成方法と digest の計算方法を変えない。上限ちょうどの
値は受理する。上限超過は`protocol_field_too_long`として分類し、control character 混入や空文字による
`protocol_field_invalid`と区別する。producer 側が本文を切り詰めれば通る失敗と、値そのものが不正な
失敗は対処が違う。

`policyRefs`と`authorityRefs`の repository-relative path は、canonical な単一行であることを要求する。
改行や control character を含む値は canonical bytes と record surface の両方へ載るため、protocol 層で
拒否する。

`operationId`、`runId`、`attemptId`、`causationId`等の identifier は、英数字で始まり、英数字と`-`、
`_`、`.`だけを含む128文字以内の値とする。Run は workspace を持つため identifier は path segment へも
載りうる。`.`や`..`のような値を protocol 層で通すと、拒否が filesystem 層まで遅れる。

versioned ref は schema と digest の組で検証・比較する。digest が同じでも schema が異なる ref を
同一視しない。ref schema の version 部分は opaque に扱い、既知 version でなくても組として保持・比較
できるが、envelope 自身の schema は既知 version だけを受理する。

Operation / Result の canonicalization と validation rule は fixture で固定する。protocol を変更する
場合、本書、parser、fixture、test を同じ change で更新する。
