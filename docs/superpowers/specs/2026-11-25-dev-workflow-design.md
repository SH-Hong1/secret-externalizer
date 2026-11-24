# 개발 방식 정비 설계

- 날짜: 2026-11-25
- 참고: `SH-Hong1/app-surveyor`의 `.githooks/`, `scripts/commit-lint.sh`, `.github/`, `docs/adr/0004`~`0006`

## 1. 배경과 의도

secret-externalizer는 계획 1~7을 끝내고 남은 일을 고치는 단계에 있다. 지금은 다음과 같이 일한다.

- 요구사항과 남은 일: `docs/next-steps.md`라는 체크 목록 하나(끝나지 않은 항목 약 60개)
- 결정: `docs/decisions/`의 결정 기록 71개
- 설계: `docs/superpowers/specs/`의 설계 문서 두 개
- 검증: Claude Code hook 셋. 파일을 고칠 때마다 포맷과 린트를 하고(`go-edit-check.sh`), 턴이 끝날 때마다 `make check`(`stop-verify.sh`)와 AI 규칙 검토(`stop-review.sh`)를 한다.
- CI: 없음. 병합 방식은 셋 다 허용되어 있다.

사용자는 app-surveyor에서 쓰는 방식으로 옮기려 한다. 검사는 커밋할 때 git hook이 하고, 같은 검사를 CI가 다시 한다. 일은 GitHub Issue로 관리하고, CI를 통과한 PR만 병합한다. 이렇게 하면 검증이 AI 도구의 턴에 묶이지 않고, 남은 일과 진행 상황을 GitHub에서 볼 수 있다.

## 2. 목표, 하지 않는 것, 실패로 보는 것

### 목표

1. 설계 문서는 Google Design Doc 구성으로 `docs/design/secret-externalizer.md` 한 곳에 둔다.
2. 결정은 `docs/adr/`의 ADR로 관리한다.
3. 요구사항과 task는 GitHub Milestone, Issue, Project로 관리한다. PR이 병합되면 Issue가 자동으로 닫힌다.
4. 커밋 메시지는 Angular 형식이고 `commit-msg` hook이 검사한다.
5. 라벨은 Angular 타입(`type: feat` 등)으로 만든다.
6. Claude Code의 Stop hook을 없애고, 기계로 하는 검사는 `pre-commit` hook으로 옮긴다.
7. AI만 판단할 수 있는 규칙(주석과 기록, Security 등)은 CI에서 Claude가 PR을 검토한다.
8. CI가 통과하면 PR이 병합된다.
9. 저장소에 `README.md`를 둔다.
10. 공개 예제 프로젝트(전자정부 표준프레임워크 `eGovFramework/egovframe-web-sample`)에 스캔부터 확인 스캔까지를 반복해서 돌릴 수 있는 검증을 두고, 실제 스택의 화면에서도 한 번 끝까지 돌려 본다.

### 하지 않는 것

- 제품 코드의 동작은 바꾸지 않는다. 남은 일 Issue의 내용을 고치는 것은 이 일이 아니다.
- 결정 기록의 본문은 고치지 않는다. 옮기고 참조 경로만 고친다.
- `docs/superpowers/plans/`의 지난 계획 문서는 고치지 않는다. 그 안의 옛 경로는 그대로 둔다.
- lefthook, commitlint 같은 도구는 들이지 않는다.

### 실패로 보는 것

- 남은 일 가운데 하나라도 Issue로 옮겨지지 않고 사라지는 것.
- 결정 기록의 번호가 바뀌거나 내용이 달라지는 것.
- CI의 AI 검토가 비밀 값이나 secret을 로그나 PR 댓글에 남기는 것.
- 검사를 건너뛴 커밋이 CI를 거치지 않고 `main`에 들어가는 것.

## 3. 설계

### 3.1 문서 위치

| 지금 | 바뀐 뒤 |
|---|---|
| `docs/decisions/NNNN-*.md`, `README.md` | `docs/adr/NNNN-*.md`, `README.md` (`git mv`, 번호와 본문 그대로) |
| `docs/superpowers/specs/2026-11-18-secret-externalizer-design.md`, `2026-11-23-finding-key-design.md` | `docs/design/secret-externalizer.md` 하나로 다시 씀. 두 원본은 지운다(git 이력에 남는다) |
| `docs/design/*.dc.html`, `docs/design/README.md` (화면 시안) | `docs/design/screens/` |
| `docs/next-steps.md` | Issue로 옮기고 지운다. `CLAUDE.md`의 `@docs/next-steps.md` 줄도 지운다 |
| 없음 | `README.md` |

`AGENTS.md`, `CLAUDE.md`, `CHANGELOG.md`, `docs/local-run.md`, 코드 주석에서 옮긴 경로를 가리키는 곳을 모두 고친다. 지난 계획 문서와 ADR 본문 안의 옛 경로는 고치지 않는다. ADR `README.md`에 "옛 경로 `docs/decisions/`는 이 폴더다"라고 한 줄 적는다.

### 3.2 설계 문서 (Google Design Doc)

app-surveyor의 `docs/design/app-surveyor.md`와 같은 구성을 쓴다.

1. 배경과 범위(사용자 포함)
2. 목표와 하지 않는 것(목표, 성공 기준, 하지 않는 것, 실패로 보는 것)
3. 설계(개요, 사용 흐름, 구성 요소, 데이터 모델, 실행 환경 등 이 시스템에 맞는 하위 절)
4. 검토한 다른 방법
5. 공통 관심사(비밀 값과 고객정보, 테스트, 완료 판정)
6. 위험과 한계
7. 구현하는 사람에게 맡기는 것

내용은 두 원본 설계와 그 뒤에 확정된 결정(ADR 0001~0071)을 반영한 **지금의 시스템**을 설명한다. 바뀐 결정은 최종 상태로 쓰고, 근거는 ADR 번호로 가리킨다. 원본에 있던 계획 단계별 순서는 넣지 않는다.

### 3.3 GitHub 설정

- **라벨:** 기본 라벨을 모두 지우고 app-surveyor와 같은 12개를 만든다. `type: feat`, `fix`, `docs`, `refactor`, `style`, `perf`, `test`, `build`, `ci`, `revert`와 `needs: decision`, 그리고 AI 검토용 `review: override`(3.6)다.
- **Project:** 사용자 계정에 Project `secret-externalizer`를 만들고 저장소에 연결한다. 만드는 Issue는 모두 Project에 넣는다. 닫힌 Issue를 Done으로 옮기는 기본 workflow를 켠다.
- **Milestone:** next-steps의 절마다 하나씩 만든다. "개발 방식 정비"(이 일), "설정 키", "계획 7에서 남은 일", "계획 6에서 남은 일", "계획 5에서 남은 일", "계획 4에서 남은 일", "계획 3에서 남은 일", "그 밖의 남은 일"(최상위 목록에 남은 것).
- **Issue 템플릿:** `.github/ISSUE_TEMPLATE/`에 `feature.yml`, `task.yml`, `bug.yml`을 둔다(app-surveyor와 같은 구성, 한국어). PR 템플릿에는 `Closes #`를 둔다.
- **병합:** 스쿼시만 허용한다. 스쿼시 커밋의 제목은 PR 제목, 본문은 PR 본문이다. 병합된 브랜치를 지우고(이미 켜짐) auto-merge를 켠다.
- **브랜치 보호(`main`):** 이 저장소는 공개라서 Free에서도 쓸 수 있다. CI의 `check`, `integration`, `pull-request`, `review` 체크 통과를 필수로 하고, 직접 push를 막으며, 관리자에게도 적용한다. 리뷰 승인은 필수로 하지 않는다(혼자 쓰는 저장소다).
- **병합 흐름:** PR을 만들면 `gh pr merge --auto --squash`로 자동 병합을 건다. 필수 체크가 모두 통과하면 GitHub가 병합하고 `Closes #N`으로 Issue가 닫힌다.

### 3.4 남은 일을 Issue로 옮기기

- `docs/next-steps.md`에서 체크되지 않은 항목 하나가 Issue 하나다. 체크된 항목은 옮기지 않는다.
- 제목은 한 줄 요약, 본문은 항목의 원문 전체와 관련 ADR 링크(새 경로)다. 라벨은 항목의 성격에 맞는 `type: *` 하나이고, 결정이 먼저 필요한 항목(예: "어떻게 할지 정한다")에는 `needs: decision`을 더한다.
- "준비" 절의 내용은 `README.md`로 옮긴다. "지금 상태" 문단은 옮기지 않는다(그 내용은 ADR과 git 이력에 있다).
- "계획 3 이후의 계획 문서는 … 한글로 쓰고 검토를 받은 뒤 구현한다"는 Issue가 아니라 `AGENTS.md`의 작업 방식 규칙으로 옮긴다.
- 옮긴 뒤 `next-steps.md`의 항목 수와 만든 Issue 수를 대조하고, 대조표를 PR 본문에 둔다.

### 3.5 git hook

- `.githooks/pre-commit`: app-surveyor의 것과 같은 구조다. 스테이징된 파일 가운데 검사 대상(`*.go`, `go.mod`, `go.sum`, `.golangci.yml`, `Makefile`, `rules/`, `internal/web/` 템플릿과 CSS 입력, `testdata/`, `.githooks/`, `scripts/`, `deploy/`의 스크립트)이 있으면 스테이징된 Go 파일을 포맷하고 `make check`를 실행한다. 대상이 없으면(문서만 바꾼 커밋) 아무것도 하지 않는다.
- `.githooks/commit-msg`: `scripts/commit-lint.sh`를 부른다. 형식은 `<type>(<scope>): <subject>`, 72자 이하, 소문자로 시작, 마침표로 끝나지 않음, 둘째 줄은 비어 있음, 본문에 `Refs #N`이나 `Closes #N` 줄이 있음. `Merge`, `fixup!` 등 git이 쓰는 메시지는 통과한다. 타입은 `build ci docs feat fix perf refactor revert style test`다.
- `make setup`이 `git config core.hooksPath .githooks`를 한다. README의 첫 단계다.
- 커밋 제목과 PR 제목은 영어다(AGENTS.md의 규칙 그대로). 스쿼시 병합에서 PR 제목이 `main`의 커밋 제목이 되기 때문이다. PR 본문은 한국어로 쓴다. commit-lint는 app-surveyor의 것을 그대로 쓴다(72자 제한).
- **Claude Code hook은 모두 없앤다.** `.claude/settings.json`의 `PostToolUse`와 `Stop`을 지우고 `.claude/hooks/`의 스크립트를 지운다. `hooks_test.sh`가 하던 린트 설정 확인(`testdata`의 `.go.txt` 사례)은 `scripts/lint-rules_test.sh`로 옮겨 `make check`에 남기고, `scripts/hooks_test.sh`를 새로 두어 commit-lint와 두 git hook을 시험한다.

### 3.6 CI

`.github/workflows/ci.yml`. PR(`opened`, `edited`, `synchronize`, `reopened`, `labeled`)과 `main` push에서 돈다.

- **`check`**: Go를 `go.mod` 버전으로 준비하고 `bin/`을 `Makefile`과 `deploy/worker/scanners.lock`의 해시로 캐시한 뒤 `make tools scanners`, `make check`를 실행한다.
- **`integration`**: `compose.yaml`의 `--profile test` 서비스(postgres, `valkey-test`, `openbao-test`, `pdf-test`)를 띄우고 `make test-integration`을 실행한다. 필수 체크다. compose의 `test` 네트워크는 NAT가 꺼져 있고 최상위 `secrets:`가 호스트의 `$HOME/.config/…` 파일을 가리키므로, 러너에서는 필요한 파일을 작업 안에서 만들고 포트만 러너에 여는 CI용 compose 덮어쓰기 파일(`.github/compose.ci.yaml`)을 쓴다. 이 작업이 러너에서 실제로 도는지가 이 일의 첫 확인 대상이다(구현하는 사람의 판단).
- **`pull-request`**(PR만): `commit-lint.sh --title`로 PR 제목을, `--closes`로 본문의 `Closes #N`을 확인한다.
- **`review`**(PR만): `anthropics/claude-code-action`으로 AI 규칙 검토를 한다.
  - 지금 `stop-review.sh`와 `review-prompt.md`가 하는 일을 옮긴다. 프롬프트는 `.github/review-prompt.md`에 두고, 검토 대상은 PR의 diff다. Go, `go.mod`, 템플릿, SQL이 바뀌지 않은 PR은 검토 없이 통과한다.
  - 검토자는 읽기 도구만 쓴다. 결과는 위반 목록 JSON이고, 위반이 있으면 PR에 댓글 하나로 남기고 체크를 실패시킨다.
  - PR에 `review: override` 라벨이 붙어 있으면 Security 절이 아닌 위반은 댓글만 남기고 통과한다. 지금 hook의 "한 번 되돌려 보낸 뒤에는 Security만 막는다"를 옮긴 것이다.
  - 검토자가 돌았는데 판정을 내지 못하면(API 장애, 시간 초과, 사용 한도, 읽을 수 없는 출력) 체크는 실패한다. 지금 hook과 달리 통과시키지 않는다. 필수 체크라서 통과시키면 검토하지 않은 Security 위반이 `main`에 들어갈 수 있기 때문이다. 출구는 작업 다시 실행이고, `review: override`는 판정이 없을 때는 통하지 않는다(구현하는 사람의 판단).
  - 인증은 저장소 secret `CLAUDE_CODE_OAUTH_TOKEN`(사용자가 `claude setup-token`으로 만든다)이나 `ANTHROPIC_API_KEY`다. secret이 없으면 체크는 실패한다(조용히 통과하지 않는다).
  - 비밀 값이 로그와 댓글에 남지 않도록, 검토자는 위반의 위치와 규칙과 고칠 방법만 쓰고 코드 줄을 인용하지 않는다고 프롬프트에 적는다.
- 워크플로의 권한은 기본 `contents: read`이고, `review`만 `pull-requests: write`를 더한다. 외부 action은 커밋 SHA로 고정한다.

### 3.7 AGENTS.md와 README.md

- `AGENTS.md`에 "Commit messages"와 "Work tracking" 절을 더한다. 커밋 형식, task 하나에 브랜치와 PR 하나, PR 본문의 `Closes #N`, 자동 병합, ADR 위치, 설계 문서 위치, 계획 문서는 한국어로 쓰고 검토를 받은 뒤 구현한다는 규칙이 들어간다. "Formatting and tooling"의 `make check` 설명과 hook 설명을 git hook 기준으로 고친다. 검토 단계가 CI로 옮겨진 것을 반영한다.
- `README.md`(한국어. AGENTS.md의 언어 규칙에서 `docs/`와 같이 다룬다고 AGENTS.md에 적는다): 무엇을 하는 도구인지, 구성(web, worker, applier, OpenBao 등) 한 단락, 빠른 시작(`docs/local-run.md`로 안내), 개발 준비(`make tools`, `make setup`, `jq` 불필요 여부), 검사(`make check`, `make test-integration`), 일하는 방식(Issue → 브랜치 → 커밋 형식 → PR → 자동 병합), 문서 지도(설계, ADR, CHANGELOG, local-run).

### 3.8 공개 예제 검증

app-surveyor가 완료 기준으로 공개 예제에 실행해 보는 것을 이 저장소에서는 반복할 수 있는 검증으로 만든다.

- **대상:** `https://github.com/eGovFramework/egovframe-web-sample`. 커밋을 `scripts/samples.lock`(`이름 URL 커밋` 한 줄)에 고정한다. 받을 수 없게 되면 다른 공개 eGovFrame/Spring MVC 예제로 바꾸고 ADR로 남긴다.
- **받기:** `scripts/fetch-sample.sh`가 고정한 커밋만 저장소 밖(`${XDG_CACHE_HOME:-$HOME/.cache}/secret-externalizer/samples/<이름>-<커밋>`)에 받고, 받은 HEAD가 고정한 커밋인지 확인한다. 예제의 소스와 결과는 저장소에 넣지 않는다.
- **자동 검증:** `make sample-check`가 예제를 받고 `TestIntegration_PublicSample`(`internal/apply`)을 `SAMPLE_PROJECT_DIR`로 돌린다. 이 테스트는 `TestIntegration_ScanApplyRescan`과 같은 실제 스캐너, postgres, OpenBao를 쓰며 `SAMPLE_PROJECT_DIR`가 없으면 건너뛴다. 예제를 임시 작업 공간의 새 git 저장소로 복사해서 다음을 확인한다.
  1. 스캔이 성공하고, 찾은 항목의 (파일, 종류, 설정 키) 목록이 `internal/apply/testdata/public-sample.golden`과 같다. 이 파일에는 값도 미리보기도 없다. 규칙을 바꿔 목록이 바뀌면 `-update`로 고치고 그 차이를 PR에서 검토한다.
  2. 추천 판정대로 점검하고 적용한 뒤의 확인 스캔에서 외부화한 비밀(비밀번호, 토큰, 키) 값이 다시 나오지 않는다.
  3. 원본 값이 DB의 어떤 텍스트 열에도, worker 로그에도 없다.
  4. 적용 전후의 파일 차이가 적용한 자리와 manifest뿐이다.
- **CI:** `sample` 작업으로 돈다. 외부 저장소를 받다가 실패할 수 있으므로 필수 체크가 아니다.
- **화면 확인(한 번):** 로컬 스택(`docs/local-run.md`)에 예제를 프로젝트로 등록해 스캔, 판정, 점검, 적용, 확인 스캔, 리포트와 PDF, 대시보드까지 화면으로 돌려 보고, 찾은 결함마다 Issue를 만들어 Milestone "공개 예제 검증"에 넣는다. 확인한 것은 ADR에 남긴다.

### 3.9 기록

- ADR 넷을 새로 쓴다. "0072 검사는 커밋할 때 git hook과 CI로 한다"(0014를 대체), "0073 AI 규칙 검토는 CI에서 PR마다 한다", "0074 task 하나를 PR 하나로 스쿼시 병합하고 CI 통과로 자동 병합한다", "0075 공개 예제로 스캔부터 확인 스캔까지 검증한다". 0014의 상태를 `대체됨 (0072 참조)`로 바꾼다.
- `CHANGELOG.md`의 `[Unreleased]`에 한 줄씩 남긴다.

### 3.10 순서

이 일 자체도 새 방식으로 한다. 먼저 라벨, Project, "개발 방식 정비" Milestone과 그 task Issue를 만들고, 각 task를 브랜치와 PR 하나로 진행한다. CI와 브랜치 보호는 CI 워크플로가 `main`에 들어간 뒤에 켠다(그 전에 켜면 첫 PR이 존재하지 않는 체크를 기다린다). 직전 브랜치에서 고친 테스트 주석(`internal/scan/encoded_test.go`)은 첫 PR에 함께 넣는다.

## 4. 검토한 다른 방법

- **AI 검토를 `make review`로 손으로 돌리기:** 비용이 없지만 빠뜨릴 수 있다. 사용자가 CI 방식을 골랐다.
- **AI 검토 없애기(app-surveyor와 같게):** 주석과 Security 규칙을 강제할 곳이 없어진다.
- **남은 일을 큰 묶음 Issue 몇 개로 만들기:** task 하나가 PR 하나라는 규칙과 맞지 않고 자동으로 닫히지 않는다. 사용자가 항목마다 만드는 쪽을 골랐다.
- **결정 기록을 `docs/decisions/`에 그대로 두기:** 경로를 바꾸지 않아도 되지만 app-surveyor와 위치가 달라진다. 형식은 이미 ADR이므로 옮기는 비용은 참조 수정뿐이다.
- **GitHub Wiki에 문서 두기:** 문서가 코드와 같은 PR에서 바뀌지 않는다(app-surveyor ADR 0005와 같은 이유).

## 5. 위험과 한계

- AI 검토는 PR마다 비용이 들고 판단이 흔들릴 수 있다. `review: override`가 그 출구다. Security 위반은 출구가 없다.
- `integration` 작업은 GitHub 러너에서 OpenBao, Gotenberg 등을 띄우므로 느리다. 여기서 실패가 잦으면 필수 체크에서 빼는 결정을 따로 한다.
- 관리자에게도 브랜치 보호를 걸면 CI가 고장 났을 때 `main`을 고치는 것도 PR과 CI를 거쳐야 한다. 그때는 보호 설정을 잠시 풀고 ADR에 남긴다.
- 공개 예제 하나가 담은 비밀의 종류는 고객 프로젝트보다 적다. 이 검증이 잡는 것은 주로 실제 구조의 프로젝트에서 드러나는 스캔과 적용의 동작 결함과 오탐이다. 누락은 사례 예제(`internal/ruleset/testdata/rule-cases`)가 계속 맡는다.
- `--no-verify`로 hook을 건너뛸 수 있다. CI가 같은 검사를 하므로 `main`에는 들어가지 않는다.

## 6. 구현하는 사람에게 맡기는 것

- 설계 문서 3절의 하위 절 구성과 문장.
- 각 Issue의 제목 문장과 라벨 선택.
- `review` 작업에서 검토 결과 JSON을 읽어 체크를 실패시키는 방법(action의 출력이나 별도 단계).
- `bin/` 캐시 키의 세부.
