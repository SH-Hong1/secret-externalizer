// Package scanexec runs an external scanner and captures the report it
// writes to standard output. A scanner's output can hold the plaintext values
// it found, so the output stays in memory, standard error is discarded, and
// no error from this package holds any of it.
package scanexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"
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

// errTooLarge stops the copy of standard output once the limit is passed.
var errTooLarge = errors.New("output too large")

// limitBuffer collects output up to max bytes. Past that it keeps nothing
// more, calls stop once, and fails every write.
type limitBuffer struct {
	max  int
	stop func()

	mu       sync.Mutex
	buf      bytes.Buffer
	exceeded bool
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.exceeded {
		return 0, errTooLarge
	}
	if b.buf.Len()+len(p) > b.max {
		b.exceeded = true
		b.buf = bytes.Buffer{}
		b.stop()
		return 0, errTooLarge
	}
	return b.buf.Write(p)
}

func (b *limitBuffer) result() (out []byte, exceeded bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Bytes(), b.exceeded
}

// Run runs c and returns what it wrote to standard output. The scanner runs
// in its own process group, and when ctx ends or the output passes the limit
// the whole group is killed.
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
	stdout := &limitBuffer{max: maxOutput, stop: cancel}
	cmd.Stdout = stdout
	cmd.Stderr = nil // the null device
	cmd.WaitDelay = waitDelay
	ownGroup(cmd)

	if err := cmd.Start(); err != nil {
		// Nothing has run yet, so the error holds no output, only the path.
		return nil, fmt.Errorf("starting %s: %w: %w", c.Tool, scan.ErrScannerFailed, err)
	}
	waitErr := cmd.Wait()
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		killGroup(cmd)
	}
	out, exceeded := stdout.result()

	if ctx.Err() != nil {
		return nil, fmt.Errorf("running %s: %w", c.Tool, ctx.Err())
	}
	if exceeded {
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
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		// The exit code was fine, but the output did not close in time or
		// could not be copied.
		return nil, fmt.Errorf("running %s: output did not close: %w", c.Tool, scan.ErrScannerFailed)
	}
	return out, nil
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
