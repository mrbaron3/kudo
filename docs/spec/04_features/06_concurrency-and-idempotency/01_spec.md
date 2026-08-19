# 4.6. Dependency・並行実行・冪等性 機能仕様

明示された dependency だけを readiness gate とし、独立 Issue の並行実行を許可しながら、同じ対象への
重複実行と競合する mutation を防ぐ機能である。

## 目的

- repository 全体を直列化せず、dependency-free な Task を安全に並行実行する。
- duplicate webhook、polling、dispatch、retry が同じ論理結果へ収束するようにする。
- 外部変更を検出し、自動化が人間や別 Run の変更を上書きしないようにする。

## 4.6.1. Dependency と Readiness

- `dependsOn` だけを readiness edge とする。
- parent、Issue 番号、Project phase から実行順序を推測しない。
- dependency の完了状態を live authority から確認して claim input に固定する。
- dependency blocked と worker / provider capacity 待ちを別の状態として扱う。
- dependency graph の循環や曖昧な参照を実行可能として扱わない。

## 4.6.2. Scoped Concurrency

- dependency のない ready Issue は、Run scoped lease と専用 workspace で同時実行できる。
- 同じ Issue に writer-capable な Run を二つ作らない。
- 一つの Run で state-advancing Operation を同時に一つだけ実行する。
- 一つの worktree に writer を一つだけ置き、review 中は対象 head を変更しない。
- repository 全体を覆う global lock を置かない。

## 4.6.3. Idempotency と外部干渉

- webhook / polling、Operation dispatch、GitHub mutation の重複を stable identity で吸収する。
- branch / Pull Request mutation 前後で expected state と live state を照合する。
- timeout 後は mutation 済みかを観測してから retry し、同じ branch / PR / comment を重複作成しない。
- head、base、PR identity の外部変更は blind overwrite せず stale または `needs_human` とする。

## 受け入れ上の不変条件

- ready Issue の並行数を一つに固定しない。
- scope claim と execution lease は Issue、Run、Operation の必要最小範囲に限定する。
- 同じ idempotency identity に異なる semantic input を受理しない。
- Review Worker は並行実行中も implementation workspace や write credential を共有しない。

## 正本

- [03. システム仕様](../../03_system-spec/) F-09
- [End-to-end workflow](../../../workflow.md) — Idempotency and recovery
- [Architecture](../../../architecture.md) — Scheduling and concurrency
- [GitHub routing policy](../../../github-routing.md)
- [Worker Operation Protocol](../../../contracts/operation-protocol-v1alpha1.md)
