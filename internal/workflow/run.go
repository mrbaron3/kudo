package workflow

import "github.com/mrbaron3/kudo/internal/contract"

// Approval は gate を通した review 結果の binding である。
// verdict そのものではなく「どの head のどの request が承認されたか」だけを持つ。
type Approval struct {
	Head          string
	RequestDigest contract.Digest
}

// Run は 1 Issue に対する実行の durable aggregate である。
//
// 解決済みの opaque な identity だけを持ち、Issue Contract の parse 結果、section 本文、
// raw body、canonical YAML bytes を保持しない。transition が Readiness や DependsOn を
// 読み始めると、workflow が Task Context の version ごとに分岐する parser になる。
//
// Version は store が採番する optimistic concurrency の版であり、transition function は
// 触らない。phase の進行と版の採番を同じ層に置くと、pure な決定と永続化順序が絡む。
type Run struct {
	ID      string
	Issue   contract.IssueRef
	Version int64
	Phase   Phase

	// Input は staleness 判定の対象となる semantic identity である。
	Input InputIdentity
	// Observation は audit lineage の最新観測であり、identity には寄与しない。
	Observation contract.Digest

	PullRequest contract.PullRequestRef

	// FixedHead は Worker が固定したが未 publish の head である。
	FixedHead string
	// PublishedHead は PR へ publish 済みで review が繋留される head である。
	PublishedHead string
	// PublishedTestHead は test validity review に publish した最新 head である。
	// final request_changes 後の修復では PublishedHead が以前の実装 head を指すため、
	// test approval の binding を独立に検証できるよう保持する。
	PublishedTestHead string
	// ChecksHead は refactor 後の required checks が通った head である。
	// PublishedHead と別に持つのは、checks を通していない head が publish されても
	// final approve の gate を通せないようにするためである。
	ChecksHead string

	TestApproval  *Approval
	FinalApproval *Approval
}
