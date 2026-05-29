package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"syscall"
	"time"
)

const DefaultTermGrace = 30 * time.Second

type Command struct {
	Path      string
	Args      []string
	Dir       string
	Env       []string
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	Timeout   time.Duration
	TermGrace time.Duration
}

type Result struct {
	ExitCode int
	TimedOut bool
}

type Runner interface {
	Run(context.Context, Command) (Result, error)
}

type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, spec Command) (Result, error) {
	if spec.Path == "" {
		return Result{ExitCode: -1}, fmt.Errorf("exec path is empty")
	}

	runCtx := ctx
	cancel := func() {}
	if spec.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
	}
	defer cancel()

	cmd := osexec.CommandContext(runCtx, spec.Path, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.Stdin = spec.Stdin
	cmd.Stdout = spec.Stdout
	cmd.Stderr = spec.Stderr

	grace := spec.TermGrace
	if grace <= 0 {
		grace = DefaultTermGrace
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := cmd.Process.Signal(syscall.SIGTERM)
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	cmd.WaitDelay = grace

	err := cmd.Run()
	result := Result{TimedOut: errors.Is(runCtx.Err(), context.DeadlineExceeded)}
	if err == nil {
		return result, nil
	}

	result.ExitCode = -1
	var exitErr *osexec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	return result, err
}
