# 4.1. Issue 受付・Contract 検証・Claim 詳細設計

[機能仕様](01_spec.md)を、reconciliation、claim、durable transition の三つの境界で実現する。
protocol field の厳密な定義は versioned contract、component の権限は
[Architecture](../../05_design/01_architecture.md) を正本とする。

## 責務分担

| 境界 | 責務 |
| --- | --- |
| Webhook adapter | 署名と payload を検証し、IssueRef の reconcile を trigger する |
| Poller | 起動時と定期実行で configured repository の候補 IssueRef を列挙する |
| Controller | trigger を `ReconcileIssue` へ集約し、phase 導出、Execution / Escalation Policy の解決、label / comment の記録を調停する |
| Issue Worker claim handler | GitHub Reader、Issue Compiler、Context Resolverを調停し、schema/digest/baseだけを持つclaim contextを構築する。model sessionは起動しない |
| GitHub / repository reader | API response を typed data へ変換し、live Issue、relationship、dependency、authority、base を取得する。Issue Contract の意味解析は行わない |
| Issue Compiler | verified Issue identity と raw body だけから Issue Observation、canonical Task Context、Claim Requirements を決定論的に生成する唯一の pure component |
| Context Resolver | Claim Requirements に従って relationship、dependency completion、authority content、base を live source から解決し、Context Manifest と claim evidence を構築する |
| GitHub recorder | claim checkpoint（PR body machine block）、label、comment を marker で冪等に記録する |

## Reconciliation

1. ingress は trigger を受けて IssueRef 単位の reconciliation を起動する。重複 trigger は観測の
   再実行になるだけである。
2. candidate 判定は event payload ではなく live GitHub response に対して行う。
3. 候補外なら terminal な skip result として終了し、Run を作らない。
4. merged な kudo PR を持つ IssueRef は `skipped_already_merged` として終了し、`ai-ready` を外して
   `ai-merged` と案内 comment を再記録する（[GitHub routing policy](../../05_design/04_github-routing.md)）。
5. 候補なら claim Operation を dispatch する。排他は branch `kudo/issue-<n>` の ref create が確定する。

Webhook と polling は同じ application operation を呼び、candidate rule を adapter 内へ複製しない。

## Contract compilation と Claim

Issue Worker の claim handler は GitHub Reader が取得した raw Issue body と verified Issue identity を
[Issue Compiler](../../05_design/contracts/task-context-v1alpha1.md#issue-compiler-boundary)へ渡す。Compiler は
[Issue Contract](../../05_design/contracts/issue-contract-v1alpha1.md)を deterministic に strict parse し、
次を生成する。

- 観測 audit 用の Issue Observation
- model-bearing Operation の入力になる canonical Task Context
- relationship と authority 解決に必要な Claim Requirements

Context Resolver は Claim Requirements に従って relationship、dependency、authority reference、base を
live source から解決し、[Task Context Protocol](../../05_design/contracts/task-context-v1alpha1.md)に従って
次を生成する。

- authority content と base を bind する Context Manifest
- claim 時点の base SHA と dependency evidence

Issue Compiler は外部 I/O を持たず、Issue Contract の section と Acceptance Criteria を解釈する唯一の
component である。GitHub Reader は transport を解析するだけで、Compiler 以外の Controller、claim
handler、後続 Worker は raw body を意味解析しない。model-bearing Operation へ渡す Issue 表現は
canonical Task Contextに限定する。raw Issue body、Task Context、Context Manifestは永続化せず、後続の
Issue Worker / Review Workerが各Operationでlive sourceから再生成する。sourceとauthority contentはYAMLへ
要約せず、固定baseまたはGitHubから取得したexact bytesをそのAttemptだけで使う。

Controller は Issue Worker の claim result とは別に、deployment configuration から provider、model、adapter、
tool、timeout を解決して immutable Execution Policy を構築する。attempt retry と review round の上限も
Controller configuration から Escalation Policy へ固定する。Issue 本文、authority、Worker Result から policy 値を推測しない。

Task Context の不足を自然言語要約や provider session で補完しない。Issue Worker が Execution Policy を
選択または生成せず、Controller が reviewer や実装者の入力に依存しない control-plane policy として所有する。

## Durable transition と外部投影

claim成功を受理する前に、Issue WorkerはCompiler version、Issue Observation ref/body digest、Task Context
ref、Context Manifest ref、base SHAからなるclaim contextを返す。Controllerはversioned schema、
digest、Issue identityとのbindingを検証する。raw Issue body、Issue Observation YAML、Task Context YAML、
Context Manifest YAMLは保存しない。

claimの確定は次の順で行う。branch `kudo/issue-<n>` のref create（atomic、既存なら claim 失敗）、
bootstrap commit、draft PRのensure、claim checkpoint（claim context、Execution / Escalation Policy ref）
のPR body machine blockへの記録。途中でprocessが停止しても、再観測が「branchはあるがPRが無い」等の
中間状態を導出し、残りの手順を冪等に完了する。label記録の失敗はRunを巻き戻さず、次のreconcileが
収束させる。

branch ref create のatomicityにより、並行したwebhook / pollingが別々のRunを作らないようにする。

## Failure と Recovery

- GitHub / network の一時障害は同じ logical Operation の新しい attempt として再試行する。
- contract rejection、authority conflict、dependency blocked は transport failure と分けて保存する。
- process再起動後のattemptは以前のprovider sessionに依存せず、claim checkpointとlive
  GitHub/sourceから入力を再取得・再compileする。
- label / comment の記録は marker を検索してから行い、重複を作らない。

## 検証方針

- webhook と polling が同じ reconciliation result へ収束することを fake GitHub で検証する。
- duplicate delivery、concurrent claim、記録の retry を deterministic な GitHub / clock fake で検証する。
- contract parser の missing、unknown、duplicate、ambiguous input を fixture で網羅する。
- live response と event payload が異なる場合に live state が採用されることを検証する。
- required claim context fieldの欠落、schema/digest/Issue identityの不一致ではclaim successを受理しないことを検証する。
- 各後続Operationの開始時・完了時にlive Issue/authorityを再取得し、同じdigestなら継続、変更ならstaleとして返すことを検証する。
- 同じ deployment configuration が同じ Execution Policy ref になり、Issue body や Worker Result が policy を
  変更できないことを検証する。

## 参照

- [End-to-end workflow](../../05_design/02_workflow.md) §1〜§2
- [Architecture](../../05_design/01_architecture.md) — GitHub adapters、State model
- [Worker Operation Protocol](../../05_design/contracts/operation-protocol-v1alpha1.md)
