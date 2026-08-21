# Kudo 仕様書

Kudo のプロダクト仕様、機能別 Acceptance Criteria、詳細設計、versioned contract、実装計画を格納する、
リポジトリ内で唯一の文書体系である。文書への入口は本ファイルに集約し、ADR を置く [docs/adr/](../adr/)
（[ADR-0004](../adr/0004-single-documentation-root.md) 2026-08-21 追記）を唯一の例外として、`docs/spec/` の
外に別の正本や補足文書を作らない。

完成形の **Why / What / How** と、現在地を示す project 文書を同じ体系内で分離する。
同じ field、state、権限、判断理由を複数文書で再定義せず、概要文書から一つの正本へリンクする。

## 読む順番

1. [01_product-design/](01_product-design/)
   — 解決する課題、利用者、提供価値、製品境界、完成条件
2. [02_reliability-strategy/](02_reliability-strategy/)
   — TDD、独立 review、immutable evidence、復旧可能性を組み合わせる理由
3. [03_system-spec/](03_system-spec/)
   — アクター、機能要件、構成、workflow、非機能要件をまとめた中央仕様
4. [04_features/](04_features/)
   — 機能単位の Acceptance Criteria と機能固有の詳細設計
5. [05_design/](05_design/)
   — architecture、workflow、runtime、protocol、review policy の正本（ADR は [docs/adr/](../adr/)）
6. [06_project/](06_project/)
   — 実装状況、delivery order、開発手順、移行記録、保留事項

## ファイル構成

```text
docs/
├── adr/
│   └── NNNN-<decision-name>.md
└── spec/
    ├── README.md
    ├── 01_product-design/
    │   └── README.md
    ├── 02_reliability-strategy/
    │   └── README.md
    ├── 03_system-spec/
    │   └── README.md
    ├── 04_features/
    │   ├── README.md
    │   └── NN_feature-name/
    │       ├── 01_spec.md
    │       └── 02_design.md
    ├── 05_design/
    │   ├── README.md
    │   ├── 01_architecture.md
    │   ├── 02_workflow.md
    │   ├── 03_runtime-platform.md
    │   ├── 04_github-routing.md
    │   ├── contracts/
    │   └── review-policies/
    └── 06_project/
        ├── README.md
        ├── 01_implementation-plan.md
        ├── 02_development.md
        ├── 03_migration-from-servo.md
        └── 04_evaluation-harness.md
```

トップレベルの章と機能を番号付きディレクトリにすることで、directory-first の表示でも読む順番を保つ。
機能ディレクトリでは `01_spec.md` と `02_design.md` を固定の入口とし、サブ機能は文書内の見出しで表す。
見出し番号や将来の可能性だけを理由にファイルを分割しない。

## 正本の分担

| 関心事 | 正本 |
| --- | --- |
| プロダクトの目的、対象範囲、完成条件 | [01. プロダクト設計](01_product-design/) |
| system-wide な機能・非機能要件 | [03. システム仕様](03_system-spec/) |
| 機能ごとの振る舞いと実現方法 | [04. 機能仕様と詳細設計](04_features/) |
| Issue から merge 完了までの規範的な順序 | [End-to-end workflow](05_design/02_workflow.md) |
| Controller / Worker の責務と権限 | [Architecture](05_design/01_architecture.md) |
| Compose、PostgreSQL、volume、secret、復旧運用 | [Runtime platform](05_design/03_runtime-platform.md) |
| GitHub candidate、webhook / polling、label | [GitHub routing policy](05_design/04_github-routing.md) |
| machine-readable protocol | [contracts/](05_design/contracts/) |
| Review Worker の品質判断基準 | [review-policies/](05_design/review-policies/) |
| 技術選択と変更理由 | [docs/adr/](../adr/) |
| 実装状況、実装順序、milestone | [Implementation plan](06_project/01_implementation-plan.md) |
| 開発・検証手順 | [Development environment](06_project/02_development.md) |

## SSOT 原則

- `docs/spec/` と ADR 専用の `docs/adr/` だけを repository-facing documentation の root とする。
- 同じ要求は一つの文書だけが定義し、他文書は要約せず正本へリンクする。
- `05_design/contracts/` の意味または path を変える場合は、文書、parser、fixture、test を同じ変更で更新する。
- review policy の意味を変える場合は新しい versioned path を追加し、進行中 Request の基準を上書きしない。
- accepted ADR を置き換える場合は既存 ADR を黙って書き換えず、新しい ADR で supersede する。
- 完成形と現在地を混在させず、進捗と残作業は [06_project/](06_project/) で管理する。

## 更新順序

1. 変更対象が product、system-wide requirement、特定機能、共通設計、project state のどれかを判定する。
2. 観測可能な保証を変える場合は `04_features/<feature>/01_spec.md` を先に更新する。
3. 実現方法を変える場合は同じ機能の `02_design.md` または `05_design/` の正本を更新する。
4. protocol を変える場合は対応する contract、parser、fixture、test を同じ変更で更新する。
5. 実装時期と作業分解は `06_project/01_implementation-plan.md` または Task Issue で管理する。
