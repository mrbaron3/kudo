# 4.6. Dependency・並行実行・冪等性 受け入れ要件

[03. システム仕様](../../03_system-spec/) F-09 に対する Why / What の受け入れ基準である。
dependency graph、GitHub CAS、stable identity の実現方法は [詳細設計](02_design.md) で扱う。

## サブ機能一覧

| ID | サブ機能 | 優先度 |
| --- | --- | --- |
| 4.6.1 | Dependency と Readiness | 高 |
| 4.6.2 | Scoped Concurrency | 高 |
| 4.6.3 | Idempotency と外部干渉 | 高 |

## 4.6.1. Dependency と Readiness

**ユーザーストーリー**

- 誰が: 複数の Task Issue を Kudo に依頼する開発者
- 何を: 明示した dependency が完了した Task だけを実行し、無関係な Task は待たせず進めたい
- なぜ: 暗黙の順序に依存せず、安全性と処理能力を両立できるから

**事前条件**

- Task Issue の `dependsOn` が Issue Contract に従って記述されている。
- dependency の live completion state を取得できる。

**受け入れ基準**

- **正常系: Dependency-free Task**
  - Given Task に `dependsOn` がなく、candidate と contract 条件を満たしている。
  - When readiness を評価する。
  - Then 他 Issue の番号、parent、Project phase を待たずに claim 可能になる。

- **正常系: Dependency 完了**
  - Given すべての `dependsOn` Issue が完了条件を満たしている。
  - When live dependency state を評価する。
  - Then dependency evidence が claim input に固定され、Task は ready になる。

- **待機系: Dependency 未完了**
  - Given 一つ以上の `dependsOn` Issue が未完了である。
  - When readiness を評価する。
  - Then dependency blocked として待機し、writer-capable execution は開始されない。

- **境界: Parent の非Dependency化**
  - Given Task に parent Issue があるが `dependsOn` には指定されていない。
  - When readiness を評価する。
  - Then parent は hierarchy と traceability にだけ使われ、実行順序を作らない。

- **異常系: 循環または曖昧な参照**
  - Given dependency graph に循環、自己参照、解決不能な参照がある。
  - When readiness を計算する。
  - Then ready とせず、曖昧な順序を推測せずに理由を提示する。

- **分類: Capacity 待ち**
  - Given dependency は完了しているが、worker または provider capacity が埋まっている。
  - When scheduling を行う。
  - Then capacity waiting として扱い、dependency blocked と記録しない。

**非機能要件**

- 決定性: 同じ dependency graph と live completion state から同じ readiness result を得る。
- 可観測性: blocked edge と capacity wait reason を区別して追跡できる。
- 拡張性: dependency-free Issue の数を一つの global lock で直列化しない。

**完了条件**

- 自動テスト: dependency-free / 完了 / 未完了 / parent / cycle / capacity待ちを検証する。
- 証跡: claim input から readiness 判定に使用した dependency evidence を辿れる。

## 4.6.2. Scoped Concurrency

**ユーザーストーリー**

- 誰が: 複数の独立 Task を処理するリポジトリ管理者
- 何を: 独立した Issue を隔離された workspace で並行実行したい
- なぜ: 同じ対象への競合を防ぎながら、repository 全体の処理を不要に直列化しないため

**事前条件**

- 各 Task の readiness が確定している。
- Run、Operation、workspace に scoped identity がある。

**受け入れ基準**

- **正常系: 独立 Issue の並行実行**
  - Given dependency のない複数の ready Issue がある。
  - When configured capacity に空きがある。
  - Then それぞれが異なる Run（branch / PR）、workspace を持ち、同時に実行できる。

- **排他: 同一 Issue の Run**
  - Given 同じ IssueRef に複数の claim request がある。
  - When active Run を確定する。
  - Then writer-capable Run は最大一つになり、他 request は同じ結果へ収束する。

- **排他: 同一 Run の Operation**
  - Given 同じ Run に複数の state-advancing Operation が同時に eligible になる。
  - When dispatch を行う。
  - Then 同時に一つだけが実行され、obsolete Result は current state を上書きしない。

- **隔離: Worktree と Review**
  - Given 複数 Run の implementation と review が並行している。
  - When 各 worker が workspace を準備する。
  - Then Issue Worker は Run 専用 mutable worktree、Review Worker は exact head の独立 read-only checkout を使う。

- **非直列化: Repository Global Lock の禁止**
  - Given 異なる Issue と worktree を対象にした二つの Operation がある。
  - When scheduling する。
  - Then repository 単位の global lock を理由に一方を待たせない。

**非機能要件**

- 隔離性: worktree、provider session、private state、write credential を Run / role 間で共有しない。
- 整合性: 破壊を防ぐ排他の根拠は GitHub の CAS（ref create、compare-and-push、SHA 指定 merge）に置く。
- 可観測性: capacity、Run / Operation identity を追跡できる。

**完了条件**

- 並行テスト: 異なる Issue が同時進行し、同じ Issue / Run は排他されることを検証する。
- 境界テスト: Review Worker が implementation workspace を参照できないことを確認する。

## 4.6.3. Idempotency と外部干渉

**ユーザーストーリー**

- 誰が: Kudo と同じ GitHub repository を操作する開発者
- 何を: retry や重複 event が副作用を増やさず、人間の変更も自動的に上書きされないようにしたい
- なぜ: 自動化と人間の作業が同じ branch / Pull Request 上で安全に共存できるから

**事前条件**

- reconciliation、Operation、GitHub mutation、記録に stable identity（marker）がある。
- mutation input に expected live state と desired state が含まれている。

**受け入れ基準**

- **冪等性: Duplicate Event**
  - Given 同じ GitHub delivery または同じ IssueRef の webhook / polling が重複する。
  - When ingress と reconciliation を処理する。
  - Then 一つの logical trigger / Run result に収束する。

- **冪等性: Duplicate Dispatch**
  - Given 同じ Operation identity が timeout または再観測で複数回 dispatch される。
  - When Worker が Result を確定する。
  - Then 同じ input に対する記録は marker により一つへ収束し、副作用を重複させない。

- **冪等性: GitHub Mutation の再試行**
  - Given branch push、Pull Request ensure、body update、または status projection の response が失われる。
  - When 同じ mutation を再試行する。
  - Then live desired state を確認し、新しい branch、Pull Request、comment を重複作成しない。

- **競合: 同一 Identity への異なる Input**
  - Given 同じ idempotency identity に異なる semantic input digest が渡される。
  - When duplicate 判定を行う。
  - Then 既存成功として扱わず、identity conflict として停止する。

- **外部干渉: Head または Base の変更**
  - Given expected observation 後に人間または別 automation が branch head / PR base を変更する。
  - When mutation 直前に live state を再取得する。
  - Then blind overwrite せず stale とし、以前の approval を変更後の input に適用しない。

- **外部干渉: Pull Request の Close / Merge**
  - Given Kudo の merge intent に紐付かない close / merge が外部から行われる。
  - When 次の mutation または review freshness check を行う。
  - Then 品質 verdict にせず、人間が判断できる evidence とともに `needs_human` へ送る。

- **冪等性: 自分の Merge の再観測**
  - Given `merge_pull_request` の idempotency identity が durable に記録され、merge commit の親が期待 head である。
  - When 応答を失った retry が live state を再取得する。
  - Then 外部干渉として扱わず、同じ merge を成功として確認し、二重 merge を作らない。

**非機能要件**

- 冪等性: stable identity と input digest の組み合わせを durable に保持する。
- 安全性: compare-and-mutate により外部変更を上書きしない。
- 可観測性: duplicate、already applied、conflict、stale を別の結果として追跡できる。

**完了条件**

- 自動テスト: duplicate event / dispatch / mutation / identity conflict / external push / closeを検証する。
- 障害テスト: mutation 前後の timeout から一つの GitHub state へ収束する。

## 参照する正本

- [End-to-end workflow](../../05_design/02_workflow.md) — Idempotency and recovery
- [Architecture](../../05_design/01_architecture.md) — Scheduling and concurrency
- [GitHub routing policy](../../05_design/04_github-routing.md)
- [Worker Operation Protocol](../../05_design/contracts/operation-protocol-v1alpha1.md)
