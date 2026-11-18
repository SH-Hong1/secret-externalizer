# 이어서 할 일

이 문서는 세션이 바뀌어도 남은 일이 빠지지 않도록 적어 둔 목록이다. `CLAUDE.md`가 이 문서를 불러오므로 이 폴더에서 연 세션은 항상 이 목록을 읽는다.

- 항목을 끝내면 체크 표시를 하고, 결과를 한 줄로 적는다.
- 절 하나가 모두 끝나면 그 절을 지운다.
- 목록이 비면 이 문서와 `CLAUDE.md`의 `@docs/next-steps.md` 줄을 함께 지운다.

## 준비

- 이 폴더(`secret-externalizer`)에서 세션을 연다. 상위 폴더에서 열면 hook 설정이 적용되지 않는다.
- `jq`가 설치되어 있어야 한다.
- `bin/`은 git에 올라가지 않는다. 새로 받은 저장소에서는 `make tools`를 먼저 실행한다.

## 다음 계획

설계 문서: `docs/superpowers/specs/2026-11-18-secret-externalizer-design.md`

| 번호 | 계획 | 상태 |
|---|---|---|
| 1 | 개발 규칙과 검증 hook | 완료 |
| 2 | 골격과 로그인 | 구현 완료, 브라우저 확인과 PR 검토 대기 (`docs/superpowers/plans/2026-11-18-02-skeleton-and-login.md`) |
| 3 | 프로젝트 등록과 스캔 | |
| 4 | 화면 디자인 시안과 판정 화면 | |
| 5 | 적용 | |
| 6 | 리포트와 PDF | |
| 7 | 대시보드 | |

- [x] 계획 2 전 정리(PR #2)가 병합되었는지 확인한다. 2026-11-18에 병합되었다.
- [ ] `cmd/worker`를 만들면 `AGENTS.md` "Project layout"의 진입점 문장에 추가한다. 계획 2는 worker를 계획 3으로 미루는 안으로 작성했다.
- [x] 계획 2의 "검토 때 정해 주실 것" 7개 항목에 사용자의 답을 받고, 그에 맞게 계획을 고친 뒤 구현한다. Task 1~7을 구현했다.
- [ ] 계획 2 Task 7의 확인 표(1~9)를 Chrome에서 확인한다. curl로는 모두 확인했다. 방법은 `docs/local-run.md`에 있다.
- [ ] 계획 2의 PR을 만들고 검토를 받는다.
- [ ] 계획 3 이후의 계획 문서는 `docs/superpowers/plans/`에 한글로 쓰고, 사용자의 검토를 받은 뒤 구현한다.
- 계획 4에서는 구현 전에 Claude design으로 화면 시안을 만들어 확인받고, 구현은 frontend-design 스킬로 한다. 배치 시안은 `.superpowers/brainstorm/` 아래에 있다(git에는 올라가지 않는다).
