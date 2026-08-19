# 4.5. Retry・Recovery・Human Escalation 機能仕様

一時障害から自動回復できる状況と、人間の判断を必要とする状況を区別し、process 停止や再試行を
またいでも同じ workflow を安全に継続する機能である。

## 目的

- execution attempt の失敗を品質 verdict と混同せず、再試行可能性に応じて処理する。
- mutable process memory ではなく durable state と immutable artifact から回復する。
- 自動化の判断境界を越えた場合は、理由と必要な対応を明示して停止する。

## 4.5.1. Retry

- logical Operation と execution attempt を分ける。
- timeout、rate limit、一時的な network / provider failure は bounded backoff で再試行する。
- provider session は attempt ごとに新規作成し、resume token や conversation database を引き継がない。
- invalid provider output、protocol validation error、quality verdict を transport retry と区別する。
- retry budget と review round 上限を別の予算として扱う。

## 4.5.2. Recovery

- lease expiry 後は commit と immutable artifact から新しい attempt を再構築する。
- Run phase、Operation result、attempt、lease、artifact reference を durable に保持する。
- 外部 mutation の前後で expected state と live state を照合し、結果不明の timeout 後も安全に再試行する。
- state transition と external projection intent を同じ transaction で保存する。

## 4.5.3. Human Escalation と再開

- review round 上限、安全判断、authority conflict、外部干渉では `needs_human` へ遷移する。
- 停止 phase、理由 code、evidence、必要な対応を durable record と日本語 comment に残す。
- 人間の `ai-ready` 再付与後にだけ、安全な resume または supersede を行う。
- semantic input が同じなら同じ Run を再開でき、変更されていれば古い Run を supersede する。
- 人間への差し戻しで無人区間の review round counter をリセットし、生涯 counter は保持する。

## 受け入れ上の不変条件

- transport failure、quality verdict、stale input、human decision を別の結果として保持する。
- changed semantic input に以前の approval を移し替えない。
- paused Run の resume / supersede 時にも、同じ Issue の writer を二つ存在させない。
- `needs_human` を自動的な無限 retry で解除しない。

## 正本

- [03. システム仕様](../../03_system-spec/) F-08
- [End-to-end workflow](../../../workflow.md) — Durable states、Escalation and resumption、Recovery
- [Architecture](../../../architecture.md) — Queue、lease、recovery
- [Worker Operation Protocol](../../../contracts/operation-protocol-v1alpha1.md)
- [ADR-0003](../../../decisions/0003-review-round-limit.md)
