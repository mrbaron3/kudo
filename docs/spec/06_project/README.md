# 06. プロジェクト文書

完成形の仕様とは分離して、現在の実装状況、delivery order、開発手順、移行判断、保留事項を管理する。
製品 behavior や protocol の正本は本ディレクトリに置かず、[01〜05](../) の該当文書へリンクする。

## 文書一覧

| 文書 | 役割 |
| --- | --- |
| [Implementation plan](01_implementation-plan.md) | 現在地、milestone、実装順序、exit criteria |
| [Development environment](02_development.md) | Compose を使った開発、test、image build、debug 手順 |
| [Servo からの移行判断](03_migration-from-servo.md) | 継承した概念、Kudo 固有の判断、移行しない項目 |
| [Evaluation harness — deferred](04_evaluation-harness.md) | 評価基盤を保留する理由と、将来決めるべき論点 |

## 分離ルール

- target behavior、protocol、architecture は `01`〜`05` を更新する。
- 実装済み / 未実装、優先順位、milestone は `01_implementation-plan.md` を更新する。
- 一時的な開発手順は `02_development.md` に置き、deployment contract を再定義しない。
- 保留事項を実装へ追加する場合は、先に feature spec、design、必要な ADR を確定する。
