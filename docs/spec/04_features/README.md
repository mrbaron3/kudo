# 04. 機能仕様と詳細設計

[03_system-spec/](../03_system-spec/) の機能要件を、機能単位の **Spec** と **Design** へ展開する。
各機能は一つのディレクトリを持ち、利用者や外部境界から観測できる保証と、その保証を実現する
内部設計を分離する。

protocol field、canonical encoding、review policy など、既に versioned な正本がある事項は再定義しない。
各文書は担当する要求と設計判断を説明し、厳密な定義へリンクする。

## 機能構成

| ID | 機能 | 対応する中央仕様 | 機能仕様 | 詳細設計 |
| --- | --- | --- | --- | --- |
| 4.1 | Issue 受付・Contract 検証・Claim | F-01 / F-02 | [Spec](01_issue-intake-and-claim/01_spec.md) | [Design](01_issue-intake-and-claim/02_design.md) |
| 4.2 | Test Authoring・RED・Test Validity Review | F-03 / F-04 | [Spec](02_test-first-review/01_spec.md) | [Design](02_test-first-review/02_design.md) |
| 4.3 | Implementation・GREEN・Final Review | F-05 / F-06 | [Spec](03_implementation-review/01_spec.md) | [Design](03_implementation-review/02_design.md) |
| 4.4 | Pull Request 確定・Merge | F-07 | [Spec](04_pull-request-handoff/01_spec.md) | [Design](04_pull-request-handoff/02_design.md) |
| 4.5 | Retry・Recovery・Human Escalation | F-08 | [Spec](05_recovery-and-escalation/01_spec.md) | [Design](05_recovery-and-escalation/02_design.md) |
| 4.6 | Dependency・並行実行・冪等性 | F-09 | [Spec](06_concurrency-and-idempotency/01_spec.md) | [Design](06_concurrency-and-idempotency/02_design.md) |

## ディレクトリ規約

```text
NN_feature-name/
├── 01_spec.md
└── 02_design.md
```

- 機能をディレクトリの単位とする。
- `01_spec.md` は Why / What を扱い、サブ機能ごとのユーザーストーリーと Acceptance Criteria を記述する。
- `02_design.md` は component、Operation、state、artifact、外部境界、failure、検証方法を記述する。
- サブ機能は両文書内で同じ ID の見出しとして対応付け、見出しだけを理由にファイルへ分割しない。
- 図、fixture、schema など複数の補助資料が実際に必要になった場合だけ、機能ディレクトリへ追加する。
- 実装状況と作業順序は [Implementation plan](../06_project/01_implementation-plan.md) で管理し、完成形の仕様へ混在させない。

## Spec の構成

`01_spec.md` は冒頭で対応する system requirement と Design への分担を示し、サブ機能一覧の後に
各サブ機能の Acceptance Criteria を同じ構造で記述する。

```markdown
# 4.x. 機能名 受け入れ要件

## サブ機能一覧

| ID | サブ機能 | 優先度 |
| --- | --- | --- |
| 4.x.1 | サブ機能名 | 高 |

## 4.x.1. サブ機能名

**ユーザーストーリー**

- 誰が: <actor>
- 何を: <達成したいこと>
- なぜ: <得られる価値>

**事前条件**

- <外部から識別できる前提>

**受け入れ基準**

- **正常系: シナリオ名**
  - Given <観測可能な前提>
  - When <操作またはevent>
  - Then <観測可能な結果>

**非機能要件**

- <性能、セキュリティ、回復性、可観測性など、このサブ機能に必要な品質>

**完了条件**

- 自動テスト: <受け入れ基準を証明するテスト>
- 証跡またはデモ: <人間が完了を確認する方法>
```

- 一つのシナリオは一つの観測可能な behavior を扱う。
- scenario 名に正常系、異常系、回復系、冪等性、権限、鮮度などの意図を示す。
- Given には実装内部の都合ではなく、外部入力または確定済み artifact を置く。
- Then は「正しく処理される」ではなく、state、status、artifact、mutation の結果を具体化する。
- 非機能要件は全機能共通の標語を繰り返さず、当該サブ機能で確認可能な品質だけを書く。
- 完了条件は受け入れ基準を証明する automated test と、必要な evidence / demo を示す。

## Spec と Design の境界

**Spec に書くもの:**

- actor が実行する操作と外部から確認できる結果
- normal flow、主要な failure、retry、stale、escalation の分岐
- Acceptance Criteria と保存または提示する evidence の対応
- authority、security、idempotency に関する振る舞い上の不変条件

**Design に書くもの:**

- Controller、Issue Worker、Review Worker、adapter の責務分担
- Operation と state transition、artifact の流れ
- external mutation 前後の照合、retry、lease、recovery の方法
- deterministic test で確認する境界

**いずれにも複製しないもの:**

- versioned contract の field と canonical encoding
- review policy の判定基準
- PostgreSQL の確定 table / index / SQL
- provider 固有 prompt や private session state
- Compose service、volume、secret の共通 runtime contract

これらは [contracts/](../05_design/contracts/)、[review-policies/](../05_design/review-policies/)、
[Architecture](../05_design/01_architecture.md)、[Runtime platform](../05_design/03_runtime-platform.md) の該当する正本を参照する。

## 更新ルール

1. 変更対象が system-wide requirement か、特定機能の振る舞いか、機能固有の実現方法かを判定する。
2. 観測可能な保証を変える場合は `01_spec.md` を先に更新する。
3. 実現方法を変える場合は同じ機能の `02_design.md` と、必要な共通設計・ADRを更新する。
4. protocol を変える場合は `docs/spec/05_design/contracts/`、parser、fixture、test を同じ変更で更新する。
5. 一時的な進捗や未実装項目は仕様へ書かず、Implementation plan または Task Issue で管理する。
