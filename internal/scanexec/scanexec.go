// Package scanexec runs an external scanner and captures the report it
// writes to standard output. A scanner's output can hold the plaintext values
// it found, so the output stays in memory, standard error is discarded, and
// no error from this package holds any of it.
package scanexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// DefaultMaxOutput is the most standard output a scanner may write, 256 MiB.
// A scanner that writes more is stopped and the run fails.
const DefaultMaxOutput = 256 << 20

// defaultWaitDelay is how long Run waits, once the scanner has exited or been
// killed, for anything else that still holds its standard output.
const defaultWaitDelay = 5 * time.Second

// maxVersion is the longest version string Version accepts.
const maxVersion = 64

// Env is the whole environment a scanner runs with. Home and TmpDir should be
// on a writable temporary file system: the scanners keep caches there.
type Env struct {
	Path, Home, TmpDir string
}

// Command is one run of a scanner.
type Command struct {
	// Tool names the scanner in errors.
	Tool string
	// Bin is the path of the executable.
	Bin string
	// Args are the arguments, passed to the executable without a shell.
	Args []string
	// Dir is the working directory. Empty means the caller's.
	Dir string
	// Env is the environment; nothing else is passed.
	Env Env
	// OKCodes are the exit codes of a successful run. Empty means only 0.
	OKCodes []int
	// MaxOutput is the most standard output accepted, in bytes. Zero means
	// DefaultMaxOutput.
	MaxOutput int
	// WaitDelay bounds the wait for standard output to close once the
	// scanner has exited. Zero means five seconds.
	WaitDelay time.Duration
}

// output is what was read from a scanner's standard output.
type output struct {
	data     []byte
	exceeded bool
}

// readOutput reads r until end of file or until more than max bytes have
// come, and then calls stop and keeps none of it.
func readOutput(r io.Reader, maxOutput int, stop func()) output {
	data, err := io.ReadAll(io.LimitReader(r, int64(maxOutput)+1))
	if len(data) > maxOutput {
		stop()
		return output{exceeded: true}
	}
	if err != nil {
		// Only a pipe that Run closed itself fails to read; Run already
		// treats that run as failed.
		return output{}
	}
	return output{data: data}
}

// Run runs c and returns what it wrote to standard output. The scanner runs
// in its own process group. When ctx ends or the output passes the limit the
// whole group is killed, and once the scanner has exited whatever is left
// of its group is killed too, so no process of the scanner outlives Run.
//
// The run fails when standard output is still open WaitDelay after the
// scanner has exited, because a process it left behind holds it; the output
// read until then is not returned.
//
// When ctx ends Run returns an error that wraps ctx.Err(). Every other
// failure wraps scan.ErrScannerFailed and says only the tool, and its exit
// code or a fixed reason. A scanner that cannot be started also gets the
// error from starting it, which names the executable.
func Run(ctx context.Context, c Command) ([]byte, error) {
	maxOutput := c.MaxOutput
	if maxOutput <= 0 {
		maxOutput = DefaultMaxOutput
	}
	waitDelay := c.WaitDelay
	if waitDelay <= 0 {
		waitDelay = defaultWaitDelay
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, c.Bin, c.Args...)
	cmd.Env = []string{"PATH=" + c.Env.Path, "HOME=" + c.Env.Home, "TMPDIR=" + c.Env.TmpDir}
	cmd.Dir = c.Dir
	cmd.Stderr = nil // the null device
	cmd.WaitDelay = waitDelay
	ownGroup(cmd)

	// The pipe is made here rather than by exec, so that Run sees whether
	// standard output reached end of file: exec hides a forced close when the
	// exit code is not 0.
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("starting %s: %w: %w", c.Tool, scan.ErrScannerFailed, err)
	}
	defer func() {
		_ = pr.Close() // a second close after a forced one fails harmlessly
	}()
	cmd.Stdout = pw
	startErr := cmd.Start()
	_ = pw.Close() // the child has its own copy; closing ours lets end of file arrive
	if startErr != nil {
		// Nothing has run yet, so the error holds no output, only the path.
		return nil, fmt.Errorf("starting %s: %w: %w", c.Tool, scan.ErrScannerFailed, startErr)
	}
	read := make(chan output, 1)
	go func() { read <- readOutput(pr, maxOutput, cancel) }()

	waitErr := cmd.Wait()
	timer := time.NewTimer(waitDelay)
	defer timer.Stop()
	var out output
	forced := false
	select {
	case out = <-read:
	case <-timer.C:
		forced = true
	}
	killGroup(cmd)
	if forced {
		_ = pr.Close() // ends the read of a pipe that a process outside the group may still hold
		<-read
	}

	if ctx.Err() != nil {
		return nil, fmt.Errorf("running %s: %w", c.Tool, ctx.Err())
	}
	if out.exceeded {
		return nil, fmt.Errorf("running %s: output too large: %w", c.Tool, scan.ErrScannerFailed)
	}
	code := cmd.ProcessState.ExitCode()
	if code < 0 {
		return nil, fmt.Errorf("running %s: killed by a signal: %w", c.Tool, scan.ErrScannerFailed)
	}
	okCodes := c.OKCodes
	if len(okCodes) == 0 {
		okCodes = []int{0}
	}
	if !slices.Contains(okCodes, code) {
		return nil, fmt.Errorf("%s exited with code %d: %w", c.Tool, code, scan.ErrScannerFailed)
	}
	var exitErr *exec.ExitError
	if forced || (waitErr != nil && !errors.As(waitErr, &exitErr)) {
		return nil, fmt.Errorf("running %s: output did not close: %w", c.Tool, scan.ErrScannerFailed)
	}
	return out.data, nil
}

// Version runs c, which asks a scanner for its version, and returns the
// version it printed. The output must be a single word of printable ASCII.
func Version(ctx context.Context, c Command) (string, error) {
	c.MaxOutput = maxVersion + 2
	out, err := Run(ctx, c)
	if err != nil {
		return "", fmt.Errorf("reading the version: %w", err)
	}
	v := strings.TrimSpace(string(out))
	if v == "" || len(v) > maxVersion || strings.IndexFunc(v, func(r rune) bool { return r <= ' ' || r > '~' }) >= 0 {
		return "", fmt.Errorf("%s printed no version: %w", c.Tool, scan.ErrScannerFailed)
	}
	return v, nil
}
