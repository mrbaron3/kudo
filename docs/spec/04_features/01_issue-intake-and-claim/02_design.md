# 4.1. Issue 受付・Contract 検証・Claim 詳細設計

[機能仕様](01_spec.md)を、reconciliation、claim、durable transition の三つの境界で実現する。
protocol field の厳密な定義は versioned contract、component の権限は
[Architecture](../../05_design/01_architecture.md) を正本とする。

## 責務分担

| 境界 | 責務 |
| --- | --- |
| Webhook adapter | 署名と payload を検証し、delivery ID と IssueRef を durable inbox へ記録する |
| Poller | 起動時と定期実行で configured repository の候補 IssueRef を列挙する |
| Controller | trigger を `ReconcileIssue` へ集約し、claim lease、Execution / Escalation Policy の解決、Run transition、status projection を調停する |
| Issue Worker | live Issue、relationship、dependency、authority、base を取得し、claim result と immutable claim artifact を構築する |
| Artifact Store | claim artifact の bytes を content-addressed かつ write-once に保存し、digest / length / schema を検証する |
| PostgreSQL adapter | inbox、claim 排他、artifact metadata / ref、Run 作成、outbox を transaction と constraint で保護する |

## Reconciliation

1. ingress は trigger identity と IssueRef を保存し、同じ delivery の重複を吸収する。
2. Controller は IssueRef 単位の reconciliation を起動する。
3. candidate 判定は event payload ではなく live GitHub response に対して行う。
4. 候補外なら terminal な skip result を保存し、Run を作らない。
5. 候補なら IssueRef scoped claim lease を取得して claim Operation を発行する。

Webhook と polling は同じ application operation を呼び、candidate rule を adapter 内へ複製しない。

## Contract compilation と Claim

Issue Worker は Issue body を [Issue Contract](../../05_design/contracts/issue-contract-v1alpha1.md) に従って
deterministic に parse する。relationship、dependency、authority reference は live API から解決し、
[Task Context Protocol](../../05_design/contracts/task-context-v1alpha1.md) に従って次を生成する。

- audit lineage 用の Issue Observation
- model-bearing Operation の入力になる canonical Task Context
- authority content と base を bind する Context Manifest
- claim 時点の base SHA と dependency evidence

Controller は Issue Worker の claim result とは別に、deployment configuration から provider、model、adapter、
tool、timeout を解決して immutable Execution Policy を構築する。attempt retry と review round の上限も
Controller configuration から Escalation Policy へ固定する。Issue 本文、authority、Worker Result から policy 値を推測しない。

Task Context の不足を自然言語要約や provider session で補完しない。Issue Worker が Execution Policy を
選択または生成せず、Controller が reviewer や実装者の入力に依存しない control-plane policy として所有する。

## Durable transition と外部投影

claim 成功を受理する前に、raw Issue body、Issue Observation、Task Context、Context Manifest の bytes を
Artifact Store へ保存し、digest / length / schema と required logical name を検証する。Controller は解決した
Execution / Escalation Policy の canonical ref も検証し、保存済み claim artifact ref と同じ Run へ固定する。

bytes の durability を確認した後、claim 成功時は Run、artifact metadata / ref、policy ref、base、claim result、
次 Operation を一つの database transaction で確定する。同じ transaction で status projection intent を
outbox へ追加し、GitHub mutation は commit 後に非同期実行する。これにより、一時的な GitHub failure と
workflow state を分離する。

IssueRef に active writer-capable Run を一つだけ許す constraint と claim lease により、並行した
webhook / polling が別々の Run を作らないようにする。

## Failure と Recovery

- GitHub / network の一時障害は同じ logical Operation の新しい attempt として再試行する。
- contract rejection、authority conflict、dependency blocked は transport failure と分けて保存する。
- lease expiry 後の attempt は以前の provider session に依存せず、保存済み入力から再構築する。
- status projection は stable identity で再送し、重複 label / comment を作らない。

## 検証方針

- webhook と polling が同じ reconciliation result へ収束することを fake GitHub で検証する。
- duplicate delivery、concurrent claim、outbox 再送を deterministic な store / clock fake で検証する。
- contract parser の missing、unknown、duplicate、ambiguous input を fixture で網羅する。
- live response と event payload が異なる場合に live state が採用されることを検証する。
- required claim artifact の欠落、digest / length / schema 不一致では claim success を受理しないことを検証する。
- 同じ deployment configuration が同じ Execution Policy ref になり、Issue body や Worker Result が policy を
  変更できないことを検証する。

## 参照

- [End-to-end workflow](../../05_design/02_workflow.md) §1〜§2
- [Architecture](../../05_design/01_architecture.md) — GitHub adapters、Durable model、Queue / lease
- [Worker Operation Protocol](../../05_design/contracts/operation-protocol-v1alpha1.md)
