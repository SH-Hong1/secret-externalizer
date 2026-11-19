//go:build unix

package scanexec_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/scanexec"
)

// The tests run this test binary as the fake scanner. Run with arguments
// "-test.run=^TestHelperProcess$ -- <mode> <args>" it acts out mode and exits
// without running any other test. Under the race detector a process that
// exits with status 0 waits a second before it ends, so the tests that need
// a quick successful run use /bin/echo and /usr/bin/env instead.

const leak = "dev-only-fake-leak"

func TestHelperProcess(*testing.T) {
	i := slices.Index(os.Args, "--")
	if i < 0 || i+1 >= len(os.Args) {
		return
	}
	mode, args := os.Args[i+1], os.Args[i+2:]
	switch mode {
	case "exit":
		fmt.Println(leak)
		fmt.Fprintln(os.Stderr, leak)
		code, err := strconv.Atoi(args[0])
		if err != nil {
			os.Exit(2)
		}
		os.Exit(code)
	case "flood":
		writePID(args[0])
		for {
			if _, err := fmt.Println(leak); err != nil {
				os.Exit(0)
			}
		}
	case "block":
		writePID(args[0])
		// Stand in for a scanner that never finishes. An empty select would
		// end the process as a deadlock.
		time.Sleep(time.Hour)
		os.Exit(0)
	case "orphan":
		child := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestHelperProcess$", "--", "block", args[0]) //nolint:gosec // G702: reruns this test binary with a path from the test
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		fmt.Println("partial")
		os.Exit(0)
	}
	os.Exit(2)
}

// writePID writes the process ID to path, which may be a FIFO the test is
// waiting on.
func writePID(path string) {
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil { //nolint:gosec // G703: the path is a temporary file of the test
		os.Exit(2)
	}
}

// command returns a Command that runs bin with args.
func command(t *testing.T, bin string, args ...string) scanexec.Command {
	t.Helper()
	return scanexec.Command{
		Tool: "fake",
		Bin:  bin,
		Args: args,
		Env:  scanexec.Env{Path: "/usr/bin:/bin", Home: t.TempDir(), TmpDir: t.TempDir()},
	}
}

// helper returns a Command that runs this test binary in the given mode.
func helper(t *testing.T, args ...string) scanexec.Command {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return scanexec.Command{
		Tool: "fake",
		Bin:  self,
		Args: append([]string{"-test.run=^TestHelperProcess$", "--"}, args...),
		Env:  scanexec.Env{Path: "/usr/bin:/bin", Home: t.TempDir(), TmpDir: t.TempDir()},
	}
}

// checkGone reports an error if a process with pid still exists.
func checkGone(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("process %d still exists after Run returned (kill -0: %v)", pid, err)
	}
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: the path is a temporary file of the test
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatalf("reading pid from %q: %v", b, err)
	}
	return pid
}

func checkNoLeak(t *testing.T, err error) {
	t.Helper()
	if msg := fmt.Sprintf("%+v %#v", err, err); strings.Contains(msg, leak) {
		t.Errorf("Run() error = %q, contains the scanner output", msg)
	}
}

func TestRun_Output(t *testing.T) {
	out, err := scanexec.Run(t.Context(), command(t, "/bin/echo", "a", "b"))
	if err != nil || string(out) != "a b\n" {
		t.Errorf("Run(echo a b) = %q, %v, want \"a b\"", out, err)
	}
}

func TestRun_NonZeroExit(t *testing.T) {
	for _, code := range []int{1, 3} {
		_, err := scanexec.Run(t.Context(), helper(t, "exit", strconv.Itoa(code)))
		if !errors.Is(err, scan.ErrScannerFailed) {
			t.Errorf("Run(exit %d) error = %v, want ErrScannerFailed", code, err)
			continue
		}
		checkNoLeak(t, err)
		if want := fmt.Sprintf("fake exited with code %d", code); !strings.Contains(err.Error(), want) {
			t.Errorf("Run(exit %d) error = %q, want it to contain %q", code, err, want)
		}
	}
}

func TestRun_OKCodes(t *testing.T) {
	c := helper(t, "exit", "3")
	c.OKCodes = []int{0, 3}
	out, err := scanexec.Run(t.Context(), c)
	if err != nil || strings.TrimSpace(string(out)) != leak {
		t.Errorf("Run(exit 3, OKCodes 0 and 3) = %q, %v, want the output and no error", out, err)
	}
}

func TestRun_OutputTooLarge(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	c := helper(t, "flood", pidFile)
	c.MaxOutput = 4096
	out, err := scanexec.Run(t.Context(), c)
	if !errors.Is(err, scan.ErrScannerFailed) || !strings.Contains(err.Error(), "output too large") {
		t.Fatalf("Run(flood) = %d bytes, %v, want ErrScannerFailed for output too large", len(out), err)
	}
	if out != nil {
		t.Errorf("Run(flood) returned %d bytes of output, want none", len(out))
	}
	checkNoLeak(t, err)
	checkGone(t, readPID(t, pidFile))
}

func TestRun_ContextCancelled(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "started")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := scanexec.Run(ctx, helper(t, "block", fifo))
		done <- err
	}()
	pid := readPID(t, fifo) // returns once the fake scanner is running
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run(block) after cancel error = %v, want context.Canceled", err)
	}
	checkGone(t, pid)
}

func TestRun_ChildHoldsOutput(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "child")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	c := helper(t, "orphan", fifo)
	c.WaitDelay = 100 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		_, err := scanexec.Run(t.Context(), c)
		done <- err
	}()
	child := readPID(t, fifo)
	err := <-done
	if !errors.Is(err, scan.ErrScannerFailed) {
		t.Errorf("Run(orphan) error = %v, want ErrScannerFailed", err)
	}
	// The child is not ours to reap, so whether it is gone cannot be checked
	// without waiting; kill it in case the process group kill missed it.
	_ = syscall.Kill(child, syscall.SIGKILL) // cleanup only; the child is normally gone already
}

func TestRun_Env(t *testing.T) {
	c := command(t, "/usr/bin/env")
	out, err := scanexec.Run(t.Context(), c)
	if err != nil {
		t.Fatalf("Run(env) error = %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(out)), "\n")
	slices.Sort(got)
	want := []string{"HOME=" + c.Env.Home, "PATH=/usr/bin:/bin", "TMPDIR=" + c.Env.TmpDir}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Run(env) environment mismatch (-want +got):\n%s", diff)
	}
}

func TestRun_StartFails(t *testing.T) {
	c := command(t, "/bin/echo")
	c.Bin = filepath.Join(t.TempDir(), "missing")
	if _, err := scanexec.Run(t.Context(), c); !errors.Is(err, scan.ErrScannerFailed) {
		t.Errorf("Run(missing binary) error = %v, want ErrScannerFailed", err)
	}
}

func TestVersion(t *testing.T) {
	tests := []struct {
		c       scanexec.Command
		want    string
		wantErr bool
	}{
		{command(t, "/bin/echo", "1.10.1"), "1.10.1", false},
		{command(t, "/bin/echo", "  2.0.0  "), "2.0.0", false},
		{command(t, "/bin/echo", ""), "", true},
		{command(t, "/bin/echo", "two words"), "", true},
		{command(t, "/bin/echo", strings.Repeat("9", 100)), "", true},
		{command(t, "/bin/echo", "1.0\x01"), "", true},
		{helper(t, "exit", "1"), "", true},
	}
	for _, tt := range tests {
		got, err := scanexec.Version(t.Context(), tt.c)
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("Version(%q) = %q, %v, want %q, error %v", tt.c.Args, got, err, tt.want, tt.wantErr)
		}
		if err != nil {
			checkNoLeak(t, err)
		}
	}
}
