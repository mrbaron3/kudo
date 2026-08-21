package contract

import (
	"fmt"
	"slices"
	"strings"
)

// 正本は docs/spec/05_design/contracts/operation-protocol-v1alpha1.md の Artifact logical names である。

// ArtifactName は Operation Result と Artifact Manifest が共有する logical name の語彙である。
//
// ArtifactKind とは別の値空間である。ArtifactKind は bytes 自体の規則（versioned schema と
// media type）を決めるが、logical name は artifact が table の中で果たす役割を決める。
// source bundleのようにkindを持たない不透明bytesもあるため、両者は1対1に対応しない。
// GitHubから再構築できるIssue由来payloadは、この永続artifact語彙へ含めない。
//
// 語彙は配備設定ではなく契約として pure core に置く。必須集合を Execution Policy 等の
// producer が作る artifact へ移すと、producer が自分に課される gate 条件を自分で緩められる。
type ArtifactName string

const (
	// ArtifactNameTestPlan は Acceptance Criteria と test の対応を示す計画である。
	ArtifactNameTestPlan ArtifactName = "test-plan"
	// ArtifactNameRedEvidence は test が実装前に失敗したことの実行証跡である。
	ArtifactNameRedEvidence ArtifactName = "red-evidence"
	// ArtifactNameGreenEvidence は実装後に test が通ったことの実行証跡である。
	ArtifactNameGreenEvidence ArtifactName = "green-evidence"
	// ArtifactNameCheckEvidence は refactor 後の required checks と Issue Verification の証跡である。
	ArtifactNameCheckEvidence ArtifactName = "check-evidence"
	// ArtifactNameSourceBundle は head SHA を再構築・検証できる immutable snapshot である。
	// Review Worker は Issue Worker の worktree を mount しないため、これが唯一の source 経路になる。
	ArtifactNameSourceBundle ArtifactName = "source-bundle"
	// ArtifactNameTestValidityResult は final review が前提とする承認済み test validity verdict である。
	ArtifactNameTestValidityResult ArtifactName = "test-validity-result"
	// ArtifactNamePullRequestDraft は PR へ載せる summary、risk、manual verification の草稿である。
	ArtifactNamePullRequestDraft ArtifactName = "pull-request-draft"
	// ArtifactNamePullRequestObservation は publish 済み PR の exact observation record である。
	// Review Request はこれを audit lineage として参照し、live PR との照合の基準点にする。
	ArtifactNamePullRequestObservation ArtifactName = "pull-request-observation"
	// ArtifactNamePerformanceEvidence は bound 宣言がある Task の測定証跡である。
	// bound 宣言の有無は Task Context の内容に依存し、この pure validator は Task Context を
	// parse しないため、必須化は静的な kind 別集合ではなく review の deterministic
	// prerequisite（#28）が行う。語彙だけをここで固定する。
	ArtifactNamePerformanceEvidence ArtifactName = "performance-evidence"
	// ArtifactNameTestRevisionReport は implement / repair_implementation が「承認済み test の
	// 変更が必要」と判断した根拠である。blocking Review Result を持たない差し戻しであり、
	// この report が revise_tests session へ渡す唯一の finding になる。
	ArtifactNameTestRevisionReport ArtifactName = "test-revision-report"
)

// artifactNames は語彙の宣言順である。producer が名前を綴り直さずに参照できるよう公開する。
var artifactNames = []ArtifactName{
	ArtifactNameTestPlan,
	ArtifactNameRedEvidence,
	ArtifactNameGreenEvidence,
	ArtifactNameCheckEvidence,
	ArtifactNameSourceBundle,
	ArtifactNameTestValidityResult,
	ArtifactNamePullRequestDraft,
	ArtifactNamePullRequestObservation,
	ArtifactNamePerformanceEvidence,
	ArtifactNameTestRevisionReport,
}

// reconstructibleIssueInputNamesはlive sourceから各Operationで再構築する予約名である。
// output/manifestのopenな拡張語彙を維持しつつ、これらを別名の永続的な正本として
// 復活させる経路は明示的に閉じる。
var reconstructibleIssueInputNames = map[string]bool{
	"raw-issue-body":    true,
	"issue-observation": true,
	"task-context":      true,
	"context-manifest":  true,
}

// requiredOperationOutputs は kind の succeeded Result が固定していなければならない
// logical name である。protocol 文書の kind 表が Output として挙げる artifact のうち、
// head と external reference のように別 field が担うものを除いた集合と一致させる。
//
// publish_head、finalize_pull_request、merge_pull_request が PR observation を必須にするのは、
// publish の成否と PR の状態遷移（draft の ensure、ready 化、merged）を Controller が観測
// record から検証できなければ、Review Request の lineage と merge 完了の根拠を用意できない
// ためである。merge の成否を Result の真偽値ではなく観測 record に置くことで、応答を失った
// retry が同じ観測から自分の merge を再確認できる。
// requiredTestRevisionOutputs は test_revision_required の Result が固定していなければ
// ならない logical name である。succeeded の kind 別集合と違い outcome に紐付く。
// 差し戻しは blocking Review Result を作らないため、report が無いと revise_tests session は
// 「何を直すのか」を提供する入力を持たない。
var requiredTestRevisionOutputs = []ArtifactName{ArtifactNameTestRevisionReport}

var requiredOperationOutputs = map[OperationKind][]ArtifactName{
	OperationClaim:                {},
	OperationAuthorTests:          {ArtifactNameTestPlan, ArtifactNameRedEvidence, ArtifactNameSourceBundle},
	OperationReviseTests:          {ArtifactNameTestPlan, ArtifactNameRedEvidence, ArtifactNameSourceBundle},
	OperationImplement:            {ArtifactNameGreenEvidence, ArtifactNameCheckEvidence, ArtifactNamePullRequestDraft, ArtifactNameSourceBundle},
	OperationRepairImplementation: {ArtifactNameGreenEvidence, ArtifactNameCheckEvidence, ArtifactNamePullRequestDraft, ArtifactNameSourceBundle},
	OperationPublishHead:          {ArtifactNamePullRequestObservation},
	OperationFinalizePullRequest:  {ArtifactNamePullRequestObservation},
	OperationMergePullRequest:     {ArtifactNamePullRequestObservation},
}

// requiredReviewEntries は review kind の Artifact Manifest が備えていなければならない
// logical nameである。protocol文書が「最低限参照できるようにする」と定める、
// live sourceから再構築できないevidenceの集合と一致させる。
//
// source-bundle は head を生成する Operation の必須 output である。model の成果ではないが、
// Issue Worker が checkpoint commit から固定しなければ、workspace を持たない Controller は
// review 開始時にこの entry を用意できない。
// pull-request-observation を両 kind の必須にするのは、review が PR へ繋留されるためで
// ある。publish 時点の観測が manifest に無いと、reviewer は live PR との照合の基準点を
// 持てない。performance-evidence は bound 宣言時だけの条件付き entry であり、静的な
// 必須集合には含めない。
var requiredReviewEntries = map[ReviewKind][]ArtifactName{
	ReviewTestValidity: {
		ArtifactNameTestPlan,
		ArtifactNameRedEvidence,
		ArtifactNameSourceBundle,
		ArtifactNamePullRequestObservation,
	},
	ReviewFinalImplementation: {
		ArtifactNameTestPlan,
		ArtifactNameRedEvidence,
		ArtifactNameSourceBundle,
		ArtifactNameTestValidityResult,
		ArtifactNameGreenEvidence,
		ArtifactNameCheckEvidence,
		ArtifactNamePullRequestDraft,
		ArtifactNamePullRequestObservation,
	},
}

// conditionalReviewEntries は kind の Artifact Manifest が条件付きで備える logical name
// である。必須になる条件は Task Context の内容（例: performance bound の宣言）に依存し、
// Task Context を parse しない本 validator では静的に強制できない。条件の判定と欠落の
// 拒否は Review Worker の deterministic prerequisite が行い、ここは語彙と帰属だけを固定する。
var conditionalReviewEntries = map[ReviewKind][]ArtifactName{
	ReviewTestValidity:        {},
	ReviewFinalImplementation: {ArtifactNamePerformanceEvidence},
}

// ConditionalReviewEntries は review kind の条件付き manifest entry を返す。
// 未知の kind では nil を返す。
func ConditionalReviewEntries(kind ReviewKind) []ArtifactName {
	return slices.Clone(conditionalReviewEntries[kind])
}

// 宣言漏れの kind は必須集合が nil になり、「要求が無い」と区別できないまま gate が
// 無効になる。要求が空になる kind が将来ありうる以上、空を異常として機械的に扱う
// こともできない。fail-open を検出可能に留めず、宣言矛盾を持つ binary が
// 起動しないようにする。producer の契約違反ではないので ProtocolError にはしない。
func init() {
	for kind := range operationKindRules {
		if _, ok := requiredOperationOutputs[kind]; !ok {
			panic("operation kind " + string(kind) + " の必須 output logical name が宣言されていない")
		}
	}
	for kind := range reviewKinds {
		if _, ok := requiredReviewEntries[kind]; !ok {
			panic("review kind " + string(kind) + " の必須 manifest entry が宣言されていない")
		}
		if _, ok := conditionalReviewEntries[kind]; !ok {
			panic("review kind " + string(kind) + " の条件付き manifest entry が宣言されていない")
		}
	}
}

// ArtifactNames は logical name の語彙を宣言順で返す。
func ArtifactNames() []ArtifactName { return slices.Clone(artifactNames) }

// RequiredOperationOutputs は kind の succeeded Result が固定しなければならない
// logical name を返す。未知の kind では nil を返す。
func RequiredOperationOutputs(kind OperationKind) []ArtifactName {
	return slices.Clone(requiredOperationOutputs[kind])
}

// RequiredReviewEntries は review kind の Artifact Manifest が備えなければならない
// logical name を返す。未知の kind では nil を返す。
func RequiredReviewEntries(kind ReviewKind) []ArtifactName {
	return slices.Clone(requiredReviewEntries[kind])
}

// NamedArtifact は Operation が固定した output を logical name で引けるようにする。
//
// Name を ArtifactName にしないのは、必須集合が下限であって上限ではないからである。
// Worker は語彙外の evidence も残してよく、table は開いている。閉じているのは
// 「これが無ければ次 gate へ進めない」という required 側だけである。
type NamedArtifact struct {
	Name   string
	Digest Digest
}

// validateNamedArtifacts は output を logical name で引く table として検証する。
// name の重複を通すと、同じ name が二つの digest を指す Result が保存され、後続
// Operation はどちらを読むかを選べない。
func validateNamedArtifacts(field string, artifacts []NamedArtifact) error {
	seen := map[string]bool{}
	for i, artifact := range artifacts {
		name := fmt.Sprintf("%s[%d]", field, i)
		if !validArtifactName(artifact.Name) {
			return protocolErr(ProtocolFieldInvalid, name+".name",
				"artifact の logical name が不正: %q", artifact.Name)
		}
		if reconstructibleIssueInputNames[artifact.Name] {
			return protocolErr(ProtocolKindConstraint, name+".name",
				"Issue由来の再構築可能なinputは永続artifactにできない: %q", artifact.Name)
		}
		if seen[artifact.Name] {
			return protocolErr(ProtocolFieldDuplicate, name+".name",
				"logical name が重複: %s", artifact.Name)
		}
		seen[artifact.Name] = true
		if !artifact.Digest.Valid() {
			return protocolErr(ProtocolFieldInvalid, name+".digest",
				"digest が不正: %q", artifact.Digest)
		}
	}
	return nil
}

// requireArtifactNames は required な logical name が table に揃っているかを検証する。
//
// 欠落を 1 件目で打ち切らず全件を error へ載せる。欠落集合は「不完全な table」という
// 一つの違反の内容であって別々の違反ではなく、1 件ずつ返すと producer は必須集合の
// 件数だけ gate を往復することになる。
func requireArtifactNames(field, subject string, required []ArtifactName, present map[string]bool) error {
	missing := make([]string, 0, len(required))
	for _, name := range required {
		if !present[string(name)] {
			missing = append(missing, string(name))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return protocolErr(ProtocolKindConstraint, field,
		"%s が要求する logical name が揃っていない: %s", subject, strings.Join(missing, ", "))
}

func namedArtifactSet(artifacts []NamedArtifact) map[string]bool {
	present := make(map[string]bool, len(artifacts))
	for _, artifact := range artifacts {
		present[artifact.Name] = true
	}
	return present
}

func artifactEntrySet(entries []ArtifactEntry) map[string]bool {
	present := make(map[string]bool, len(entries))
	for _, entry := range entries {
		present[entry.Name] = true
	}
	return present
}
