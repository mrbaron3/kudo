package provider

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mrbaron3/kudo/internal/agentpackage"
	"github.com/mrbaron3/kudo/internal/contract"
)

func TestCodexAndClaudeUseSamePackageRequestInFreshSessions(t *testing.T) {
	t.Parallel()

	pkg, request := testInvocation(t)
	wantOutput := []byte(`{"schema":"kudo.agent-output/test_validity/v1alpha1","verdict":"approve","findings":[]}`)
	tests := map[string]struct {
		newAdapter func(*fakeRunner, *fakeSessionFactory) *CLIAdapter
		policy     contract.WorkerExecutionPolicy
		processOut []byte
		wantArg    string
		stateEnv   string
	}{
		"codex": {
			newAdapter: func(runner *fakeRunner, sessions *fakeSessionFactory) *CLIAdapter {
				return NewCodexCLIAdapter("/opt/bin/codex", runner, sessions, []string{"OPENAI_API_KEY=test"})
			},
			policy: contract.WorkerExecutionPolicy{
				Provider: "codex", Model: "gpt-test", Adapter: "codex-cli", AdapterVersion: "1.2.3",
				ToolPermissions: []string{"repository:read"}, Timeout: time.Minute,
			},
			processOut: wantOutput, wantArg: "--output-schema", stateEnv: "CODEX_HOME=/session/",
		},
		"claude": {
			newAdapter: func(runner *fakeRunner, sessions *fakeSessionFactory) *CLIAdapter {
				return NewClaudeCLIAdapter("/opt/bin/claude", runner, sessions, []string{"ANTHROPIC_API_KEY=test"})
			},
			policy: contract.WorkerExecutionPolicy{
				Provider: "claude", Model: "claude-test", Adapter: "claude-cli", AdapterVersion: "2.3.4",
				ToolPermissions: []string{"repository:read"}, Timeout: time.Minute,
			},
			processOut: claudeResult(t, wantOutput), wantArg: "--json-schema", stateEnv: "CLAUDE_CONFIG_DIR=/session/",
		},
	}

	var providerPrompts [][]byte
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			runner := &fakeRunner{version: tt.policy.AdapterVersion, processOutput: tt.processOut}
			sessions := &fakeSessionFactory{}
			adapter := tt.newAdapter(runner, sessions)
			invocation := Invocation{Package: pkg, Request: request, WorkingDirectory: "/checkout/head"}

			first, err := adapter.Execute(t.Context(), tt.policy, invocation)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			second, err := adapter.Execute(t.Context(), tt.policy, invocation)
			if err != nil {
				t.Fatalf("second Execute() error = %v", err)
			}
			if string(first) != string(wantOutput) || string(second) != string(wantOutput) {
				t.Fatalf("outputs = %s / %s", first, second)
			}
			if len(sessions.created) != 2 || sessions.created[0] == sessions.created[1] || !sessions.allClosed() {
				t.Fatalf("fresh sessions = %#v", sessions)
			}
			if len(runner.commands) != 4 {
				t.Fatalf("commands = %d, want version+execute x2", len(runner.commands))
			}
			execute := runner.commands[1]
			if execute.Directory != "/checkout/head" || !contains(execute.Args, tt.wantArg) || contains(execute.Args, "resume") {
				t.Fatalf("execute command = %#v", execute)
			}
			if !containsPrefix(execute.Environment, tt.stateEnv) {
				t.Fatalf("fresh state env が無い: %v", execute.Environment)
			}
			if contains(execute.Environment, "UNRELATED_HOST_VALUE=leak") {
				t.Fatal("host environment が child へ混入した")
			}
			providerPrompts = append(providerPrompts, append([]byte(nil), execute.Stdin...))
		})
	}
	if len(providerPrompts) != 2 {
		t.Fatalf("provider prompts = %d, want 2", len(providerPrompts))
	}
	if string(providerPrompts[0]) != string(providerPrompts[1]) {
		t.Fatalf("provider ごとに package/request prompt が変化: %q / %q", providerPrompts[0], providerPrompts[1])
	}
}

func TestCLIAdapterSeparatesExecutionFailureFromQualityOutput(t *testing.T) {
	t.Parallel()

	policy := contract.WorkerExecutionPolicy{
		Provider: "codex", Model: "gpt-test", Adapter: "codex-cli", AdapterVersion: "1.2.3",
		ToolPermissions: []string{"repository:read"}, Timeout: time.Minute,
	}
	tests := map[string]struct {
		runner *fakeRunner
		mutate func(*contract.WorkerExecutionPolicy, *Invocation)
		want   error
	}{
		"version mismatch": {
			runner: &fakeRunner{version: "9.9.9"},
			want:   ErrAdapterVersionMismatch,
		},
		"non-zero exit": {
			runner: &fakeRunner{version: "1.2.3", exitCode: 17, stderr: []byte("provider failed")},
			want:   ErrProcessFailure,
		},
		"timeout": {
			runner: &fakeRunner{version: "1.2.3", runErr: context.DeadlineExceeded},
			want:   context.DeadlineExceeded,
		},
		"policy mismatch": {
			runner: &fakeRunner{version: "1.2.3"},
			mutate: func(policy *contract.WorkerExecutionPolicy, _ *Invocation) { policy.Provider = "claude" },
			want:   ErrPolicyMismatch,
		},
		"tool permission": {
			runner: &fakeRunner{version: "1.2.3"},
			mutate: func(policy *contract.WorkerExecutionPolicy, _ *Invocation) {
				policy.ToolPermissions = nil
			},
			want: ErrPolicyMismatch,
		},
		"request schema": {
			runner: &fakeRunner{version: "1.2.3"},
			mutate: func(_ *contract.WorkerExecutionPolicy, invocation *Invocation) {
				invocation.Request = []byte(`{"schema":"wrong"}`)
			},
			want: ErrInvocationInvalid,
		},
		"package bytes": {
			runner: &fakeRunner{version: "1.2.3"},
			mutate: func(_ *contract.WorkerExecutionPolicy, invocation *Invocation) {
				invocation.Package.Instructions[0] = 'X'
			},
			want: ErrInvocationInvalid,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			pkg, request := testInvocation(t)
			sessions := &fakeSessionFactory{}
			adapter := NewCodexCLIAdapter("/opt/bin/codex", tt.runner, sessions, nil)
			gotPolicy := policy
			invocation := Invocation{Package: pkg, Request: request, WorkingDirectory: "/checkout/head"}
			if tt.mutate != nil {
				tt.mutate(&gotPolicy, &invocation)
			}
			_, err := adapter.Execute(t.Context(), gotPolicy, invocation)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func testInvocation(t *testing.T) (agentpackage.Package, []byte) {
	t.Helper()
	pkg, err := agentpackage.Load(os.DirFS("../../.."), "agent-packages/test_validity/v1alpha1")
	if err != nil {
		t.Fatal(err)
	}
	return pkg, append([]byte(nil), pkg.Fixtures[0].Input...)
}

func claudeResult(t *testing.T, output []byte) []byte {
	t.Helper()
	if !json.Valid(output) {
		t.Fatal("test output が JSON でない")
	}
	return append(append([]byte(`{"type":"result","structured_output":`), output...), '}')
}

type fakeRunner struct {
	version       string
	processOutput []byte
	stderr        []byte
	exitCode      int
	runErr        error
	commands      []Command
}

func (f *fakeRunner) Run(_ context.Context, command Command) (ProcessResult, error) {
	command.Args = append([]string(nil), command.Args...)
	command.Environment = append([]string(nil), command.Environment...)
	command.Stdin = append([]byte(nil), command.Stdin...)
	f.commands = append(f.commands, command)
	if len(command.Args) == 1 && command.Args[0] == "--version" {
		prefix := "codex-cli "
		if strings.Contains(command.Executable, "claude") {
			prefix = ""
			return ProcessResult{Stdout: []byte(f.version + " (Claude Code)\n")}, nil
		}
		return ProcessResult{Stdout: []byte(prefix + f.version + "\n")}, nil
	}
	if f.runErr != nil {
		return ProcessResult{}, f.runErr
	}
	return ProcessResult{Stdout: append([]byte(nil), f.processOutput...), Stderr: append([]byte(nil), f.stderr...), ExitCode: f.exitCode}, nil
}

type fakeSessionFactory struct {
	created  []string
	sessions []*fakeSession
}

func (f *fakeSessionFactory) New(context.Context) (Session, error) {
	path := "/session/" + string(rune('1'+len(f.created)))
	session := &fakeSession{path: path, files: map[string][]byte{}}
	f.created = append(f.created, path)
	f.sessions = append(f.sessions, session)
	return session, nil
}

func (f *fakeSessionFactory) allClosed() bool {
	for _, session := range f.sessions {
		if !session.closed {
			return false
		}
	}
	return true
}

type fakeSession struct {
	path   string
	files  map[string][]byte
	closed bool
}

func (f *fakeSession) Path() string { return f.path }
func (f *fakeSession) WriteFile(name string, data []byte) error {
	f.files[name] = append([]byte(nil), data...)
	return nil
}
func (f *fakeSession) Close() error { f.closed = true; return nil }

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsPrefix(values []string, want string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, want) {
			return true
		}
	}
	return false
}
