# 이어서 할 일

이 문서는 세션이 바뀌어도 남은 일이 빠지지 않도록 적어 둔 목록이다. `CLAUDE.md`가 이 문서를 불러오므로 이 폴더에서 연 세션은 항상 이 목록을 읽는다.

- 항목을 끝내면 체크 표시를 하고, 결과를 한 줄로 적는다.
- 절 하나가 모두 끝나면 그 절을 지운다.
- 목록이 비면 이 문서와 `CLAUDE.md`의 `@docs/next-steps.md` 줄을 함께 지운다.

## 준비

- 이 폴더(`secret-externalizer`)에서 세션을 연다. 상위 폴더에서 열면 hook 설정이 적용되지 않는다.
- `jq`가 설치되어 있어야 한다.
- `bin/`은 git에 올라가지 않는다. 새로 받은 저장소에서는 `make tools`를 먼저 실행한다.

## 1. hook 실제 동작 확인

계획 1에서 hook 스크립트는 셸 테스트(`bash .claude/hooks/hooks_test.sh`)로 검증했지만, Claude Code 세션 안에서 발동하는지는 확인하지 못했다. 가장 먼저 한다.

- [ ] `/hooks`에서 `PostToolUse`(matcher `Edit|Write`) 1건과 `Stop` 2건(command, agent)이 프로젝트 설정으로 보인다.
- [ ] 수정 hook: `internal/hooktry/x.go`에 오류를 버리는 코드(`os.Remove("x")`)를 Write 도구로 쓰면 `errcheck` 지적이 대화에 돌아온다.
- [ ] 수정 hook: 들여쓰기가 공백인 Go 파일을 쓰면 `gofumpt` 지적이 돌아온다.
- [ ] 수정 hook: `.md` 파일을 고칠 때는 아무 출력이 없다.
- [ ] 종료 hook(command): 실패하는 테스트가 있는 Go 파일을 남긴 채 작업을 끝내려 하면, 실패 내용과 함께 작업이 이어진다.
- [ ] 종료 hook(command): 같은 실패가 한 번 더 이어지면 사용자에게 "still failing" 메시지가 보이고 작업이 끝난다.
- [ ] 종료 hook(agent): `make check`는 통과하지만 `// 2026-11-18: changed from map to slice per review` 같은 주석이 있는 파일을 남기면 `Comments and records` 위반으로 막는다.
- [ ] 종료 hook(agent): 문서만 고친 경우 바로 통과한다.
- [ ] 종료 hook(agent)이 동작하지 않으면 원인을 확인한다. `agent` 유형은 실험 기능이다. 확인할 것은 `.claude/settings.json`의 필드 이름, 응답 형식(`{"ok": true}` / `{"ok": false, "reason": "..."}`), 에이전트가 `git` 명령을 실행할 수 있는지다. 실행할 수 없으면 `.claude/hooks/review-prompt.md`의 1단계가 "무엇이 바뀌었는지 알 수 없으면 승인"으로 빠지므로 검토가 사실상 동작하지 않는다. 이 경우 `prompt` 유형으로 바꾸고 변경 내용을 command hook이 만들어 넘기는 방식을 검토한다.
- [ ] 확인에 쓴 임시 파일(`internal/hooktry/` 등)을 지운다.

## 2. 계획 2를 시작하기 전에 정리할 것

계획 1의 최종 검토에서 사소한 지적으로 분류해 고치지 않고 남긴 항목이다. 계획 2(골격과 로그인)가 `cmd/web`을 확장하므로 그 전에 정리한다. 각 수정은 실패하는 테스트를 먼저 쓴다.

### cmd/web

- [ ] `var version = "dev"`가 `AGENTS.md`의 "패키지 수준의 변경 가능한 상태 금지"와 어긋난다. 링크 시점 주입을 예외로 규칙에 적거나, 값을 다른 방식으로 넘긴다.
- [ ] `main`의 `fmt.Fprintf(os.Stderr, ...)`가 오류를 버린다. `_ =`와 이유를 적는다.
- [ ] `run`이 `ctx`를 쓰지 않고 `SIGINT`/`SIGTERM` 처리가 없다. 계획 2에서 서버를 띄울 때 함께 넣는다.
- [ ] 테스트가 `context.Background()`를 쓴다. `t.Context()`로 바꾼다.
- [ ] `web -h`가 사용법을 출력한 뒤 `web: parsing flags: flag: help requested`를 한 번 더 출력하고 종료 코드 1로 끝난다. `flag.ErrHelp`는 정상 종료로 처리하고, 플래그 오류를 두 번 출력하지 않는다.

### AGENTS.md

- [ ] "Accept interfaces and return concrete types"와 `NewServer(...) http.Handler`, `http.Handler`를 돌려주는 handler 규칙이 어긋난다. 예외를 명시한다.
- [ ] `cmp.Diff`와 `errgroup`을 쓰라고 하면서, 의존성마다 결정 기록을 요구한다. 두 의존성의 결정 기록을 `docs/decisions/`에 추가한다.
- [ ] "Today those are `cmd/web` and `cmd/worker`"라고 적혀 있으나 `cmd/worker`는 아직 없다. 문장을 고치거나 계획 2에서 만든 뒤 맞춘다.
- [ ] 규칙에는 있으나 린터가 강제하지 않는 것을 설정에 넣는다: `nolintlint`(사유 필수), `sloglint`의 `static-msg`와 `key-naming-case: snake`, 테스트의 `_test` 패키지 사용.
- [ ] `gosec` G204가 규칙이 권하는 `exec.CommandContext` 호출을 지적할 것이다. "린트 지적은 억제하지 않는다"와 충돌하지 않게 처리 방법을 정해 규칙에 적는다.

### hook과 설정

- [ ] `testdata` 제외가 부분 문자열 일치다. `internal/testdatabase` 같은 패키지가 린트에서 빠진다. `.golangci.yml`은 `(^|/)testdata/`로, `go-edit-check.sh`는 저장소 기준 상대 경로로 판단하게 고친다.
- [ ] `./bin`에 도구가 없을 때 종료 hook이 `make tools` 안내 없이 make 오류만 보여준다. `Makefile`의 `lint`, `fmt`가 도구 파일 타깃에 의존하게 한다.
- [ ] `go-edit-check.sh`가 상대 경로 입력을 현재 폴더 기준으로 해석한다. 절대 경로가 아니면 저장소 루트를 앞에 붙인다.
- [ ] `review-prompt.md`가 "Formatting and tooling" 절(의존성 추가, `nolint` 사유)을 확인하지 않는다.
- [ ] `hooks_test.sh`가 `make check`에 연결되어 있지 않다. 연결한다.
- [ ] 한글이나 공백이 든 폴더 안의 Go 파일은 Go의 import 경로 제약 때문에 항상 린트에 실패한다. 고칠 수 없는 제약이므로 `AGENTS.md`의 "Project layout"에 "디렉터리 이름은 ASCII 소문자"를 규칙으로 적는다.

## 3. 다음 계획

설계 문서: `docs/superpowers/specs/2026-11-18-secret-externalizer-design.md`

| 번호 | 계획 | 상태 |
|---|---|---|
| 1 | 개발 규칙과 검증 hook | 구현 완료, PR 검토 대기 |
| 2 | 골격과 로그인 | 계획 작성 전 |
| 3 | 프로젝트 등록과 스캔 | |
| 4 | 화면 디자인 시안과 판정 화면 | |
| 5 | 적용 | |
| 6 | 리포트와 PDF | |
| 7 | 대시보드 | |

- [ ] 1절과 2절을 끝낸 뒤 계획 2를 작성한다. 계획 문서는 `docs/superpowers/plans/`에 한글로 쓰고, 사용자의 검토를 받은 뒤 구현한다.
- 계획 4에서는 구현 전에 Claude design으로 화면 시안을 만들어 확인받고, 구현은 frontend-design 스킬로 한다. 배치 시안은 `.superpowers/brainstorm/` 아래에 있다(git에는 올라가지 않는다).
