# ADR-0006: Issue contextをlive sourceから再構築する

- Status: accepted（2026-08-21）
- Supersede対象: [ADR-0002](0002-pr-anchored-review.md)のIssue freshness（保存済みObservation照合）

## Context

Task IssueはGitHubが正本である。Workerが必要なのは保存済みYAMLではなく、「現在のlive contentがclaim時に承認した意味的入力と同じか」である。raw Issue body、Issue Observation、Task Context、Context ManifestのcanonicalYAMLをArtifact Storeへ保存すると、GitHubとは別にIssue由来contentの保存、参照、schema移行、retention、GCを設計する必要が生じる。

## Decision

Issue由来のcanonical contentは永続化せず、必要とする各Operationの開始時と完了時にlive sourceから再構築して、claim時に固定したidentityと照合する。

- claim成功時はCompiler version、Issue Observation ref/body digest、Task Context ref、Context Manifest ref、base SHAだけをstructured claim contextとしてPostgreSQLへ固定する。
- identityが一致したcanonical Task Contextとauthority bytesだけをそのAttemptのmodel inputとして使い、Attempt終了時に破棄する。不一致はquality verdictへ変換せず`stale_input`として返す。
- Artifact Storeはtest、patch/source snapshot、command evidence、Review Resultなど、live sourceから再取得できない成果物に限定する。

再構築の手順・照合規則・Compiler versionの扱いの現在形は[workflow.md](../spec/05_design/02_workflow.md)「Live context reconstruction」と[task-context-v1alpha1.md](../spec/05_design/contracts/task-context-v1alpha1.md)「Live reconstruction and freshness」を正本とする。

## Alternatives

- **canonical YAMLをArtifact StoreまたはPostgreSQLのYAML columnへ保存する**: Issue由来contentの保存・参照・schema移行・retention・GCという第三の正本のlifecycle設計が丸ごと必要になる。Workerの本当の要求は「live contentがclaim時の意味的入力と同じか」の検証であり、保存はその要求に答えない。exactに同じYAMLはcontent addressingで重複排除できるが、lifecycle自体を不要にできる再構築のほうが強い。

## Consequences

- GitHubがIssue contentの唯一の正本、PostgreSQLがworkflowと期待digestの正本になり、Task Contextが第三の永続的な正本にならない。
- 各OperationがGitHub readを行うためrate limitと一時障害の影響を受ける。取得失敗は保存済み本文で代行せず、retry可能なtransport failureとして扱う。
- GitHub Issueが後から編集されると、過去Runが読んだ本文bytesをdigestだけから復元できない。このruntimeはfreshnessと実行時同一性を保証し、過去本文の完全archiveは保証しない。
- active Runを継続する間はclaim時のCompiler versionを実行可能に保つ。対応versionを削除するdeploymentでは新Compilerを暗黙適用せず、既存Runを停止して新しいclaimへ移す。
- 更新される文書: workflow.md（Live context reconstruction節）、task-context-v1alpha1.md（Live reconstruction and freshness、Canonical payload and persistence boundary）、operation-protocol-v1alpha1.md（Freshness and mutation）。

## Compatibility baseline

本Decisionの採用時点ではPostgreSQL RunStore、Artifact Store、Issue/Review Worker runtimeは未実装であり、永続化済みのprotocol payload、Artifact Manifest、active Runは存在しない。そのため、M1で定義した`v1alpha1`の未配備baselineを本Decisionに合わせて改訂し、`claimContext` fieldとIssue由来logical nameの非永続化規則を同じversionへ反映する。使用実績のない旧表現を`v1alpha2`として併存させない。

このbaseline改訂はM2の永続化開始までに限る。RunStoreがprotocol payloadを保存した後は、canonical fieldの追加、既存payloadをrejectするvalidation強化、identity bytesの変更を同じschema名へ加えない。非互換変更は新しいprotocol versionとして旧versionと併存させる。

## Revisit conditions

- 法務・監査上、過去Runが読んだ本文bytesの完全archiveが必要になった場合、実行context storeとは分離した別decisionとしてarchiveを追加する。
- 各OperationのGitHub read起因のrate limit消費または一時障害による停止が、実測で貫通・運用の支配的な失敗要因になった場合、キャッシュや保存の再導入を（stalenessの保証を壊さない形で）別ADRとして再検討する。
