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
- `cmd/web`이 `WEB_ADDR`(기본 `:8080`)에서 HTTP 서버를 띄우고 SIGINT/SIGTERM에서 정상 종료한다. `GET /healthz`, 보안 헤더, 요청 로그, panic 복구를 포함한다.
- 화면을 버퍼에 그린 뒤 성공했을 때만 쓰는 템플릿 렌더러와 오류 화면(400, 403, 404, 500, 503). 알 수 없는 경로는 404 화면을 보여준다.
- `internal/auth`: 사용자, 역할, 로그에 원문이 남지 않는 클라이언트 시크릿, 로그인 시도(state, nonce, PKCE verifier) 타입
- `internal/auth.Authenticator`: OIDC Authorization Code + PKCE 로그인. ID 토큰의 서명, 발급자, 대상, 만료, nonce를 확인하고 `roles` 클레임에서 역할을 읽는다. 발급자에 닿는 주소(`DiscoveryURL`)를 토큰의 발급자 주소와 따로 지정할 수 있다.
- 결정 기록 0010 (go-oidc, oauth2)
- `internal/session`: valkey에 로그인 세션과 로그인 시도를 저장한다. 키는 ID의 SHA-256이고, 세션은 유휴 30분과 절대 8시간, 로그인 시도는 10분 안에 만료된다. 토큰은 저장하지 않는다. `ID`는 로그에 원문이 남지 않는다.
- `make test-integration`: `TEST_VALKEY_ADDR`가 가리키는 valkey를 대상으로 전체 테스트를 실행한다. 주소가 없으면 안내와 함께 실패한다. valkey가 필요한 테스트는 주소가 없으면 건너뛰므로 `make check`는 valkey 없이 통과한다.
- 결정 기록 0011 (valkey-go), 0013 (세션 설계)

### 변경

- `web -h`는 사용법을 표준 출력에 쓰고 종료 코드 0으로 끝난다. 플래그 오류는 한 번만 출력한다.
- `make lint`, `make fmt`는 `bin/`에 도구가 없으면 먼저 설치한다.
- 종료 시 검토가 `go.mod` 변경을 확인하고, 결정 기록 없는 의존성 추가를 지적한다.

### 수정

- panic 복구가 panic 값 대신 값의 타입과 스택만 로그에 남기고, 500 오류 화면을 보여주며, `http.ErrAbortHandler`는 다시 panic으로 넘긴다.
- 이름이 `testdata`로 시작하는 패키지(예: `internal/testdatabase`)가 린트에서 빠지던 문제
- 저장소가 `testdata`라는 이름의 폴더 아래에 있으면 수정 hook이 모든 파일을 건너뛰던 문제
- 수정 hook이 상대 경로 입력을 현재 폴더 기준으로 해석하던 문제. 저장소 루트 기준으로 해석한다.
- 오류 화면 템플릿(`error.html`)이 없으면 시작할 때 멈춘다.
- 응답을 이미 쓰기 시작한 뒤 panic이 나면 오류 화면을 덧붙이지 않고 로그만 남긴다.
- 로그인 시도(`Attempt`)를 `%#v`로 출력하면 state, nonce, PKCE verifier가 드러나던 문제
