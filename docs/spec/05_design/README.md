# 05. 共通詳細設計

[04_features/](../04_features/) の各 `02_design.md` に閉じない、複数機能で共有する **How** と、
machine-readable protocol、review policy、技術判断を格納する。機能固有の Operation、sequence、failure
handling は対応する機能ディレクトリに置き、ここへ複製しない。

## 文書構成

| 領域 | 正本 | 定義するもの |
| --- | --- | --- |
| Component / package architecture | [Architecture](01_architecture.md) | Controller / Worker、port / adapter、永続化、並行性、権限境界 |
| Workflow / state machine | [End-to-end workflow](02_workflow.md) | Issue 受付から merge 完了までの順序、retry、stale、escalation |
| Runtime / deployment | [Runtime platform](03_runtime-platform.md) | Compose service、PostgreSQL、volume、secret、backup / recovery |
| GitHub integration | [GitHub routing policy](04_github-routing.md) | candidate、webhook / polling、label / comment projection |
| Issue input | [Issue Contract](contracts/issue-contract-v1alpha1.md) | Task Issue の構造と authority semantics |
| Context compilation | [Task Context Protocol](contracts/task-context-v1alpha1.md) | Observation、Task Context、Manifest、Policy |
| Worker messaging | [Worker Operation Protocol](contracts/operation-protocol-v1alpha1.md) | Operation / Result、Attempt、artifact logical name |
| Review messaging | [Implementation–Review Protocol](contracts/review-protocol-v1alpha1.md) | Request / Result、manifest、verdict、staleness |
| Test review policy | [Test Validity Review Policy](review-policies/test-validity-v1alpha1.md) | RED と test validity の評価観点 |
| Final review policy | [Final Implementation Review Policy](review-policies/final-implementation-v1alpha1.md) | final head の必須 / 条件付き評価観点 |
| Compose 採用判断 | [ADR-0001](../../adr/0001-compose-runtime.md) | Compose を canonical runtime とする理由と再検討条件 |
| PR-anchored review | [ADR-0002](../../adr/0002-pr-anchored-review.md) | review を publish 済み draft PR へ繋留する判断 |
| Review round 上限 | [ADR-0003](../../adr/0003-review-round-limit.md) | 自動修正 loop の上限と escalation policy |
| 文書の単一ルート | [ADR-0004](../../adr/0004-single-documentation-root.md) | `docs/spec/` への正本集約と protocol path 移行 |
| 自動 merge | [ADR-0005](../../adr/0005-auto-merge.md) | 承認済み head の自動 merge、完了 terminal、merge gate |
| Live context再構築 | [ADR-0006](../../adr/0006-live-context-reconstruction.md) | Issue由来YAMLを保存せず、各Operationで再取得・再compileする判断 |
| 縦slice delivery | [ADR-0007](../../adr/0007-vertical-slice-delivery.md) | 実行順序を層でなく貫通sliceにした判断とcontract freeze |
| ADRの置き場所 | [ADR-0008](../../adr/0008-adr-directory-outside-spec.md) | ADRを`docs/spec/`体系の外の`docs/adr/`に置く判断 |

```text
05_design/
├── README.md
├── 01_architecture.md
├── 02_workflow.md
├── 03_runtime-platform.md
├── 04_github-routing.md
├── contracts/
│   ├── issue-contract-v1alpha1.md
│   ├── task-context-v1alpha1.md
│   ├── operation-protocol-v1alpha1.md
│   └── review-protocol-v1alpha1.md
└── review-policies/
    ├── test-validity-v1alpha1.md
    └── final-implementation-v1alpha1.md
```

ADR は [docs/adr/](../../adr/) に置く（[ADR-0008](../../adr/0008-adr-directory-outside-spec.md)）。

## 機能設計との分担

| 内容 | 配置先 |
| --- | --- |
| 一機能の actor、振る舞い、Acceptance Criteria | `04_features/<feature>/01_spec.md` |
| 一機能の Operation、sequence、failure、検証方法 | `04_features/<feature>/02_design.md` |
| 複数機能が共有する architecture、workflow、runtime、GitHub 規約 | `05_design/01`〜`04` |
| machine-readable protocol と canonical encoding | `05_design/contracts/` |
| Review Request が参照する versioned 品質基準 | `05_design/review-policies/` |
| 新しい技術選択と採用理由 | `docs/adr/` |

迷う場合は、まず機能の `02_design.md` に責務を書き、同じ定義を複数機能が必要とすることが確認できた
時点で共通設計へ抽出する。

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

## 更新条件

1. 文書が所有する事項と、他の正本へ委譲する事項を冒頭で明記する。
2. failure、idempotency、authority、recovery、concurrency の境界を説明する。
3. 実装と deterministic test がどの設計項目を証明するか対応付ける。
4. versioned contract / policy の意味を変える場合は新しい version path または明示的な互換性判断を伴う。
5. implementation progress や一時的 workaround を target design として混在させない。
