# Kudo

Kudo は、人間が定型の GitHub Issue を用意すると、独立した test validity review を挟んだ TDD で実装し、人間がレビューできる Pull Request まで届ける issue-to-PR runtime です。

目標はデモ用の最小ループではありません。Webhook の欠落、process 再起動、provider failure、複数 Issue の同時実行を前提に、実行状態と証跡を復元できる単一ホスト向けの運用可能なシステムを作ります。

## Product flow

1. 人間が [Issue Contract](docs/contracts/issue-contract-v1alpha1.md) に従って Task Issue を記述し、`mrbaron3`を assign して`ai-ready`を付ける。
2. Controller が GitHub webhook または定期 polling から同じ Issue reconciliation を起動する。
3. Issue Worker が現在の Issue と参照先を検証して claim し、Controller が Issue を`ai-in-progress`へ投影する。
4. 新規 provider session がテストを先に作り、対象機能が未実装であることを示す RED 証跡を固定する。
5. Review Worker の新規 read-only session が Issue と immutable artifact を読み、test validity を判定する。指摘があれば、新規の修正 session へ versioned finding を返す。
6. 承認済みテストを入力に、新規 implementation session が GREEN、refactor、規定の検証を完了する。
7. Review Worker が最終成果を独立レビューし、approve 後に Issue Worker だけが Pull Request を作成する。
8. Issue を`ai-review-waiting`へ投影し、人間の Pull Request review へ引き渡す。

各 model-bearing Operation は必ず fresh session で実行します。同じ Run の worktree を引き継ぐ場合も、前 session の transcript や private memory は渡さず、Issue Revision、commit、artifact、Review Result だけを明示的に handoff します。

## Runtime

正式な実行基盤は Docker Compose です。同じ Go binary を役割別 container として起動します。

- `controller`: webhook、60秒ごとの fallback polling、state transition、Operation dispatch、GitHub status projection
- `issue-worker`: test/implementation session、command、worktree、branch、Pull Request mutation の唯一の所有者
- `review-worker`: immutable input と独立 checkout だけを読む reviewer
- `postgres`: Run、Operation queue、lease、inbox/outbox、artifact metadata の正本
- `migrate`: schema migration を行う one-shot job

artifact は content-addressed な named volume、Issue Worker の workspace は専用 named volume に保存します。Controller と Review Worker は implementation worktree を mount しません。Container へ Docker socket を渡さず、provider CLI は各 Worker container 内の child process として実行します。

詳細は [Runtime platform](docs/runtime-platform.md) と [Compose 採用 ADR](docs/decisions/0001-compose-runtime.md) を参照してください。

## Repository status

文書は完成形の product/runtime specification を定義しています。現在の code は CLI bootstrap と Milestone 0 の Compose 開発基盤（build/test image、PostgreSQL、integration test 入口）まで実装済みで、workflow 本体は未実装です。実装順序と全体の完了条件は [Implementation plan](docs/implementation-plan.md) を正とします。

## Development

正式な開発・テスト入口は Docker Compose です。host へ Go / PostgreSQL を install せず、build と check を実行できます。手順の詳細は [Development environment](docs/development.md) を参照してください。

```sh
cp .env.example .env
docker compose run --build --rm check        # container 内で mise run check
docker compose run --build --rm integration  # PostgreSQL integration test
```

host で直接開発する場合は Go 1.26.5 と [mise](https://mise.jdx.dev/) を使用します。

```sh
mise install
mise run check
go run ./cmd/kudo help
```

## Documents

文書の役割と優先順位は [Documentation map](docs/README.md) にまとめています。

- [Product vision](docs/vision.md)
- [End-to-end workflow](docs/workflow.md)
- [Architecture](docs/architecture.md)
- [Runtime platform](docs/runtime-platform.md)
- [GitHub routing policy](docs/github-routing.md)
- [Implementation plan](docs/implementation-plan.md)
- [Issue Contract](docs/contracts/issue-contract-v1alpha1.md)
- [Worker Operation Protocol](docs/contracts/operation-protocol-v1alpha1.md)
- [Implementation–Review Protocol](docs/contracts/review-protocol-v1alpha1.md)
- [Compose 採用 ADR](docs/decisions/0001-compose-runtime.md)
- [Servo からの移行判断](docs/migration-from-servo.md)
- [保留中の評価ハーネス](docs/deferred/evaluation-harness.md)
