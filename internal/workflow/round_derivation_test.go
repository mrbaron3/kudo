package workflow

import "testing"

const (
	testImplementerCommentAuthorID int64 = 303
	testReviewerCommentAuthorID    int64 = 404
)

// AC-3: round は marker と author identity から導出し、現在の無人区間は直近の
// ai-ready 付与より後だけを数える。本文が似ているだけの comment は数えない。
func TestDeriveReviewRoundsAuthenticatesMarkersAndUsesLatestReadyBoundary(t *testing.T) {
	observation := openObservation()
	observation.Issue.LabelEvents = []LabelEventObservation{
		{Name: LabelReady, Added: true, Sequence: 10},
		{Name: LabelReady, Added: true, Sequence: 50},
	}
	observation.Comments = []CommentObservation{
		// 前の無人区間。lifetime には入るが current stretch には入らない。
		{ID: 1, Sequence: 20, AuthorID: testReviewerCommentAuthorID,
			Marker: &CommentMarkerObservation{Kind: CommentMarkerTestValidity, Round: 1, Head: testApprovedHead}},
		// 現在の無人区間の正規 record。
		{ID: 2, Sequence: 60, AuthorID: testReviewerCommentAuthorID,
			Marker: &CommentMarkerObservation{Kind: CommentMarkerTestValidity, Round: 2, Head: testLiveHead}},
		{ID: 3, Sequence: 70, AuthorID: testReviewerCommentAuthorID,
			Marker: &CommentMarkerObservation{Kind: CommentMarkerFinalImplementation, Round: 1, Head: testLiveHead}},
		{ID: 4, Sequence: 80, AuthorID: testImplementerCommentAuthorID,
			Marker: &CommentMarkerObservation{Kind: CommentMarkerTestRevisionReport, Round: 3, Head: testApprovedHead}},
		// 他 actor、人間、marker 無しの類似本文は数えない。
		{ID: 5, Sequence: 90, AuthorID: testImplementerCommentAuthorID,
			Marker: &CommentMarkerObservation{Kind: CommentMarkerTestValidity, Round: 99, Head: testLiveHead}},
		{ID: 6, Sequence: 91, AuthorID: 999,
			Marker: &CommentMarkerObservation{Kind: CommentMarkerFinalImplementation, Round: 99, Head: testLiveHead}},
		{ID: 7, Sequence: 92, AuthorID: testReviewerCommentAuthorID,
			Body: "kudo:test-validity round 99 の finding に見える自然文"},
		{ID: 8, Sequence: 93, AuthorID: testReviewerCommentAuthorID,
			Marker: &CommentMarkerObservation{Kind: CommentMarkerTestRevisionReport, Round: 99, Head: testLiveHead}},
	}

	got := DeriveReviewRounds(observation, CommentAuthorIdentities{
		Implementer: testImplementerCommentAuthorID,
		Reviewer:    testReviewerCommentAuthorID,
	})
	want := DerivedReviewRounds{
		CurrentStretch: ReviewRounds{TestValidity: 2, FinalImplementation: 1},
		Lifetime:       ReviewRounds{TestValidity: 3, FinalImplementation: 1},
	}
	if got != want {
		t.Fatalf("rounds = %+v, want %+v", got, want)
	}
}

func TestDeriveReviewRoundsDoesNotMutateObservation(t *testing.T) {
	observation := openObservation()
	observation.Issue.LabelEvents = []LabelEventObservation{{Name: LabelReady, Added: true, Sequence: 1}}
	observation.Comments = []CommentObservation{{
		ID: 1, Sequence: 2, AuthorID: testReviewerCommentAuthorID,
		Marker: &CommentMarkerObservation{Kind: CommentMarkerTestValidity, Round: 1, Head: testLiveHead},
	}}
	before := cloneObservationForTest(observation)

	first := DeriveReviewRounds(observation, CommentAuthorIdentities{Reviewer: testReviewerCommentAuthorID})
	second := DeriveReviewRounds(observation, CommentAuthorIdentities{Reviewer: testReviewerCommentAuthorID})
	if first != second {
		t.Fatalf("同じ観測から異なる round: first=%+v second=%+v", first, second)
	}
	if len(observation.Comments) != len(before.Comments) || observation.Comments[0] != before.Comments[0] {
		t.Fatal("round 導出が観測を変更した")
	}
}
