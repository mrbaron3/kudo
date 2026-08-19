# 4.4. Pull Request 確定・人間への Handoff 詳細設計

[機能仕様](01_spec.md)を、finalize gate、Issue Worker の Pull Request mutation、Controller の status projection
に分けて実現する。

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

## Durable completion と Projection

Issue Worker Result を受理した transaction で handoff completion と `ai-review-waiting` projection intent を
記録する。outbox consumer が label / comment を再送し、projection failure は finalized Pull Request や
workflow completion を巻き戻さない。

## 外部干渉

- expected head / base と live state が違う場合は stale とし、push や body update を行わない。
- Pull Request が close / merge 済みなら `needs_human` へ routing する。
- transient API failure は同じ logical Operation の新しい attempt として再試行する。
- 人間が編集した body の扱いは observation と required section の整合を確認し、無条件に上書きしない。

## 検証方針

- head、base、open / draft の組み合わせごとに finalize gate を table-driven test で検証する。
- body 生成が同じ artifact input から同じ結果になることを検証する。
- mutation timeout 後の retry と outbox 再送が一つの状態へ収束することを fake GitHub で検証する。
- Controller / Review Worker credential では Pull Request mutation ができない構成を確認する。

## 参照

- [End-to-end workflow](../../05_design/02_workflow.md) §7
- [Architecture](../../05_design/01_architecture.md) — Issue Worker、Mutation authority
- [GitHub routing policy](../../05_design/04_github-routing.md) — Review waiting
- [Worker Operation Protocol](../../05_design/contracts/operation-protocol-v1alpha1.md)
