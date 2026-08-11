// Package integration は Compose 環境を前提とする opt-in の integration test を置く。
//
// 通常の `mise run check` からは除外され、`-tags=integration` を付けた場合だけ
// 実行される。標準の実行入口は `docker compose run --build --rm integration`
// （container 内では `mise run test:integration`）である。
package integration
