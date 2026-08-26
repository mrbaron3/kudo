package workflow

import (
	"fmt"
	"time"

	"github.com/mrbaron3/kudo/internal/contract"
)

// DerivedReviewRounds は観測から導出した round 数である。
//
// CurrentStretch だけが上限判定に使われる。Lifetime は reset せず、差し戻しを
// 繰り返す Run を人間が識別するための lineage として ledger と telemetry が使う。
type DerivedReviewRounds struct {
	CurrentStretch ReviewRounds
	Lifetime       ReviewRounds
}

// DeriveReviewRounds は Pull Request 上の marker 付き comment から round 数を導出する。
//
// pure である。counter を保存せず、marker が書いている round 番号も使わない。
// 数えるのは次の 2 種類だけで、いずれも作成 identity で他 actor と構造的に区別する。
//
//   - Reviewer 名義の finding comment: gate ごとに 1 round
//   - Implementer 名義の test-revision-report: test_validity の 1 round
//     （quality verdict ではないが test gate を再び開く差し戻しである）
//
// attempt failure、stale input、transport failure、protocol validation error は
// verdict でも差し戻しでもないため marker を持たず、ここで数えられない。
//
// 無人区間の起点は直近の`ai-ready`付与である。同時刻の記録は前の区間として数える。
// GitHub の timestamp は秒解像度でありうるため、境界のずれは escalation が遅れる側へ
// 倒す（ADR-0001 の Consequences）。
//
// config の actor identity が解決できない場合は counter を返さず error にする。0 を
// 返すと「まだ予算がある」へ倒れ、無人 loop が止まらなくなる。
func DeriveReviewRounds(observation Observation, config DeriveConfig) (DerivedReviewRounds, error) {
	if err := config.Validate(); err != nil {
		return DerivedReviewRounds{}, fmt.Errorf("review round を導出できない: %w", err)
	}
	boundary, hasBoundary := latestReadyLabelAdd(observation.Issue.LabelEvents, config.ReadyLabel)

	var rounds DerivedReviewRounds
	for _, comment := range observation.Comments {
		gate, ok := roundConsumingGate(comment, config)
		if !ok {
			continue
		}
		addRound(&rounds.Lifetime, gate)
		if !hasBoundary || comment.CreatedAt.After(boundary) {
			addRound(&rounds.CurrentStretch, gate)
		}
	}
	return rounds, nil
}

// latestReadyLabelAdd は無人区間の起点になる直近の`ai-ready`付与時刻を返す。
//
// 起点が観測できない場合は生涯 counter と同じ範囲を数える。観測の欠落で counter を
// 0 にすると、上限が効かないまま loop が回り続ける。
func latestReadyLabelAdd(events []LabelEventObservation, readyLabel string) (time.Time, bool) {
	var latest time.Time
	found := false
	for _, event := range events {
		if !event.Added || event.Label != readyLabel {
			continue
		}
		if !found || event.OccurredAt.After(latest) {
			latest = event.OccurredAt
			found = true
		}
	}
	return latest, found
}

// roundConsumingGate は comment が round を消費するかと、消費する gate を返す。
func roundConsumingGate(comment CommentObservation, config DeriveConfig) (contract.ReviewKind, bool) {
	if comment.Marker == nil {
		return "", false
	}
	switch comment.Marker.Kind {
	case CommentMarkerFinding:
		if comment.AuthorID != config.Reviewer.CommentAuthorID {
			return "", false
		}
		switch comment.Marker.Review {
		case contract.ReviewTestValidity, contract.ReviewFinalImplementation:
			return comment.Marker.Review, true
		}
		return "", false
	case CommentMarkerTestRevisionReport:
		if comment.AuthorID != config.Implementer.CommentAuthorID {
			return "", false
		}
		return contract.ReviewTestValidity, true
	}
	return "", false
}

func addRound(rounds *ReviewRounds, gate contract.ReviewKind) {
	if gate == contract.ReviewFinalImplementation {
		rounds.FinalImplementation++
		return
	}
	rounds.TestValidity++
}
