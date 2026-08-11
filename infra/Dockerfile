# Kudo の開発/テスト image と最小 runtime image を一つの multi-stage Dockerfile で定義する。
#
# - build/test toolchain は Go 1.26.5 を tag + digest で固定する
# - runtime stage は distroless static の nonroot image に静的 binary だけを置く
# - BuildKit の cache mount で module/build cache を layer から分離する
# - Docker socket / Docker-in-Docker には依存しない
#
# base image を更新するときは tag と digest を必ず同時に差し替える
# （取得例: docker buildx imagetools inspect golang:1.26.5-trixie）。
ARG GO_IMAGE=golang:1.26.5-trixie@sha256:98988b42f3293b627bf07c884ff17181a59501769cd8c06c7ba901e0ce2c9853
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot@sha256:1b7b9f0f0e0a1d2155f531db587cc48ec26aaf97ab64364225f5bf18a054e66a

# --- source: module 解決とソース展開。BUILDPLATFORM 上で実行し cross-compile を高速化する
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS source
# go.mod と異なる toolchain の暗黙 download を禁止し、image の Go に固定する
ENV GOTOOLCHAIN=local
WORKDIR /src
# 依存だけを先に解決し、ソース変更で module download layer を無効化しない
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .

# --- build: TARGETOS/TARGETARCH 向けの静的 binary を cross-compile する
FROM source AS build
ARG TARGETOS
ARG TARGETARCH
ARG KUDO_VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${KUDO_VERSION}" -o /out/kudo ./cmd/kudo

# --- dev: non-root の開発/テスト image。container 内の標準 check 入口は `mise run check`
FROM ${GO_IMAGE} AS dev
ARG MISE_VERSION=v2026.8.4
ARG MISE_SHA256_LINUX_X64=b6760c6c4d5e629c31e31cb8a5018316338b01592408062a2aed673cec63cb2d
ARG MISE_SHA256_LINUX_ARM64=1e51effb42a8f1fdbf1c39b1c7a452c809cbd791df7b7f59351c0920ed8e7ef6
ARG TARGETARCH

# host と同じ task 定義（mise.toml）を container 内でも使うため、
# mise を version + sha256 固定で導入する
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) mise_arch=x64; mise_sha256="${MISE_SHA256_LINUX_X64}" ;; \
      arm64) mise_arch=arm64; mise_sha256="${MISE_SHA256_LINUX_ARM64}" ;; \
      *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/mise.tar.gz \
      "https://github.com/jdx/mise/releases/download/${MISE_VERSION}/mise-${MISE_VERSION}-linux-${mise_arch}.tar.gz"; \
    echo "${mise_sha256}  /tmp/mise.tar.gz" | sha256sum -c -; \
    tar -C /tmp -xzf /tmp/mise.tar.gz; \
    install -m 0755 /tmp/mise/bin/mise /usr/local/bin/mise; \
    rm -rf /tmp/mise /tmp/mise.tar.gz; \
    mise --version

RUN useradd --create-home --uid 10001 --user-group kudo

# Go toolchain は golang image のものを使い、mise には go を管理させない。
# cache は kudo user の home 配下に置き、Compose の named volume と一致させる。
ENV GOTOOLCHAIN=local \
    GOPATH=/home/kudo/go \
    GOMODCACHE=/home/kudo/go/pkg/mod \
    GOCACHE=/home/kudo/.cache/go-build \
    MISE_DISABLE_TOOLS=go \
    MISE_TRUSTED_CONFIG_PATHS=/workspace

WORKDIR /workspace
# module 取得層は go.mod/go.sum だけに依存させ、ソース編集では無効化させない
COPY --from=source /src/go.mod /src/go.sum ./
USER kudo
# bind mount なしの image 単体でも offline で check できるよう module を事前取得する
RUN mkdir -p "${GOMODCACHE}" "${GOCACHE}" && go mod download

# ソースは read-only 入力として root 所有のまま展開する（COPY は USER に関係なく root 所有で
# 書き込むため、kudo user からは読み取り専用）。Compose では同じ path へ live なソースを
# read-only bind mount して上書きする。
COPY --from=source /src/ /workspace/
COPY --from=build /out/kudo /usr/local/bin/kudo
CMD ["mise", "run", "check"]

# --- runtime: binary と CA 証明書だけの最小 runtime image（nonroot）
FROM ${RUNTIME_IMAGE} AS runtime
COPY --from=build /out/kudo /usr/local/bin/kudo
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/kudo"]
CMD ["help"]
