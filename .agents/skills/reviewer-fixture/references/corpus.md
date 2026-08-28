# fixture corpus の書き方

corpus は `internal/reviewerfixture/testdata/corpus/<case>.json` に 1 case 1 ファイルで置く。
Go binary へ `embed` されるので、追加・変更したらビルドし直す。

## 責務の境界

corpus が持つのは**中身（散文）だけ**である。それを包む marker、machine block、digest、head SHA、
Implementer identity は seeder が計算して付ける。corpus に digest を書かない。

| corpus が持つもの | seeder が付けるもの |
| --- | --- |
| test file の Go ソース | commit・branch・head SHA |
| test plan の Markdown 本文 | marker（kind / round / head / digest） |
| RED evidence の YAML 本文 | machine block（mediaType / digest / base64 payload） |
| どの欠陥を入れるか（`fault`） | check run と、その App identity |

## schema

```json
{
  "name": "<ファイル名と一致させる>",
  "fault": "none | digest-mismatch | missing-required-input | missing-marker",
  "testFile": {
    "path": "<repository 相対パス。_test.go で終わること>",
    "data": "<Go のソース>"
  },
  "testPlan": "<Markdown>",
  "redEvidence": "<YAML>"
}
```

未知 field は拒否される。`name` はファイル名と一致していなければならない。

## 制約

- `testFile.path` は相対パスで、`_test.go` で終わり、`.git/` 配下を指さないこと。合成される
  commit はこの 1 ファイルの追加だけを含む。
- `testPlan`・`redEvidence`・`testFile.data` はいずれも空にできない。
- 本文に `<!-- kudo-marker ` と `<!-- kudo-machine ` を含めない。record surface の予約 prefix で
  あり、parser が record の境界を誤認する
  （`docs/spec/05_design/contracts/operation-protocol-v1alpha1.md`）。
- test plan は machine block の base64 だけでなく、人間向け本文にも展開される。GitHub 上で
  Pull Request を開いた人間が base64 を復号せずに読めることを保つため、可読な Markdown で書く。

## negative case の作り方

**壊すのは 1 点だけにする。** それ以外は正常形のまま保つ。複数箇所が同時に壊れていると、Reviewer
が拒否したときに「どの欠陥を検出したのか」が判別できず、テストとして機能しない。

欠陥は corpus の payload を汚して表現しない。`fault` に種類を宣言し、seeder が正常形を組んだあと
から 1 点だけ差し引く。`digest-mismatch` の payload は正常なままで、seeder が digest だけを別値に
差し替える。

## 変更したあと

1. `go test ./internal/reviewerfixture ./internal/adapter/github` で golden と parser 往復を確認する。
2. golden の差分が意図したものか目で確認する。意図した変更なら golden を更新する。
3. 実 GitHub で試すときは、対象 Issue の `kudo/issue-<number>` branch を**削除してから** seeder を
   実行する。再実行は既存 head の内容を検証せず再利用するので、削除しないと変更が反映されない。
