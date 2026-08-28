---
name: reviewer-fixture
description: >-
  Review Worker 開発用の fixture Pull Request を開発専用 repository へ合成する。
  claim checkpoint 付き draft PR、test-only head、kudo/evidence-red check run、
  test plan comment を、正常形と 3 種の negative case で用意する。明示的呼び出し専用:
  Claude Code では /reviewer-fixture [case]、Codex では $reviewer-fixture メンション。
argument-hint: "[valid|digest-mismatch|missing-required-input|missing-marker]"
disable-model-invocation: true
---

# Reviewer fixture: Review Worker 開発用の入力を用意する

Review Worker（#25）は、Implementer が作った claim 形 Pull Request を入力に取る。Implementer
本体（#17・#24・#29）の完成を待たずに Reviewer を開発できるよう、同じ形の Pull Request を
開発専用 repository へ合成するのがこのスキルの役割である。

正本の手順は `docs/spec/06_project/02_development.md` の「Reviewer fixture PR seeder」section に
ある。このスキルはそこへ入る導線と、実行時に必要な
判断（どの case か、いつ作り直すか、何を確認するか）を束ねる。

## 絶対にしないこと: record surface を自分で書く

**marker、machine block、check run output を、このセッションが文章として組み立ててはならない。**
必ず `cmd/kudo-reviewer-fixture` を実行して生成させる。

理由は marker の `digest` が payload の SHA-256 であり、その payload は同じ record の中にある
ためである。自分の出力のハッシュを自分の出力へ書くことはできない。手で書けば必ず digest 不一致
になり、それは `digest-mismatch` case がわざと再現している欠陥そのものなので、**Reviewer が
正しく弾いたのか fixture が壊れているのかを区別できなくなる**。`marker.head` も commit を push
するまで存在しないので、同じ理由で後決めになる。

同様に `kudo/evidence-red` は check run であって文章ではない。Checks API と Implementer 相当の
App identity が要る。

## このスキルの範囲

- 実行するのは fixture PR の合成と、その結果の確認までである。
- **Review Worker 本体の実装・実行はスコープ外**。
- **fixture の内容の品質評価もスコープ外**。seeder は形の合成だけを行う（#71 Scope）。
- production image の `kudo` binary には seeder を含めない。`cmd/kudo-reviewer-fixture` だけに置く。

## 前提

実 GitHub を変更する opt-in 操作である。次が揃っていないなら、推測で補わず停止して不足を報告する。

| 前提 | 理由 |
| --- | --- |
| fixture 専用の repository | 使い捨て前提の branch 操作を行う。実プロジェクトの repository を指定しない |
| fixture 専用の Task Issue | `kudo/issue-<number>` branch を占有する |
| 開発専用 credential | 対象 repository の Contents / Pull requests / Issues / Checks への write |
| Implementer 相当の bot user ID と GitHub App ID | Reviewer は creator identity を照合するので、誰が書いたことにするかを明示する必要がある |

credential は `.env` へ保存せず、実行 process にだけ渡す。token・秘密鍵・credential file path を
出力しない。

## case の選び方

`--case` は正常形と、1 点だけ欠陥を入れた 3 つを取る。negative case は指定した欠陥以外を正常形の
まま保つので、Reviewer が「狙った理由で」拒否したかを判定できる。

| case | 何が欠けるか | 何を確かめるための入力か |
| --- | --- | --- |
| `valid` | なし | Reviewer が正常な入力を受理し、verdict を返せるか |
| `digest-mismatch` | RED evidence の digest だけが payload と一致しない | digest 照合が効いているか |
| `missing-required-input` | RED evidence check run が存在しない | 必須 input の欠落を検出するか |
| `missing-marker` | test plan comment の marker 行だけが無い | marker の無い record を採用しないか |

指定が無ければ `valid` を使う。

## 手順

```sh
export KUDO_FIXTURE_GITHUB_TOKEN='<development credential>'

go run ./cmd/kudo-reviewer-fixture \
  --repository <owner>/<fixture-repository> \
  --issue <number> \
  --comment-author-id <implementer bot user ID> \
  --check-run-app-id <implementer GitHub App ID> \
  --case <case>
```

成功すると `{"fixture":…,"pullRequest":…,"branch":…,"headSha":…}` を stdout へ出す。この PR 番号
と head SHA が Reviewer 開発の入力になるので、報告に含める。

## 再実行と作り直し

再実行は commit message、bootstrap lineage、Pull Request、marker、Implementer の comment author ID
/ check run App ID を照合して既存を再利用し、重複 PR・重複 check run・重複 comment を作らない。

**ただし既存 head の tree や blob が corpus の payload と一致するかまでは検証しない。** 使い捨て
repository を前提にした意図的な割り切りである。したがって:

- **corpus を編集したあとは、branch `kudo/issue-<number>` を削除してから実行する。**
  削除せず再実行すると、古い head をそのまま再利用して変更が反映されない。
- 無関係な commit が同じ branch を使っている場合は、上書きせず停止する。
- 並行実行は想定しない。ref 更新が衝突したら回復せずエラーを返すので、branch の状態を確認して
  作り直す。

## 検証

fixture を変更したときは、実 GitHub を触る前に決定的なテストで形を固定する。

```sh
go test ./internal/reviewerfixture ./internal/adapter/github
```

`TestValidFixtureRecordSurfacesGolden` が、生成した record surface を byte 単位で固定し、gateway
parser が追加変換なしに読めることを確認する（#71 AC-4）。golden が変わったら、その差分が意図した
ものかを必ず目で確認する。意図した変更なら golden を更新し、意図していないなら実装を直す。

live 境界での再実行検証は opt-in で行う。

```sh
export KUDO_FIXTURE_REPOSITORY='<owner>/<fixture-repository>'
export KUDO_FIXTURE_ISSUE_NUMBER='<number>'
export KUDO_FIXTURE_IMPLEMENTER_COMMENT_AUTHOR_ID='<id>'
export KUDO_FIXTURE_IMPLEMENTER_CHECK_RUN_APP_ID='<id>'
export KUDO_FIXTURE_GITHUB_TOKEN='<development credential>'

mise run test:reviewer-fixture-live
```

## corpus を増やす・変える

fixture の中身（test file、test plan、RED evidence の本文）は
`internal/reviewerfixture/testdata/corpus/*.json` のデータであり、自由に書き換えてよい。
手順と制約は `references/corpus.md` に従う。

新しい **欠陥の種類**を足す場合はデータだけでは足りず、`Fault` と seeder の分岐を変更する必要が
ある。その場合は先に「どの record surface の何を壊すか」を 1 点に特定してから実装する。

## ハーネス対応（Claude Code / Codex）

手順は両ハーネスで同一である。差分は呼び出しと引数だけ。

- Claude Code: `/reviewer-fixture <case>`（引数は本文へ展開される）
- Codex: `$reviewer-fixture` メンション。同じユーザーメッセージ中の case 名が引数

- 引数: $ARGUMENTS
- 上の行に `$ARGUMENTS` が文字どおり残っている場合（引数展開のないハーネス）は、呼び出し時の
  ユーザーメッセージから case 名を読み取る。

## 停止条件

続行すると誤った成果物になるか、構造的に続行できない場合だけ停止する。

- 前提（fixture 専用 repository / Issue、credential、identity）のいずれかが欠けている
- `kudo/issue-<number>` に無関係な commit が乗っている
- ref 更新が衝突した
- `mise run check` または `go test ./internal/reviewerfixture` が失敗している

いずれも入力または実行環境が壊れているという観測であり、判断で埋めるものではない。
