# Kudo

Kudo は、実装前に test validity を独立レビューする、軽量な TDD issue-to-PR automation です。

現在は greenfield bootstrap の段階です。Servo の実装を移植するのではなく、検証に必要な最小の契約から Go で作り直します。

## Runtime model

Kudo の初期ランタイムには、次の3つの論理コンポーネントがあります。

- Issue Worker: readyなTask Issueを直接取得して検証・claimし、テスト作成、RED証跡、実装、GREEN証跡、PR作成を担う
- Review Worker: immutable artifact を読み、test validity と最終実装を独立にレビューする
- Controller: 状態遷移、再試行、artifact の受け渡しを決定論的に制御する

Controllerは品質を採点しません。品質上のverdictはReview Workerが所有します。また、これらは当初から別serviceにせず、1つのGo binary内の明確な境界として実装します。ただし、modelを使うWorker Operationごとに新しいprovider sessionを作り、Worker間またはOperation間でconversation transcriptを共有しません。

## Initial scope

最初のvertical sliceは、1 Task Issue / 1 worktree / 1 branch / 1 PRのend-to-endを対象にします。Task IssueはEpicのsub-issueでも構いませんが、自身の実行境界を完結させます。実運用では、dependencyのないready Task Issueをそれぞれ独立したRunとして同時並行に実行できます。

1. Issue Contract を検証する
2. Issue Worker がテストと RED 証跡を作る
3. Review Worker が test validity を判定する
4. approve 後に Issue Worker が実装と GREEN 証跡を作る
5. PR を作成し、Review Worker が最終実装を判定する

評価ハーネス、pass@k、複数候補の比較、OTel 上の分析機能、merge/deploy は初期スコープ外です。

## Development

Go 1.26.5 と [mise](https://mise.jdx.dev/) を使用します。

```sh
mise install
mise run check
go run ./cmd/kudo help
```

## Documents

- [Product vision](docs/vision.md)
- [Architecture](docs/architecture.md)
- [Implementation plan](docs/implementation-plan.md)
- [Issue Contract](docs/contracts/issue-contract-v1alpha1.md)
- [Implementation–Review Protocol](docs/contracts/review-protocol-v1alpha1.md)
- [Servo からの移行判断](docs/migration-from-servo.md)
- [保留中の評価ハーネス](docs/deferred/evaluation-harness.md)
