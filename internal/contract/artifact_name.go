package contract

import (
	"fmt"
	"slices"
	"strings"
)

// 正本は docs/contracts/operation-protocol-v1alpha1.md の Artifact logical names である。

// ArtifactName は Operation Result と Artifact Manifest が共有する logical name の語彙である。
//
// ArtifactKind とは別の値空間である。ArtifactKind は bytes 自体の規則（versioned schema と
// media type）を決めるが、logical name は artifact が table の中で果たす役割を決める。
// source bundle のように kind を持たない不透明 bytes があり、逆に authority が Issue
// reference のときは同じ kind の raw body が別々の name で複数入るため、両者は 1 対 1 に
// 対応しない。綴りが一致する 4 件は偶然ではなく既定の name を kind から取っただけで、
// 一方の都合でもう一方を変えてよい関係ではない。
//
// 語彙は配備設定ではなく契約として pure core に置く。必須集合を Execution Policy 等の
// producer が作る artifact へ移すと、producer が自分に課される gate 条件を自分で緩められる。
type ArtifactName string

const (
	// ArtifactNameRawIssueBody は observation 時点の Issue body bytes である。
	// Issue Observation は body digest しか持たないため、これが無いと reviewer は
	// 観測が何を指していたかを再構成できない。
	ArtifactNameRawIssueBody ArtifactName = "raw-issue-body"
	// ArtifactNameIssueObservation は取得 identity と exact body digest の記録である。
	ArtifactNameIssueObservation ArtifactName = "issue-observation"
	// ArtifactNameTaskContext は model session へ渡す canonical Task Context である。
	ArtifactNameTaskContext ArtifactName = "task-context"
	// ArtifactNameContextManifest は解決済み実装入力の closure である。
	ArtifactNameContextManifest ArtifactName = "context-manifest"
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
)

// artifactNames は語彙の宣言順である。producer が名前を綴り直さずに参照できるよう公開する。
var artifactNames = []ArtifactName{
	ArtifactNameRawIssueBody,
	ArtifactNameIssueObservation,
	ArtifactNameTaskContext,
	ArtifactNameContextManifest,
	ArtifactNameTestPlan,
	ArtifactNameRedEvidence,
	ArtifactNameGreenEvidence,
	ArtifactNameCheckEvidence,
	ArtifactNameSourceBundle,
	ArtifactNameTestValidityResult,
	ArtifactNamePullRequestDraft,
}

// requiredOperationOutputs は kind の succeeded Result が固定していなければならない
// logical name である。protocol 文書の kind 表が Output として挙げる artifact のうち、
// head と external reference のように別 field が担うものを除いた集合と一致させる。
//
// create_pull_request が空なのは、この kind の成果が PR reference と観測した head であり、
// 新しい artifact を作らないためである。空は「検証しない」ではなく「要求が無い」を意味する。
var requiredOperationOutputs = map[OperationKind][]ArtifactName{
	OperationClaim: {
		ArtifactNameRawIssueBody,
		ArtifactNameIssueObservation,
		ArtifactNameTaskContext,
		ArtifactNameContextManifest,
	},
	OperationAuthorTests:          {ArtifactNameTestPlan, ArtifactNameRedEvidence, ArtifactNameSourceBundle},
	OperationReviseTests:          {ArtifactNameTestPlan, ArtifactNameRedEvidence, ArtifactNameSourceBundle},
	OperationImplement:            {ArtifactNameGreenEvidence, ArtifactNameCheckEvidence, ArtifactNamePullRequestDraft, ArtifactNameSourceBundle},
	OperationRepairImplementation: {ArtifactNameGreenEvidence, ArtifactNameCheckEvidence, ArtifactNamePullRequestDraft, ArtifactNameSourceBundle},
	OperationCreatePullRequest:    {},
}

// requiredReviewEntries は review kind の Artifact Manifest が備えていなければならない
// logical name である。protocol 文書が「最低限参照できるようにする」と定める集合のうち、
// 件数が入力ごとに変わる authority content を除いたものと一致させる。
//
// source-bundle は head を生成する Operation の必須 output である。model の成果ではないが、
// Issue Worker が checkpoint commit から固定しなければ、workspace を持たない Controller は
// review 開始時にこの entry を用意できない。
var requiredReviewEntries = map[ReviewKind][]ArtifactName{
	ReviewTestValidity: {
		ArtifactNameRawIssueBody,
		ArtifactNameIssueObservation,
		ArtifactNameTaskContext,
		ArtifactNameContextManifest,
		ArtifactNameTestPlan,
		ArtifactNameRedEvidence,
		ArtifactNameSourceBundle,
	},
	ReviewFinalImplementation: {
		ArtifactNameRawIssueBody,
		ArtifactNameIssueObservation,
		ArtifactNameTaskContext,
		ArtifactNameContextManifest,
		ArtifactNameTestPlan,
		ArtifactNameRedEvidence,
		ArtifactNameSourceBundle,
		ArtifactNameTestValidityResult,
		ArtifactNameGreenEvidence,
		ArtifactNameCheckEvidence,
		ArtifactNamePullRequestDraft,
	},
}

// 宣言漏れの kind は必須集合が nil になり、「要求が無い」と区別できないまま gate が
// 無効になる。create_pull_request のように空が正当な kind があるため、空を異常として
// 機械的に扱うこともできない。fail-open を検出可能に留めず、宣言矛盾を持つ binary が
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
