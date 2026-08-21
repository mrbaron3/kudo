# ADR-0006: Issue contextをlive sourceから再構築する

- Status: Accepted
- Date: 2026-08-21

## Context

Task IssueはGitHubが正本である。raw Issue body、Issue Observation、Task Context、Context Manifestの
canonical YAMLをArtifact Storeへ保存すると、GitHubとは別にIssue由来contentの保存、参照、schema移行、
retention、GCを設計する必要がある。Workerが必要なのは保存済みYAMLではなく、「現在のlive contentが
claim時に承認した意味的入力と同じか」である。

## Decision

1. claim handlerはlive IssueをIssue Compilerでcompileし、Context Resolverでauthority/baseを解決する。
2. claim成功時はCompiler version、Issue Observation ref/body digest、Task Context ref、Context Manifest ref、
   base SHAだけをstructured claim contextとしてPostgreSQLへ固定する。
3. raw Issue body、Issue Observation YAML、Task Context YAML、Context Manifest YAMLはArtifact Storeまたは
   PostgreSQLのYAML columnへ保存しない。
4. Task Contextを必要とするIssue Worker / Review Worker Operationは開始時と完了時にlive Issue/authorityを
   再取得し、claim時と同じCompiler versionでcanonical identityを再計算する。
5. identityが一致したcanonical Task Contextとauthority bytesだけをそのAttemptのmodel inputとして使い、
   Attempt終了時に破棄する。不一致はquality verdictへ変換せず`stale_input`として返す。
6. Artifact Storeはtest、patch/source snapshot、command evidence、Review Resultなどlive sourceから再取得
   できない成果物に限定する。

## Consequences

- GitHubがIssue contentの唯一の正本、PostgreSQLがworkflowと期待digestの正本になり、Task Contextが第三の
  永続的な正本にならない。
- exactに同じYAMLはcontent addressingで重複排除できるが、Issue由来artifactのlifecycle自体を不要にできる。
- 各OperationがGitHub readを行うためrate limitと一時障害の影響を受ける。取得失敗は保存済み本文で代行せず、
  retry可能なtransport failureとして扱う。
- GitHub Issueが後から編集されると、過去Runが読んだ本文bytesをdigestだけから復元できない。このruntimeは
  freshnessと実行時同一性を保証し、過去本文の完全archiveは保証しない。法務・監査上のarchiveが必要になった
  場合は、実行context storeとは分離した別decisionで追加する。
- active Runを継続する間はclaim時のCompiler versionを実行可能に保つ。対応versionを削除するdeploymentでは
  新Compilerを暗黙適用せず、既存Runを停止して新しいclaimへ移す。

## Compatibility baseline

本Decisionの採用時点ではPostgreSQL RunStore、Artifact Store、Issue/Review Worker runtimeは未実装であり、
永続化済みの`kudo.worker-result/v1alpha1`、Artifact Manifest、active Runは存在しない。そのため、M1で定義した
`v1alpha1`の未配備baselineを本Decisionに合わせて改訂し、`claimContext` fieldとIssue由来logical nameの
非永続化規則を同じversionへ反映する。使用実績のない旧表現を`v1alpha2`として併存させない。

このbaseline改訂はM2の永続化開始までに限る。RunStoreがprotocol payloadを保存した後は、canonical fieldの追加、
既存payloadをrejectするvalidation強化、identity bytesの変更を同じschema名へ加えない。非互換変更は新しい
protocol versionとして旧versionと併存させ、active RunがpinしたCompiler / protocol versionを実行可能に保つ。
