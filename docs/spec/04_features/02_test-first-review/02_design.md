# 4.2. Test Authoring・RED・Test Validity Review 詳細設計

[機能仕様](01_spec.md)を、Issue Worker の test lane、draft Pull Request への publish、Review Worker の
read-only evaluation として実現する。

## Operation の流れ

```text
author_tests
  -> RED evidence 固定
  -> publish_head(test-only head)
  -> review(test_validity)
       ├─ approve         -> implementation gate
       ├─ request_changes -> revise_tests -> publish -> new review
       └─ needs_human     -> escalation
```

各 model-bearing Operation は新しい provider process と operation-scoped state directory を使用する。
同じ Run の worktree は Issue Worker が引き継げるが、継続情報は commit と artifact へ固定する。

## Test Authoring と RED

ControllerはTask Context / Context Manifestの期待digest、base / head、Execution Policyをbindした
`author_tests` を Issue Worker へ dispatch する。Issue Worker は test plan、test patch、test-only commit を
作り、repository が規定する command を実行する。

RED evidence は command、exit status、bounded output、environment identity、対象 head を含み、
record surface から一意に参照できるようにする。RED の因果を確認できない Result は publish や
review の入力として受理しない。

## Publish と Review binding

Issue Worker の `publish_head` は expected head と live branch head を照合してから push し、同じ Run の
draft Pull Request を冪等に ensure する。Controller は published head、PR reference、PR observation が
durable に記録された後にだけ Review Request を発行する。

Review Request は exact head、Context Manifest、Execution Policy、required inputs、policy ref へ bind する。
Review Worker は live Issue / PR freshness を決定論的に照合した後、disposable read-only checkout と fresh
provider session で [test_validity Agent Package instructions](../../../../agent-packages/test_validity/v1alpha1/instructions.md)
を適用する。

## Verdict routing

- `approve`: Result digest と承認対象を固定し、implementation gate を開く。
- `request_changes`: finding と元 artifact を新しい `revise_tests` session へ渡す。修正後は新しい head と
  manifest で必ず再 review する。
- `needs_human`: 自動修正できない authority または安全判断として durable に停止する。
- transport / protocol failure: quality verdict とせず、retry または terminal execution failure とする。
- stale input: review を進めず、現在の identity に対する新しい Request を要求する。

`revise_tests` は review の `request_changes` だけでなく、implementation lane の `test_revision_required`
（[4.3 詳細設計](../03_implementation-review/02_design.md)）からも dispatch される。入力の finding が
Review Result か `test-revision-report` かの違いだけで、publish と再 review の流れは同じである。
どちらの差し戻しも `test_validity` gate の無人 round 予算を消費する。

## Isolation

- Issue Worker だけが implementation worktree と Pull Request を変更できる。
- Review Worker は Issue Worker workspace を参照せず、判定対象への write を持たない。verdict / finding の記録は自分の App 名義で行う。
- transcript、resume token、provider private state を Operation 間で共有しない。
- review round counter と上限は Controller が保持し、reviewer の品質判断入力へ渡さない。

## 検証方針

- test-only commit と RED evidence の binding を filesystem / process fake で検証する。
- publish 前後の expected / live head 不一致を fake GitHub で検証する。
- missing policy、unsupported version、stale manifest が review verdict にならないことを検証する。
- approve、request_changes、needs_human の各 routing と round 上限を deterministic store / clock で検証する。

## 参照

- [End-to-end workflow](../../05_design/02_workflow.md) §3〜§4
- [Architecture](../../05_design/01_architecture.md) — Issue Worker、Review Worker、Context and session isolation
- [Implementation–Review Protocol](../../05_design/contracts/review-protocol-v1alpha1.md)
