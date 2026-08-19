# Kudo

Kudo は、定型の GitHub Task Issue から、独立した test validity review を含む TDD workflow を実行し、
証跡付き Pull Request を人間へ引き渡す issue-to-PR runtime です。

## Documentation

プロダクト仕様、機能別 Acceptance Criteria、詳細設計、versioned contract、実装計画、開発手順は、
すべて [Kudo 仕様書](docs/spec/README.md) に集約しています。`docs/spec/` をリポジトリ内で唯一の
document root とし、本 README は入口だけを提供します。

## Development

正式な開発・テスト入口は Docker Compose です。

```sh
cp .env.example .env
docker compose run --build --rm check
docker compose run --build --rm integration
```

host で直接実行する場合は Go 1.26.5 と [mise](https://mise.jdx.dev/) を使用します。

```sh
mise install
mise run check
go run ./cmd/kudo help
```

環境構築、image build、PostgreSQL integration test の詳細は
[Development environment](docs/spec/06_project/02_development.md) を参照してください。
