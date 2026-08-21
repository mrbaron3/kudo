# 4.4. Pull Request 確定・Merge 詳細設計

[機能仕様](01_spec.md)を、finalize gate、merge gate、Issue Worker の Pull Request mutation、Controller の
完了 projection に分けて実現する。merge の判断規則は [ADR-0005](../../05_design/decisions/0005-auto-merge.md)
を正とする。

## Finalize gate

Controller は次の identity が一致する場合だけ `finalize_pull_request` Operation を発行する。

- Run に固定された repository、base、branch、Pull Request reference
- live Pull Request の open state、base、head
- final Review Request / Result が承認した head と Artifact Manifest
- GREEN、required checks、review policy を含む required evidence

PR body や draft / ready 表示だけの差分は observation lineage として扱えるが、head、base、PR identity の
差分は finalize を止める。

## Pull Request mutation

Issue Worker は expected Pull Request observation を入力として live state を再取得し、compare-and-mutate で
body 更新と draft 解除を行う。Controller は content を生成する材料を route できるが、Pull Request 自体を
変更しない。

required body は immutable artifact と Result から決定論的に構築し、少なくとも次を参照可能にする。

- Task Issue と Acceptance Criteria
- test plan、RED、GREEN、required checks
- test validity と final implementation の Review Result
- Run ID、base SHA、final head SHA、residual risk

mutation の idempotency key は Run、Pull Request、final head、operation kind から安定して導出し、timeout 後の
retry が Pull Request を重複作成しないようにする。

## Merge gate

finalize completion が durable に記録された後、Controller は次がすべて成立する場合だけ `merge_pull_request`
Operation を発行する。

- final approve が live PR head と一致する final head と Artifact Manifest へ bind されている
- live PR が open、base が Context Manifest の base と一致、head が approved head と一致
- base branch の required status check が同じ head SHA に対して success
- GitHub が mergeable を返す（conflict が無い）

check が pending の間は Operation を `retry_wait` として backoff し、retry budget を消費しない。execution
deadline を超えた pending、check failure、conflict、protection の拒否、merge commit 不許可は `merge_blocked`
として `needs_human` へ routing する。

## Merge mutation

Issue Worker は期待 head SHA を明示した compare-and-merge で merge commit を作る。merge method は merge commit
に固定し、squash / rebase を選ばせない。成功後に head branch を冪等に削除し、merge commit SHA と merged 状態を
`pull-request-observation` として固定する。

merge の idempotency identity（repository、Run、Pull Request、期待 head、operation kind）を mutation 前に
durable へ記録する。応答を受け取れなかった retry では、記録済み intent と live 観測を照合し、merge commit の親が
期待 head であれば自分の mutation の再観測として成功へ収束させる。intent が無い merged / closed 観測は
`external_mutation_conflict` とする。

## Durable completion と Projection

Issue Worker Result を受理した transaction で、finalize completion または merge completion と、対応する
projection intent を記録する。merge completion の projection intent は Task Issue の close と `ai-merged` label を
含む。outbox consumer が label / comment / close を再送し、projection failure は成立済みの merge や workflow
completion を巻き戻さない。closing keyword で既に closed の Issue に対する close は no-op として成功にする。

## 外部干渉

- expected head / base と live state が違う場合は stale とし、push、body update、merge を行わない。
- Kudo の merge intent に紐付かない close / merge を観測したら `needs_human` へ routing する。
- transient API failure は同じ logical Operation の新しい attempt として再試行する。
- 人間が編集した body の扱いは observation と required section の整合を確認し、無条件に上書きしない。

## 検証方針

- head、base、open / draft の組み合わせごとに finalize gate を table-driven test で検証する。
- merge gate を approve、live head、check status（success / failure / pending）、mergeable の組み合わせで
  table-driven に検証する。
- body 生成が同じ artifact input から同じ結果になることを検証する。
- merge / branch 削除 / Issue close の timeout 後 retry と outbox 再送が一つの状態へ収束することを fake GitHub で
  検証する。intent 有無による merged 観測の解釈差も同じ fake で検証する。
- Controller / Review Worker credential では Pull Request mutation と merge ができない構成を確認する。

## 参照

- [End-to-end workflow](../../05_design/02_workflow.md) §7–8
- [ADR-0005](../../05_design/decisions/0005-auto-merge.md) — merge gate と failure routing
- [Architecture](../../05_design/01_architecture.md) — Issue Worker、Mutation authority
- [GitHub routing policy](../../05_design/04_github-routing.md) — Merge completion
- [Worker Operation Protocol](../../05_design/contracts/operation-protocol-v1alpha1.md)
