# 개발 규칙과 검증 hook Implementation Plan (계획 1/7)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 이후 모든 코드가 따를 Go 규칙 문서와 린터 설정, 그리고 규칙 위반을 자동으로 막는 두 단계 hook을 갖춘 저장소 기반을 만든다.

**Architecture:** 규칙은 `AGENTS.md`(영문)에 두고 `CLAUDE.md`가 이를 불러온다. 포맷터와 린터는 버전을 고정해 `./bin`에 설치하고 `Makefile` 타깃으로만 실행한다. hook은 셸 스크립트 두 개(수정 직후 검사, 작업 종료 검사)와 종료 시점의 검토 에이전트 하나로 구성하며, 스크립트는 셸 테스트로 검증한다.

**Tech Stack:** Go 1.27, gofumpt, golangci-lint v2, GNU make, bash, jq, Claude Code hooks

**Spec:** `docs/superpowers/specs/2026-11-18-secret-externalizer-design.md` (9절 "개발 규칙과 검증", 10절 1단계)

## 전체 계획의 구성

설계 문서는 서로 독립적으로 동작을 확인할 수 있는 여러 부분으로 이루어져 있어, 계획을 일곱 개로 나눈다. 각 계획은 앞 계획이 끝난 뒤 그때의 코드를 보고 작성한다.

| 번호 | 계획 | 끝났을 때 확인할 수 있는 것 |
|---|---|---|
| 1 | 개발 규칙과 검증 hook (이 문서) | 규칙을 어긴 Go 파일을 쓰면 hook이 막는다 |
| 2 | 골격과 로그인 | compose로 띄워 Keycloak 로그인 후 역할별 빈 화면이 보인다 |
| 3 | 프로젝트 등록과 스캔 | 예제 소스를 스캔해 탐지 항목이 저장된다 |
| 4 | 화면 디자인 시안과 판정 화면 | 탐지 항목을 화면에서 판정한다 |
| 5 | 적용 | 소스가 치환되고 manifest와 OpenBao에 기록된다 |
| 6 | 리포트와 PDF | 스캔 리포트를 보고 PDF로 받는다 |
| 7 | 대시보드 | 통계와 추이가 보인다 |

## Global Constraints

- 규칙 파일(`AGENTS.md`, hook 프롬프트, 린터 설정의 주석)은 영문으로 쓴다.
- 사용자가 검토하는 문서(`CHANGELOG.md`, `docs/decisions/`, 계획, 설계)는 한글로 쓴다.
- 코드 주석은 최종 상태의 로직과 기능 설명만 쓴다. 의사결정과 수정 이력은 주석에 쓰지 않는다.
- 변경 내역은 `CHANGELOG.md`, 의사결정과 이유는 `docs/decisions/`에 한 건당 파일 하나로 쓴다.
- 포맷은 gofumpt를 따른다.
- 도구 버전은 `Makefile`에 고정한다. 실행 시점의 최신 안정 버전을 확인해 적는다.
- Go 모듈 경로는 `github.com/SH-Hong1/secret-externalizer`다.
- hook 스크립트는 실패 시 종료 코드 2와 함께 원인을 stderr로 출력한다. 검사 대상이 아니면 아무 출력 없이 0으로 끝난다.

## Review Focus

1. 공백이나 한글이 들어간 경로의 파일을 수정했을 때 hook이 경로를 잘못 쪼개지 않고 검사한다. (Task 3)
2. Go가 아닌 파일(`.md`, `.yml`, 템플릿)이나 저장소 밖의 파일을 수정했을 때 hook이 조용히 통과한다. (Task 3)
3. `./bin`에 도구가 없을 때 검사를 건너뛰고 통과하지 않는다. 설치 방법을 알리며 실패한다. (Task 3, Task 4)
4. 종료 hook이 실패를 알린 뒤 다시 호출되었을 때 무한히 반복하지 않는다. (Task 4)
5. `testdata/` 아래의 일부러 잘못 쓴 예제 파일은 린트 대상에서 빠진다. (Task 1, Task 3)

## File Structure

| 파일 | 책임 |
|---|---|
| `go.mod` | 모듈 정의, Go 버전 |
| `cmd/web/main.go` | web 바이너리 진입점. 지금은 `run` 골격만 둔다 |
| `cmd/web/main_test.go` | `run` 골격 테스트 |
| `Makefile` | 도구 설치와 `fmt`, `lint`, `vet`, `test`, `check` 타깃 |
| `.golangci.yml` | 린터와 포맷터 설정 |
| `AGENTS.md` | Go 규칙 (영문) |
| `CLAUDE.md` | `AGENTS.md`를 불러오는 한 줄 |
| `CHANGELOG.md` | 변경 내역 (한글) |
| `docs/decisions/README.md` | 결정 기록 작성 방법 (한글) |
| `docs/decisions/0001-*.md` ~ `0006-*.md` | 설계에서 확정된 결정 (한글) |
| `.claude/settings.json` | hook 등록 |
| `.claude/hooks/go-edit-check.sh` | 수정 직후 검사 |
| `.claude/hooks/stop-verify.sh` | 작업 종료 검사 |
| `.claude/hooks/review-prompt.md` | 종료 시 검토 에이전트의 지시문 (영문) |
| `.claude/hooks/hooks_test.sh` | 두 스크립트의 셸 테스트 |
| `.claude/hooks/testdata/` | 테스트용 Go 파일 |

---

### Task 1: 모듈, 도구 고정, 린터 설정

**Files:**
- Create: `go.mod`, `cmd/web/main.go`, `cmd/web/main_test.go`, `Makefile`, `.golangci.yml`
- Modify: `.gitignore`

**Interfaces:**
- Produces:
  - `make tools`: `./bin/gofumpt`, `./bin/golangci-lint` 설치
  - `make fmt`: 전체 포맷 적용
  - `make lint`: 전체 린트. 위반 시 0이 아닌 코드로 종료
  - `make vet`, `make test`
  - `make check`: `lint`, `vet`, `test`를 차례로 실행
  - `func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error` (`cmd/web/main.go`)

- [ ] **Step 1: `go mod init github.com/SH-Hong1/secret-externalizer` 실행, `go.mod`의 Go 버전을 `1.27`로 둔다**

- [ ] **Step 2: 실패하는 테스트 작성** (`cmd/web/main_test.go`)

```go
func TestRun_VersionFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"web", "-version"}, func(string) string { return "" }, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got := stdout.String(); !strings.HasPrefix(got, "secret-externalizer web ") {
		t.Errorf("stdout = %q, want prefix %q", got, "secret-externalizer web ")
	}
}

func TestRun_UnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"web", "-nope"}, func(string) string { return "" }, &stdout, &stderr)
	if err == nil {
		t.Fatal("run() error = nil, want error")
	}
}
```

- [ ] **Step 3: 실패 확인**

Run: `go test ./cmd/web/`
Expected: FAIL, `undefined: run`

- [ ] **Step 4: `cmd/web/main.go` 구현**

`main`은 `run`을 호출하고 오류가 있으면 stderr에 쓰고 `os.Exit(1)` 하는 것만 한다. `run`은 `flag.NewFlagSet(args[0], flag.ContinueOnError)`로 `-version`을 처리하고, 플래그 출력은 `stderr`로 보낸다. `-version`이 없으면 지금은 `nil`을 돌려준다. 버전 문자열은 패키지 변수 `version = "dev"`.

- [ ] **Step 5: 통과 확인**

Run: `go test ./cmd/web/`
Expected: PASS

- [ ] **Step 6: `Makefile` 작성**

- 변수: `GOFUMPT_VERSION`, `GOLANGCI_LINT_VERSION` (실행 시점의 최신 안정 버전으로 고정), `BIN := $(CURDIR)/bin`
- `tools`: `GOBIN=$(BIN) go install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)`, `GOBIN=$(BIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)`
- `fmt`: `$(BIN)/golangci-lint fmt ./...`
- `lint`: `$(BIN)/golangci-lint run ./...`
- `vet`: `go vet ./...`
- `test`: `go test -race ./...`
- `check`: `lint vet test`
- 모든 타깃을 `.PHONY`로 선언

- [ ] **Step 7: `.golangci.yml` 작성 (golangci-lint v2 형식)**

```yaml
version: "2"
linters:
  default: standard
  enable:
    - bodyclose
    - contextcheck
    - copyloopvar
    - errname
    - errorlint
    - exhaustive
    - gocritic
    - gosec
    - misspell
    - nilerr
    - noctx
    - revive
    - rowserrcheck
    - sloglint
    - sqlclosecheck
    - unconvert
    - unparam
    - usestdlibvars
  exclusions:
    paths:
      - testdata
formatters:
  enable:
    - gofumpt
    - goimports
  settings:
    gofumpt:
      extra-rules: true
    goimports:
      local-prefixes:
        - github.com/SH-Hong1/secret-externalizer
  exclusions:
    paths:
      - testdata
```

- [ ] **Step 8: `.gitignore`에 `bin/` 추가**

- [ ] **Step 9: 검증**

Run: `make tools && make check`
Expected: 린트 위반 0건, 테스트 PASS

- [ ] **Step 10: 린터가 실제로 잡는지 확인**

`cmd/web/main.go`에 `_ = os.Remove("x")` 대신 `os.Remove("x")` 한 줄을 임시로 넣는다.
Run: `make lint`
Expected: `errcheck` 위반으로 실패. 확인 후 그 줄을 지우고 `make lint`가 통과하는지 다시 확인한다.

- [ ] **Step 11: Commit**

```bash
git add go.mod cmd Makefile .golangci.yml .gitignore
git commit -m "chore: add module skeleton, pinned tools, and linter config"
```

---

### Task 2: 규칙 문서와 기록 문서

**Files:**
- Create: `AGENTS.md`, `CLAUDE.md`, `CHANGELOG.md`, `docs/decisions/README.md`, `docs/decisions/0001-web-worker-split.md`, `docs/decisions/0002-scanners-via-exec.md`, `docs/decisions/0003-openbao-holds-originals.md`, `docs/decisions/0004-no-ai-in-phase-one.md`, `docs/decisions/0005-keycloak-oidc-and-roles.md`, `docs/decisions/0006-pdf-in-separate-container.md`

**Interfaces:**
- Produces: `AGENTS.md`의 절 제목. Task 4의 검토 지시문이 이 제목들을 인용한다.
  `Project layout`, `Programs and wiring`, `HTTP`, `Errors`, `Interfaces and types`, `Context and concurrency`, `Data access`, `Logging`, `Security`, `Testing`, `Comments and records`, `Formatting and tooling`

- [ ] **Step 1: `AGENTS.md` 작성 (영문)**

절마다 아래 규칙을 명령문으로 쓴다. 각 규칙은 한두 문장이고, 필요한 곳에만 3~6줄의 예를 붙인다. 출처 문서 이름은 절 끝에 한 줄로 적는다.

- **Project layout**: `cmd/<binary>/main.go` per binary; all other code under `internal/`; no `pkg/`, `util`, `common`, `helpers`, or `models` packages; package names are short, lower-case, singular, and name what the package provides; no package-level mutable state; no `init()` except for registering with the standard library.
- **Programs and wiring**: `main` only calls `run(ctx, args, getenv, stdout, stderr) error`; read configuration once in `run` and pass it down as typed values; constructors take dependencies as explicit parameters; shut down on `SIGINT`/`SIGTERM` through context cancellation with a bounded grace period.
- **HTTP**: a `NewServer(deps...) http.Handler` constructor builds the whole handler; all routes are registered in one `routes.go` using `net/http` method-and-path patterns; a handler is a function that returns `http.Handler` and closes over its dependencies; request and response types live inside the handler that uses them; middleware has the shape `func(http.Handler) http.Handler`; templates are parsed once at start-up; handlers never write a partial response after an error.
- **Errors**: return errors, never panic for expected conditions; wrap with `fmt.Errorf("doing x: %w", err)` using lower-case messages without trailing punctuation; handle an error once (either log it or return it); compare with `errors.Is`/`errors.As`; sentinel errors are named `ErrX`, error types `XError`; never discard an error silently.
- **Interfaces and types**: define interfaces in the package that consumes them; keep them small; accept interfaces, return concrete types; do not create an interface before a second implementation or a test needs one; make the zero value useful; choose pointer or value receivers consistently per type; use named types for identifiers and enumerations instead of bare strings and ints.
- **Context and concurrency**: `ctx context.Context` is the first parameter and is never stored in a struct; every goroutine has an owner that waits for it and a way to stop; the sender closes a channel; prefer `errgroup` to hand-rolled wait groups with error channels; protect shared state with a mutex placed directly above the fields it guards; tests run with `-race`.
- **Data access**: SQL lives in one package per store; always pass `ctx`; always close rows and check `rows.Err()`; transactions are started and finished in the same function; no query built by string concatenation of input.
- **Logging**: use `log/slog` with constant messages and key-value attributes; pass the logger as a dependency; log at the edge of the program, not in library-like packages.
- **Security**: a secret value never appears in a log line, an error message, a template, a database column, or a test fixture committed to the repository; types that hold secret values implement `LogValue` and `String` to return a redacted form; all file paths that come from input are resolved and checked against the workspace root before use; external commands are run with `exec.CommandContext` and an argument slice, never through a shell.
- **Testing**: table-driven tests with `t.Run` subtests; standard library `testing` with `cmp.Diff` for structured comparison; failure messages follow `Func(input) = got, want want`; test through the public API of the package; use `t.Helper`, `t.Cleanup`, `t.TempDir`; no `time.Sleep` for synchronisation; fakes are hand-written and live next to the test.
- **Comments and records**: every exported identifier has a doc comment that begins with its name; comments describe what the code does and why the logic is the way it is, as it stands now; a comment never records a decision history, a previous implementation, a date, an author, or a ticket; changes go to `CHANGELOG.md`; decisions and their reasons go to `docs/decisions/`; documents under `docs/` and `CHANGELOG.md` are written in Korean, code and comments in English.
- **Formatting and tooling**: code is formatted by `make fmt`; `make check` must pass before a task is finished; lint findings are fixed, not suppressed; a `//nolint` directive names the linter and gives the reason on the same line.

- [ ] **Step 2: `CLAUDE.md` 작성**

내용은 다음 한 줄이다.

```
@AGENTS.md
```

- [ ] **Step 3: `CHANGELOG.md` 작성 (한글)**

Keep a Changelog 형식. 머리말 한 단락, `## [Unreleased]` 아래 `### 추가`에 이 계획에서 추가한 것(모듈 골격, 린터 설정, 규칙 문서, hook)을 한 줄씩 적는다.

- [ ] **Step 4: `docs/decisions/README.md` 작성 (한글)**

파일 이름 규칙(`NNNN-영문-요약.md`, 번호는 4자리 순번)과 본문 형식을 적는다. 본문은 `# 제목`, `- 날짜`, `- 상태(확정/대체됨)`, `## 배경`, `## 결정`, `## 이유`, `## 검토한 다른 방법` 순서다. 결정이 바뀌면 기존 파일을 고치지 않고 새 파일을 만들어 기존 파일의 상태를 "대체됨"으로 바꾼다고 적는다.

- [ ] **Step 5: 결정 기록 6건 작성 (한글)**

각 파일은 설계 문서의 해당 부분을 옮겨 README의 형식으로 쓴다. 날짜는 모두 2026-11-18, 상태는 확정.

| 파일 | 설계 문서의 출처 |
|---|---|
| `0001-web-worker-split.md` | 4절 전체 |
| `0002-scanners-via-exec.md` | 4절 C, 5절 "스캐너 호출", "도구별 역할", 2절의 trivy 제외 |
| `0003-openbao-holds-originals.md` | 6절 "탐지 항목이 갖는 정보", "OpenBao 사용 범위", 고객정보 비보관 |
| `0004-no-ai-in-phase-one.md` | 2절 비목표의 AI 항목, 6절 "판정"의 제안자 구조 |
| `0005-keycloak-oidc-and-roles.md` | 7절 (`user`, `admin`) |
| `0006-pdf-in-separate-container.md` | 8절 "리포트"의 PDF 부분 |

- [ ] **Step 6: 검증**

Run: `grep -c '^## ' AGENTS.md`
Expected: `12`

Run: `cat CLAUDE.md`
Expected: `@AGENTS.md`

Run: `ls docs/decisions | wc -l`
Expected: `7`

- [ ] **Step 7: Commit**

```bash
git add AGENTS.md CLAUDE.md CHANGELOG.md docs/decisions
git commit -m "docs: add Go rules, changelog, and decision records"
```

---

### Task 3: 수정 직후 검사 hook

**Files:**
- Create: `.claude/hooks/go-edit-check.sh`, `.claude/hooks/hooks_test.sh`, `.claude/hooks/testdata/unformatted.go.txt`, `.claude/hooks/testdata/errcheck.go.txt`, `.claude/hooks/testdata/clean.go.txt`, `.claude/settings.json`

**Interfaces:**
- Consumes: `./bin/gofumpt`, `./bin/golangci-lint` (Task 1)
- Produces:
  - `go-edit-check.sh`: stdin으로 hook JSON을 받는다. `.tool_input.file_path`를 읽는다. 종료 코드 0은 통과, 2는 위반(stderr에 원인).
  - `hooks_test.sh`: 인자 없이 실행. 모든 사례가 통과하면 `ok`를 출력하고 0으로 끝난다. Task 4가 여기에 사례를 추가한다.
  - 테스트 도우미 `run_edit_hook <file_path>`: 주어진 경로로 hook JSON을 만들어 `go-edit-check.sh`에 넘기고 종료 코드를 `$status`, stderr를 `$output`에 담는다.

테스트용 Go 파일은 `.go.txt`로 저장해 린트와 빌드 대상에서 빠지게 하고, 테스트가 임시 디렉터리에 `.go`로 복사해 쓴다.

- [ ] **Step 1: 테스트용 파일 작성**

- `unformatted.go.txt`: `package sample` 아래에 들여쓰기가 공백으로 된 함수 하나
- `errcheck.go.txt`: gofumpt 포맷은 맞지만 `os.Remove("x")`의 오류를 버리는 함수 하나
- `clean.go.txt`: 포맷과 린트를 모두 통과하는, 문서 주석이 있는 함수 하나

- [ ] **Step 2: 실패하는 테스트 작성** (`.claude/hooks/hooks_test.sh`)

각 사례는 저장소 안의 임시 패키지 디렉터리(`internal/hooktest_$$/`, 종료 시 `trap`으로 삭제)에 파일을 복사한 뒤 hook을 실행한다.

```bash
case_unformatted_go_fails() {
  cp "$TESTDATA/unformatted.go.txt" "$PKG/sample.go"
  run_edit_hook "$PKG/sample.go"
  assert_eq 2 "$status" "unformatted file"
  assert_contains "$output" "gofumpt"
}

case_lint_violation_fails() {
  cp "$TESTDATA/errcheck.go.txt" "$PKG/sample.go"
  run_edit_hook "$PKG/sample.go"
  assert_eq 2 "$status" "errcheck violation"
  assert_contains "$output" "errcheck"
}

case_clean_go_passes() {
  cp "$TESTDATA/clean.go.txt" "$PKG/sample.go"
  run_edit_hook "$PKG/sample.go"
  assert_eq 0 "$status" "clean file"
  assert_eq "" "$output" "clean file output"
}

case_non_go_file_passes_silently() {
  run_edit_hook "$ROOT/AGENTS.md"
  assert_eq 0 "$status" "markdown file"
  assert_eq "" "$output" "markdown file output"
}

case_path_with_space_and_hangul() {
  mkdir -p "$PKG/한글 폴더"
  cp "$TESTDATA/unformatted.go.txt" "$PKG/한글 폴더/sample.go"
  run_edit_hook "$PKG/한글 폴더/sample.go"
  assert_eq 2 "$status" "path with space and hangul"
}

case_file_outside_repo_passes() {
  cp "$TESTDATA/unformatted.go.txt" "$TMP_OUTSIDE/sample.go"
  run_edit_hook "$TMP_OUTSIDE/sample.go"
  assert_eq 0 "$status" "file outside repository"
}

case_testdata_file_passes() {
  mkdir -p "$PKG/testdata"
  cp "$TESTDATA/unformatted.go.txt" "$PKG/testdata/sample.go"
  run_edit_hook "$PKG/testdata/sample.go"
  assert_eq 0 "$status" "file under testdata"
}

case_missing_tools_fails_loudly() {
  cp "$TESTDATA/clean.go.txt" "$PKG/sample.go"
  HOOK_BIN_DIR="$TMP_OUTSIDE/empty" run_edit_hook "$PKG/sample.go"
  assert_eq 2 "$status" "missing tools"
  assert_contains "$output" "make tools"
}

case_deleted_file_passes() {
  run_edit_hook "$PKG/gone.go"
  assert_eq 0 "$status" "file that no longer exists"
}
```

- [ ] **Step 3: 실패 확인**

Run: `bash .claude/hooks/hooks_test.sh`
Expected: FAIL, `go-edit-check.sh`를 찾을 수 없음

- [ ] **Step 4: `.claude/hooks/go-edit-check.sh` 구현**

동작 순서는 다음과 같다. 변수는 모두 큰따옴표로 감싼다.

1. `jq -r '.tool_input.file_path // empty'`로 경로를 읽는다. 비어 있으면 0.
2. 확장자가 `.go`가 아니거나, 파일이 없거나, 경로가 `$CLAUDE_PROJECT_DIR`(없으면 `git rev-parse --show-toplevel`) 밖이거나, 경로에 `/testdata/` 구간이 있으면 0.
3. 도구 디렉터리는 `${HOOK_BIN_DIR:-$ROOT/bin}`. `gofumpt`나 `golangci-lint`가 없으면 `run 'make tools' first`를 stderr에 쓰고 2.
4. `gofumpt -extra -l <file>`의 출력이 있으면 `gofumpt: <file> is not formatted; run 'make fmt'`를 쓰고 2.
5. 저장소 루트에서 `golangci-lint run <파일이 속한 디렉터리의 상대 경로>`를 실행한다. 실패하면 그 출력을 stderr로 옮기고 2.
6. 그 밖에는 출력 없이 0.

- [ ] **Step 5: 통과 확인**

Run: `bash .claude/hooks/hooks_test.sh`
Expected: `ok`

- [ ] **Step 6: `.claude/settings.json`에 등록**

```json
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Edit|Write",
        "hooks": [
          {
            "type": "command",
            "command": "\"$CLAUDE_PROJECT_DIR\"/.claude/hooks/go-edit-check.sh",
            "timeout": 60
          }
        ]
      }
    ]
  }
}
```

- [ ] **Step 7: 실제 동작 확인**

Claude Code에서 `/hooks`로 등록 여부를 확인한다. 그다음 `internal/hooktry/x.go`에 오류를 버리는 코드를 Write 도구로 써 보고, hook의 지적이 대화에 돌아오는지 확인한다. 확인 후 `internal/hooktry/`를 지운다.

- [ ] **Step 8: Commit**

```bash
chmod +x .claude/hooks/go-edit-check.sh
git add .claude
git commit -m "chore: add per-edit Go format and lint hook"
```

---

### Task 4: 작업 종료 검사 hook과 규칙 검토

**Files:**
- Create: `.claude/hooks/stop-verify.sh`, `.claude/hooks/review-prompt.md`
- Modify: `.claude/hooks/hooks_test.sh`, `.claude/settings.json`, `CHANGELOG.md`

**Interfaces:**
- Consumes: `make check` (Task 1), `AGENTS.md`의 절 제목 (Task 2), `hooks_test.sh`의 `assert_eq`, `assert_contains` (Task 3)
- Produces:
  - `stop-verify.sh`: stdin으로 hook JSON을 받는다. 종료 코드 0은 통과, 2는 실패(stderr에 원인).
  - 테스트 도우미 `run_stop_hook <stop_hook_active: true|false>`

- [ ] **Step 1: 실패하는 테스트 추가** (`.claude/hooks/hooks_test.sh`)

`stop-verify.sh`는 실행할 명령을 `${HOOK_CHECK_CMD:-make check}`에서 읽는다. 테스트는 이 변수로 결과를 바꾼다.

```bash
case_stop_passes_when_check_passes() {
  HOOK_CHECK_CMD="true" run_stop_hook false
  assert_eq 0 "$status" "check passes"
}

case_stop_blocks_when_check_fails() {
  HOOK_CHECK_CMD="sh -c 'echo lint-broke; exit 1'" run_stop_hook false
  assert_eq 2 "$status" "check fails"
  assert_contains "$output" "lint-broke"
}

case_stop_does_not_loop() {
  HOOK_CHECK_CMD="sh -c 'exit 1'" run_stop_hook true
  assert_eq 0 "$status" "second stop after a block"
  assert_contains "$output" "still failing"
}

case_stop_skips_when_no_go_module_changes() {
  HOOK_CHECK_CMD="sh -c 'exit 1'" HOOK_CHANGED_FILES="docs/a.md" run_stop_hook false
  assert_eq 0 "$status" "only documents changed"
}
```

- [ ] **Step 2: 실패 확인**

Run: `bash .claude/hooks/hooks_test.sh`
Expected: FAIL, `stop-verify.sh`를 찾을 수 없음

- [ ] **Step 3: `.claude/hooks/stop-verify.sh` 구현**

1. 변경 파일 목록은 `${HOOK_CHANGED_FILES:-$(git status --porcelain | 경로만 추출)}`. 그 안에 `.go`, `go.mod`, `go.sum`, `.golangci.yml`, `Makefile`, `.tmpl`, `.html`이 하나도 없으면 0.
2. `${HOOK_CHECK_CMD:-make check}`를 저장소 루트에서 실행한다. 성공하면 0.
3. 실패했고 입력의 `.stop_hook_active`가 `true`이면, `checks are still failing; reporting instead of retrying`과 출력의 마지막 40줄을 stderr에 쓰고 0. 같은 실패로 무한히 되돌아가는 것을 막는다.
4. 실패했고 `stop_hook_active`가 `false`이면 출력의 마지막 40줄을 stderr에 쓰고 2.

- [ ] **Step 4: 통과 확인**

Run: `bash .claude/hooks/hooks_test.sh`
Expected: `ok`

- [ ] **Step 5: `.claude/hooks/review-prompt.md` 작성 (영문)**

검토 에이전트의 지시문이다. 다음 내용을 담는다.

- Run `git status --porcelain` and `git diff HEAD`. If no Go source, template, or migration file changed, approve at once.
- Read `AGENTS.md`. Review only the changed lines and the files they belong to.
- Check, by section name: `Project layout`, `Programs and wiring`, `HTTP`, `Errors`, `Interfaces and types`, `Context and concurrency`, `Data access`, `Logging`, `Security`, `Testing`, `Comments and records`.
- Do not report anything `make check` already enforces (formatting, lint findings).
- In `Comments and records`, flag any comment that records a decision, a previous implementation, a date, an author, or a ticket, and flag a behaviour change that has no `CHANGELOG.md` entry.
- In `Security`, flag any secret value that can reach a log line, error message, template, or database column.
- Report at most ten violations, each as `file:line — rule section — what to change`. Block only for violations of a rule written in `AGENTS.md`; do not block for preferences.
- If there are no violations, approve.

- [ ] **Step 6: `.claude/settings.json`에 Stop hook 추가**

`hooks`에 다음을 더한다. 먼저 Claude Code 공식 hooks 문서에서 `agent` 유형 hook의 필드 이름과 승인/차단 응답 형식을 확인하고, 다르면 문서를 따른다.

```json
"Stop": [
  {
    "hooks": [
      {
        "type": "command",
        "command": "\"$CLAUDE_PROJECT_DIR\"/.claude/hooks/stop-verify.sh",
        "timeout": 600
      },
      {
        "type": "agent",
        "prompt": "Follow the instructions in .claude/hooks/review-prompt.md and review the uncommitted changes in this repository.",
        "timeout": 300
      }
    ]
  }
]
```

- [ ] **Step 7: 실제 동작 확인**

Claude Code 세션에서 다음 세 가지를 확인한다. 각 확인 뒤 임시 파일을 지운다.

1. 테스트가 실패하는 Go 파일을 남긴 채 작업을 끝내려 하면, 실패 내용과 함께 작업이 이어진다.
2. `make check`는 통과하지만 `// 2026-11-18: changed from map to slice per review` 같은 주석이 있는 파일을 남기면, 검토 에이전트가 `Comments and records` 위반으로 막는다.
3. 문서만 고친 경우에는 두 hook이 모두 바로 통과한다.

- [ ] **Step 8: `CHANGELOG.md` 갱신**

`### 추가`에 수정 직후 검사와 작업 종료 검사 hook을 한 줄씩 적는다.

- [ ] **Step 9: Commit**

```bash
chmod +x .claude/hooks/stop-verify.sh
git add .claude CHANGELOG.md
git commit -m "chore: add stop-time verification hook and rules review"
```
