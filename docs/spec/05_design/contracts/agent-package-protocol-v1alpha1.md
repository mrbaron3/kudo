# Agent Package Protocol v1alpha1

## Purpose

Kudo の model-bearing Operation が使う評価観点、instructions、input/output schema、tool profile、fixtureを、
Codex Skill、Claude agent/plugin、provider session から独立した repository 所有の package として定義する。
最初の package は Review Operation `test_validity` 一つであり、一つの fresh session が全観点を評価する。
観点別 fan-out、score、合議は本 version に含めない（設計 Issue [#85](https://github.com/mrbaron3/kudo/issues/85)）。

Agent Package は判断内容の正本であるが workflow runtime ではない。GitHub 観測、Task Context / Context
Manifest の live 再構築、digest/schema/staleness、App identity、CAS、worktree、retry、record surface、gate
判定は deterministic な Kudo runtime に残す。Package はこれらの権限を持たず、検証済み immutable input
から structured result を提案するだけである。

## Repository layout

```text
agent-packages/<operation>/<version>/
├── agent-package.json
├── instructions.md
├── input.schema.json
├── output.schema.json
├── tool-profile.json
└── fixtures/
    ├── approve.input.json
    ├── approve.output.json
    ├── request-changes.input.json
    └── request-changes.output.json
```

manifest schema は`kudo.agent-package/v1alpha1`である。`test_validity`の正本は
`agent-packages/test_validity/v1alpha1/`に置く。Codex Skill、Claude 固有 agent、plugin、slash command を
用意する場合も、この package を読み込む薄い launcher とし、instructions や schema を複製しない。

## Manifest

```json
{
  "schema": "kudo.agent-package/v1alpha1",
  "name": "test_validity",
  "version": "v1alpha1",
  "operation": "test_validity",
  "instructions": {
    "path": "instructions.md",
    "mediaType": "text/markdown; charset=utf-8",
    "digest": "sha256:<digest>"
  },
  "inputSchema": {
    "path": "input.schema.json",
    "mediaType": "application/json",
    "digest": "sha256:<digest>"
  },
  "outputSchema": {
    "path": "output.schema.json",
    "mediaType": "application/json",
    "digest": "sha256:<digest>"
  },
  "toolProfile": {
    "path": "tool-profile.json",
    "mediaType": "application/json",
    "digest": "sha256:<digest>"
  },
  "fixtures": [
    {
      "name": "approve",
      "input": {"path": "fixtures/approve.input.json", "mediaType": "application/json", "digest": "sha256:<digest>"},
      "output": {"path": "fixtures/approve.output.json", "mediaType": "application/json", "digest": "sha256:<digest>"}
    }
  ]
}
```

component path は package root からの clean な relative path でなければならず、absolute path、`.`、`..`、
重複 path を拒否する。manifest と tool profile は unknown/duplicate field を拒否する strict JSON とする。
component は4 MiBを上限とし、raw bytes の SHA-256 が manifest digest と一致しなければ load しない。

Package ref は`AgentPackageRef{schema,digest}`である。digest は manifest を field 固定順、fixture name 順の
compact JSON へ canonical encode した bytes に計算する。manifest は全 component の raw digest を含むため、
Package ref が instructions、schema、tool profile、fixture の closure identity になる。manifest file 自体の
indent や末尾改行は identity に含めない。

Review Request は Package ref を semantic input として固定する。component の変更は新しい Package ref と
新しい Review Request を要求し、以前の Review Result を stale にする。path だけ、deployment default、
provider 側の Skill 名から package version を推測しない。

## JSON Schema subset

input/output schema は Draft 2020-12、root`type: object`を必須とし、全object schemaで
`additionalProperties: false`を要求する。
Kudo v1alpha1 が受理する keyword は`$schema`、`$id`、`$defs`、local`$ref`、`type`、
`additionalProperties`、`required`、`properties`、`items`、`const`、`enum`、`pattern`、`minItems`、
`maxItems`、`minLength`、`maxLength`、`oneOf`に限定する。未知 keyword を無視すると schema typo が
fail-open になるため、package load 時に拒否する。`$ref`は存在するlocal`$defs`だけを参照でき、参照先以外の
制約を黙って落とさないようsibling keywordを持たないnodeに限定する。

全 fixture input/output は package load 時に同じ schema validator を通す。fixture は model の評価 harness
や pass@k ではなく、package contract の最小 example corpus である。live provider test の代替ではない。

## Tool profile

tool profile schema は`kudo.agent-tool-profile/v1alpha1`である。

```json
{
  "schema": "kudo.agent-tool-profile/v1alpha1",
  "capabilities": ["repository:read"],
  "network": "none"
}
```

capability は provider tool 名ではない。adapter が抽象 capability を Codex sandbox または Claude built-in
tools へ写像する。Package が要求する capability は Run に固定した Review Worker Execution Policy の
`toolPermissions`の部分集合でなければならない。`test_validity` v1alpha1 は`repository:read`だけを持ち、
repository write、GitHub、process mutation、model-visible network を持てない。

## Immutable test_validity input

Kudo runtime は provider 起動前に次を行う。

1. Review Request、Package ref、Artifact Manifest を strict validation する。
2. live 再構築した Task Context / Context Manifest と Request の期待 ref を照合する。
3. authority の identity/digest/bytes と Context Manifest を順序付きで照合する。
4. 全 artifact の name/media type/length/digest/bytes と Artifact Manifest を照合する。
5. Review Request digest、Package ref、head SHA、policy refs、canonical Task Context、authority、artifact だけを
   `kudo.agent-input/test_validity/v1alpha1` JSONへencodeし、package input schemaで再検証する。

UTF-8 bytes は`encoding: utf-8`、それ以外は`encoding: base64`で表現する。artifact は logical name 順へ
canonicalizeし、authority は Context Manifest の優先順を保つ。raw Issue body、Issue comment、provider
session、credential、GitHub API response、mutable worktree path は入力に含めない。checkout path は local
launcher metadataであり provider envelope の identity ではない。provider が repository tool を使う場合は、
Review Worker が別途`headSha`を検証した disposable read-only checkoutだけを working directory にする。

## Structured output and binding

`test_validity` provider output は`kudo.agent-output/test_validity/v1alpha1`であり、`verdict`と`findings`だけを
持つ。Review Request digest、review run ID、created time は model に自己申告させず Kudo runtime が付与する。

runtime は次の順で受理する。

1. duplicate field、unknown field、package output schema を strict validationする。
2. finding の evidence ref が immutable request、instructions、Task Context、authority、artifact の digest
   集合に含まれることを確認する。
3. provider output から`kudo.review-result/v1alpha1`を構築する。
4. verdict/finding 整合と Review Request digest binding を Implementation–Review Protocol で検証する。

invalid JSON/schema、未知 verdict、未束縛 evidence、`approve`+blocking finding、
`request_changes`/`needs_human`+blocking finding欠落は`provider_invalid_response`相当の execution failureであり、
quality verdictへ変換しない。

## Provider launchers

Codex/Claude adapter は共通 provider envelope（package ref、instructions、immutable request）を受け取る薄い
launcherである。毎 attempt、新しい processとstate directoryを作り、version probeの結果をExecution
Policyの`adapterVersion`と照合する。host environment全体を継承せず、明示されたcredential environmentと
operation state pathだけを渡す。

- Codex: non-interactive`exec`、ephemeral session、user config/rulesとproject doc auto-discovery無効、
  read-only sandbox、package output schema指定。
- Claude: non-interactive print、bare/no-session-persistence、skills/plugins/hooks/MCP無効、read-onlyの
  `Read,Glob,Grep`だけ、package output schema指定。

provider wrapperの差（Codexの直接JSON、Claudeの`structured_output`）をadapterで除いた後は、同じpackage
output bytesをruntimeへ返す。session ID、resume/continue、provider固有agent定義をhandoffに使わない。

timeout、non-zero exit、truncated output、version mismatch、invalid structured outputはReview Resultと別の
execution failureである。bounded retry、telemetry、redaction、record surfaceへの記録はReview Worker側が行う。

## Versioning

instructionsの必須観点、input/output field、tool capability、fixtureの意味を変える場合は新しいversion
directoryを作る。既存directoryを新しいRequestへ暗黙適用しない。typoや説明だけでもcomponent digestは
変わるため、同じversion directoryを編集した場合もPackage refが変わり既存Resultはstaleになる。
