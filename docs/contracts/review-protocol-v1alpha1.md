# Implementation–Review Protocol v1alpha1

## Purpose

ImplementationとReviewを、同じrepository、binary、OS processを利用できても、同じmodel/provider sessionまたはmutable contextを共有しない独立したroleとして接続する。

Implementationはsource変更、worktree、branch、commit、PR mutationを所有する。Reviewはread-only inputからverdictを作り、Implementationは自分のrequestをapproveできない。

各Review Requestは新しいprovider sessionで処理する。前のImplementationまたはReview sessionのconversation transcript、session ID、private memoryを入力にしない。

## Review Request

Review Requestは `kudo.review-request/v1alpha1` としてversion付けする。

```yaml
schema: kudo.review-request/v1alpha1
requestId: 01KUDOEXAMPLE
kind: test_validity
producerRunId: run-01
repository: github://owner/repository
issue: github://owner/repository/issues/42
issueRevision: sha256:<digest>
headSha: <git-commit-sha>
contextManifest: sha256:<digest>
artifactManifest: sha256:<digest>
policyRefs:
  - docs/contracts/issue-contract-v1alpha1.md
createdAt: 2026-08-11T00:00:00Z
```

`kind` は初期版で次の2種類とする。

- `test_validity`: test plan、test patch、RED 証跡がIssue Contractを正しく検証するか
- `final_implementation`: approved testとIssue Contractに対して実装が正しく、回帰や重大なriskがないか

Review Workerは`issueRevision`が指すIssue Revision artifactを検証したうえで、`issue`をGitHubから直接取得し、現在bodyのdigestがartifact内の`bodyDigest`と一致することを確認する。保存済みIssue本文だけを現在のIssueであるかのように扱わない。GitHub accessをfakeへ置き換えるtestでも、同じIssue Reader contractを通す。

Request identityは、schema、kind、repository、Issue reference、Issue Revision digest、head SHA、Context Manifest digest、artifact manifest digest、policy refsから決まる。同じ`requestId`でもこれらが異なる入力を同一requestとして扱ってはならない。

local path、worktree path、provider session ID、会話履歴、application-private database recordをreviewの必須入力にしない。

## Artifact Manifest

manifestは、reviewに必要なartifactのlogical name、media type、byte length、SHA-256 digestを列挙する。最低限、`test_validity` では次を参照できるようにする。

- Issue Revision evidence。取得時のIssue本文、取得identity、body digestを含む
- Context Manifest
- implementation brief
- test plan
- test patchまたは固定済みsource snapshot
- RED command、exit status、stdout/stderr artifact

`final_implementation` では、approved test validity result、実装patch、GREEN証跡を追加する。artifactのbytesが変われば新しいdigestとrequestを作る。

## Review Result

Review Resultは `kudo.review-result/v1alpha1` としてversion付けする。

```yaml
schema: kudo.review-result/v1alpha1
requestDigest: sha256:<digest>
reviewRunId: review-01
verdict: request_changes
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

verdictは次のいずれかとする。

- `approve`: blocking findingがなく、対象requestを次の状態へ進められる
- `request_changes`: 修正可能なblocking findingがある
- `needs_human`: authority conflictや安全上の判断など、人の決定が必要

findingは `expected`、`observed`、`evidenceRefs` を持ち、単なる感想にしない。Review Resultはproducerのworktreeを変更せず、新しいartifactとして保存する。

## Failure and staleness

timeout、rate limit、network error、provider crash、invalid responseはtransport/execution failureであり、`request_changes`や`needs_human`という品質verdictに変換しない。Issueを取得できない場合もtransport failureであり、保存済み本文だけでreviewを続けない。

Issue Revision、Context Manifest、commit SHA、artifact manifest、policy refのいずれかが変わった時点で既存Resultはstaleになる。live Issueのbody digestがIssue Revision artifact内の`bodyDigest`と一致しない場合は品質verdictを返さず、stale inputとしてControllerへ返す。修正後は新しいReview Requestを発行し、古いResultを新しい入力のapprovalとして再利用しない。
