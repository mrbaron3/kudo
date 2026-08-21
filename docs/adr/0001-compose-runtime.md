# ADR-0001: Docker Compose を正式な実行基盤とする

- Status: accepted（2026-08-11）

## Context

Kudo は、常駐する Controller、複数の Issue/Review Worker、durable state、immutable artifact、GitHub webhook endpoint を必要とする。Webhook の取りこぼしを polling で回復し、process crash 後も Run と Operation を再開できなければならない。

開発 host は Apple Silicon Mac だが、展開先を個別の macOS daemon 設定へ固定したくない。また、Issue Worker と Review Worker の filesystem、credential、provider session を分け、同じ repository 内の独立 Issue を並行処理したい。

候補は、macOS 上の native daemon、単一 all-in-one container、Apple Container、Apple Container の experimental Kubernetes、Docker Compose だった。

## Decision

Docker Compose を Kudo の canonical orchestrator とする。application は一つの Go module と一つの`kudo` binary に保ち、role（controller / issue worker / review worker / migrate）を Compose service として別 container で起動する。workflow state と queue は PostgreSQL を起点に置き、artifact と workspace は用途を分けた named volume に置く。Docker socket と Docker-in-Docker は使用しない。

service 構成、volume/network 契約、supported runtime、image 配布形式の現在形は [Runtime platform](../spec/05_design/03_runtime-platform.md) を正本とする。

## Rationale

Compose は services、network、volume、health、dependency、restart lifecycle を一つの declarative application model で管理できる。Mac と Linux で同じ compose file と OCI image を使え、Kudo 固有の host supervisor を実装する必要がない。

PostgreSQL を state と queue に併用することで、Run transition と次 Operation/outbox の記録を一つの transaction にできる。初期の単一 host 運用に Redis や message broker を加えずに、複数 Worker、lease、retry、recovery を実現できる。

role container を分けることで、単一 binary を保ったまま、implementation workspace と branch / commit / Pull Request の write authority を Issue Worker だけに与え、Review Worker を read-only にできる。Controller の GitHub write authority は durable state に基づく Issue label / comment の status projection に限定する。

## Alternatives

### Native macOS daemon

launchd で Go daemon と provider CLI を直接動かす案は採用しない。Mac 固有の setup、upgrade、secret、process supervision が deployment contract になり、Linux への移行と role isolation が難しくなるためである。

### Single all-in-one container and SQLite

小規模 demo には単純だが、Controller/Review/Implementation の権限分離、複数 Worker の lease、crash recovery、durable queue を一つの process/filesystem に寄せすぎるため採用しない。

### Apple Container as the canonical orchestrator

Apple Container は OCI image と VM 単位の強い isolation に魅力があり、単体 sandbox の候補にはなる。一方、Kudo が必要とする multi-service application を Compose と同等に宣言・運用する canonical interface を現時点で提供していない。host の Apple Container control plane を Linux container 内の Controller から操作するには host bridge が必要になり、「すべてを container として同じ構成で展開する」という目的を損なう。

そのため canonical deployment には採用せず、OCI image compatibility と将来の per-Operation sandbox adapter の候補として残す。

### Apple Container Kubernetes

Apple Container の local Kubernetes support は experimental であり、単一 host の Kudo に Kubernetes control plane と manifest 群を導入する利益が現時点では小さいため採用しない。

## Consequences

- macOS host には Compose-capable container runtime が必要になる。
- PostgreSQL と artifact volume の backup/restore が必須の運用責務になる。
- Worker image に provider CLI と repository toolchain を含める release process が必要になる。
- Compose は単一 host boundary である。multi-host scale には artifact/workspace と scheduler の新しい判断が必要になる。
- Apple Container だけが提供する isolation は標準経路では使わない。必要になれば Worker の Sandbox Runner boundary で再評価する。
- Docker Desktop の組織利用条件は deployment owner が確認する。Kudo の image と Compose contract 自体は OCI/Compose standard に保つ。
- 更新される文書: [Runtime platform](../spec/05_design/03_runtime-platform.md)（deployment 契約の正本）、[Development environment](../spec/06_project/02_development.md)。

## Revisit conditions

次のいずれかが成立した場合、この ADR を新しい ADR で再検討する。

- Apple Container が production-ready な Compose 相当の application lifecycle を提供する。
- provider code execution に container 内 process isolation を超える明確な要件が生じる。
- single-host named volume が measured throughput、availability、recovery 要件を満たさなくなる。
- multi-host deployment または Kubernetes platform が組織標準になる。

## References

- [Apple Container](https://github.com/apple/container)
- [Apple Container technical overview](https://github.com/apple/container/blob/main/docs/technical-overview.md)
- [Apple Container command reference](https://github.com/apple/container/blob/main/docs/command-reference.md)
- [Docker Compose documentation](https://docs.docker.com/compose/)
- [PostgreSQL versioning policy](https://www.postgresql.org/support/versioning/)
