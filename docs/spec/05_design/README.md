# 05. 共通詳細設計

[04_features/](../04_features/) の各 `02_design.md` に閉じない、複数機能で共有する **How** を扱う。
機能固有の Operation、sequence、failure handling は対応する機能ディレクトリに置き、本ディレクトリへ
同じ説明を複製しない。

Kudo には既に関心事別の正本があるため、この初版ではそれらを `spec` 配下へ複製または移動しない。
本ファイルを共通設計の索引とし、新しい設計文書は共有境界が確定した時点で追加する。

## 現在の共通詳細設計

| 設計領域 | 正本 | 定義するもの |
| --- | --- | --- |
| Component / package architecture | [Architecture](../../architecture.md) | Controller / Worker、port / adapter、永続化、並行性、権限境界 |
| Workflow / state machine | [End-to-end workflow](../../workflow.md) | Issue 受付から PR handoff までの共通順序、retry、stale、escalation |
| GitHub integration | [GitHub routing policy](../../github-routing.md) | candidate、webhook / polling、label / comment projection |
| Runtime / deployment | [Runtime platform](../../runtime-platform.md) | Compose service、PostgreSQL、volume、secret、backup / recovery |
| Issue input | [Issue Contract](../../contracts/issue-contract-v1alpha1.md) | Task Issue の構造と authority semantics |
| Context compilation | [Task Context Protocol](../../contracts/task-context-v1alpha1.md) | Observation、Task Context、Manifest、Policy |
| Worker messaging | [Worker Operation Protocol](../../contracts/operation-protocol-v1alpha1.md) | Operation / Result、attempt、artifact logical name |
| Review messaging | [Implementation–Review Protocol](../../contracts/review-protocol-v1alpha1.md) | Request / Result、manifest、verdict、staleness |
| Test review policy | [Test Validity Review Policy](../../review-policies/test-validity-v1alpha1.md) | RED と test validity の評価観点 |
| Final review policy | [Final Implementation Review Policy](../../review-policies/final-implementation-v1alpha1.md) | final head の必須 / 条件付き評価観点 |
| 技術判断 | [decisions/](../../decisions/) | Compose、PR-anchored review、review round 上限の判断理由 |

## 機能設計との分担

| 内容 | 配置先 |
| --- | --- |
| 一機能の actor、振る舞い、受け入れ条件 | `04_features/<feature>/01_spec.md` |
| 一機能の Operation、sequence、failure、検証方法 | `04_features/<feature>/02_design.md` |
| 複数機能が共有する data model、artifact、adapter、runtime 規約 | `05_design/` |
| machine-readable protocol と canonical encoding | `docs/contracts/` |
| 新しい技術選択と採用理由 | `docs/decisions/` |

迷う場合は、まず機能の `02_design.md` に責務を書き、同じ定義を複数機能が必要とすることが確認できた
時点で共通設計へ抽出する。

## 今後追加し得る設計ファイル

次は共有設計が必要になった場合の候補であり、空の placeholder は作成しない。

```text
05_design/
├── README.md
├── 01_data-model.md
├── 02_artifact-and-workspace.md
├── 03_adapters.md
└── 04_runtime-and-operations.md
```

- `01_data-model.md`: Run、Operation、Attempt、lease、inbox、outbox の共通 persistence model
- `02_artifact-and-workspace.md`: content-addressed store、manifest、Run workspace の共通規約
- `03_adapters.md`: GitHub、provider、process adapter に共通する interface と failure mapping
- `04_runtime-and-operations.md`: queue、capacity、diagnostics、backup / restore、graceful shutdown

feature固有の reconciliation、review loop、handoff、resumption sequence は、対応する
`04_features/<feature>/02_design.md` に置く。

## 設計原則

- protocol、application、adapter、deployment の境界を混ぜず、変更対象の正本を一つに決める。
- Controller は transition と routing を行い、review judgment や provider session を所有しない。
- GitHub ingress / polling / API は run-once application operation の薄い adapter に保つ。
- Issue Worker だけに implementation mutation を許可し、Review Worker は read-only に保つ。
- PostgreSQL を durable workflow state と queue の正本にし、外部 observability storage で代替しない。
- artifact は content-addressed かつ immutable とし、mutable worktree を role 間で共有しない。
- interface は利用側に置き、GitHub、process、clock、filesystem、provider、telemetry を fake で検証可能にする。
- Go standard library を優先し、依存追加は明示的な boundary と理由がある場合に限定する。
- 新しい技術判断が必要なら、実装だけに埋め込まず ADR を追加する。
- 見出し番号や将来の可能性だけを理由に文書を分割しない。

## 共通設計を追加する条件

1. 二つ以上の機能が同じ定義を必要とするか、system-wide invariant を所有する。
2. 文書が所有する事項と、既存の正本へ委譲する事項を冒頭で明記できる。
3. failure、idempotency、authority、recovery、concurrency の境界を説明できる。
4. 実装と deterministic test がどの設計項目を証明するか対応付けられる。
5. implementation progress や一時的 workaround を target design として混在させない。
