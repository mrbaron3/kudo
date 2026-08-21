# 01. プロダクト設計

Kudo が誰のどの問題を解き、どこまでを製品責任として引き受け、何をもって完成とするかを定義する。
本書をプロダクトの目的、境界、完成条件の正本とし、システム全体の要求は
[03. システム仕様](../03_system-spec/) で具体化する。

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
`ai-review-waiting` へ投影された時点である。merge、release、最終的な製品判断は人間が所有する。

## 3. 利用者と責任

| 利用者 | Kudo に期待すること | 利用者が所有する判断 |
| --- | --- | --- |
| Issue Author | 定型 Issue から、受け入れ条件に沿った実装 PR が作られる | Outcome、Scope、Acceptance Criteria、authority、停止条件の確定 |
| Repository Maintainer | RED / GREEN / review の根拠を追跡できる PR をレビューする | 最終 review、merge、release |
| Operator | 再起動や外部障害後も Run を診断し、安全に回復する | credential、capacity、運用 policy、人間への escalation 対応 |

Kudo は曖昧な要求を補完するプロダクトではない。必要情報が不足または競合する場合は claim を拒否し、
rejection category と対象 section / field を示して人間へ差し戻す。

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
- Kubernetes を必須とする deployment
- Servo の API、database schema、artifact layout との互換性

評価 harness は runtime telemetry と分離し、別の design decision まで保留する。

## 6. 必須成果物と Lineage

対象 Issue から作られた Pull Request には、少なくとも次の lineage が存在しなければならない。

- GitHub から直接取得した exact body を固定する Issue Observation
- strict parse 済み Issue を固定表現にした canonical Task Context
- base commit、dependency completion、authority content を固定した Context Manifest
- Issue の Acceptance Criteria に対応する test plan と test patch
- 対象機能が未実装であることを示す RED command evidence
- 実装と隔離された Review Worker による test validity approval
- 承認済み test を通す実装、refactor、GREEN command evidence
- 最終 head に対する独立した final implementation approval
- 実行した必須 check、その結果、残存 risk を含む Pull Request

artifact、Review Request、Review Result は immutable identity を持つ。Context Manifest、Execution Policy、
head SHA、artifact digest、policy reference のいずれかが変われば、以前の review approval は再利用しない。
Issue Observation だけの変化は audit lineage への追記であり、approval を stale にしない。

## 7. 完成条件

完成とは happy path の demo が一度動くことではない。少なくとも次を automated test または明示した
live verification で証明する。

- webhook を失っても polling が同じ候補を発見する。
- duplicate、遅延、順不同 event が二重 Run または二重 Pull Request を作らない。
- Controller または Worker の再起動後に、lease と durable state から処理を回復できる。
- test review の `request_changes` が fresh session に handoff され、approve まで実装へ進まない。
- RED、GREEN、refactor 後 checks、二つの review approval が対象 digest と一致する。
- dependency のない複数 Issue は同時実行でき、同じ Issue は二重実行されない。
- Review Worker は implementation worktree と write credential を持たない。
- Task Context に影響する Issue 変更、または head 変更が以前の approval を stale にする。
- Pull Request と `ai-review-waiting` projection が crash / retry 下でも一度だけ成立する。
- Compose stack の health、migration、backup / restore、graceful shutdown 手順が検証されている。
