package exec

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

func TestOSRunnerTimeoutKillsProcess(t *testing.T) {
	ctx := context.Background()
	result, err := OSRunner{}.Run(ctx, Command{
		Path:      os.Args[0],
		Args:      []string{"-test.run=TestOSRunnerHelperProcess"},
		Env:       append(os.Environ(), "ECHO_EXEC_HELPER_PROCESS=1"),
		Timeout:   200 * time.Millisecond,
		TermGrace: 20 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("OSRunner.Run returned nil error, want timeout")
	}
	if !result.TimedOut {
		t.Fatalf("TimedOut = false, want true; err=%v", err)
	}
	if result.ExitCode == 0 {
		t.Fatalf("ExitCode = 0, want non-zero for killed process; err=%v", err)
	}
}

func TestOSRunnerHelperProcess(t *testing.T) {
	if os.Getenv("ECHO_EXEC_HELPER_PROCESS") != "1" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	for {
		time.Sleep(time.Hour)
	}
}
