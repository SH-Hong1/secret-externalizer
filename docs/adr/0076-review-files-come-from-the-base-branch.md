# AI 검토는 base 브랜치의 규칙과 판정 스크립트로 한다

- 날짜: 2026-11-25
- 상태: 확정

## 배경

CI의 `review` 작업(0073)은 `pull_request` 이벤트로 돌고, PR의 병합 ref를 체크아웃한다. 그래서 검토자가 읽는 `AGENTS.md`와 `.github/review-prompt.md`, 판정을 내리는 `scripts/review-verdict.sh`가 모두 PR 쪽의 파일이었다. PR이 이 파일들을 바꾸면 자기를 검토하는 기준을 바꿀 수 있었고, 이 파일들은 검토 대상(Go, `go.mod`, 템플릿, SQL)이 아니어서 그 변경은 검토되지도 않았다.

같은 전체 검토에서 두 가지가 더 나왔다. 판정 스크립트는 절 이름이 정확히 `Security`이고 처음 10개 안에 있을 때만 Security 위반으로 보아, `security`라고 쓰거나 11번째에 있는 Security 위반은 `review: override`로 통과되었다. 또 체크아웃이 작업의 토큰을 `.git/config`에 남겨, 읽기 도구를 가진 검토자가 그 토큰을 읽을 수 있었다.

## 결정

- `review` 작업은 체크아웃한 뒤 `AGENTS.md`, `.github/review-prompt.md`, `scripts/review-verdict.sh`를 base 브랜치의 것으로 덮어쓰고 검토한다.
- 이 세 파일이나 `.github/` 아래 파일을 바꾸는 PR도 검토 대상이다. 그 변경은 base의 규칙으로 검토된다.
- 판정 스크립트는 모든 위반을 보고, 절 이름에 대소문자와 상관없이 `security`가 들어 있으면 Security 위반으로 센다. 댓글의 목록은 Security 위반을 앞에 둔다.
- 체크아웃은 `persist-credentials: false`로 토큰을 남기지 않는다. 댓글은 `GH_TOKEN`으로 쓴다.

## 이유

- 검토 기준은 검토받는 변경 밖에 있어야 한다. base의 파일을 쓰면 규칙을 바꾸는 PR도 지금의 규칙으로 검토된다.
- Security 위반은 라벨로 통과시킬 수 없다는 것이 0073의 결정이다. 검토자의 표기나 위반 수에 따라 그 결정이 무너지면 안 된다.

## 알려진 한계

- 워크플로 파일(`.github/workflows/ci.yml`)은 여전히 PR의 것이 돈다. PR이 `review` 작업 자체를 바꾸면 검토를 끌 수 있다. 그런 변경은 검토 대상이 되어 댓글로는 드러나지만 막히지는 않는다. 혼자 쓰는 저장소라 다른 사람의 승인을 필수로 둘 수 없어 받아들인다.
- 규칙을 바꾸는 PR은 바꾸기 전의 규칙으로 검토된다. 새 규칙은 병합된 뒤의 PR부터 적용된다.

## 검토한 다른 방법

- `pull_request_target`으로 base의 워크플로를 돌리기: PR의 코드를 secret이 있는 작업에서 다루게 되어 위험이 더 크다.
- CODEOWNERS와 리뷰 승인 필수: 저장소 주인이 하나라 자기 PR을 승인할 사람이 없다.
