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
	// EscalationPolicy は claim 時に pin した gate 予算 artifact の ref である。
	//
	// Input へ含めない。上限は reviewer が読まない値であり review 判断の入力ではないため、
	// 値を変えただけで進行中 Run を supersede させてはならない。監査のために保持し、
	// escalation comment が「この Run に固定された上限」の根拠として引用する。
	EscalationPolicy contract.EscalationPolicyRef
	// RoundLimits は pin 済み policy から解決した gate ごとの round 上限である。
	// transition は artifact を decode しないため、解決済みの値を Run が運ぶ。
	RoundLimits contract.ReviewRoundLimits
	// Rounds は現在の無人区間で確定した review round 数であり、上限判定に使う。
	//
	// 上限が縛るのは「人間が次にこの Run を見るまでに何 round 回すか」であって Run の
	// 生涯合計ではない。したがって escalation のたびに 0 へ戻る。人間へ差し戻した後も
	// counter が上限のままだと、修正後の review が予算 0 になり、1 round で収束する
	// 修正にも automation が追従できない。
	Rounds ReviewRounds
	// TotalRounds は Run の生涯で確定した review round 数である。reset せず、
	// 差し戻しを繰り返す Run を人間が識別するための lineage として使う。
	TotalRounds ReviewRounds
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
