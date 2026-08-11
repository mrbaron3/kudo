# Product vision

## Problem

既存の Servo は、runtime、provider integration、review orchestration、評価実験、運用基盤が早い段階で結合し、最重要仮説を短い周期で検証しにくくなった。

Kudo は「テストが実装より先に存在し、そのテスト自体が独立に妥当と判定された後で実装する」という一点を、最小の issue-to-PR loop で検証する。

## Intended outcome

ready な GitHub Issue から、次の来歴を機械的に追跡できる PR を作る。

- Issue Workerが直接取得し、digestで固定したIssue RevisionとContext Manifest
- Issue から導出した test plan と test patch
- 対象の振る舞いが未実装であることを示す RED 証跡
- 実装と隔離された Review Worker による test validity verdict
- 同じテストが通る実装と GREEN 証跡
- immutable input に対する最終実装 review verdict

## First vertical slice

最初のvertical sliceは、単一リポジトリの1 Task Issueをend-to-endで証明する。これはruntime全体を直列化する制約ではない。TaskはEpicのsub-issueでもよく、`dependsOn`で結ばれていないready Task Issueは、専用worktreeと独立sessionを使って同時並行に実行できる。

各Taskは自身のOutcome、Scope、Acceptance、Verification、Decision Authorityを完結させる。親Epicは成果trace、依存Issueはreadiness gateであり、親や依存の会話履歴を暗黙の実装contextにしない。

1. Issue WorkerがIssueを直接取得し、Issue Contractの構文と実行可能性を検証する
2. run identity、Issue Revision、解決済みContext Manifestを固定する
3. 専用 worktree と branch を作る
4. test plan、テスト、RED 証跡を artifact として固定する
5. Review Worker が test validity を判定する
6. approve の場合だけ実装し、GREEN 証跡を固定する
7. PR を作成または更新し、最終レビューを実施する

各段階は run-once operation として実装し、watcher はそれを起動する薄い adapter にする。これにより、ポーリング間隔や webhook 配信なしに core behavior をテストできる。

## Principles

- Correctness before throughput: 独立Runの並行実行を許可しつつ、来歴、dependency gate、worktree/session隔離を崩してthroughputを上げない
- Explicit contracts: 欠落情報を推測せず、claim 前に拒否する
- Issue-rooted context: Task Issue自身が実行境界を完結させ、必要なauthorityだけを明示的なreferenceで解決する
- Immutable handoff: worker 間ではパスや会話履歴ではなく digest 付き artifact を渡す
- Independent review: 実装者は自身の test validity または最終実装を approve できない
- Fresh sessions: modelを使うWorker Operationごとに新しいprovider sessionを作り、conversation transcriptを次Operationへ渡さない
- Small runtime: 最初は単一 Go module、単一 binary、単一プロセスでよい
- Replaceable edges: GitHub、model provider、process、storage、telemetry は adapter として交換可能にする

## Not in the first slice

- pass@k や複数候補の比較
- offline/online evaluation harness
- repair supervisor や複数 review session の合議
- merge、deploy、release automation
- tmux、container、PostgreSQL を前提とする実行基盤
- Servo の API、database、artifact layout との互換性
