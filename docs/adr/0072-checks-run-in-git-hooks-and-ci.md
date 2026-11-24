# 검사는 커밋할 때 git hook으로, PR에서 CI로 한다

- 날짜: 2026-11-25
- 상태: 확정

## 배경

지금까지는 Claude Code hook이 검사했다. 파일을 고칠 때마다 포맷과 린트를 했고(`go-edit-check.sh`), 턴이 끝날 때마다 `make check`(`stop-verify.sh`)와 AI 규칙 검토(`stop-review.sh`, 0014)를 했다. 턴마다 수 분이 들었고, 검토 에이전트가 턴 종료를 잘못 막은 적이 여러 번 있다. 검사는 Claude Code로 일할 때만 돌았고, 사람이 직접 한 커밋은 검사를 거치지 않았다.

## 결정

- 검사는 커밋할 때 git hook으로 한다. hook은 `.githooks/`에 두고 `make setup`이 `core.hooksPath`로 연결한다.
- `pre-commit`: Go 소스, 빌드 설정, 규칙, 템플릿, 스크립트 같은 검사 대상이 스테이징되어 있으면 스테이징된 Go 파일을 포맷하고 `make check`를 실행한다. 문서만 바꾼 커밋은 검사하지 않는다. 스테이징한 뒤에 다시 고친 Go 파일이 있으면 커밋을 멈춘다. 포맷하면서 의도적으로 빼 둔 변경까지 스테이징하지 않기 위해서다.
- `commit-msg`: `scripts/commit-lint.sh`로 커밋 메시지가 Angular 형식인지, 본문에 `Refs #N`이나 `Closes #N`이 있는지 확인한다.
- 같은 검사를 PR에서 CI가 다시 한다.
- Claude Code hook은 모두 없앤다. 린트 설정이 규칙을 잡는지 보던 hook 테스트의 사례는 `scripts/lint-rules_test.sh`로 옮겨 `make check`에 남긴다.
- AI 규칙 검토는 턴 종료가 아니라 PR의 CI에서 한다(0073).

## 이유

- 검사가 필요한 때는 변경을 기록하는 때다. 작업 중간의 상태는 검사를 통과하지 않아도 된다.
- git hook은 누가 커밋하든 같게 동작하고, AI 도구에 매이지 않는다.
- `--no-verify`로 hook을 건너뛰어도 CI가 잡는다.
- app-surveyor가 같은 방식으로 일하고 있어 두 저장소의 작업 방식이 같아진다.

## 검토한 다른 방법

- Claude Code hook 유지: 위 배경의 문제가 남는다.
- lefthook, pre-commit 같은 hook 관리 도구: 도구가 하나 더 필요하다. hook이 둘뿐이라 셸 스크립트로 충분하다.
- commitlint: Node.js가 필요하다. 확인할 규칙이 적어서 셸 스크립트로 쓰고 테스트를 붙였다.
