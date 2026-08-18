package contract

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// ExecutionPolicySchemaV1Alpha1 は Run 開始時に固定する execution policy schema である。
const ExecutionPolicySchemaV1Alpha1 = "kudo.execution-policy/v1alpha1"

// WorkerExecutionPolicy は一つの worker role の provider 実行境界を固定する。
// credential、secret path、session identity を表す field は持たない。
type WorkerExecutionPolicy struct {
	Provider        string
	Model           string
	Adapter         string
	AdapterVersion  string
	ToolPermissions []string
	Timeout         time.Duration
}

// ExecutionPolicy は Issue Worker と Review Worker の設定を一つの Run snapshot にする。
type ExecutionPolicy struct {
	Schema       string
	IssueWorker  WorkerExecutionPolicy
	ReviewWorker WorkerExecutionPolicy
}

// ExecutionPolicyRef は policy schema と canonical artifact digest の組である。
type ExecutionPolicyRef struct {
	Schema string
	Digest Digest
}

// EncodeExecutionPolicy は policy を検証し、canonical payload と ref を返す。
func EncodeExecutionPolicy(policy ExecutionPolicy) (ExecutionPolicyRef, ArtifactPayload, error) {
	if err := validateExecutionPolicy(policy); err != nil {
		return ExecutionPolicyRef{}, ArtifactPayload{}, err
	}
	data := encodeExecutionPolicy(policy)
	payload := newArtifactPayload(
		ArtifactKindExecutionPolicy,
		ExecutionPolicySchemaV1Alpha1,
		MediaTypeYAML,
		data,
	)
	return ExecutionPolicyRef{Schema: ExecutionPolicySchemaV1Alpha1, Digest: payload.Digest}, payload, nil
}

func validateExecutionPolicy(policy ExecutionPolicy) error {
	if policy.Schema != ExecutionPolicySchemaV1Alpha1 {
		return protocolErr(ProtocolSchemaUnknown, "schema",
			"execution policy schema は %q でなければならない: %q", ExecutionPolicySchemaV1Alpha1, policy.Schema)
	}
	if err := validateWorkerExecutionPolicy("issueWorker", policy.IssueWorker); err != nil {
		return err
	}
	return validateWorkerExecutionPolicy("reviewWorker", policy.ReviewWorker)
}

func validateWorkerExecutionPolicy(name string, policy WorkerExecutionPolicy) error {
	fields := []struct {
		name  string
		value string
	}{
		{"provider", policy.Provider},
		{"model", policy.Model},
		{"adapter", policy.Adapter},
		{"adapterVersion", policy.AdapterVersion},
	}
	// canonical な単一行の判定は validCanonicalLine に一本化する。以前はここで
	// utf8 妥当性と `\r\n\x00` の混入だけを個別に検査しており、共有述語より弱い規則が
	// この経路にだけ残っていた。上限や control character の規則を強化しても policy だけ
	// 古い規則のまま通る状態を作らないため、述語を重複させない。
	for _, field := range fields {
		if !validCanonicalLine(field.value, MaxCanonicalLineBytes) {
			return protocolErr(canonicalLineCode(field.value, MaxCanonicalLineBytes),
				name+"."+field.name, "空、canonical な単一行でない、または上限 %d byte を超えている", MaxCanonicalLineBytes)
		}
	}
	if policy.Timeout <= 0 {
		return protocolErr(ProtocolFieldInvalid, name+".timeout", "timeout は正でなければならない: %v", policy.Timeout)
	}
	seen := map[string]bool{}
	for i, permission := range policy.ToolPermissions {
		field := fmt.Sprintf("%s.toolPermissions[%d]", name, i)
		if !validCanonicalLine(permission, MaxCanonicalLineBytes) {
			return protocolErr(canonicalLineCode(permission, MaxCanonicalLineBytes), field,
				"空、canonical な単一行でない、または上限 %d byte を超えている", MaxCanonicalLineBytes)
		}
		if seen[permission] {
			return protocolErr(ProtocolFieldDuplicate, field, "tool permission が重複: %s", permission)
		}
		seen[permission] = true
	}
	return nil
}

func encodeExecutionPolicy(policy ExecutionPolicy) []byte {
	var b strings.Builder
	writeYAMLString(&b, 0, "schema", policy.Schema)
	b.WriteString("issueWorker:\n")
	encodeWorkerExecutionPolicy(&b, policy.IssueWorker)
	b.WriteString("reviewWorker:\n")
	encodeWorkerExecutionPolicy(&b, policy.ReviewWorker)
	return []byte(b.String())
}

func encodeWorkerExecutionPolicy(b *strings.Builder, policy WorkerExecutionPolicy) {
	writeYAMLString(b, 2, "provider", policy.Provider)
	writeYAMLString(b, 2, "model", policy.Model)
	writeYAMLString(b, 2, "adapter", policy.Adapter)
	writeYAMLString(b, 2, "adapterVersion", policy.AdapterVersion)
	permissions := append([]string(nil), policy.ToolPermissions...)
	slices.Sort(permissions)
	writeYAMLStringList(b, 2, "toolPermissions", permissions)
	writeYAMLString(b, 2, "timeout", policy.Timeout.String())
}

// ReadExecutionPolicyArtifact は ref/payload を照合して保存 bytes を返す。
func ReadExecutionPolicyArtifact(ref ExecutionPolicyRef, payload ArtifactPayload) ([]byte, error) {
	if !validSchemaIdentity(ref.Schema, executionPolicySchemaPrefix) {
		return nil, protocolSchemaErr("schema", ref.Schema, "ExecutionPolicyRef schema が不正: %q", ref.Schema)
	}
	return readVersionedArtifact(ArtifactKindExecutionPolicy, ref.Schema, ref.Digest, payload)
}
