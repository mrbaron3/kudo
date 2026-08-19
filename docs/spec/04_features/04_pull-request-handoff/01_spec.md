# 4.4. Pull Request 確定・人間への Handoff 機能仕様

承認済み final head の Pull Request を、人間が通常の review を開始できる状態へ確定する機能である。
Kudo の自動実行は handoff で終了し、merge や Issue close までは行わない。

## 目的

- final approval と required checks の対象が live Pull Request head と一致することを保証する。
- 人間が変更内容、検証証跡、review 結果、残存リスクを一つの Pull Request から追跡できるようにする。
- durable workflow state を先に確定し、GitHub status をその投影として扱う。

## 4.4.1. Finalize 前検証

- final approval、required checks、live Pull Request head が同じ commit を指すことを確認する。
- Pull Request が open で、対象 Run と repository / branch / base が一致することを確認する。
- changed head、base、PR identity、artifact binding を以前の approval で通過させない。
- Pull Request の外部 close / merge は品質 verdict にせず、人間への escalation とする。

## 4.4.2. Pull Request 確定

- Issue Worker だけが required Pull Request body を確定し、draft を解除する。
- body に Task Issue、Acceptance Criteria、RED / GREEN / checks、二つの Review Result、residual risk、
  Run / base / head identity を含める。
- body は durable artifact と versioned Result から決定論的に生成する。
- expected state と live state を照合し、人間による外部変更を blind overwrite しない。

## 4.4.3. Handoff

- ready 化を durable に記録してから、Issue を `ai-review-waiting` へ投影する。
- final Pull Request と Issue comment から、人間が必要な evidence reference を辿れるようにする。
- merge、Issue close、release、human review comment への対応は自動実行しない。

## 受け入れ上の不変条件

- final approval と異なる head を ready for review にしない。
- Controller と Review Worker は Pull Request を変更しない。
- GitHub projection の一時失敗で handoff の durable state を巻き戻さない。
- draft 解除の重複 attempt は一つの最終状態へ収束する。

## 正本

- [03. システム仕様](../../03_system-spec/) F-07
- [End-to-end workflow](../../../workflow.md) §7
- [GitHub routing policy](../../../github-routing.md)
- [Architecture](../../../architecture.md) — Mutation authority
- [Implementation–Review Protocol](../../../contracts/review-protocol-v1alpha1.md)
