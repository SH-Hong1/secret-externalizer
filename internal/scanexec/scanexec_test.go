//go:build unix

package scanexec_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
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
		// orphan <fifo> <code> <stdout|none>: start a child that outlives
		// this process, holding standard output open or not, print
		// something, and exit with code.
		child := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestHelperProcess$", "--", "hold", args[0]) //nolint:gosec // G702: reruns this test binary with a path from the test
		if args[2] == "stdout" {
			child.Stdout = os.Stdout
		}
		// Wait until the child is running and known to the test before
		// exiting, since the scanner's exit kills the child's group.
		ready, readyW, err := os.Pipe()
		if err != nil {
			os.Exit(2)
		}
		child.ExtraFiles = []*os.File{readyW}
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		_ = readyW.Close() // the child has its own copy
		if _, err := ready.Read(make([]byte, 1)); err != nil {
			os.Exit(2)
		}
		code, err := strconv.Atoi(args[1])
		if err != nil {
			os.Exit(2)
		}
		fmt.Println("partial")
		os.Exit(code)
	case "hold":
		// hold <fifo>: write the process ID to the FIFO and keep it open
		// until the process dies, so the reader sees end of file only then;
		// then tell the parent through descriptor 3 that it is running.
		f, err := os.OpenFile(args[0], os.O_WRONLY, 0) //nolint:gosec // G703: the path is a FIFO of the test
		if err != nil {
			os.Exit(2)
		}
		if _, err := fmt.Fprintln(f, os.Getpid()); err != nil {
			os.Exit(2)
		}
		ready := os.NewFile(3, "ready") // from the orphan mode's ExtraFiles
		if _, err := ready.Write([]byte{1}); err != nil {
			os.Exit(2)
		}
		_ = ready.Close() // the parent has read the byte
		time.Sleep(time.Hour)
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

// childFIFO makes a FIFO for an orphan child of the fake scanner and
// returns its path and a function that reads the child's process ID from it.
// After that, gone reports whether the child has died, waiting at most ten
// seconds: the FIFO reaches end of file once the child's descriptors are
// closed by its death.
func childFIFO(t *testing.T) (fifo string, pid func() int, gone func() bool) {
	t.Helper()
	fifo = filepath.Join(t.TempDir(), "child")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	var r *bufio.Reader
	var f *os.File
	pid = func() int {
		var err error
		f, err = os.Open(fifo) //nolint:gosec // G304: the path is a temporary FIFO of the test
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() }) // read only
		r = bufio.NewReader(f)
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("reading the child's pid: %v", err)
		}
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			t.Fatalf("reading the child's pid from %q: %v", line, err)
		}
		t.Cleanup(func() { _ = syscall.Kill(n, syscall.SIGKILL) }) // in case the test failed; ESRCH is expected
		return n
	}
	gone = func() bool {
		eof := make(chan struct{})
		go func() {
			_, _ = io.Copy(io.Discard, r) // returns at end of file, or when the cleanup closes f
			close(eof)
		}()
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case <-eof:
			return true
		case <-timer.C:
			return false
		}
	}
	return fifo, pid, gone
}

func TestRun_ChildHoldsOutput(t *testing.T) {
	tests := []struct {
		name    string
		code    string
		okCodes []int
	}{
		{"Exit0", "0", nil},
		{"AcceptedExit3", "3", []int{0, 3}},
		{"FailingExit1", "1", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fifo, pid, gone := childFIFO(t)
			c := helper(t, "orphan", fifo, tt.code, "stdout")
			c.OKCodes = tt.okCodes
			c.WaitDelay = 100 * time.Millisecond
			done := make(chan error, 1)
			var out []byte
			go func() {
				var err error
				out, err = scanexec.Run(t.Context(), c)
				done <- err
			}()
			child := pid()
			err := <-done
			if !errors.Is(err, scan.ErrScannerFailed) || out != nil {
				t.Errorf("Run(orphan exiting %s) = %q, %v, want no output and ErrScannerFailed", tt.code, out, err)
			}
			if !gone() {
				t.Errorf("Run(orphan exiting %s) returned, but its child %d still runs", tt.code, child)
			}
		})
	}
}

func TestRun_ChildOutlivesScanner(t *testing.T) {
	fifo, pid, gone := childFIFO(t)
	c := helper(t, "orphan", fifo, "3", "none")
	c.OKCodes = []int{3}
	done := make(chan error, 1)
	var out []byte
	go func() {
		var err error
		out, err = scanexec.Run(t.Context(), c)
		done <- err
	}()
	child := pid()
	if err := <-done; err != nil || string(out) != "partial\n" {
		t.Errorf("Run(orphan without stdout) = %q, %v, want \"partial\\n\" and no error", out, err)
	}
	if !gone() {
		t.Errorf("Run(orphan without stdout) returned, but its child %d still runs", child)
	}
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

func TestRun_Dir(t *testing.T) {
	c := command(t, "/bin/pwd")
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c.Dir = dir
	out, err := scanexec.Run(t.Context(), c)
	if err != nil || strings.TrimSpace(string(out)) != dir {
		t.Errorf("Run(pwd in %s) = %q, %v, want %s", dir, out, err, dir)
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
