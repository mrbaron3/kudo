# Evaluation harness — deferred

## Decision

評価ハーネスはKudoの初期実装に含めない。まず単一runのcontract、artifact lineage、test validity review、最終reviewが再現可能に動くことを確認する。

Runtime Controllerは必要だが、evaluation harnessではない。Controllerは1 runの安全な状態遷移を担い、evaluation harnessは複数run、候補、設定、model/provider、system versionを比較する。

## Questions to settle later

pass@kなどの指標を採用する前に、少なくとも次を定義する必要がある。

- Trial unit: 何を1試行とするか
- Success predicate: test通過、review approve、merge可能性、実運用成果のどれか
- Candidate generation: 同じinputからどの条件を変えてk候補を作るか
- Independence: retry、共有context、repairが試行独立性をどう壊すか
- Censoring: timeout、infra failure、人へのescalationを分母と成功判定でどう扱うか
- Reproducibility: repository SHA、Issue Revision、Context Manifest、policy、model/provider設定をどう固定するか
- Cost and latency: 品質と同時に何を最適化するか
- Online/offline boundary: production runとbenchmark runをどう分離するか

これらが決まるまでは、pass@kをproduction retryの成功率やReview Workerの品質指標として流用しない。

## OTel boundary

Kudoは将来の分析に必要なrun ID、state transition、artifact digest、duration、token/cost、verdictなどをOTelへemitできる。ただし、次はKudo側のauthoritative recordに残す。

- Issue Revision、Context Manifest、contract digest
- artifact manifest
- Review Request / Result
- idempotencyとworkflow state

OTel backendにquery、集計、dashboard、experiment comparisonを持たせるか、Kudo側に専用evaluatorを置くかは、評価仕様を決めてから選ぶ。telemetry retentionやsamplingによってworkflow correctnessが変わってはならない。
