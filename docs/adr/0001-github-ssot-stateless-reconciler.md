# ADR-0001: GitHubを唯一の正本とするstateless reconcilerとし、独自の永続層を持たない

- Status: accepted（2026-08-22）
- Supersede対象: なし（本ADRからADR運用を仕切り直す。旧ADR群の削除はContextを参照）

## Context

2026-08-22時点のKudoはalphaであり、外部consumerを持たない。決定前の設計は「PostgreSQLを
workflow stateとqueueの正本、content-addressed Artifact Storeをevidence bytesの正本とし、
Docker Composeで役割別containerを分離する」自己完結型のworkflow engineだった。run store
（`runs` / `run_transitions` / `run_review_bindings` / `artifact_refs`、PR #57）は実装済み、
Artifact Storeは計画段階だった。

見直しの入口は「Review Workerのレビュー結果をDBへ保存する設計の再検討」である。議論を通じて
次の要求と前提が確定した。

- 複数プロジェクトで大量のPRとレビューが並走する想定であり、ローカルストレージを圧迫し得る
  設計を取らない。
- 将来の評価ハーネスのため、どのcommitにどの指摘があったかの対応が恒久的に追えること。
- 開発でGitHubは必ず使うため、GitHubへの依存は問題ないという製品判断。
- rate limitは現段階では設計制約から除外する（2026-08-22の製品判断。概算も行っていない）。
- 同一repositoryへ複数のKudo instanceを並行配置する要求はない（本決定の前提）。

議論で得た中心的な観察は次の2つである。

1. **DBを持ってもreconciliationは消えない。** 人間のpush・PR close・label操作という外部
   mutationがある以上、GitHubを観測してローカル状態と突き合わせる論理は必須であり、旧設計の
   複雑さの本体（digest binding、staleness規則、Artifact Manifest、GC設計）は「2つの正本の
   同期」から派生していた。正本を1つにすれば同期問題ごと消える。
2. **GitHubはKudoが必要とするprimitiveを既に提供している。** gitはcontent-addressed store
   そのものであり、check runはcommit SHAへ構造的に束縛され作成したGitHub App以外は書き換え
   られず、ref作成・compare-and-push・SHA指定mergeはgateに必要なcompare-and-swapを成す。

なお本決定と同時に、旧ADR 0001〜0008（0007はa7fedc2で廃止済み）を削除し、番号を0001から
仕切り直した。削除は決定記録の破棄であり、旧ADRから仕様側へ移管済みの規範（auto-merge、
live context再構築、単一documentation root等）の効力は変えない。旧内容はgit履歴
（3707799以前の`docs/adr/`）から参照できる。

## Decision

Kudoはworkflow stateとevidenceの正本となる独自の永続層（PostgreSQL run store、
content-addressed Artifact Store）を持たず、GitHubを唯一の正本とするstateless reconcilerとして
動作する。本決定が禁じるのは「workflow状態・レビュー結果・evidenceの正本を自前で持つこと」で
あり、正本性を持たない用途（タスク種別ごとのmodel割り当てなどの設定）でdatabaseを将来利用する
ことは妨げない。状態はGitHubの観測物（label、branch、PR、
check run、comment）から導出し、正本性が必要なmutationはGitHubのCAS（ref作成、
compare-and-push、SHA指定merge）でfenceする。

配置の核は次のとおり。状態写像・schema・手順の正本は仕様側（Consequencesの一覧）に置く。

- レビューverdictはcheck run（App所有で第三者が改竄できず、SHAへ構造的に束縛される）
- finding本文はPRコメント。round計数はmarker付きコメントの計数として導出し、独立した
  counter状態を持たない
- snapshot・evidenceはgit commit
- Issue claimはbranch ref作成のatomicity

## Alternatives

- **現行設計の維持＋PRコメントへのwrite-through投影（terminal後にローカルをGC）**:
  Run進行中はDB/Artifact Storeが正本、terminal後はPRコメントが正本という時期分割。正本が
  2つ存在する期間の同期機構（binding検証、staleness規則、GC設計）がそのまま残る。
  「GitHubへ依存してよい」という製品前提の下では、同期コストに見合う自己完結性の便益が
  ないため却下。GitHub依存を拒む前提に戻るなら再評価の余地がある。
- **PRコメントを常時SSOTとし、DBはgate照合のみ保持する折衷**: コメントはrepository write
  権限者が編集・削除できるため、無人auto-mergeのgate入力として偽造可能。この欠陥は
  App所有のcheck runで解けるため、コメントをverdictの正本にする必然がなく却下。
- **round counterを専用labelで管理**: コメント数から導出可能な状態の二次コピーとなり、
  コメントとlabelの同期義務・remove+addの非atomic性・全プロジェクトへのlabel定義増殖を
  持ち込むため却下。一目で見たい要求が出た場合も、正本は導出のままlabelを投影として
  追加する。
- **round counterのみDBで管理**: counter 1つのためにDB・migration・接続管理という運用面を
  維持するのは釣り合わず却下。

## Consequences

受け入れた代償・リスク:

- GitHubの可用性がそのままKudoの可用性になる。障害中はworkflowが進行できない。
- 状態読み出しがAPI呼び出しになり、rate limit予算と結合する。本決定はこれを意図的に
  無視しており、概算すら行っていない（Revisit conditions参照）。
- check runの利用にはGitHub App認証が必須になる（PATでは不可）。
- check run outputは64KiB上限であり、超過するevidenceの置き場規則が仕様側で必要になる。
- 状態機械は消えず、観測の組からphaseを導出する関数へ移動する。導出関数はあらゆる中間状態
  （branchあり・PRなし等）に答えを持たなければならない。
- コメント削除等の人為的操作でround計数が狂い得る。round上限はsecurity境界ではなく無人
  loop防止のソフト上限であり、狂いはescalationが遅れる方向に倒れることを受け入れる。
- 旧ADRの削除により、存続する決定（auto-merge等）の「なぜ」の記録が失われる。仕様側の
  規範と git 履歴で代替する。

更新される文書（実体は仕様側の変更で行う）:

- `CLAUDE.md`（canonical runtime・PostgreSQL・Compose記述）
- `docs/spec/05_design/01_architecture.md`、`03_runtime-platform.md`、`02_workflow.md`、
  `04_github-routing.md`
- `docs/spec/05_design/contracts/review-protocol-v1alpha1.md`、
  `operation-protocol-v1alpha1.md`（task-context-v1alpha1.mdは影響確認）
- `docs/spec/04_features/01〜06`の各spec/design
- `docs/spec/06_project/01_implementation-plan.md`、`03_migration-from-servo.md`、
  `04_evaluation-harness.md`
- `docs/spec/02_reliability-strategy/README.md`、`03_system-spec/README.md`

退役するコード: `internal/adapter/postgres/`のrun store schemaとその利用箇所（PR #57の
成果）。alphaで外部consumerがないため削除可能。PostgreSQL adapter自体の存廃は将来の設定
用途（2026-08-22時点で要件未確定）に委ね、本ADRでは決めない。既存Issueの改廃は本ADRの
範囲外とし、別途整理する。

## Revisit conditions

- 同一repositoryへ複数Kudo instanceを並行配置する要求が生じた
- rate limitの実測が運用を阻害した（読み出し償却・webhook化で解けない水準に達した）
- GitHubに表現できないauthoritative state（コスト予算の集計、cross-repoスケジューリング等）
  が要件になった。なお正本性を持たない設定のdatabase保存は本決定と矛盾せず、再検討を要しない
- check runの不変性・ref CASなど、依拠したGitHub側primitiveの仕様が変わった
- 観測からの状態導出のlatencyがreconcile要件を実測で満たさなくなった

## References

- 決定の経路: 2026-08-22のClaude Codeセッションにおける設計議論。本ADRはその記録である
- 対応するIssue/PRは本ADR作成時点で提示されていない
- 退役対象の実装: PR #57（PostgreSQL run store、Issue #13）
- 旧ADR群: git履歴 3707799 以前の `docs/adr/`
