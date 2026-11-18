# 변경 내역

이 프로젝트에서 달라진 내용을 기록한다. 형식은 [Keep a Changelog](https://keepachangelog.com/ko/1.1.0/)를 따른다. 결정의 이유는 여기에 쓰지 않고 `docs/decisions/`에 쓴다.

## [Unreleased]

### 추가

- Go 모듈과 `cmd/web` 진입점 골격
- 버전을 고정한 gofumpt, golangci-lint 설치와 `make fmt`, `make lint`, `make vet`, `make test`, `make check`
- golangci-lint 설정
- Go 규칙 문서 `AGENTS.md`와 이를 불러오는 `CLAUDE.md`
- 결정 기록 `docs/decisions/` 0001~0006
- Go 파일을 쓰거나 고칠 때마다 포맷과 린트를 검사하는 hook
- 작업을 마칠 때 전체 검사를 실행하고 `AGENTS.md` 준수 여부를 검토하는 hook
- 결정 기록 0007~0009 (go-cmp, errgroup, gosec G204 제외)
- 린트 규칙: `nolint`에 대상 린터와 사유 필수, `slog` 메시지는 상수, `slog` 키는 snake_case, 테스트는 `_test` 패키지
- `make hooks`. `make check`가 hook 스크립트 테스트도 실행한다.
- `AGENTS.md`: 디렉터리 이름 규칙, 링크 시점 버전 변수 예외, `http.Handler` 반환 예외, gosec G204 제외

### 변경

- `web -h`는 사용법을 표준 출력에 쓰고 종료 코드 0으로 끝난다. 플래그 오류는 한 번만 출력한다.
- `make lint`, `make fmt`는 `bin/`에 도구가 없으면 먼저 설치한다.
- 종료 시 검토가 `go.mod` 변경을 확인하고, 결정 기록 없는 의존성 추가를 지적한다.

### 수정

- 이름이 `testdata`로 시작하는 패키지(예: `internal/testdatabase`)가 린트에서 빠지던 문제
- 저장소가 `testdata`라는 이름의 폴더 아래에 있으면 수정 hook이 모든 파일을 건너뛰던 문제
- 수정 hook이 상대 경로 입력을 현재 폴더 기준으로 해석하던 문제. 저장소 루트 기준으로 해석한다.
