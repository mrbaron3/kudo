# Implementation plan

## Approach

外部接続なしで検証できるcoreから、1つずつvertical sliceを広げる。各incrementは、次のincrementへ進む前に決定論的なunit testと明示的なfailure behaviorを持つ。

## Increment 1 — Issue Contract and context resolution

repository identityとIssue numberを入力にし、Issue Reader越しにIssueを直接取得して、validated Issue Revision、Context Manifest、または構造化されたclaim rejectionを返す。

- fixed section、YAML block、required fieldをstrictにparseする
- unknown field、duplicate key、不正enum、欠落ACを拒否する
- Issue identityをevent envelopeから付与し、body内の自己申告に依存しない
- parent identity、dependency completion、authority referenceを明示的に解決する
- native relationshipとContract blockの不一致を拒否する
- Issue Revision、base SHA、Context ManifestとSHA-256 digestを生成する
- Epic配下のready Task、blocked dependency、欠落contextをfixtureで固定する

Issue Reader、relationship resolver、repository content readerはfakeを使う。このincrementではlive GitHub API、filesystem watcher、model providerへ接続しない。

## Increment 2 — Artifact and review protocol

byte列をcontent-addressedに保存し、Issue Revision / Context Manifest / Review Request / Resultを検証できるようにする。

- atomic writeとdigest verification
- manifest内のmissing/corrupt artifact検出
- request identityとresult binding
- changed head/artifactによるstaleness判定
- transport failureとquality verdictの型レベルでの分離

最初はtemporary directoryを使うfilesystem implementationとin-memory fakeで十分とする。

## Increment 3 — Controller

pure state transition functionと、idempotentなcommand execution boundaryを作る。

- eventと現在stateから、許可された次actionだけを返す
- duplicate deliveryをno-opにする
- Issue dependency graphから全ready candidateを決定し、dependencyのないIssueをglobal lockなしでclaimできる
- claim leaseはIssueRef、execution leaseはRun IDへscopeし、同じIssueの二重実行だけを防ぐ
- dependency cycleをclaim rejectionとし、capacity待ちをdependency blockedと混同しない
- retry可能なtransport failureと `needs_human` を分ける
- test validity approveなしにimplementationへ進めない
- final approveなしにcompletedへ進めない

clock、ID generator、lease、storeはinterface越しに注入する。

## Increment 4 — Local end-to-end slice

fixture repository、fake Issue Reader、fake workersを使い、IssueRefからReview Resultまでをprocess内で実行する。実際のGitHubやmodel providerより先に、artifact lineageとrecoveryをE2Eで証明する。

同一processであってもmodel-bearing Worker Operationごとに新しいsession identityが作られ、前Operationのconversation memoryを入力にしないことをfake session factoryで検証する。

dependencyのない2 Issueが同時にactive Runとなり、3つ目のdependent Issueは先行Runのcompletionとbase統合まで開始しないことをfixtureで検証する。

## Increment 5 — GitHub and worker adapters

最後に外部境界を1つずつ接続する。

1. GitHub Issue event取得とclaim表示
2. worktree / branch lifecycle
3. Operationごとにfresh sessionを作るtest authoring providerとRED command runner
4. isolated Review Worker
5. Operationごとにfresh sessionを作るimplementation providerとGREEN command runner
6. PR create/updateとfinal review trigger

watcherはrun-once handlerを呼ぶだけにし、polling自体へbusiness logicを置かない。live integration testはopt-inにし、core behaviorの唯一の検証手段にしない。

## Bootstrap exit criteria

現在のrepository準備は、次を満たした時点で完了とする。

- Go module、CLI entrypoint、required checksが動く
- runtime boundariesとdeferred scopeが文書化されている
- Issue / Review contract baselineとGitHub templateがある
- Servo由来の採用・非採用項目が追跡できる
- Increment 1を既存実装へ依存せず開始できる
