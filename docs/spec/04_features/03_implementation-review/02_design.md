# 4.3. Implementation・GREEN・Final Review 詳細設計

[機能仕様](01_spec.md)を、Issue Worker の implementation lane と Review Worker の final review lane に
分離して実現する。

## Operation の流れ

```text
approved test validity result
  -> implement
  -> GREEN / refactor / required checks
  -> publish_head(final head)
  -> review(final_implementation)
       ├─ approve         -> Pull Request 確定と merge gate
       ├─ request_changes -> repair_implementation -> publish -> new review
       └─ needs_human     -> escalation
```

## Implementation lane

Controllerはapproved test resultとそのbinding、Task Context / Context Manifestの期待digest、現在head、
Execution Policy を `implement` input として固定する。Issue Worker は Run 専用 worktree で production code を
変更し、承認済み test を書き換えない。

test 変更の必要性を検出した場合は、未承認の変更を最後に承認された test checkpoint へ rollback し、
rollback 済み head と `test-revision-report` を持つ `test_revision_required` Result として返す。Controller は
`test_validity` の無人 round 予算を1消費して `revise_tests` を dispatch し、上限到達時は
`review_round_limit_exceeded` として `needs_human` へ送る。implementation session が独自判断で
test approval の対象を書き換えて進行してはならない。

## Evidence と Publish

Issue Worker は対象 test、必要な regression test、Issue Verification、repository required checks を実行する。
Task Contextまたはauthorityにperformance boundが宣言されている場合は、Taskが指定する測定commandを固定した条件で複数回実行し、結果要約とbound比較を`performance-evidence`として生成する。bound宣言がない場合、標準harnessを推測して測定を必須化しない。
各 evidence は command、result、environment、producer、final head とともに Artifact Manifest へ記録する。

すべての required evidence が揃った後に `publish_head` を実行する。expected branch head と live state を
照合し、外部 push がある場合は blind overwrite せず stale とする。published final head と PR observation の
durable record が final review の前提になる。

## Final review lane

Controller は final head、approved test result、implementation patch、GREEN / refactor / check evidence、
required policy ref を Review Request へ bind する。Review Worker は live freshness を検証し、独立した
read-only checkout と fresh session で
[Final Implementation Review Policy](../../05_design/review-policies/final-implementation-v1alpha1.md) を適用する。

条件付き観点を含む applicability 宣言が欠けた Result は binding 境界で拒否する。Controller は verdict の
schema と freshness を検証するが、reviewer の品質判断を上書きしない。

## Repair と再評価

`request_changes` の finding は versioned Result として固定し、新しい `repair_implementation` session へ
渡す。repair 後は checks、publish、final review を新しい head に対してやり直す。以前の approval を
異なる head や manifest に移し替えない。

## 検証方針

- approval 前の `implement` dispatch と、approved test の暗黙変更を拒否する。
- `test_revision_required` の round 消費と、上限到達時の escalation routing を検証する。
- GREEN / required checks、またはbound宣言時の`performance-evidence`が不足した Result から final review を発行しない。
- final head、artifact、policy の変更で approval が stale になることを検証する。
- Issue Worker と Review Worker の credential、workspace、provider state が分離されることを境界 test で確認する。

## 参照

- [End-to-end workflow](../../05_design/02_workflow.md) §5〜§6
- [Architecture](../../05_design/01_architecture.md) — Mutation authority、Context and session isolation
- [Worker Operation Protocol](../../05_design/contracts/operation-protocol-v1alpha1.md)
- [Implementation–Review Protocol](../../05_design/contracts/review-protocol-v1alpha1.md)
