# Evaluation harness — deferred

## Decision

評価ハーネスはKudo runtimeの完成条件に含めない。まずproduction workflowとして、単一runのcontract、record surfaceのlineage、test validity review、最終review、mergeとIssue closeが再現可能に動くことを確認する。

Runtime Controllerは必要だが、evaluation harnessではない。Controllerは1 runの安全な状態遷移を担い、evaluation harnessは複数run、候補、設定、model/provider、system versionを比較する。

## Questions to settle later

pass@kなどの指標を採用する前に、少なくとも次を定義する必要がある。

- Trial unit: 何を1試行とするか
- Success predicate: test通過、review approve、merge可能性、実運用成果のどれか
- Candidate generation: 同じinputからどの条件を変えてk候補を作るか
- Independence: retry、共有context、repairが試行独立性をどう壊すか
- Censoring: timeout、infra failure、人へのescalationを分母と成功判定でどう扱うか
- Reproducibility: repository SHA、Issue Observation、Task Context、Context Manifest、policy、model/provider設定をどう固定するか
- Cost and latency: 品質と同時に何を最適化するか
- Online/offline boundary: production runとbenchmark runをどう分離するか

これらが決まるまでは、pass@kをproduction retryの成功率やReview Workerの品質指標として流用しない。

## OTel boundary

Kudoは将来の分析に必要なrun ID、phase遷移、payload digest、duration、token/cost、verdictなどをOTelへemitできる。ただし、次はGitHub上のauthoritative record surfaceから導出・参照できる状態を保つ。

- claim checkpoint（Issue Observation / Task Context / Context Manifestのref）
- evidence / verdict check runとfinding comment（commit SHAとの対応を含む）
- Review Request / Resultのidentity
- idempotency markerとworkflow state

PRとレビューの対応、どのcommitにどの指摘があったかは、PR timeline（check run、marker comment）から
機械的に復元できることを評価ハーネスの前提とする。

OTel backendにquery、集計、dashboard、experiment comparisonを持たせるか、Kudo側に専用evaluatorを置くかは、評価仕様を決めてから選ぶ。telemetry retentionやsamplingによってworkflow correctnessが変わってはならない。
