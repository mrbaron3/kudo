package workflow

import (
	"fmt"
	"strings"
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
	if observation.Issue.Number != config.Issue {
		return DerivedReviewRounds{}, fmt.Errorf(
			"review round を導出できない: 観測は Issue %d のもので、対象は Issue %d である",
			observation.Issue.Number, config.Issue)
	}
	record, err := runPullRequest(observation)
	if err != nil {
		return DerivedReviewRounds{}, fmt.Errorf("review round を導出できない: %w", err)
	}
	boundary, hasBoundary := latestReadyLabelAdd(observation.Issue.LabelEvents, config.ReadyLabel)

	var rounds DerivedReviewRounds
	for _, comment := range observation.Comments {
		if record == nil || comment.PullRequest != record.Number {
			continue
		}
		gate, ok := roundConsumingGate(observation, comment, config)
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
		if !event.Added || !strings.EqualFold(event.Label, readyLabel) {
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
//
// 語彙外の gate を持つ finding marker は計上しない。marker 自体は protocol 違反だが、
// その検出は record を書いた側の validation の責務であり、ここで error にすると
// counter が返らなくなって上限判定が止まる。
func roundConsumingGate(observation Observation, comment CommentObservation,
	config DeriveConfig) (contract.ReviewKind, bool) {
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
		default:
			return "", false
		}
		// approve にも advisory finding が付きうる（review-protocol-v1alpha1.md）。
		// approve は次の gate へ進むので自動修正 loop の churn を生まず、予算を消費しない。
		// 判定の根拠は改竄できない verdict check run 側に置く。approve と証明できた
		// finding だけを除外し、head も check run も観測できない場合は計上する。
		// 逆に倒すと、観測の欠落で予算が減らず無人 loop が止まらなくなる。
		if approvedFinding(observation, config, comment.Marker) {
			return "", false
		}
		return comment.Marker.Review, true
	case CommentMarkerTestRevisionReport:
		if comment.AuthorID != config.Implementer.CommentAuthorID {
			return "", false
		}
		return contract.ReviewTestValidity, true
	}
	return "", false
}

// approvedFinding は finding marker が approve verdict に付随する記録かを返す。
func approvedFinding(observation Observation, config DeriveConfig,
	marker *CommentMarkerObservation) bool {
	if marker.Head == "" {
		return false
	}
	name := CheckRunTestValidity
	if marker.Review == contract.ReviewFinalImplementation {
		name = CheckRunFinalImplementation
	}
	verdict, err := verdictOn(observation.CheckRuns, config, name, marker.Head)
	return err == nil && verdict == contract.VerdictApprove
}

// addRound は gate ごとの counter を進める。語彙外の gate を既定で test 側へ数えない。
// 到達不能を前提にした既定は、gate 語彙が増えたときに黙って予算を削る。
func addRound(rounds *ReviewRounds, gate contract.ReviewKind) {
	switch gate {
	case contract.ReviewTestValidity:
		rounds.TestValidity++
	case contract.ReviewFinalImplementation:
		rounds.FinalImplementation++
	}
}

// runPullRequest は round を数える対象の記録面を返す。
//
// Run の lineage には supersede した旧 PR が closed のまま残るため、Issue 全体の
// comment を数えると旧 Run の round が現在の無人区間へ混入する。ADR-0001 が受け入れた
// 計数のずれは escalation が遅れる向きだけなので、過大計上は避ける。
func runPullRequest(observation Observation) (*PullRequestObservation, error) {
	active, lineage, err := classifyPullRequests(observation.PullRequests)
	if err != nil {
		return nil, err
	}
	if active != nil {
		return active, nil
	}
	return lineage.merged, nil
}
