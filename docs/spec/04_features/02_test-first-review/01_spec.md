# 4.2. Test Authoring・RED・Test Validity Review 受け入れ要件

[03. システム仕様](../../03_system-spec/) F-03 / F-04 に対する Why / What の受け入れ基準である。
Operation、artifact binding、review isolation の実現方法は [詳細設計](02_design.md) で扱う。

## サブ機能一覧

| ID | サブ機能 | 優先度 |
| --- | --- | --- |
| 4.2.1 | Test Authoring | 高 |
| 4.2.2 | RED Evidence | 高 |
| 4.2.3 | Test Validity Review | 高 |

## 4.2.1. Test Authoring

**ユーザーストーリー**

- 誰が: 実装を依頼した Task Issue 作成者
- 何を: Acceptance Criteria を検証する test を production implementation より先に作ってほしい
- なぜ: 実装が要件を満たすことを、後付けではない独立した期待 behavior で確認できるから

**事前条件**

- Claimが成功し、Task Context / Context Manifestの期待digest、Compiler version、base SHAがclaim checkpointへ固定されている。
- Issue Worker が Run 専用 worktree を使用できる。

**受け入れ基準**

- **正常系: Acceptance Criteria からの Test 作成**
  - Given 検証可能な Acceptance Criteria と authority content が固定されている。
  - When fresh test authoring session を開始する。
  - Then 各 Acceptance Criteria ID と test case の対応を示す test plan、test code、test-only checkpoint が作られる。

- **境界: Production 実装の先行防止**
  - Given test authoring phase で対象 behavior がまだ実装されていない。
  - When test code を追加する。
  - Then production behavior は変更されず、差分は test と必要最小限の test fixture に限定される。

- **隔離: Fresh Session**
  - Given 同じ Run で以前の claim または別 Operation が完了している。
  - When `author_tests` を開始する。
  - Then 新しい provider session が作られ、transcript、resume token、private memory は入力に含まれない。

- **修正系: Review Finding による Revision**
  - Given test validity review が versioned `request_changes` Result を返している。
  - When `revise_tests` を開始する。
  - Then finding と immutable artifact が新しい session に明示的に渡され、新しい test checkpoint が作られる。

- **修正系: Implementation からの差し戻し**
  - Given implement が `test_revision_required` で rollback 済み head と `test-revision-report` を返している。
  - When `revise_tests` を開始する。
  - Then report と immutable artifact が新しい session に渡され、新しい test checkpoint と RED evidence が
    作られる。

- **異常系: AC と Test の対応不足**
  - Given 一つ以上の Acceptance Criteria に対応する test case がない。
  - When test authoring Result を検証する。
  - Then 完了として受理されず、不足している Acceptance Criteria ID が特定される。

**非機能要件**

- 再現性: test plan、patch、base / head identity から test-only checkpoint を追跡できる。
- 隔離性: Operation 間で provider conversation を共有しない。
- 可観測性: Acceptance Criteria と test case の traceability を artifact として参照できる。

**完了条件**

- 自動テスト: AC traceability / production差分拒否 / fresh session / revision input を検証する。
- 証跡: test plan、RED evidence が test head へ束縛された record surface（check run / comment）から参照できる。

## 4.2.2. RED Evidence

**ユーザーストーリー**

- 誰が: Pull Request を後から確認する人間 reviewer
- 何を: 追加 test が対象 behavior の未実装を理由に失敗した証拠を確認したい
- なぜ: 常に失敗する test や環境故障を、妥当な TDD の RED と誤認しないため

**事前条件**

- Test Authoring が test-only checkpoint を作成している。
- test command と実行環境を識別できる。

**受け入れ基準**

- **正常系: 期待どおりの RED**
  - Given 対象 behavior が未実装で、追加 test がその behavior を検証している。
  - When 規定された test command を test-only head で実行する。
  - Then 対象 behavior の欠如に対応する failure が得られ、RED evidence として固定される。

- **異常系: Environment Failure**
  - Given dependency unavailable、toolchain failure、または test 実行前の環境故障がある。
  - When test command が失敗する。
  - Then valid RED として扱われず、実行 failure として分類される。

- **異常系: 無関係な既存 Failure**
  - Given 追加 test と無関係な既存 test だけが失敗する。
  - When RED causality を評価する。
  - Then test validity review へ進まず、対象 behavior の RED が不足していると判定される。

- **証跡: Immutable Binding**
  - Given test plan、test patch、command result が揃っている。
  - When RED evidence を確定する。
  - Then command、exit status、bounded stdout / stderr、environment identity、test-only head が同じ manifest に bind される。

- **公開: Test-only Head**
  - Given valid RED evidence が固定されている。
  - When `publish_head` を実行する。
  - Then exact test-only head が同じ draft Pull Request へ公開され、PR reference と observation が保存される。

**非機能要件**

- 完全性: RED evidence は対象 head と command identity を欠かさない。
- 安全性: stdout / stderr は bounded に保存し、secret を成果物へ含めない。
- 冪等性: publish の再試行で branch または Pull Request を重複作成しない。

**完了条件**

- 自動テスト: valid RED / environment failure / unrelated failure / evidence不足を検証する。
- 自動テスト: publish timeout 後の再試行が同じ draft Pull Request に収束する。

## 4.2.3. Test Validity Review

**ユーザーストーリー**

- 誰が: 実装結果を受け取る人間 reviewer
- 何を: production implementation 前に、test と RED の妥当性を独立 reviewer に確認してほしい
- なぜ: 誤った test を満たす実装が自動生成されることを防げるから

**事前条件**

- test-only head と RED evidence が draft Pull Request へ固定されている。
- supported version の Review Request と test validity policy が利用できる。

**受け入れ基準**

- **正常系: Approve**
  - Given live Issue / PR が Request と一致し、test が AC を十分に検証し、RED の因果が妥当である。
  - When Review Worker が fresh read-only session で評価する。
  - Then `approve` Result と承認対象 digest が固定され、implementation gate が開く。

- **修正系: Request Changes**
  - Given test の不足または不正確さを自動修正可能な blocking finding がある。
  - When review が完了する。
  - Then versioned `request_changes` Result が返り、fresh `revise_tests` session へ finding と artifact が渡される。

- **停止系: Needs Human**
  - Given authority conflict または自動化の判断境界を越える安全判断が必要である。
  - When review が完了する。
  - Then `needs_human` Result が固定され、implementation は開始されない。

- **鮮度: Head または Manifest の変更**
  - Given Request 作成後に head、Context Manifest、Execution Policy、input payload、policy ref のいずれかが変わる。
  - When review 前提を照合する。
  - Then 以前の approval を再利用せず、品質 verdict と区別した stale result になる。

- **隔離: Read-only Review**
  - Given implementation workspace に未公開の mutable state がある。
  - When Review Worker が review する。
  - Then exact published head から独立 checkout を作り、implementation workspace や write credential を使用しない。

- **失敗分類: Transport または Protocol Failure**
  - Given policy の取得失敗、unsupported version、invalid Result が発生する。
  - When Controller が review binding を検証する。
  - Then quality verdict に変換されず、retry または protocol failure として扱われる。

**非機能要件**

- 独立性: test author と reviewer は mutable workspace、session、private memory を共有しない。
- 鮮度: approval は exact input identity にだけ有効である。
- 可観測性: verdict、finding、policy ref、承認対象 digest を immutable Result から追跡できる。

**完了条件**

- 自動テスト: approve / request_changes / needs_human / stale / transport failure を検証する。
- 境界テスト: Review Worker が read-only checkout と read-only GitHub authority だけを使うことを確認する。

## 参照する正本

- [End-to-end workflow](../../05_design/02_workflow.md) §3〜§4
- [Worker Operation Protocol](../../05_design/contracts/operation-protocol-v1alpha1.md)
- [Implementation–Review Protocol](../../05_design/contracts/review-protocol-v1alpha1.md)
- [Test Validity Review Policy](../../05_design/review-policies/test-validity-v1alpha1.md)
