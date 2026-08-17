package contract

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
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
		return fmt.Errorf("execution policy schema は %q でなければならない", ExecutionPolicySchemaV1Alpha1)
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
	for _, field := range fields {
		if !utf8.ValidString(field.value) || strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s.%s が空または UTF-8 でない", name, field.name)
		}
		if strings.ContainsAny(field.value, "\r\n\x00") {
			return fmt.Errorf("%s.%s に改行または NUL は許可しない", name, field.name)
		}
	}
	if policy.Timeout <= 0 {
		return fmt.Errorf("%s.timeout は正でなければならない", name)
	}
	seen := map[string]bool{}
	for i, permission := range policy.ToolPermissions {
		if !utf8.ValidString(permission) || strings.TrimSpace(permission) == "" {
			return fmt.Errorf("%s.toolPermissions[%d] が空または UTF-8 でない", name, i)
		}
		if strings.ContainsAny(permission, "\r\n\x00") {
			return fmt.Errorf("%s.toolPermissions[%d] に改行または NUL は許可しない", name, i)
		}
		if seen[permission] {
			return fmt.Errorf("%s.toolPermissions[%d] が重複", name, i)
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
		return nil, fmt.Errorf("ExecutionPolicyRef schema が不正: %q", ref.Schema)
	}
	return readVersionedArtifact(ArtifactKindExecutionPolicy, ref.Schema, ref.Digest, payload)
}
