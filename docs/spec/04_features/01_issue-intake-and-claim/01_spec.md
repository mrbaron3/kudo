# 4.1. Issue 受付・Contract 検証・Claim 機能仕様

GitHub 上の Task Issue を実行候補として検出し、明示された契約と authority を検証したうえで、
後続 Operation が再構築可能な Run input を固定する機能である。

## 目的

- webhook の欠落や重複があっても、同じ live Issue を同じ規則で評価する。
- 曖昧または不足した契約を model の推測で補わず、実行開始前に拒否する。
- Task Issue、authority、dependency、base commit を immutable な実行 context として固定する。

## 4.1.1. Candidate 検出

- webhook を低遅延の通知経路、startup と既定60秒ごとの polling を回復経路として併用する。
- 両経路を同じ冪等な `ReconcileIssue` へ集約する。
- event payload の Issue body を実装入力にせず、live GitHub state で候補条件を確認する。
- open、non-PR、assignee `mrbaron3`、label `ai-ready` をすべて満たす Issue だけを候補にする。
- 条件を満たさない Issue は failure や escalation ではなく、候補外として終了する。

## 4.1.2. Contract 検証

- fixed section と Contract block を strict parse し、missing、unknown、duplicate、ambiguous input を拒否する。
- Acceptance Criteria の ID と本文の対応を検証する。
- Task Issue を execution-context root とし、parent、dependency、comment の prose を暗黙に継承しない。
- 実装入力に使う外部参照は `authorityRefs` に明示されたものだけに限定する。
- human decision が必要な conflict と、再試行可能な transport failure を別の結果として扱う。

## 4.1.3. Claim と Context 固定

- Issue Worker が live GitHub API から claim evidence を取得する。
- Issue Observation、canonical Task Context、Context Manifest、Execution Policy、base SHA を固定する。
- dependency 未完了、authority conflict、base の不整合を claim success として扱わない。
- claim 成功と Run 作成を durable state として確定してから、GitHub status を投影する。
- 同じ Issue に対する concurrent claim は一つの writer-capable Run に収束させる。

## 受け入れ上の不変条件

- `ai-ready` は完成した契約に対する execution request であり、契約内容そのものではない。
- parent は hierarchy、dependency は readiness gate とし、Task が明示しない prose を context に加えない。
- candidate 外、dependency 待ち、contract rejection、transport failure を区別して記録する。
- 同じ Issue に writer-capable な Run を二つ作らない。
- GitHub への label / comment 投影失敗を理由に、確定済み Run を巻き戻さない。

## 正本

- [03. システム仕様](../../03_system-spec/) F-01 / F-02
- [End-to-end workflow](../../../workflow.md) §1〜§2
- [Issue Contract](../../../contracts/issue-contract-v1alpha1.md)
- [Task Context Protocol](../../../contracts/task-context-v1alpha1.md)
- [GitHub routing policy](../../../github-routing.md)
