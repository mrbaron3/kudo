# 01. プロダクト設計

Kudo が誰のどの問題を解き、どこまでを製品責任として引き受けるかを定義する。
厳密な完成条件は [Product vision](../../vision.md)、システム全体の要求は
[03. システム仕様](../03_system-spec/) を参照する。

## 1. 解決する課題

Issue からコードを生成するだけでは、人間が安心して Pull Request をレビューできる状態にはならない。
少なくとも次の問題が残る。

- Issue の不足を会話履歴や実装者の推測で補うと、何を根拠に実装したか再現できない。
- 実装者自身が追加した test だけでは、その test が受け入れ条件を正しく表すか独立に確認できない。
- model session や process-local memory に進行状態を置くと、再起動や timeout 後に安全に再開できない。
- webhook の欠落、重複 event、外部 push、provider failure が二重実行や古い approval の再利用を招く。
- 最終差分だけを見ても、RED、GREEN、review、必須 check がどの commit に対して成立したか追跡しにくい。

Kudo はこれらを、明示的な Issue Contract、TDD gate、独立 review、immutable evidence、durable
workflow を一つの issue-to-PR runtime として提供することで解決する。

## 2. プロダクトのゴール

人間が実装 session を監視し続けなくても、定型の Task Issue から次の状態まで自動で進める。

1. 実行可能な Issue だけを claim する。
2. 受け入れ条件に対応する test を先に作り、未実装を示す RED を固定する。
3. 実装とは隔離された Review Worker が test validity を承認する。
4. 承認済み test を変えずに実装し、GREEN、refactor、必須 check の証跡を固定する。
5. 最終 head を独立 review し、承認された同じ head の Pull Request を人間へ渡す。

正常な完了点は、Pull Request が review 可能な状態になり、Issue が
`ai-review-waiting`へ投影された時点である。merge、release、最終的な製品判断は人間が所有する。

## 3. 利用者と責任

| 利用者 | Kudo に期待すること | 利用者が所有する判断 |
| --- | --- | --- |
| Issue Author | 定型 Issue から、受け入れ条件に沿った実装 PR が作られる | Outcome、Scope、Acceptance Criteria、authority、停止条件の確定 |
| Repository Maintainer | RED / GREEN / review の根拠を追跡できる PR をレビューする | 最終 review、merge、release |
| Operator | 再起動や外部障害後も Run を診断し、安全に回復する | credential、capacity、運用 policy、人間への escalation 対応 |

Kudo は曖昧な要求を補完するプロダクトではない。必要情報が不足または競合する場合は claim を拒否するか、
根拠と必要な対応を示して人間へ差し戻す。

## 4. 提供価値

### 4.1. 実装前に test の妥当性を検証する

production code より先に test と RED evidence を作り、別の read-only session が Acceptance Criteria
との対応を評価する。実装しやすい test へ都合よく寄せることを workflow の境界で防ぐ。

### 4.2. 判断根拠を commit と artifact に固定する

Issue Observation、canonical Task Context、Context Manifest、test plan、command evidence、Review
Result を digest 付きで残す。どの入力と head に対する approval かを再計算できるようにする。

### 4.3. 中断を前提に安全に回復する

workflow state と queue を PostgreSQL に置き、外部 mutation は idempotency identity と live state
照合を伴って行う。process が失われても、確定済み commit と artifact から新しい attempt を開始する。

### 4.4. 実装と review の独立性を保つ

Issue Worker だけが implementation worktree、branch、Pull Request を変更できる。Review Worker は
別 checkout と immutable input だけを読み、write credential と implementation workspace を持たない。

## 5. プロダクト境界

Kudo が担当する範囲は、実行依頼の検出から、人間がレビューできる Pull Request の handoff までである。
次は標準 workflow に含めない。

- 曖昧な Issue を人間に代わって完成させること
- Pull Request の merge、Issue close、deploy、release
- 人間の Pull Request review comment に対する自動修正 loop
- pass@k、best-of-N、複数 candidate の競争実行や scoring dashboard
- 複数 host を前提とした distributed scheduler
- Servo の API、database schema、artifact layout との互換性

## 6. 成功条件

一度 happy path が動くことだけを完成とはしない。重複・遅延 event、webhook 欠落、process restart、
stale input、複数 Issue の同時実行を含む条件で、正しい Run と Pull Request に収束する必要がある。
検証対象の完全な一覧は [Product vision — Definition of product completion](../../vision.md) を正とする。
