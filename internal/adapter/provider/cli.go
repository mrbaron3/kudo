// Package provider は Agent Package を provider 固有 CLI へ渡す薄い launcher adapter を提供する。
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mrbaron3/kudo/internal/agentpackage"
	"github.com/mrbaron3/kudo/internal/contract"
)

var (
	ErrInvocationInvalid      = errors.New("provider invocation が不正")
	ErrPolicyMismatch         = errors.New("Execution Policy と adapter が不一致")
	ErrAdapterVersionMismatch = errors.New("provider adapter version が不一致")
	ErrProcessFailure         = errors.New("provider process が失敗")
)

type Invocation struct {
	Package          agentpackage.Package
	Request          []byte
	WorkingDirectory string
}

type Command struct {
	Executable  string
	Args        []string
	Environment []string
	Directory   string
	Stdin       []byte
}

type ProcessResult struct {
	Stdout    []byte
	Stderr    []byte
	ExitCode  int
	Truncated bool
}

type Runner interface {
	Run(context.Context, Command) (ProcessResult, error)
}

type Session interface {
	Path() string
	WriteFile(name string, data []byte) error
	Close() error
}

type SessionFactory interface {
	New(context.Context) (Session, error)
}

type cliFlavor struct {
	provider string
	adapter  string
	stateEnv string
}

var (
	codexFlavor  = cliFlavor{provider: "codex", adapter: "codex-cli", stateEnv: "CODEX_HOME"}
	claudeFlavor = cliFlavor{provider: "claude", adapter: "claude-cli", stateEnv: "CLAUDE_CONFIG_DIR"}
)

type CLIAdapter struct {
	flavor      cliFlavor
	executable  string
	runner      Runner
	sessions    SessionFactory
	environment []string
}

func NewCodexCLIAdapter(executable string, runner Runner, sessions SessionFactory, environment []string) *CLIAdapter {
	return newCLIAdapter(codexFlavor, executable, runner, sessions, environment)
}

func NewClaudeCLIAdapter(executable string, runner Runner, sessions SessionFactory, environment []string) *CLIAdapter {
	return newCLIAdapter(claudeFlavor, executable, runner, sessions, environment)
}

// Run は reviewagent.Provider を満たし、consumer 側の interface に provider 固有型を漏らさない。
func (a *CLIAdapter) Run(
	ctx context.Context,
	policy contract.WorkerExecutionPolicy,
	pkg agentpackage.Package,
	request []byte,
	workingDirectory string,
) ([]byte, error) {
	return a.Execute(ctx, policy, Invocation{
		Package: pkg, Request: request, WorkingDirectory: workingDirectory,
	})
}

func newCLIAdapter(flavor cliFlavor, executable string, runner Runner, sessions SessionFactory, environment []string) *CLIAdapter {
	return &CLIAdapter{
		flavor: flavor, executable: executable, runner: runner, sessions: sessions,
		environment: append([]string(nil), environment...),
	}
}

// Execute は一回の attempt ごとに新しい state directory と二つの新しい process
// （version probe、model invocation）を作る。session ID、resume token、host environment は渡さない。
func (a *CLIAdapter) Execute(ctx context.Context, policy contract.WorkerExecutionPolicy, invocation Invocation) ([]byte, error) {
	if err := a.validate(policy, invocation); err != nil {
		return nil, err
	}
	prompt, err := providerEnvelope(invocation)
	if err != nil {
		return nil, err
	}
	session, err := a.sessions.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("fresh provider session: %w", err)
	}
	output, executeErr := a.executeInSession(ctx, session, policy, invocation, prompt)
	closeErr := session.Close()
	if executeErr != nil {
		return nil, errors.Join(executeErr, closeErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("provider session cleanup: %w", closeErr)
	}
	return output, nil
}

func (a *CLIAdapter) validate(policy contract.WorkerExecutionPolicy, invocation Invocation) error {
	if a == nil || a.runner == nil || a.sessions == nil || !filepath.IsAbs(a.executable) {
		return fmt.Errorf("%w: executable、runner、session factory が必要", ErrInvocationInvalid)
	}
	if policy.Provider != a.flavor.provider || policy.Adapter != a.flavor.adapter {
		return fmt.Errorf("%w: got provider=%q adapter=%q, want provider=%q adapter=%q",
			ErrPolicyMismatch, policy.Provider, policy.Adapter, a.flavor.provider, a.flavor.adapter)
	}
	if strings.TrimSpace(policy.Model) == "" || strings.TrimSpace(policy.AdapterVersion) == "" || policy.Timeout <= 0 {
		return fmt.Errorf("%w: model、adapterVersion、timeout が必要", ErrPolicyMismatch)
	}
	if !filepath.IsAbs(invocation.WorkingDirectory) {
		return fmt.Errorf("%w: read-only checkout path が absolute でない", ErrInvocationInvalid)
	}
	if invocation.Package.Manifest.Operation == "" || invocation.Package.Ref.Schema != contract.AgentPackageSchemaV1Alpha1 {
		return fmt.Errorf("%w: Agent Package が不正", ErrInvocationInvalid)
	}
	if err := agentpackage.Validate(invocation.Package); err != nil {
		return fmt.Errorf("%w: Agent Package closure: %v", ErrInvocationInvalid, err)
	}
	permissions := make(map[string]bool, len(policy.ToolPermissions))
	for _, permission := range policy.ToolPermissions {
		permissions[permission] = true
	}
	for _, capability := range invocation.Package.ToolProfile.Capabilities {
		if !permissions[capability] {
			return fmt.Errorf("%w: package capability %q が Execution Policy で許可されていない",
				ErrPolicyMismatch, capability)
		}
	}
	if err := agentpackage.ValidateJSON(invocation.Package.InputSchema, invocation.Request); err != nil {
		return fmt.Errorf("%w: package input schema: %v", ErrInvocationInvalid, err)
	}
	for _, entry := range a.environment {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || name == "" || strings.ContainsAny(name, "\x00\r\n") ||
			name == "CODEX_HOME" || name == "CLAUDE_CONFIG_DIR" {
			return fmt.Errorf("%w: child environment key が不正: %q", ErrInvocationInvalid, name)
		}
	}
	return nil
}

func (a *CLIAdapter) executeInSession(
	ctx context.Context,
	session Session,
	policy contract.WorkerExecutionPolicy,
	invocation Invocation,
	prompt []byte,
) ([]byte, error) {
	operationCtx, cancel := context.WithTimeout(ctx, policy.Timeout)
	defer cancel()
	environment := append([]string(nil), a.environment...)
	environment = append(environment, a.flavor.stateEnv+"="+session.Path())
	if a.flavor == claudeFlavor {
		environment = append(environment, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
	}
	versionResult, err := a.runner.Run(operationCtx, Command{
		Executable: a.executable, Args: []string{"--version"}, Environment: environment,
		Directory: session.Path(),
	})
	if err != nil {
		return nil, fmt.Errorf("provider version probe: %w", err)
	}
	if versionResult.ExitCode != 0 || versionResult.Truncated {
		return nil, &ProcessError{Stage: "version", Result: versionResult}
	}
	actualVersion, err := parseVersion(a.flavor, versionResult.Stdout)
	if err != nil || actualVersion != policy.AdapterVersion {
		return nil, fmt.Errorf("%w: got %q, want %q", ErrAdapterVersionMismatch, actualVersion, policy.AdapterVersion)
	}

	if err := session.WriteFile("output.schema.json", invocation.Package.OutputSchema); err != nil {
		return nil, fmt.Errorf("output schema materialize: %w", err)
	}
	command := Command{
		Executable:  a.executable,
		Environment: environment,
		Directory:   invocation.WorkingDirectory,
		Stdin:       append([]byte(nil), prompt...),
	}
	if a.flavor == codexFlavor {
		command.Args = codexArgs(policy.Model, invocation.WorkingDirectory,
			filepath.Join(session.Path(), "output.schema.json"))
	} else {
		command.Args = claudeArgs(policy.Model, string(invocation.Package.OutputSchema), invocation.Package.ToolProfile)
	}
	result, err := a.runner.Run(operationCtx, command)
	if err != nil {
		return nil, fmt.Errorf("provider process: %w", err)
	}
	if result.ExitCode != 0 || result.Truncated {
		return nil, &ProcessError{Stage: "execute", Result: result}
	}
	if a.flavor == claudeFlavor {
		return extractClaudeStructuredOutput(result.Stdout)
	}
	return append([]byte(nil), result.Stdout...), nil
}

type ProcessError struct {
	Stage  string
	Result ProcessResult
}

func (e *ProcessError) Error() string {
	return fmt.Sprintf("%s: stage=%s exit=%d truncated=%t", ErrProcessFailure, e.Stage, e.Result.ExitCode, e.Result.Truncated)
}

func (e *ProcessError) Unwrap() error { return ErrProcessFailure }

func codexArgs(model, workDir, schemaPath string) []string {
	return []string{
		"exec", "--ephemeral", "--ignore-user-config", "--ignore-rules", "--strict-config",
		"-c", "project_doc_max_bytes=0", "--model", model, "--sandbox", "read-only",
		"--cd", workDir, "--output-schema", schemaPath, "-",
	}
}

func claudeArgs(model, outputSchema string, profile agentpackage.ToolProfile) []string {
	tools := ""
	if len(profile.Capabilities) > 0 {
		tools = "Read,Glob,Grep"
	}
	return []string{
		"--print", "--bare", "--no-session-persistence", "--disable-slash-commands",
		"--strict-mcp-config", "--mcp-config", "{}", "--permission-mode", "dontAsk",
		"--tools", tools, "--model", model, "--json-schema", outputSchema, "--output-format", "json", "-",
	}
}

func parseVersion(flavor cliFlavor, output []byte) (string, error) {
	value := strings.TrimSpace(string(output))
	if flavor == codexFlavor {
		value = strings.TrimPrefix(value, "codex-cli ")
	} else if before, _, ok := strings.Cut(value, " "); ok {
		value = before
	}
	if value == "" || strings.ContainsAny(value, "\r\n\t") {
		return "", errors.New("provider version output が不正")
	}
	return value, nil
}

type envelope struct {
	Schema       string                   `json:"schema"`
	AgentPackage contract.AgentPackageRef `json:"agentPackage"`
	Instructions string                   `json:"instructions"`
	Request      json.RawMessage          `json:"request"`
}

func providerEnvelope(invocation Invocation) ([]byte, error) {
	data, err := json.Marshal(envelope{
		Schema: "kudo.provider-envelope/v1alpha1", AgentPackage: invocation.Package.Ref,
		Instructions: string(invocation.Package.Instructions), Request: append(json.RawMessage(nil), invocation.Request...),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: provider envelope: %v", ErrInvocationInvalid, err)
	}
	return data, nil
}

func extractClaudeStructuredOutput(data []byte) ([]byte, error) {
	var result struct {
		StructuredOutput json.RawMessage `json:"structured_output"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("%w: Claude result envelope: %v", ErrProcessFailure, err)
	}
	if len(result.StructuredOutput) == 0 || string(result.StructuredOutput) == "null" {
		return nil, fmt.Errorf("%w: Claude structured_output が無い", ErrProcessFailure)
	}
	return append([]byte(nil), result.StructuredOutput...), nil
}
