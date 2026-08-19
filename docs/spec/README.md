# Kudo 仕様書

Kudo の確定したプロダクト仕様を、目的から実装設計まで順に読める形で格納する。
本ディレクトリは完成形に対する **What / Why** と、確定後の **How** を扱う。
現在どこまで実装済みか、次に何を実装するかは
[Implementation plan](../implementation-plan.md) を正とし、仕様と進捗を混在させない。

プロダクト全体の中央仕様は [03_system-spec/](03_system-spec/) である。ただし、versioned
protocol の field、厳密な状態遷移、deployment contract、採用理由は、それぞれ既存の正本へ
委譲する。中央仕様はそれらを複製せず、システム全体の要求と対応関係を示す。

## 読む順番

1. [01_product-design/](01_product-design/)
   — 解決する課題、利用者、提供価値、担当範囲
2. [02_reliability-strategy/](02_reliability-strategy/)
   — TDD、独立 review、immutable evidence、復旧可能性を組み合わせる理由
3. [03_system-spec/](03_system-spec/)
   — アクター、機能要件、構成、workflow、非機能要件をまとめた中央仕様
4. [04_features/](04_features/)
   — 機能単位の仕様と、その機能に閉じた詳細設計
5. [05_design/](05_design/)
   — data model、artifact、adapter、runtime など機能横断の共通詳細設計への入口

## ファイル構成

```text
docs/spec/
├── README.md
├── 01_product-design/
│   └── README.md
├── 02_reliability-strategy/
│   └── README.md
├── 03_system-spec/
│   └── README.md
├── 04_features/
│   ├── README.md
│   ├── 01_issue-intake-and-claim/
│   │   ├── 01_spec.md
│   │   └── 02_design.md
│   ├── 02_test-first-review/
│   │   ├── 01_spec.md
│   │   └── 02_design.md
│   ├── 03_implementation-review/
│   │   ├── 01_spec.md
│   │   └── 02_design.md
│   ├── 04_pull-request-handoff/
│   │   ├── 01_spec.md
│   │   └── 02_design.md
│   ├── 05_recovery-and-escalation/
│   │   ├── 01_spec.md
│   │   └── 02_design.md
│   └── 06_concurrency-and-idempotency/
│       ├── 01_spec.md
│       └── 02_design.md
└── 05_design/
    └── README.md
```

トップレベルの章と機能を番号付きディレクトリにすることで、directory-first の表示でも読む順番を保つ。
機能ディレクトリでは `01_spec.md` と `02_design.md` を固定の入口とし、サブ機能は文書内の見出しで表す。
見出し番号だけを理由にファイルを分割せず、独立した schema、fixture、図などの実体が必要になった時点で
補助資料を追加する。

## 正本の分担

| 関心事 | 正本 |
| --- | --- |
| プロダクトの目的、対象範囲、完成条件 | [Product vision](../vision.md) と本ディレクトリの `01` / `03` |
| Issue から PR handoff までの規範的な順序 | [End-to-end workflow](../workflow.md) |
| Controller / Worker の責務と権限 | [Architecture](../architecture.md) |
| Compose、PostgreSQL、volume、secret、復旧運用 | [Runtime platform](../runtime-platform.md) |
| GitHub candidate、webhook / polling、label | [GitHub routing policy](../github-routing.md) |
| machine-readable な外部 protocol | [contracts/](../contracts/) |
| Review Worker の品質判断基準 | [review-policies/](../review-policies/) |
| 機能固有の振る舞いと実現方法 | [04_features/](04_features/) の各 `01_spec.md` / `02_design.md` |
| 複数機能が共有する詳細設計 | [05_design/](05_design/) |
| 技術選択と変更理由 | [decisions/](../decisions/) |
| 実装状況、実装順序、milestone | [Implementation plan](../implementation-plan.md) |

## SSOT 原則

- 同じ field、state、権限、採用理由を複数文書で再定義しない。概要から該当する正本へリンクする。
- `docs/contracts/` の意味を変える場合は、文書、parser、fixture、test を同じ変更で更新する。
- accepted ADR を置き換える場合は、既存 ADR を黙って書き換えず、新しい ADR で supersede する。
- 仕様書には完成形を書く。実装済み / 未実装、優先順位、残作業は Implementation plan で管理する。
- 未確定の設計は確定仕様として推測せず、個別文書を追加する時点で判断根拠と一緒に確定する。

## 個別仕様への展開ルール

新しい機能または設計を追加するときは、次の順に整合させる。

1. `03_system-spec/` のシステム要求に含まれるか確認する。
2. `04_features/<feature>/01_spec.md` に利用者から観測できる保証を書く。
3. 同じ機能の `02_design.md` に実現方法を書き、複数機能で共有する設計だけを `05_design/` に反映する。
4. 実装時期と作業分解を `implementation-plan.md` または Task Issue で管理する。
5. 実装、決定論的 test、文書の traceability を同じ変更で確認する。
