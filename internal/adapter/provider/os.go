package provider

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const DefaultCaptureLimit = 4 << 20

type OSSessionFactory struct {
	Root string
}

func (f OSSessionFactory) New(context.Context) (Session, error) {
	root := f.Root
	if root == "" {
		root = os.TempDir()
	}
	path, err := os.MkdirTemp(root, "kudo-agent-session-")
	if err != nil {
		return nil, err
	}
	return &osSession{path: path}, nil
}

type osSession struct {
	path   string
	closed bool
}

func (s *osSession) Path() string { return s.path }

func (s *osSession) WriteFile(name string, data []byte) error {
	if s.closed || filepath.Base(name) != name || name == "." || name == "" {
		return fmt.Errorf("session file name が不正: %q", name)
	}
	return os.WriteFile(filepath.Join(s.path, name), data, 0o600)
}

func (s *osSession) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return os.RemoveAll(s.path)
}

type ExecRunner struct {
	CaptureLimit int
}

func (r ExecRunner) Run(ctx context.Context, command Command) (ProcessResult, error) {
	limit := r.CaptureLimit
	if limit <= 0 {
		limit = DefaultCaptureLimit
	}
	cmd := exec.CommandContext(ctx, command.Executable, command.Args...)
	cmd.Dir = command.Directory
	cmd.Env = append([]string(nil), command.Environment...)
	cmd.Stdin = bytes.NewReader(command.Stdin)
	stdout := &boundedBuffer{limit: limit}
	stderr := &boundedBuffer{limit: limit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	result := ProcessResult{
		Stdout: append([]byte(nil), stdout.Bytes()...), Stderr: append([]byte(nil), stderr.Bytes()...),
		Truncated: stdout.truncated || stderr.truncated,
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err == nil {
		return result, nil
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if _, ok := err.(*exec.ExitError); ok {
		return result, nil
	}
	return result, err
}

type boundedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.truncated = b.truncated || original > 0
		return original, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		b.truncated = true
	}
	_, _ = b.Buffer.Write(data)
	return original, nil
}
