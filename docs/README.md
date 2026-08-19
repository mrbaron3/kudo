# Documentation map

Kudo の文書は、目標、振る舞い、技術構造、外部 protocol、実装順序を混在させない。実装判断では、次の順に該当する正本を参照する。

| 分類 | 文書 | 定義するもの |
| --- | --- | --- |
| Specification | [Kudo 仕様書](spec/README.md) | 目的から中央システム仕様、機能受け入れ要件、詳細設計へ進む横断的な入口 |
| Product | [Product vision](vision.md) | 利用者価値、対象範囲、完成条件、非目標 |
| Product | [End-to-end workflow](workflow.md) | Issue 受付から PR handoff までの状態遷移と TDD/review loop |
| Architecture | [Architecture](architecture.md) | Controller / Worker の責務、権限、永続化、並行性、Go package 境界 |
| Operations | [Runtime platform](runtime-platform.md) | Compose topology、PostgreSQL、volume、secret、recovery、運用要件 |
| Operations | [Development environment](development.md) | Compose 開発基盤の初期設定と標準 command |
| Integration | [GitHub routing policy](github-routing.md) | candidate 条件、webhook/polling、label lifecycle |
| Protocol | [Issue Contract](contracts/issue-contract-v1alpha1.md) | 人間が記述する Task Issue の機械可読 contract |
| Protocol | [Task Context Protocol](contracts/task-context-v1alpha1.md) | Issue Observation、canonical Task Context、Context Manifest、Execution Policy、Escalation Policy |
| Protocol | [Worker Operation Protocol](contracts/operation-protocol-v1alpha1.md) | Controller から Issue Worker への durable Operation / Result |
| Protocol | [Implementation–Review Protocol](contracts/review-protocol-v1alpha1.md) | immutable review request/result と verdict semantics |
| Policy | [Test Validity Review Policy](review-policies/test-validity-v1alpha1.md) | test plan、test code、RED evidenceを評価する標準観点 |
| Policy | [Final Implementation Review Policy](review-policies/final-implementation-v1alpha1.md) | final headの常時必須観点と条件付き観点 |
| Delivery | [Implementation plan](implementation-plan.md) | 現在地、実装順序、各 milestone と全体の exit criteria |
| Decision | [ADR-0001](decisions/0001-compose-runtime.md) | Docker Compose を正式基盤とする判断 |
| Decision | [ADR-0002](decisions/0002-pr-anchored-review.md) | レビューの起点を Pull Request へ移し、観点適用を session の宣言で残す判断 |
| Decision | [ADR-0003](decisions/0003-review-round-limit.md) | `request_changes` loop へ round 上限を置き、gate 予算を Escalation Policy として Run へ固定する判断 |
| History | [Servo からの移行判断](migration-from-servo.md) | 参照元から採用・非採用にした概念 |
| Deferred | [Evaluation harness](deferred/evaluation-harness.md) | runtime から意図的に分離した評価機能 |

## Authority

- `docs/contracts/` 配下は versioned protocol baseline であり、互換性に関わる変更は parsing、fixture、test と同時に行う。
- `docs/review-policies/` 配下はReview Requestの`policyRefs`から参照するversioned品質基準である。意味を変える場合は新しいversioned pathを追加し、進行中Requestの基準を上書きしない。
- Accepted ADR は技術選択の正本である。置き換える場合は既存 ADR を黙って書き換えず、新しい ADR で supersede する。
- Workflow は利用者から見える順序と gate、Architecture は内部責務、Runtime platform は deployment contract を定義する。同じ事項を複数文書へ詳述しない。
- Implementation plan の「現在地」は実装状況を表す。目標仕様を縮小する根拠にはしない。
- README と diagram は導入用の要約である。詳細と矛盾した場合は、該当する protocol、ADR、workflow specification を優先する。
- `docs/spec/` は完成形を横断的に読むための中央仕様と索引である。進捗は Implementation plan、厳密な field・遷移・権限・deployment contract は上表の関心事別の正本を優先する。

## Language and identifiers

説明文、Issue、Pull Request は日本語を基本とする。schema、state、Operation、label、configuration key のように machine-readable な識別子は英語で固定し、翻訳による別名を作らない。
