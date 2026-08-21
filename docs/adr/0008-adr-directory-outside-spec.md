# ADR-0008: ADRは`docs/spec/`体系の外の`docs/adr/`に置く

- Status: accepted（2026-08-21）
- Supersede対象: [ADR-0004](0004-single-documentation-root.md)の「repository-facing documentationのrootを`docs/spec/`一つに限定する」部分

## Context

ADR-0004は文書ルートを`docs/spec/`へ一元化し、ADRも`docs/spec/05_design/decisions/`に置いていた。しかしADRは特定機能の仕様ではなくrepository全体の決定記録であり、「現在形の仕様」を構成する`05_design`の下に置くと、仕様（living document）と決定ログ（immutable log）という性質の異なる2種類の文書が同じ階層に混在する。

## Decision

- `docs/spec/05_design/decisions/*`を`docs/adr/*`へ移動する。file名と番号は維持する。
- repository-facing documentationのrootは`docs/spec/`（仕様）と`docs/adr/`（ADR）の二つとし、それ以外を追加しない。入口は引き続き`docs/spec/README.md`に集約する。
- ADR pathが`authorityRefs` / `policyRefs`に現れる場合、[ADR-0004](0004-single-documentation-root.md)のProtocol migrationと同じ規律を適用する。旧pathを指す進行中入力は再利用せず、referenceを新pathへ明示更新する。
- 旧pathのredirect / aliasは置かない。

| 旧 path | 新 path |
| --- | --- |
| `docs/spec/05_design/decisions/*` | `docs/adr/*` |

## Alternatives

- **`docs/spec/05_design/decisions/`に留める**: ADRは機能仕様ではなく決定記録であり、仕様体系の中に置くと「現在形の正本」と「不変の決定ログ」の区別が階層上で見えない。却下した。

## Consequences

- rootが一つから二つになり、ADR-0004の「唯一のroot」という単純さは失われる。代わりに、仕様とADRの性質の違い（更新規律・不変性）がディレクトリ境界として現れる。
- 移動時点でADR pathを参照するfixture・進行中入力は存在せず、protocol migrationの実作業は発生しなかった。

## Revisit conditions

- ADR以外にも「仕様体系の外に置くべき文書種別」が現れた場合、rootを増やすのではなく、この二分（仕様 / 決定ログ）の枠組み自体を新しいADRで再検討する。
