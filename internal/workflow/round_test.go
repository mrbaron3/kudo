package workflow

import (
	"testing"
	"time"

	"github.com/mrbaron3/kudo/internal/contract"
)

func at(minute int) time.Time {
	return time.Date(2026, 8, 26, 9, minute, 0, 0, time.UTC)
}

func findingComment(id, authorID int64, kind contract.ReviewKind, minute int) CommentObservation {
	return CommentObservation{
		ID:          id,
		PullRequest: runPullRequestNumber,
		AuthorID:    authorID,
		CreatedAt:   at(minute),
		Marker:      &CommentMarkerObservation{Kind: CommentMarkerFinding, Review: kind},
	}
}

func revisionReportComment(id, authorID int64, minute int) CommentObservation {
	return CommentObservation{
		ID:          id,
		PullRequest: runPullRequestNumber,
		AuthorID:    authorID,
		CreatedAt:   at(minute),
		Marker: &CommentMarkerObservation{
			Kind: CommentMarkerTestRevisionReport,
			Head: redHead,
		},
	}
}

// countedRun は round 計数の対象になる open な Run の観測骨格を返す。
func countedRun() Observation {
	return runObservation(PullRequestStateDraft, redHead, bootstrapCommit)
}

func requireRounds(t *testing.T, observation Observation, config DeriveConfig) DerivedReviewRounds {
	t.Helper()
	rounds, err := DeriveReviewRounds(observation, config)
	if err != nil {
		t.Fatalf("DeriveReviewRounds: %v", err)
	}
	return rounds
}

// AC-3: Reviewer 名義の marker 付き finding comment と、Implementer 名義の
// test-revision-report だけが round を消費する。無人区間の起点は直近の ai-ready 付与である。
func TestDeriveReviewRoundsCountsOnlyAuthenticatedMarkers(t *testing.T) {
	observation := countedRun()
	observation.Issue.LabelEvents = []LabelEventObservation{
		{Label: LabelReady, Added: true, OccurredAt: at(0)},
		{Label: LabelReady, Added: false, OccurredAt: at(1)},
		// 直近の ai-ready 付与。ここより前の round は生涯 counter にだけ残る。
		{Label: LabelReady, Added: true, OccurredAt: at(30)},
		{Label: LabelReady, Added: false, OccurredAt: at(31)},
	}
	observation.Comments = []CommentObservation{
		// 前の無人区間。
		findingComment(1, derivedReviewerCommentAuthor, contract.ReviewTestValidity, 10),
		findingComment(2, derivedReviewerCommentAuthor, contract.ReviewFinalImplementation, 20),
		// ai-ready 付与と同時刻の記録は前の区間として数える。
		findingComment(3, derivedReviewerCommentAuthor, contract.ReviewTestValidity, 30),
		// 現在の無人区間。
		findingComment(4, derivedReviewerCommentAuthor, contract.ReviewTestValidity, 40),
		findingComment(5, derivedReviewerCommentAuthor, contract.ReviewTestValidity, 45),
		findingComment(6, derivedReviewerCommentAuthor, contract.ReviewFinalImplementation, 50),
		revisionReportComment(7, derivedImplementerCommentAuthor, 55),
		// 数えない記録。
		findingComment(8, derivedImplementerCommentAuthor, contract.ReviewTestValidity, 56),
		findingComment(9, 999, contract.ReviewFinalImplementation, 57),
		revisionReportComment(10, derivedReviewerCommentAuthor, 58),
		{ID: 11, PullRequest: runPullRequestNumber, AuthorID: derivedReviewerCommentAuthor,
			CreatedAt: at(59)},
		{ID: 12, PullRequest: runPullRequestNumber, AuthorID: derivedReviewerCommentAuthor,
			CreatedAt: at(59),
			Marker:    &CommentMarkerObservation{Kind: CommentMarkerFinding, Review: "security"}},
	}

	got := requireRounds(t, observation, derivedConfig())
	want := DerivedReviewRounds{
		CurrentStretch: ReviewRounds{TestValidity: 3, FinalImplementation: 1},
		Lifetime:       ReviewRounds{TestValidity: 5, FinalImplementation: 2},
	}
	if got != want {
		t.Fatalf("rounds = %+v, want %+v", got, want)
	}
}

// 無人区間の起点が観測できない場合は、生涯 counter と同じ範囲を数える。
// 観測の欠落で counter を 0 にすると、上限が効かないまま loop が回り続ける。
func TestDeriveReviewRoundsWithoutReadyEventCountsEverything(t *testing.T) {
	observation := countedRun()
	observation.Comments = []CommentObservation{
		findingComment(1, derivedReviewerCommentAuthor, contract.ReviewTestValidity, 10),
		findingComment(2, derivedReviewerCommentAuthor, contract.ReviewFinalImplementation, 20),
	}

	got := requireRounds(t, observation, derivedConfig())
	want := DerivedReviewRounds{
		CurrentStretch: ReviewRounds{TestValidity: 1, FinalImplementation: 1},
		Lifetime:       ReviewRounds{TestValidity: 1, FinalImplementation: 1},
	}
	if got != want {
		t.Fatalf("rounds = %+v, want %+v", got, want)
	}
}

// 導出は pure であり、同じ観測から常に同じ counter を返し、観測を書き換えない。
func TestDeriveReviewRoundsIsPure(t *testing.T) {
	observation := countedRun()
	observation.Issue.LabelEvents = []LabelEventObservation{
		{Label: LabelReady, Added: true, OccurredAt: at(5)},
	}
	observation.Comments = []CommentObservation{
		findingComment(1, derivedReviewerCommentAuthor, contract.ReviewTestValidity, 10),
		revisionReportComment(2, derivedImplementerCommentAuthor, 12),
	}
	before := cloneObservation(observation)

	first := requireRounds(t, observation, derivedConfig())
	second := requireRounds(t, observation, derivedConfig())
	if first != second {
		t.Fatalf("同じ観測から違う counter: first=%+v second=%+v", first, second)
	}
	if len(observation.Comments) != len(before.Comments) ||
		observation.Comments[0] != before.Comments[0] {
		t.Fatal("round 導出が観測を変更した")
	}
	if first.CurrentStretch != (ReviewRounds{TestValidity: 2}) {
		t.Fatalf("current stretch = %+v", first.CurrentStretch)
	}
}

// supersede した旧 Run の finding は現在の無人区間へ混入しない。混入すると新しい Run の
// 予算が実際より早く尽き、ADR-0001 が受け入れた「escalation が遅れる向き」の逆へ倒れる。
func TestDeriveReviewRoundsIgnoresSupersededLineage(t *testing.T) {
	observation := countedRun()
	observation.PullRequests = append(observation.PullRequests, PullRequestObservation{
		Number: 600, State: PullRequestStateClosed, Head: bootstrapCommit,
		HeadLineage: []string{bootstrapCommit},
	})
	stale := findingComment(1, derivedReviewerCommentAuthor, contract.ReviewTestValidity, 10)
	stale.PullRequest = 600
	observation.Comments = []CommentObservation{
		stale,
		findingComment(2, derivedReviewerCommentAuthor, contract.ReviewTestValidity, 20),
	}

	got := requireRounds(t, observation, derivedConfig())
	want := DerivedReviewRounds{
		CurrentStretch: ReviewRounds{TestValidity: 1},
		Lifetime:       ReviewRounds{TestValidity: 1},
	}
	if got != want {
		t.Fatalf("rounds = %+v, want %+v", got, want)
	}
}

// 設定が壊れている場合に 0 を返さない。0 を返すと上限判定が「まだ余裕がある」へ
// 倒れ、無人 loop が止まらなくなる。
func TestDeriveReviewRoundsRejectsIncompleteConfiguration(t *testing.T) {
	observation := countedRun()
	observation.Comments = []CommentObservation{
		findingComment(1, derivedReviewerCommentAuthor, contract.ReviewTestValidity, 10),
	}

	config := derivedConfig()
	config.Reviewer.CommentAuthorID = 0
	if _, err := DeriveReviewRounds(observation, config); err == nil {
		t.Fatal("actor identity が欠けた設定で counter を返した")
	}
}
