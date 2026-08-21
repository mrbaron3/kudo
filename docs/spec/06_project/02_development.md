# Development environment

Milestone 0 の Compose 開発基盤の手順を説明する。host へ Go、PostgreSQL、Kudo daemon を直接 install せず、同じ Docker Compose application で image build・check・PostgreSQL integration test を実行できる。

deployment contract の正本は [Runtime platform](../05_design/03_runtime-platform.md) と [ADR-0001](../../adr/0001-compose-runtime.md) である。本書は開発手順だけを扱い、仕様を再定義しない。

## 前提

- Compose-capable な container runtime
  - macOS: Docker Desktop
  - Linux: Docker Engine と Compose plugin
- Docker socket の mount と Docker-in-Docker は使用しない

host で直接開発する場合だけ、Go 1.26.5 と mise が必要になる（[README](../../../README.md) の Development を参照）。

## 初期設定

非機密の example configuration を copy する。

```sh
cp .env.example .env
```

`.env` は git 管理外で、開発専用の非機密値だけを置く。secret を書かない。

## 標準 command

| Command | 内容 |
| --- | --- |
| `docker compose run --build --rm check` | container 内で `mise run check`（format / vet / unit test）を実行する |
| `docker compose run --build --rm integration` | PostgreSQL の healthcheck を待ち、integration test を実行する |
| `docker compose up --detach --wait postgres` | PostgreSQL 18.4 を起動し healthy を待つ |
| `docker compose down` | stack を停止する（named volume は保持される） |
| `docker compose --profile check down --volumes` | stack を停止し volume も破棄する（`--profile check` がないと `check` / `integration` が使う Go cache volume は残る） |

mise の host 入口も同じ compose command を実行する。

```sh
mise run compose:check
mise run compose:integration
```

`check` と `integration` は run 専用 service（profile `check`）で、`docker compose up` では起動しない。ソースは `/workspace` へ read-only で bind mount され、書き込みは Go cache 用 named volume（`go-build-cache` / `go-mod-cache`）だけに行われる。

## Image build と smoke test

container build 定義は `infra/` に置く（`infra/Dockerfile` と `infra/compose.debug.yaml`）。現在は全 role が同一 binary を使う一つのビルドグラフのため multi-stage の単一 Dockerfile とし、内容の異なる image（provider CLI 入り worker flavor 等）が実体化する Milestone 7 で `infra/` 配下を image ごとに分割する。build context は Go module root（repository root）のままにする。

runtime image（binary と CA 証明書のみ、nonroot）:

```sh
docker build --file infra/Dockerfile --target runtime --tag kudo:local .
docker run --rm kudo:local help
docker run --rm kudo:local version
```

development/test image（Go toolchain と mise、non-root user `kudo`）:

```sh
docker build --file infra/Dockerfile --target dev --tag kudo-dev:local .
docker run --rm kudo-dev:local kudo help
docker run --rm --network=none kudo-dev:local mise run check
```

dev image はソースと module cache を内蔵しているため、bind mount と network なしでも check を再現できる。

multi-platform build の検証（macOS の `linux/arm64` と CI の `linux/amd64` を壊さない）:

```sh
docker buildx build --file infra/Dockerfile --platform linux/amd64,linux/arm64 --target runtime .
```

## PostgreSQL への接続（デバッグ）

既定で PostgreSQL の port は host へ公開しない。手元の psql などで直接見たいときだけ debug override を重ねる。

```sh
docker compose -f compose.yaml -f infra/compose.debug.yaml up --detach --wait postgres
psql "postgres://kudo:kudo-dev-password@127.0.0.1:5432/kudo"
```

## Integration test

integration test は build tag `integration` で opt-in にしている。`mise run check` と `go test ./...` には含まれない。

- 標準入口: `docker compose run --build --rm integration`
- container 内の実体: `mise run test:integration`（`KUDO_TEST_DATABASE_URL` が必要）

test は internal network 経由で `postgres:5432` へ接続し、server version（18.4 系）と書き込み・読み出しを検証する。

## Version の固定と更新手順

再現性のため、外部 input はすべて version と digest/checksum で固定している。更新するときは対応する組を必ず同時に差し替える。

| 固定対象 | 場所 | 更新方法 |
| --- | --- | --- |
| `golang:1.26.5-trixie` | `infra/Dockerfile` の `GO_IMAGE` | `docker buildx imagetools inspect golang:<tag>` で digest を取得し、tag と digest を同時更新する |
| `gcr.io/distroless/static-debian12:nonroot` | `infra/Dockerfile` の `RUNTIME_IMAGE` | 同上 |
| `postgres:18.4` | `compose.yaml` の `postgres.image` | 同上。あわせて [Runtime platform](../05_design/03_runtime-platform.md) の version 記述、本書の 18.4 記述、`test/integration/postgres_test.go` の server_version assertion を整合させる |
| mise | `infra/Dockerfile` の `MISE_VERSION` / `MISE_SHA256_LINUX_*` | release の `SHASUMS256.txt` から linux-x64 / linux-arm64 の sha256 を取得して同時更新する |
| Go toolchain（host） | `mise.toml` の `[tools]` | `infra/Dockerfile` の `GO_IMAGE` と同じ version に保つ |

`GOTOOLCHAIN=local` を設定しているため、go.mod と image の Go version がずれた場合は暗黙 download せず失敗する。

## Volume / network naming

後続 milestone が同じ Compose project を拡張する前提で、naming を固定している。

| Name | 用途 | 備考 |
| --- | --- | --- |
| network `internal` | service 間の内部通信 | 外部公開用 `ingress` network は Controller 実装時に追加する |
| volume `postgres-data` | PostgreSQL の durable state | mount 先は `/var/lib/postgresql`。PostgreSQL 18 の official image は PGDATA を `/var/lib/postgresql/18/<dir>` に置くため、`/var/lib/postgresql/data` を volume にするとデータが named volume の外へ落ちる |
| volume `go-build-cache` / `go-mod-cache` | 開発用 Go cache | 破棄して再生成できる |
| service `postgres` | workflow state / queue の正本 | `controller` / `issue-worker` / `review-worker` / `migrate` service は実装時に追加する |
