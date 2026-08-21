# ADR-0004: `docs/spec/` を唯一の文書ルートにする

- Status: Accepted
- Date: 2026-08-19
- 追記（2026-08-21）: ADR だけは `docs/spec/05_design/decisions/` から `docs/adr/` へ移動した。本文末尾の「2026-08-21 追記」を参照。

## Context

Kudo には product、workflow、architecture、runtime、protocol、review policy、ADR、implementation plan が
`docs/` 直下と複数の subdirectory に分散していた。新しい `docs/spec/` は中央仕様を提供していたが、詳細を
旧文書へ委譲していたため、二つの文書体系が並存し、入口と正本の所在が一意ではなかった。

特に contract、policy、architecture の repository path は Context Manifest、Operation、Review Request の
semantic input に含まれる。単なる file move でも path identity と digest が変わるため、alias を残して
暗黙に同一視することはできない。

## Decision

- repository-facing documentation の root を `docs/spec/` 一つに限定する。
- product と system requirement は `01`〜`03`、機能固有の Spec / Design は `04_features` に置く。
- 共通 architecture、workflow、runtime、GitHub routing、versioned contract、review policy、ADR は
  `05_design` に置く。
- implementation status、delivery order、development guide、migration record、deferred work は
  `06_project` に置き、完成形の仕様と分離する。
- 旧 path の redirect file、duplicate copy、symlink は残さない。入口は `docs/spec/README.md` に集約する。
- contract / policy ref は新しい canonical path へ更新し、parser、code comment、fixture、golden test を
  同じ変更で更新する。

## Protocol migration

次の path は semantic identity の一部であり、新 path へ置き換える。

| 旧 path | 新 path |
| --- | --- |
| `docs/contracts/*` | `docs/spec/05_design/contracts/*` |
| `docs/review-policies/*` | `docs/spec/05_design/review-policies/*` |
| `docs/architecture.md` | `docs/spec/05_design/01_architecture.md` |
| `docs/workflow.md` | `docs/spec/05_design/02_workflow.md` |
| `docs/runtime-platform.md` | `docs/spec/05_design/03_runtime-platform.md` |
| `docs/github-routing.md` | `docs/spec/05_design/04_github-routing.md` |

この変更前の Context Manifest、Operation、Review Request、approval を新 path の input に再利用しない。
進行中 Run が存在する環境では旧 Run を supersede し、Task Context と review lineage を新 path から
再構築する。Kudo は greenfield かつ protocol が `v1alpha1` の段階であるため、旧 path の互換 alias は設けない。

## Consequences

- 文書の探索と正本判断は `docs/spec/README.md` から一意に行える。
- `docs/` 直下に `spec/` 以外の文書を追加しない運用が必要になる。
- canonical fixture の body、Task Context、Manifest、Operation、Review Request digest は新 path に応じて変わる。
- 外部に保存された Task Issue template や authority ref が旧 path を指す場合は、新 path への明示更新が必要になる。
- 文書移動を cosmetic change として扱えないため、将来の path 変更にも contract migration 判断が必要になる。

## 2026-08-21 追記: ADR を `docs/adr/` へ移動する

ADR は特定機能の仕様ではなく repository 全体の決定記録であるため、`docs/spec/` 体系の外に置く。

- `docs/spec/05_design/decisions/*` は `docs/adr/*` へ移動する。file 名と番号は維持する。
- repository-facing documentation の root は `docs/spec/`（仕様）と `docs/adr/`（ADR）の二つとし、それ以外を追加しない。入口は引き続き `docs/spec/README.md` に集約する。
- ADR path が `authorityRefs` / `policyRefs` に現れる場合、本 ADR の Protocol migration と同じ規律を適用する。旧 path を指す進行中入力は再利用せず、reference を新 path へ明示更新する。
- 旧 path の redirect / alias は置かない。

| 旧 path | 新 path |
| --- | --- |
| `docs/spec/05_design/decisions/*` | `docs/adr/*` |
