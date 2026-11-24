# 개발 방식 정비 구현 계획

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**목표:** 검증을 Claude Code hook에서 git hook과 CI(AI 규칙 검토 포함)로 옮기고, 문서는 Design Doc과 ADR로, 일은 GitHub Milestone, Issue, Project로 관리하며, CI를 통과한 PR이 자동으로 병합되게 한다.

**구조:** app-surveyor의 `.githooks/`, `scripts/commit-lint.sh`, `.github/`를 가져와 이 저장소에 맞춘다. AI 검토는 `anthropics/claude-code-action`이 PR diff를 읽고 위반 JSON을 내면, 셸 스크립트 `scripts/review-verdict.sh`가 그 JSON과 `review: override` 라벨로 체크의 성패를 정한다. 문서 이동은 `git mv`이고, 남은 일은 `gh`로 Issue를 만든다.

**도구:** POSIX sh, bash, jq, GitHub Actions, `gh` CLI, docker compose

**설계:** `docs/superpowers/specs/2026-11-25-dev-workflow-design.md`

## 전체 제약

- 커밋 제목과 PR 제목은 영어이고 `<type>(<scope>): <subject>`, 72자 이하, 소문자로 시작, 마침표로 끝나지 않는다. 타입: `build ci docs feat fix perf refactor revert style test`.
- 커밋 본문에 `Refs #N`이나 `Closes #N` 줄이 있다. PR 본문에 `Closes #N`이 있다. PR 본문, `docs/`, `CHANGELOG.md`, `README.md`는 한국어다.
- task 하나 = Issue 하나 = 브랜치 하나 = PR 하나, 스쿼시 병합.
- 결정 기록의 번호와 본문은 바꾸지 않는다(0014의 상태 줄만 예외).
- `docs/superpowers/plans/`의 지난 계획 문서는 고치지 않는다.
- CI의 외부 action은 커밋 SHA로 고정한다. 워크플로 권한은 기본 `contents: read`.
- AI 검토의 프롬프트는 코드 줄을 인용하지 말라고 적는다. 판정이 없으면 체크가 실패한다. `review: override`는 Security가 아닌 위반만 통과시킨다.
- 변경마다 `CHANGELOG.md` `[Unreleased]`에 한 줄.
- 브랜치 보호와 auto-merge 의존은 Task 3이 `main`에 들어간 뒤에 켠다. 그 전 PR은 로컬 `make check` 통과 후 `gh pr merge --squash`로 병합한다.

## 검토할 때 볼 것

1. 문서만 바꾼 커밋: `pre-commit`이 `make check`를 돌리지 않고 바로 통과해야 한다 → Task 1의 `scripts/hooks_test.sh` 사례.
2. 일부만 스테이징된 Go 파일: `pre-commit`이 포맷하면서 스테이징하지 않은 변경까지 커밋하면 안 된다 → Task 1 사례.
3. 검토자의 출력이 JSON이 아니거나 `violations`가 없음: 통과하면 안 된다 → Task 3의 `scripts/review-verdict_test.sh` 사례.
4. 남은 일 항목이 Issue로 옮겨지지 않고 사라짐 → Task 7의 대조 단계.
5. ADR을 옮긴 뒤 저장소 안 링크가 옛 경로를 가리킴(지난 계획과 ADR 본문은 예외) → Task 4의 grep 단계.
6. 공개 예제의 원본 값이 golden 파일, 테스트 실패 메시지, CI 로그에 찍힘 → Task 8의 golden 형식과 실패 메시지 규칙.
7. 고정한 커밋을 받을 수 없거나 다른 커밋을 받음 → Task 8의 `fetch-sample_test.sh` 사례.

---

### Task 0: GitHub 준비 (PR 없음)

**Produces:** 라벨 12개, Project `secret-externalizer`(번호를 이후 task가 쓴다), Milestone "개발 방식 정비", Task 1~8의 Issue 번호.

- [ ] **Step 1: 기본 라벨을 지우고 app-surveyor의 라벨을 만든다**

```bash
for l in $(gh label list --json name --jq '.[].name' | tr ' ' '\001'); do gh label delete "$(printf '%s' "$l" | tr '\001' ' ')" --yes; done
gh label clone SH-Hong1/app-surveyor --force
gh label create "review: override" --color c2e0c6 --description "AI 검토의 Security가 아닌 지적을 통과시킨다"
gh label list   # 12개: type: × 10, needs: decision, review: override
```

- [ ] **Step 2: Project와 Milestone을 만든다**

```bash
gh project create --owner SH-Hong1 --title secret-externalizer   # 번호를 기록한다
gh project link <번호> --owner SH-Hong1 --repo SH-Hong1/secret-externalizer
gh api repos/SH-Hong1/secret-externalizer/milestones -f title="개발 방식 정비" -f description="검증을 git hook과 CI로 옮기고 문서와 일을 GitHub로 관리한다"
```

Project의 기본 workflow "Item closed → Done"이 켜져 있는지 `gh project view <번호> --owner SH-Hong1 --web`으로 확인한다(꺼져 있으면 사용자에게 켜 달라고 한다. API로 켤 수 없다).

- [ ] **Step 3: Task 1~8의 Issue를 만든다**

각 Issue: 제목은 아래 task 제목, 본문은 그 task의 목적 한 문단과 설계 문서 링크, Milestone "개발 방식 정비", 라벨은 Task 1·2·3 `type: ci`, Task 4·5·6·7 `type: docs`, Task 8 `type: test`. 만들 때 `gh project item-add <번호> --owner SH-Hong1 --url <issue url>`로 Project에 넣는다. 번호를 이 계획의 각 task 머리에 적는다.

---

### Task 1: git hook과 커밋 검사로 옮긴다 (Claude Code hook 제거)

**Files:**
- Create: `.githooks/pre-commit`, `.githooks/commit-msg`, `scripts/commit-lint.sh`, `scripts/hooks_test.sh`, `scripts/lint-rules_test.sh`, `scripts/testdata/lint-rules/` (`.claude/hooks/testdata/*.go.txt`를 `git mv`), `docs/decisions/0072-checks-run-in-git-hooks-and-ci.md`
- Modify: `Makefile` (`setup` 타깃 추가, `hooks` 타깃이 두 스크립트를 실행), `.claude/settings.json` (`hooks` 키 삭제; 다른 키가 없으면 파일 삭제), `docs/decisions/0014-stop-review-as-command-hook.md` (상태 줄만 `대체됨 (0072 참조)`), `CHANGELOG.md`, `internal/scan/encoded_test.go`(이미 고친 주석)
- Delete: `.claude/hooks/` 전체
- Also commit: `docs/superpowers/specs/2026-11-25-dev-workflow-design.md`, 이 계획 문서

**Interfaces:**
- Produces: `sh scripts/commit-lint.sh <file>`, `--title <text>`, `--closes <file>` — 통과 0, 거부 1과 stderr의 이유(app-surveyor와 같은 동작). `make setup`. `make hooks`(= `bash scripts/hooks_test.sh && bash scripts/lint-rules_test.sh`)는 `make check`에 남는다.

- [ ] **Step 1: 브랜치를 만든다** — `git switch -c ci/git-hooks main` (작업 트리의 `encoded_test.go` 수정과 spec 파일을 가져간다).

- [ ] **Step 2: `scripts/hooks_test.sh`를 쓴다(실패하는 테스트)**

app-surveyor의 `scripts/hooks_test.sh`를 받아(`gh api repos/SH-Hong1/app-surveyor/contents/scripts/hooks_test.sh --jq .content | base64 -d`) 이 저장소에 맞춘다. 임시 git 저장소에서 다음을 확인하는 사례가 있어야 한다.

- commit-lint 통과: `feat(scan): add x` + 빈 줄 + `Refs #1`; `fix!: y` + `Closes #2`; `Merge branch 'a'`(본문 없음); `fixup! feat: x`.
- commit-lint 거부와 이유 문구: 타입 없음, 대문자 시작, 마침표 끝, 73자 제목, 둘째 줄이 비어 있지 않음, 이슈 줄 없음, `--title`에 한글 제목 `기능 추가`(형식 불일치), `--closes`에 `Closes #` 줄 없는 본문.
- 주석 줄과 가위 줄(`# ------------------------ >8 ------------------------`) 아래는 무시한다.
- `pre-commit`: 문서만 스테이징하면 `make`를 부르지 않는다(가짜 `make`를 `PATH` 앞에 두고 호출 기록으로 확인); Go 파일을 스테이징하면 `make -s check`를 부른다; 스테이징 뒤 다시 고친 Go 파일이 있으면 exit 1과 `has changes that are not staged`.

실행: `bash scripts/hooks_test.sh` → 스크립트가 없어 실패.

- [ ] **Step 3: `scripts/commit-lint.sh`, `.githooks/commit-msg`, `.githooks/pre-commit`을 쓴다**

app-surveyor의 세 파일을 가져온다. `pre-commit`의 검사 대상 정규식을 이 저장소에 맞춘다:
`(\.go$|^go\.(mod|sum)$|^\.golangci\.yml$|^Makefile$|^rules/|^internal/web/(templates|assets|static)/|(^|/)testdata/|^\.githooks/|^scripts/|^deploy/.*\.sh$|^deploy/worker/scanners\.lock$)`. 포맷 도구는 `make -s bin/golangci-lint` 뒤 `bin/golangci-lint fmt`(이 저장소의 `make fmt`와 같다). 실행 권한을 준다(`chmod +x`, `git update-index --chmod=+x`).

- [ ] **Step 4: 린트 규칙 테스트를 옮긴다**

`.claude/hooks/hooks_test.sh`에서 `go-edit-check.sh`로 `testdata/*.go.txt`를 검사하던 사례(`unformatted`, `errcheck`, `exec`, `nolint_no_reason`, `samepkg_test`, `slog_camel_key`, `slog_dynamic_msg`, `clean`)를 `scripts/lint-rules_test.sh`로 옮긴다. hook을 거치지 않고, 임시 모듈에 사례 파일을 `.go`로 복사한 뒤 `bin/golangci-lint run`/`fmt --diff`의 결과를 같은 기대값(clean만 통과, 나머지는 해당 린터 이름이 출력에 있음)으로 확인한다. Stop hook 사례는 옮기지 않는다.

- [ ] **Step 5: Claude Code hook을 지우고 Makefile을 고친다**

`git rm -r .claude/hooks`, `.claude/settings.json`에서 `hooks` 삭제. `Makefile`:
```make
setup:
	git config core.hooksPath .githooks
hooks: $(BIN)/gofumpt $(BIN)/golangci-lint
	bash scripts/hooks_test.sh
	bash scripts/lint-rules_test.sh
```
`.PHONY`에 `setup` 추가.

- [ ] **Step 6: 확인** — `bash scripts/hooks_test.sh` → `ok`; `make check` → 통과; `make setup && git config core.hooksPath` → `.githooks`.

- [ ] **Step 7: ADR 0072와 CHANGELOG** — 0072 "검사는 커밋할 때 git hook과 PR의 CI로 한다"(배경: Stop hook의 비용과 잘못된 차단, 0014; 결정: 설계 3.5; 검토한 다른 방법: Claude hook 유지, lefthook/pre-commit, commitlint). 0014 상태 줄만 바꾼다. CHANGELOG: "Claude Code hook을 없애고 커밋할 때 git hook이 검사한다. 처음 받은 저장소에서는 `make setup`을 실행한다(0072)".

- [ ] **Step 8: 커밋, PR, 병합** — 커밋 메시지 `ci: check commits with git hooks instead of claude code hooks` + 빈 줄 + `Closes #<Task 1 issue>`(hook이 실제로 검사한다). `gh pr create --title "ci: check commits with git hooks instead of claude code hooks" --body`(한국어 본문, `Closes #…`) → `make check` 통과를 본문에 적고 `gh pr merge --squash --delete-branch`.

---

### Task 2: CI의 check, integration, pull-request 작업과 저장소 병합 설정

**Files:**
- Create: `.github/workflows/ci.yml`, `.github/compose.ci.yaml`(필요한 경우), `.github/pull_request_template.md`, `.github/ISSUE_TEMPLATE/{feature,task,bug}.yml`, `docs/decisions/0074-one-task-one-squashed-pr-merged-by-ci.md`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: `scripts/commit-lint.sh --title/--closes` (Task 1).
- Produces: 체크 이름 `check`, `integration`, `pull-request` (브랜치 보호가 이 이름을 쓴다).

- [ ] **Step 1: 템플릿** — app-surveyor의 PR 템플릿과 Issue 템플릿 셋을 가져온다. PR 템플릿 첫 주석은 "제목은 영어 커밋 메시지 형식: `<type>(<scope>): <subject>`".

- [ ] **Step 2: `ci.yml`** — 트리거 `pull_request: [opened, edited, synchronize, reopened, labeled, unlabeled]`, `push: main`; `concurrency: ci-${{ github.ref }}`, cancel. 작업:
  - `check`: checkout, setup-go(`go-version-file: go.mod`), cache `bin`(키: `hashFiles('Makefile','deploy/worker/scanners.lock')`), `make tools scanners`, `make check`.
  - `integration`: checkout, setup-go, cache, `make scanners`, `docker compose --profile test up -d --wait postgres valkey-test openbao-test pdf-test`(필요하면 `-f compose.yaml -f .github/compose.ci.yaml`), `make test-integration`에 `docs/local-run.md`가 적은 `TEST_*` 값을 넘긴다. 실패 시 `docker compose logs`를 남긴다.
  - `pull-request`(PR만): app-surveyor와 같다.

- [ ] **Step 3: integration이 러너에서 뜨는지 확인** — 브랜치를 push하고 PR을 연다. `integration`이 실패하면 원인(호스트 파일을 가리키는 `secrets:`, NAT를 끈 `test` 네트워크, 메모리)을 로그로 확인하고 `.github/compose.ci.yaml`로 고친다. 로컬에서 같은 명령으로 재현할 수 있으면 먼저 재현한다. 세 번 고쳐도 안 되면 멈추고 사용자에게 보고한다(설계 5절: 필수에서 뺄지는 따로 결정한다).

- [ ] **Step 4: 저장소 병합 설정**

```bash
gh repo edit --enable-squash-merge --enable-merge-commit=false --enable-rebase-merge=false --enable-auto-merge --delete-branch-on-merge
gh api -X PATCH repos/SH-Hong1/secret-externalizer -f squash_merge_commit_title=PR_TITLE -f squash_merge_commit_message=PR_BODY
gh repo view --json squashMergeAllowed,mergeCommitAllowed,rebaseMergeAllowed,autoMergeAllowed   # true,false,false,true
```

- [ ] **Step 5: ADR 0074와 CHANGELOG** — 0074 "task 하나를 PR 하나로 스쿼시 병합하고 CI 통과로 자동 병합한다"(app-surveyor 0006 + 공개 저장소라 브랜치 보호를 쓸 수 있다는 점 + PR 제목 영어). 커밋, PR(`ci: run checks and integration tests on pull requests`), 세 체크가 초록이면 `gh pr merge --squash`.

---

### Task 3: CI의 AI 규칙 검토와 브랜치 보호

**Files:**
- Create: `.github/review-prompt.md` (`.claude/hooks/review-prompt.md`의 git 이력 본을 기초로), `scripts/review-verdict.sh`, `scripts/review-verdict_test.sh`, `docs/decisions/0073-rules-review-runs-in-ci.md`
- Modify: `.github/workflows/ci.yml` (`review` 작업), `Makefile` (`hooks`에 `bash scripts/review-verdict_test.sh` 추가), `CHANGELOG.md`

**Interfaces:**
- Produces: `sh scripts/review-verdict.sh <verdict-json-file> <override>` — `<override>`는 `true`/`false`. 출력(stdout): PR 댓글용 마크다운. 종료: 0 통과, 1 위반, 2 판정 없음.

- [ ] **Step 1: `scripts/review-verdict_test.sh`(실패하는 테스트)** — 사례:
  - `{"violations":[]}` → exit 0, stdout 비어 있음.
  - `{"violations":[{"file":"a.go","line":3,"section":"Errors","fix":"wrap it"}]}`, override false → exit 1, stdout에 `a.go:3 — Errors — wrap it`.
  - 같은 입력, override true → exit 0, stdout에 그 줄과 `review: override` 안내.
  - Security 위반, override true → exit 1.
  - 빈 파일, `not json`, `{}`, `{"violations":"x"}` → exit 2, stdout에 "검토자가 판정을 내지 못했습니다. 작업을 다시 실행하세요."
  - 위반 11개 → 10줄만.

  `bash scripts/review-verdict_test.sh` → 실패.

- [ ] **Step 2: `scripts/review-verdict.sh` 구현** — jq로 `.violations`가 배열인지 확인(아니면 2), 처음 10개를 `"\(.file):\(.line) — \(.section) — \(.fix)"`로 출력, override면 `section == "Security"`인 것이 있을 때만 1. 테스트 통과 확인.

- [ ] **Step 3: `.github/review-prompt.md`** — `git show <Task 1 스쿼시 커밋>^:.claude/hooks/review-prompt.md`(지우기 전 본)를 기초로, 변경을 "아래" 대신 "`$RUNNER_TEMP/change.diff` 파일을 읽어라"로 바꾸고 다음을 더한다: "코드 줄이나 값을 인용하지 않는다. `fix`는 고칠 방법만 쓴다." `docs/decisions/` 언급은 그대로 둔다(Task 4가 고친다).

- [ ] **Step 4: `review` 작업** — PR만, `permissions: contents: read, pull-requests: write`, `fetch-depth: 0`.
  1. `git diff --name-only origin/${{ github.base_ref }}...HEAD -- '*.go' go.mod '*.html' '*.tmpl' '*.sql'`가 비면 "검토할 파일 없음"으로 끝낸다.
  2. `git diff origin/<base>...HEAD > $RUNNER_TEMP/change.diff`.
  3. `anthropics/claude-code-action`(SHA 고정)을 `prompt`(review-prompt.md 내용), `claude_args: --model sonnet --tools Read,Grep,Glob --json-schema '<stop-review.sh의 스키마>'`, 인증 `claude_code_oauth_token: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}`(없으면 `anthropic_api_key`)로 부른다. 구조화된 출력을 읽는 출력 이름과 입력 이름은 고정한 SHA의 `action.yml`과 문서에서 확인한다(context7). action이 PR에 직접 댓글을 쓰지 않게 설정한다.
  4. 출력 JSON을 파일로 쓰고 `override=${{ contains(github.event.pull_request.labels.*.name, 'review: override') }}`로 `review-verdict.sh` 실행. stdout이 비어 있지 않으면 `gh pr comment`로 남긴다(같은 PR의 이전 검토 댓글은 `--edit-last`로 덮어쓴다). 종료 코드를 그대로 작업의 결과로 쓴다.
  5. secret이 없으면 첫 단계에서 `::error::CLAUDE_CODE_OAUTH_TOKEN 또는 ANTHROPIC_API_KEY가 없습니다`로 실패.

- [ ] **Step 5: secret 등록 요청** — 사용자에게 `claude setup-token`으로 토큰을 만들어 `gh secret set CLAUDE_CODE_OAUTH_TOKEN`으로 등록해 달라고 요청하고 기다린다.

- [ ] **Step 6: 실제로 검토하는지 확인** — 이 PR에 임시 커밋으로 규칙 위반이 있는 Go 주석(`// previously this used X`)을 넣어 `review`가 실패하고 댓글이 달리는지, 지운 뒤 통과하는지 본다. 그다음 `review: override` 라벨로 Security가 아닌 위반이 통과하는지 본다. 임시 커밋은 되돌린다.

- [ ] **Step 7: ADR 0073, CHANGELOG, 병합**

- [ ] **Step 8: 브랜치 보호를 켠다**

```bash
gh api -X PUT repos/SH-Hong1/secret-externalizer/branches/main/protection --input - <<'EOF'
{"required_status_checks":{"strict":false,"contexts":["check","integration","pull-request","review"]},
 "enforce_admins":true,"required_pull_request_reviews":null,"restrictions":null,
 "allow_force_pushes":false,"allow_deletions":false}
EOF
```
확인: `gh api repos/SH-Hong1/secret-externalizer/branches/main/protection --jq '.required_status_checks.contexts'`가 넷을 보인다. 이후 모든 PR은 `gh pr merge --auto --squash`로 건다.

---

### Task 4: 결정 기록을 `docs/adr/`로, 화면 시안을 `docs/design/screens/`로 옮긴다

**Files:**
- Move: `docs/decisions/*` → `docs/adr/`, `docs/design/*` → `docs/design/screens/`
- Modify: `docs/adr/README.md`(옛 경로 한 줄), `AGENTS.md`, `CHANGELOG.md`(머리말 경로와 새 줄; 지난 줄은 고치지 않는다), `docs/local-run.md`, `Makefile`, `.github/review-prompt.md`, 코드 주석과 테스트에서 옮긴 경로를 가리키는 곳, `docs/next-steps.md`(Task 7이 Issue 본문에 새 경로를 쓰도록)

- [ ] **Step 1:** `git mv docs/decisions docs/adr`; `mkdir docs/design/screens && git mv docs/design/*.html docs/design/README.md docs/design/screens/`.
- [ ] **Step 2:** `grep -rn "docs/decisions\|decisions/00\|docs/design/[A-Z]" --exclude-dir=.git --exclude-dir=bin . | grep -v '^./docs/adr/\|^./docs/superpowers/plans/\|^./CHANGELOG.md'`의 모든 곳을 새 경로로 고친다. `CHANGELOG.md`는 머리말의 경로만 고친다.
- [ ] **Step 3:** 같은 grep이 빈 결과인지 확인; `make check` 통과.
- [ ] **Step 4:** CHANGELOG 한 줄, 커밋 `docs: move decision records to docs/adr`, PR, `gh pr merge --auto --squash`, 병합 확인.

---

### Task 5: 설계 문서를 Design Doc으로 다시 쓴다

**Files:**
- Create: `docs/design/secret-externalizer.md`
- Delete: `docs/superpowers/specs/2026-11-18-secret-externalizer-design.md`, `docs/superpowers/specs/2026-11-23-finding-key-design.md`
- Modify: 두 원본을 가리키는 곳(`AGENTS.md`, `docs/next-steps.md`, `docs/adr/README.md` 등. 지난 계획과 ADR 본문은 제외), `CHANGELOG.md`

- [ ] **Step 1:** 두 원본 설계, ADR 0001~0074의 결정 절, `AGENTS.md`, `compose.yaml`의 머리 주석, `docs/local-run.md`를 읽는다.
- [ ] **Step 2:** 설계 문서 3.2의 일곱 절 구성으로 쓴다. 3절은 최소한 개요(구성 요소: web, worker, applier, pdf, OpenBao, postgres, valkey, Keycloak), 사용 흐름(프로젝트 등록 → 스캔 → 판정 → 점검 → 적용 → 확인 스캔 → 리포트/대시보드), 스캔과 묶음과 설정 키, 판정, 적용, 리포트와 대시보드, 데이터 모델, 실행 환경과 네트워크, 권한(Keycloak 역할, AppRole)을 다룬다. 바뀐 결정은 최종 상태로 쓰고 문장 끝에 `(ADR 0053)`처럼 근거를 단다. 원본의 계획 단계 목록은 넣지 않는다. 6절 "위험과 한계"의 알려진 한계는 목록 대신 "남은 일은 GitHub Issue에 있다"와 Milestone 링크로 가리킨다(Task 7 뒤에 링크가 살아난다).
- [ ] **Step 3:** 원본 두 개 `git rm`, 참조 수정, `grep -rn "superpowers/specs/2026-11-1\|finding-key-design" --exclude-dir=.git . | grep -v '^./docs/adr/\|^./docs/superpowers/plans/'`가 비는지 확인.
- [ ] **Step 4:** CHANGELOG, 커밋 `docs: rewrite the design as a design doc`, PR(auto-merge).

---

### Task 6: README.md와 AGENTS.md의 작업 방식 규칙

**Files:**
- Create: `README.md`
- Modify: `AGENTS.md`, `CHANGELOG.md`

- [ ] **Step 1: `AGENTS.md`** — 
  - 머리말: 설계는 `docs/design/secret-externalizer.md`, 결정은 `docs/adr/`, 검사는 `make check`, git hook(`.githooks/`), CI.
  - 새 절 "Commit messages": 전체 제약의 커밋 형식, PR 제목도 같은 형식(스쿼시 커밋 제목이 되므로), `Refs #N`/`Closes #N`. Sources: Angular commit message guidelines.
  - 새 절 "Work tracking": 기능은 Milestone, task는 Issue, task 하나에 브랜치와 PR 하나, PR 본문 `Closes #N`, `gh pr merge --auto --squash`로 CI 통과 시 병합, 결정이 먼저 필요하면 `needs: decision`, 계획 문서는 `docs/superpowers/plans/`에 한국어로 쓰고 사용자 검토 뒤 구현, AI 검토가 잘못 막으면 `review: override`(Security 제외).
  - "Formatting and tooling": `make check` 목록에서 "hook script tests"를 "git hook과 commit-lint, review-verdict 테스트"로; `make setup` 언급.
  - "Comments and records" 언어 문장: "`CHANGELOG.md`, `README.md`, and everything under `docs/` are written in Korean."
- [ ] **Step 2: `README.md`** — 설계 3.7의 절 순서. 개발 준비에는 next-steps "준비"의 살아 있는 내용(이 폴더에서 세션을 연다는 줄과 `claude`, `jq` 요구는 hook이 없어졌으므로 뺀다; `bin/`은 git에 없으니 `make tools`)을 넣는다. 문서 지도에 Project와 Milestone 링크.
- [ ] **Step 3:** 링크가 모두 실제 파일을 가리키는지 확인(`grep -o '](\([^)#]*\)' README.md`의 각 경로 `test -e`), `make check`.
- [ ] **Step 4:** CHANGELOG, 커밋 `docs: add a readme and the work tracking rules`, PR(auto-merge).

---

### Task 7: 남은 일을 Issue로 옮기고 `docs/next-steps.md`를 지운다

**Files:**
- Delete: `docs/next-steps.md`
- Modify: `CLAUDE.md` (`@docs/next-steps.md` 줄 삭제), `docs/design/secret-externalizer.md`(6절 Milestone 링크), `CHANGELOG.md`

- [ ] **Step 1: Milestone 일곱 개** — "설정 키", "계획 7에서 남은 일", "계획 6에서 남은 일", "계획 5에서 남은 일", "계획 4에서 남은 일", "계획 3에서 남은 일", "그 밖의 남은 일".
- [ ] **Step 2: 목록을 뽑는다** — `docs/next-steps.md`에서 `- [ ]`로 시작하는 줄을 절 이름과 함께 scratchpad의 TSV(`절<TAB>원문`)로 뽑고 개수를 센다. 다음 둘은 Issue로 만들지 않는다: "지금 상태(…)" 줄(옮기지 않음), "계획 3 이후의 계획 문서는 …"(Task 6에서 AGENTS.md로 옮김). 최상위 절의 나머지(예: `ErrNotFound` 옮기기, 인코딩된 값의 한계)는 "그 밖의 남은 일"이다.
- [ ] **Step 3: Issue를 만든다** — 항목마다: 제목은 영어가 아닌 한국어 한 줄 요약(Issue 제목은 커밋이 아니다), 본문은 `## 내용` + 원문 전체(ADR 경로는 Task 4의 새 경로) + `## 출처` "`docs/next-steps.md`의 '<절>'에서 옮김". 라벨: 동작 결함은 `type: fix`, 없는 기능은 `type: feat`, 느림은 `type: perf`, 테스트 부족은 `type: test`, 정리는 `type: refactor`, 문서는 `type: docs`; "정한다", "정할지"가 들어간 항목은 `needs: decision`을 더한다. 각 Issue를 Project에 넣는다. 만든 번호를 TSV에 셋째 칸으로 적는다.
- [ ] **Step 4: 대조** — TSV 줄 수 = 만든 Issue 수 = `gh issue list --state open --limit 200 --json milestone --jq '[.[]|select(.milestone.title!="개발 방식 정비")]|length'`. 셋이 같지 않으면 멈추고 찾는다. 대조표(절별 개수와 Issue 번호 범위)를 PR 본문에 넣는다.
- [ ] **Step 5:** `git rm docs/next-steps.md`, `CLAUDE.md`에서 그 줄 삭제, 설계 문서 6절에 Milestone 링크, `grep -rn "next-steps" --exclude-dir=.git . | grep -v '^./docs/adr/\|^./docs/superpowers/'`가 비는지 확인.
- [ ] **Step 6:** CHANGELOG("남은 일은 GitHub Issue로 관리한다"), 커밋 `docs: move the remaining work to github issues`, PR(auto-merge), Milestone "개발 방식 정비"에 Task 8의 Issue만 열려 있는지 확인.

---

### Task 8: 공개 예제(eGovFrame 웹 예제)로 스캔부터 확인 스캔까지 검증한다

**Files:**
- Create: `scripts/samples.lock`, `scripts/fetch-sample.sh`, `scripts/fetch-sample_test.sh`, `internal/apply/sample_test.go`, `internal/apply/testdata/public-sample.golden`, `docs/adr/0075-public-sample-check.md`
- Modify: `Makefile` (`sample`, `sample-check` 타깃; `test-integration`이 `GOTESTFLAGS`와 `SAMPLE_PROJECT_DIR`를 넘김; `hooks`에 `bash scripts/fetch-sample_test.sh`), `.github/workflows/ci.yml` (`sample` 작업, 필수 아님), `README.md`(검사 절), `docs/design/secret-externalizer.md`(5절 완료 판정), `CHANGELOG.md`

**Interfaces:**
- Consumes: `internal/apply/endtoend_test.go`의 `realScanner`, `realStore`, `realVault`, `runScan`, `storedKeys`, `projectFiles`, `secretKind`와 `newShop`이 쓰는 작업 공간·git 준비 코드(같은 `apply_test` 패키지).
- Produces: `sh scripts/fetch-sample.sh <lock-file> <cache-dir>` — 각 줄 `이름 URL 커밋`을 `<cache-dir>/<이름>-<커밋>`에 받고 그 경로를 한 줄씩 출력한다. 이미 받았고 HEAD가 맞으면 다시 받지 않는다. 실패하면 1과 이유. 테스트가 읽는 환경 변수 `SAMPLE_PROJECT_DIR`.

- [ ] **Step 1: 커밋을 고정한다** — `gh api repos/eGovFramework/egovframe-web-sample/commits/main --jq .sha`의 커밋을 `scripts/samples.lock`에 `egovframe-web-sample https://github.com/eGovFramework/egovframe-web-sample.git <sha>`로 쓴다. 예제의 라이선스를 확인해 ADR 0075에 적는다.

- [ ] **Step 2: `scripts/fetch-sample_test.sh`(실패하는 테스트)** — 임시 폴더의 로컬 bare 저장소를 URL로 쓴다. 사례: 고정 커밋을 받아 경로를 출력하고 그 HEAD가 커밋과 같음; 두 번째 실행은 `git fetch`를 부르지 않음(가짜 `git`을 `PATH` 앞에 두고 호출 기록); 없는 커밋이면 exit 1과 `commit … not found`; 캐시 폴더의 HEAD가 다르면 새로 받음; 잠금 파일 줄이 세 칸이 아니면 exit 1. 실행 → 실패.

- [ ] **Step 3: `scripts/fetch-sample.sh` 구현** — `git init` → `git fetch --depth 1 <url> <commit>` → `git checkout --detach FETCH_HEAD` → `git rev-parse HEAD` 비교. 테스트 통과 확인.

- [ ] **Step 4: `TestIntegration_PublicSample`(`internal/apply/sample_test.go`)** — `SAMPLE_PROJECT_DIR`가 없으면 `t.Skip("SAMPLE_PROJECT_DIR is not set; run make sample-check")`. 예제를 `.git` 없이 작업 공간 아래로 복사해 새 git 저장소로 커밋하고, `TestIntegration_ScanApplyRescan`과 같은 구성(실제 스캐너, `scan.NewRunner`, `apply.NewRunner`)으로:
  1. 스캔 → 성공. 항목을 `파일<TAB>종류<TAB>설정 키` 줄로 정렬해 `testdata/public-sample.golden`과 `cmp.Diff`. `-update` 플래그면 golden을 쓴다. golden에는 값과 미리보기를 쓰지 않는다.
  2. 스캔 직후 `worker.ListPending`과 root 토큰으로 원본 값 목록을 읽어 둔다(테스트 메모리에만).
  3. 모든 묶음에 추천 판정을 내리고 점검과 적용을 돌린다(기존 테스트의 흐름).
  4. 확인 스캔 → 외부화한 묶음 가운데 `secretKind`인 것의 값이 다시 보고되지 않는다.
  5. `information_schema.columns`의 모든 `text`/`varchar`/`jsonb` 열과 worker 로그 버퍼에 2.의 값이 없다. 실패 메시지는 테이블·열 이름과 묶음 ID만 쓰고 값을 쓰지 않는다.
  6. 적용 전후 `projectFiles`의 차이가 placeholder 자리와 `secrets-manifest.yml` 밖에 없다.

- [ ] **Step 5: Makefile** — `SAMPLE_CACHE ?= $(or $(XDG_CACHE_HOME),$(HOME)/.cache)/secret-externalizer/samples`. `sample`은 `sh scripts/fetch-sample.sh scripts/samples.lock "$(SAMPLE_CACHE)"`. `sample-check`는 그 출력의 첫 줄을 `SAMPLE_PROJECT_DIR`로 넘겨 `test-integration`을 `GOTESTFLAGS="-run TestIntegration_PublicSample"`로 부른다. `make sample-check`가 golden이 없어 실패하는 것을 확인한다.

- [ ] **Step 6: golden을 만들고 검토한다** — `-update`로 golden을 쓰고 연다. 각 줄이 실제 비밀이나 접속 정보인지 예제 파일에서 확인하고, 오탐이나 누락으로 보이는 것은 Milestone "공개 예제 검증"(여기서 만든다)에 Issue로 만든다. golden은 지금 동작을 그대로 담는다(고치는 일은 그 Issue의 몫). 다시 `make sample-check` → PASS.

- [ ] **Step 7: CI `sample` 작업** — `integration`과 같은 서비스 준비 뒤 `make sample-check`. 캐시 폴더를 `hashFiles('scripts/samples.lock')` 키로 캐시한다. 브랜치 보호의 필수 목록에 넣지 않는다. PR에서 초록인지 확인.

- [ ] **Step 8: 화면에서 한 번 끝까지 확인** — `docs/local-run.md`대로 로컬 스택을 띄우고, 받은 예제를 작업 공간 아래로 복사해 git 저장소로 만든 뒤 프로젝트로 등록한다. 스캔 → 판정 화면(묶음, 키, 원본 보기) → 점검 → 적용 → 확인 스캔 → 리포트와 PDF → 대시보드를 돌고, web·worker·applier 로그에 원본 값이 없는지 `docker compose logs`를 `grep -F`로 확인한다(값은 기록에 남기지 않는다). 결함마다 Issue("공개 예제 검증"). 확인 표를 ADR 0075의 "실제 스택에서 확인한 것"에 쓴다.

- [ ] **Step 9: 기록과 병합** — ADR 0075(배경: app-surveyor의 완료 기준; 결정: 위 구성; 이유: 반복할 수 있고 예제 소스를 저장소에 넣지 않음; 검토한 다른 방법: 손으로만 확인, 예제를 testdata에 복사). 설계 문서 5절 완료 판정에 `make sample-check` 통과를 더하고 README, CHANGELOG. 커밋 `test(apply): check scan and apply against a public egovframe sample`, PR(auto-merge), Milestone "개발 방식 정비" 0 open 확인.
