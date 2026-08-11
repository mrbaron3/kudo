# Servo からの移行判断

## Strategy

KudoはServoの次versionではなく、greenfield repositoryである。Servo側のsourceと未コミットdocumentは削除・移動せず、そのまま保持する。Kudoには検証したいcore invariantsだけを再記述し、旧実装とのcompatibilityを要件にしない。

## Concepts retained

Servoの `docs/requirements/lightweight-tdd-issue-to-pr-run/` にあるdraftから、次の概念を採用した。

- machine-readableなIssue Contractとclaim前のstrict validation
- 1 Issue / 1 worktree / 1 branch / 1 PR
- test作成、RED evidence、独立test validity review、実装、GREEN evidenceの順序
- Implementationだけがmutable workspaceを所有する権限分離
- versioned Review Request / Resultとimmutable artifact handoff
- changed headまたはartifactに対するreview resultのstaleness
- transport failureとquality verdictの分離

これらは主に次のdraftを参照し、Kudoのnamespaceと縮小したscopeへ書き直した。

- `issue-contract.md`
- `implementation-review-protocol.md`
- `requirements.md` のcore workflow部分

## Deliberately not migrated

次は初期repositoryへ移していない。

- ServoのGo/TypeScript source codeとdatabase schema
- `roadmap.yaml` と旧ADR群
- provider固有のsession、effort、review profile、複数review session合議
- tmux、container、PostgreSQLを前提とするorchestration
- repair supervisor、domain alignment、best-of-N execution
- pass@k、scoring、benchmark、evaluation harness
- 旧artifact namingと旧schema namespace
- draftの包括的な `acceptance.yaml`

必要になった項目は、Servoから機械的にcopyせず、Kudoで観測された制約に対する個別のdecisionとして追加する。

## Provenance note

この選別は2026-08-11時点のServo未コミットdraftを基に行った。draftはcommitで固定されていないため、Kudo側のversioned contractを以後のsource of truthとする。
