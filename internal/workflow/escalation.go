package workflow

// EscalationReason は Run を `needs_human` phase で停止させた理由の機械可読な分類である。
//
// 正本は docs/spec/05_design/04_github-routing.md の Human escalation 節であり、Controller はこの code を
// 日本語 status comment へ載せる。error 文字列や自由記述で分岐しないのは、message 表現を
// 変えただけで Controller の分岐が壊れるためである。
//
// review verdict とは別の値空間である。`needs_human` verdict は reviewer の品質判断であり、
// EscalationReason は「Controller がどの理由で自動継続をやめたか」を表す。
type EscalationReason string

const (
	// EscalationReviewNeedsHuman は Review Result の verdict が `needs_human` だったことを表す。
	EscalationReviewNeedsHuman EscalationReason = "review_needs_human"
	// EscalationReviewRoundLimitExceeded は review gate の round 上限に達しても
	// blocking finding が解消しなかったことを表す。reviewer の判断ではなく Controller の
	// 予算切れであり、両者を区別できないと差し戻しを受けた人間が対処を選べない。
	EscalationReviewRoundLimitExceeded EscalationReason = "review_round_limit_exceeded"
	// EscalationRetryBudgetExhausted は bounded retry を使い切った execution failure を表す。
	EscalationRetryBudgetExhausted EscalationReason = "retry_budget_exhausted"
	// EscalationContractAuthorityConflict は Contract、Acceptance Criteria、authority の
	// 矛盾・不足・曖昧さを表す。
	EscalationContractAuthorityConflict EscalationReason = "contract_authority_conflict"
	// EscalationExternalMutationConflict は外部からの close/merge のように、blind mutation
	// できない外部干渉を表す。
	EscalationExternalMutationConflict EscalationReason = "external_mutation_conflict"
	// EscalationMergeBlocked は required check failure、conflict、branch protection の拒否など、
	// 承認済み head を安全に merge できない外形条件を表す。品質 verdict ではないため
	// `request_changes` へ読み替えない。reviewer は同じ判断を繰り返すだけで、CI failure の
	// 原因が Kudo の差分にあるとは限らない。
	EscalationMergeBlocked EscalationReason = "merge_blocked"
	// EscalationUnsafeMutationUnauthorized は危険な mutation への明示的許可が無いことを表す。
	EscalationUnsafeMutationUnauthorized EscalationReason = "unsafe_mutation_unauthorized"
	// EscalationSpecificationDecisionRequired は自動選択できない仕様判断を表す。
	EscalationSpecificationDecisionRequired EscalationReason = "specification_decision_required"
	// EscalationExternalConfigurationRequired は credential や外部設定が人間の操作なしに
	// 復旧できない状態を表す。
	EscalationExternalConfigurationRequired EscalationReason = "external_configuration_required"
)

// derivedEscalationReasons は state machine が Run state から自ら導出する理由である。
//
// 外部からの明示的 escalation event では指定できない。指定できると、round 上限に達して
// いない Run を「上限到達」として停止させられ、reason code と counter lineage が食い違う。
var derivedEscalationReasons = map[EscalationReason]bool{
	EscalationReviewNeedsHuman:         true,
	EscalationReviewRoundLimitExceeded: true,
	EscalationRetryBudgetExhausted:     true,
}

var escalationReasons = map[EscalationReason]bool{
	EscalationReviewNeedsHuman:              true,
	EscalationReviewRoundLimitExceeded:      true,
	EscalationRetryBudgetExhausted:          true,
	EscalationContractAuthorityConflict:     true,
	EscalationExternalMutationConflict:      true,
	EscalationMergeBlocked:                  true,
	EscalationUnsafeMutationUnauthorized:    true,
	EscalationSpecificationDecisionRequired: true,
	EscalationExternalConfigurationRequired: true,
}

// ReviewRounds は review gate ごとに quality verdict が確定した round 数である。
//
// gate ごとに独立に数える。通算にすると、片方の gate が収束しなかっただけで
// もう片方の予算を失う。attempt failure、stale input、transport failure は verdict では
// ないため round を消費しない。消費させると実行環境の不調が人間への差し戻しに化ける。
//
// Run は無人区間の counter と生涯 counter の両方をこの型で持つ。前者だけが上限判定に
// 使われ、escalation で 0 へ戻る。
type ReviewRounds struct {
	TestValidity        int
	FinalImplementation int
}
