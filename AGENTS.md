# Go rules for secret-externalizer

These rules apply to every change in this repository. They are checked by
`make check`, by the hooks in `.claude/hooks/`, and by review. Where two
well-known guides disagree, the rule written here is the one this project
follows.

The design lives in `docs/superpowers/specs/`. Decisions and their reasons
live in `docs/decisions/`. Changes are listed in `CHANGELOG.md`.

## Project layout

- Each binary has one entry point at `cmd/<binary>/main.go`. Today those are
  `cmd/web` and `cmd/worker`.
- All other code lives under `internal/`. Do not create a `pkg/` directory.
- Name a package after what it provides: `scan`, `finding`, `manifest`. Do not
  create `util`, `common`, `helpers`, `base`, or `models` packages.
- Directory names are lower-case ASCII with no spaces. Go cannot import a
  package whose path has other characters, so such a directory can never pass
  the linters.
- Package names are short, lower-case, and singular, with no underscores.
  Do not repeat the package name in its identifiers: `scan.Result`, not
  `scan.ScanResult`.
- Group code by what it does, not by technical layer. Files that change
  together live in the same package.
- Do not keep mutable state in package-level variables. Pass state through
  constructors. The one exception is a build identifier that is set at link
  time with `-ldflags -X` and that nothing assigns while the program runs.
- Do not write `init()` functions, except to register with a standard library
  registry such as `database/sql`.

Sources: Organizing a Go module; Practical Go; Go best practices, six years in.

## Programs and wiring

- `main` does one thing: it calls `run` and exits non-zero on error.

  ```go
  func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error
  ```

- Only `main` calls `os.Exit` or `log.Fatal`. Everything below it returns an
  error.
- Read configuration once, in `run`, from `args` and `getenv`. Pass it down as
  typed values. No other package reads the environment.
- A constructor takes its dependencies as explicit parameters and returns a
  ready-to-use value. Do not use functional options or dependency containers
  until a constructor has more than a few optional settings.
- Shut down through context cancellation. `run` cancels its context on
  `SIGINT` and `SIGTERM`, and each long-running component stops within a
  bounded grace period.

Sources: How I write HTTP services in Go after 13 years; Let's Go.

## HTTP

- One constructor, `NewServer(deps...) http.Handler`, builds the whole handler
  including middleware. Tests call it the same way `run` does.
- Register every route in one file, `routes.go`, using the `net/http`
  method-and-path patterns (`"GET /projects/{id}"`). The route table is read in
  one place.
- A handler is a function that returns `http.Handler` and closes over its
  dependencies. Work that is done once, such as preparing a template, happens
  in the outer function.

  ```go
  func handleProjectList(logger *slog.Logger, projects ProjectLister) http.Handler {
  	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  		// ...
  	})
  }
  ```

- Declare request and response types inside the handler that uses them, unless
  two handlers share them.
- Middleware has the shape `func(http.Handler) http.Handler`.
- Parse templates once at start-up. A missing or broken template stops the
  program from starting.
- Render into a buffer and write it only after rendering succeeds. Never send
  a partial page followed by an error.
- A request that changes state uses `POST`, `PUT`, `PATCH`, or `DELETE`, and
  is covered by CSRF protection.
- Set `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, and `IdleTimeout` on
  every `http.Server`. Give every `http.Client` a timeout.

Sources: How I write HTTP services in Go after 13 years; Let's Go; 100 Go Mistakes.

## Errors

- Return an error for every condition a caller can meet. Use `panic` only for
  a broken invariant that means the program itself is wrong.
- Add context when returning an error from another package:
  `fmt.Errorf("opening manifest: %w", err)`. Messages are lower-case, have no
  trailing punctuation, and say what was being attempted, not "failed to".
- Handle an error once. Either log it or return it, never both.
- Compare with `errors.Is` and `errors.As`, never with `==` on a wrapped error
  and never by matching message text.
- Name sentinel errors `ErrNotFound` and error types `ValidationError`.
- Never discard an error. If ignoring it is correct, assign it to `_` and
  say why in a comment on that line.
- Check errors from `Close` on anything that was written to.
- Keep the successful path at the left margin. Return early on error and do
  not write `else` after a `return`.

Sources: Go Code Review Comments; Effective Go; Uber Go Style Guide; 100 Go Mistakes.

## Interfaces and types

- Define an interface in the package that uses it, not in the package that
  implements it.
- Keep interfaces small. One or two methods is normal.
- Accept interfaces and return concrete types. The exception is HTTP:
  `NewServer`, handlers, and middleware return `http.Handler`, because that
  interface is what `net/http` composes.
- Do not write an interface until a second implementation or a test needs it.
- Make the zero value useful, or make the type impossible to build without
  its constructor.
- Choose pointer or value receivers per type and keep to that choice for all
  of its methods. Use pointer receivers when a method changes the value or the
  value holds a mutex.
- Use named types for identifiers and enumerations: `type ProjectID int64`,
  `type Verdict string`. Enumerations that start from `iota` start at one, so
  the zero value means "not set".
- Do not embed a type in an exported struct only to save typing. Embedding
  exports the embedded type's whole method set.
- A function that takes a slice or map and keeps it makes a copy first. A
  method that returns internal slice or map state returns a copy.
- Use initialisms consistently: `ID`, `URL`, `HTTP`, `PDF`, not `Id` or `Url`.
- Receiver names are one or two letters taken from the type name. Do not use
  `this` or `self`.

Sources: Go Code Review Comments; Google Go Style Guide; Uber Go Style Guide; Practical Go.

## Context and concurrency

- `ctx context.Context` is the first parameter of any function that uses the
  network, runs an external command, or can block for an unbounded time. A
  function that only reads local files of bounded size does not take one,
  because the read cannot be interrupted through a context. Do not store a
  context in a struct.
- Use context values only for request-scoped data that crosses API
  boundaries, with an unexported key type.
- Every goroutine has an owner. The owner knows how the goroutine stops and
  waits for it before returning.
- Prefer synchronous functions. Let the caller decide whether to run one in a
  goroutine.
- The sender closes a channel. A receiver never does.
- Use `errgroup.Group` for a group of goroutines that can fail, rather than a
  `sync.WaitGroup` paired with an error channel.
- Put a mutex directly above the fields it guards and leave a blank line after
  that group. Do not copy a struct that contains a mutex.
- Do not use `time.After` inside a loop. Use a `time.Timer` and reset it.
- Tests run with `-race`.

Sources: 100 Go Mistakes; Practical Go; Go Code Review Comments.

## Data access

- SQL for one store lives in one package. Other packages call that package's
  methods and never see SQL.
- Every query takes `ctx`.
- Close every `Rows` value and check `rows.Err()` after the loop.
- Start and finish a transaction in the same function. Roll back in a
  deferred call that is safe to run after a commit.
- Pass input to a query as parameters. Never build SQL by joining strings
  that contain input.
- Schema changes are migration files. Never change a migration that has been
  merged; add a new one.

Sources: Let's Go; 100 Go Mistakes.

## Logging

- Use `log/slog`. The message is a constant string, and everything variable
  is an attribute: `logger.InfoContext(ctx, "scan finished", "project_id", id)`.
- Pass the logger as a dependency. Do not call the global logger outside
  `main`.
- Log where an error is finally handled, which is normally the HTTP handler or
  the job loop. Packages below that return errors and do not log.
- Attribute keys are lower-case with underscores.

Sources: Let's Go; Practical Go.

## Security

- A secret value never appears in a log line, an error message, a template, a
  database column, a report, the manifest, or a session. Two derived forms
  are allowed in the database and on a page, and nowhere else:
  - the fingerprint, an HMAC of the value computed by OpenBao;
  - the preview built by `finding.Preview`: the first four characters of a
    secret that is at least eight characters long, followed by a mask. A
    shorter secret, a private key, and a key file show only the mask.

  The value itself is written in one place: the response to an `admin`'s
  request to reveal an original, which reads it from OpenBao for that request,
  keeps no copy, and records the request in the audit trail before reading.
  A report and its PDF carry neither the fingerprint nor the preview.
- A type that holds a secret value implements `LogValue() slog.Value` and
  `String() string`, and both return a redacted form.
- Customer data found in scanned files is never copied anywhere. Store only
  its kind, its location, a count, its fingerprint, and the preview built by
  `finding.Preview`, which for customer data is a fixed format string, except
  that a phone number keeps at most the first three digits of its leading
  group. Its original value is not stored in OpenBao, and no context lines
  are stored for a file that holds it.
- The one copy of scanned file content the tool makes is the UTF-8 copy of an
  EUC-KR file, which the scanners cannot read otherwise. It is written under
  the worker's temporary folder for one scan and removed when that scan
  ends.
- The worker changes a file of a project only while it carries out an apply
  that a user confirmed, and only at the places of bundles whose verdict is
  `externalize` or `redact`. A value leaves the source only after the worker
  has read it back from OpenBao and its fingerprint matched. A whole file is
  stored and removed only if it cannot hold customer data. The changed file
  is written through a temporary file in the same folder, which holds no
  more than the file did, and the tool never runs a git command that
  changes the repository.
- Do not commit a real secret as a test fixture. Fixtures use values that are
  obviously fake.
- Resolve every file path that comes from input, including symbolic links,
  and confirm it is inside the workspace root before using it.
- Run external commands with `exec.CommandContext` and an argument slice.
  Never pass a command line through a shell.
- Use `crypto/rand` for anything that must be unpredictable, and
  `subtle.ConstantTimeCompare` to compare secrets.
- Session cookies are `HttpOnly`, `Secure`, and `SameSite=Lax`, and hold only
  a session identifier.
- `html/template` does the escaping. Do not convert input to
  `template.HTML`, `template.JS`, or `template.URL`.

Sources: Let's Go; the project design, section 2 "failure conditions".

## Testing

- Write the test first and watch it fail before writing the code.
- Use table-driven tests with `t.Run` subtests when cases differ only in data.
- Use the standard `testing` package. Compare structured values with
  `cmp.Diff`. Do not add an assertion library.
- A failure message names the function, its input, what it returned, and what
  was wanted: `Parse(%q) = %v, want %v`. Use `t.Errorf` to keep going and
  `t.Fatalf` only when continuing makes no sense.
- Test through a package's exported API. Put tests in the `_test` package
  unless they need unexported access.
- Use `t.Helper` in helpers, `t.Cleanup` for teardown, `t.TempDir` for files,
  and `t.Context` for a context.
- Do not call `time.Sleep` to wait for something. Wait on a channel or inject
  a clock.
- Write fakes by hand and keep them beside the test that uses them. Do not
  generate mocks.
- Files that a test reads live in a `testdata` directory.
- A test that needs a running service checks for it and calls `t.Skip` with
  the reason when it is absent.

Sources: Go Code Review Comments; Google Go Style Guide; Let's Go; 100 Go Mistakes.

## Comments and records

- Every exported identifier has a doc comment that is a full sentence
  beginning with its name. Every package has one package comment.
- A comment says what the code does, or why the logic has to be the way it
  is, as the code stands now.
- A comment never records a decision history, a previous implementation, a
  date, an author, a review remark, or a ticket number. Do not write
  "changed from", "previously", "as discussed", or "per review".
- Do not leave commented-out code.
- A `TODO` is allowed only with a concrete condition for removing it.
- Every change in behaviour gets a line in `CHANGELOG.md` under
  `[Unreleased]`.
- Every decision that a later reader could reasonably question gets a file in
  `docs/decisions/`, following `docs/decisions/README.md`.
- Code, comments, commit messages, and this file are written in English.
  `CHANGELOG.md` and everything under `docs/` are written in Korean.

Sources: Effective Go; Go Code Review Comments; Practical Go.

## Formatting and tooling

- Format with `make fmt`, which runs gofumpt and goimports. Do not format by
  hand and do not argue with the formatter.
- `make check` runs the linters, `go vet`, the tests with the race detector,
  the hook script tests, the scanner fetch-script test
  (`make scanners-test`), and the deploy script tests
  (`make deploy-scripts-test`). It must pass before a task is finished.
- Fix a lint finding. Do not suppress it. Where suppression really is correct,
  write `//nolint:<linter> // <reason>` on that line.
- `gosec` rule G204 is turned off in `.golangci.yml`. It reports every
  `exec.CommandContext` call whose arguments are not constants, which is the
  form the Security section requires.
- Tool versions are pinned in `Makefile`, and the scanner versions and their
  checksums in `deploy/worker/scanners.lock`. Change them there and nowhere
  else.
- Add a dependency only when the standard library cannot reasonably do the
  job, and record the reason in `docs/decisions/`.

Sources: gofumpt; Go best practices, six years in.
