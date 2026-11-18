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
- `cmd/web`이 `WEB_ADDR`(기본 `:8080`)에서 HTTP 서버를 띄우고 SIGINT/SIGTERM에서 정상 종료한다. `GET /healthz`, 보안 헤더, 요청 로그, panic 복구를 포함한다. 보안 헤더는 CSP(`default-src 'self'; base-uri 'none'; frame-ancestors 'none'`), `X-Content-Type-Options`, `Referrer-Policy`, `Cache-Control: no-store`다. panic 복구는 panic 값 대신 요청의 메서드, 경로, 값의 타입, 스택만 로그에 남기고 500 오류 화면을 보여준다. 응답을 이미 쓰기 시작한 뒤라면 오류 화면을 덧붙이지 않고 로그만 남기며, `http.ErrAbortHandler`는 다시 panic으로 넘긴다. net/http 자체의 오류 로그도 같은 JSON 로그로 나간다.
- 화면을 버퍼에 그린 뒤 성공했을 때만 쓰는 템플릿 렌더러와 오류 화면(400, 403, 404, 500, 503). 알 수 없는 경로는 404 화면을 보여준다. 오류 화면 템플릿(`error.html`)이 없으면 시작할 때 멈춘다.
- `internal/auth`: 사용자, 역할, 클라이언트 시크릿, 로그인 시도(state, nonce, PKCE verifier) 타입. 클라이언트 시크릿과 로그인 시도의 비밀 값은 `fmt`의 모든 형식, `slog`, `encoding/json`에서 가려지며, 다른 구조체의 필드로 들어 있을 때도 가려진다.
- `internal/auth.Authenticator`: OIDC Authorization Code + PKCE 로그인. ID 토큰의 서명, 발급자, 대상, 만료, nonce를 확인하고 `roles` 클레임에서 역할을 읽는다. 발급자에 닿는 주소(`DiscoveryURL`)를 토큰의 발급자 주소와 따로 지정할 수 있으며, 이때도 discovery 문서의 `issuer`가 설정한 발급자와 다르면 만들어지지 않는다. 제한 시간(`Timeout`)이 없는 HTTP 클라이언트는 거부한다.
- 결정 기록 0010 (go-oidc, oauth2)
- `internal/session`: valkey에 로그인 세션과 로그인 시도를 저장한다. 키는 ID의 SHA-256이고, 세션은 유휴 30분과 절대 8시간, 로그인 시도는 10분 안에 만료된다. 토큰은 저장하지 않는다. `ID`는 `fmt`, `slog`, `encoding/json`에서 원문이 남지 않는다.
- `make test-integration`: `TEST_VALKEY_ADDR`가 가리키는 valkey를 대상으로 전체 테스트를 실행한다. 주소가 없으면 안내와 함께 실패한다. valkey가 필요한 테스트는 주소가 없으면 건너뛰므로 `make check`는 valkey 없이 통과한다.
- 결정 기록 0011 (valkey-go), 0013 (세션 설계)
- OIDC 로그인 흐름: `GET /login`이 로그인 시도를 저장하고 발급자로 보내며, `GET /auth/callback`이 시도를 한 번만 꺼내 state를 확인하고 코드를 교환한 뒤 세션을 만든다. 같은 브라우저에 이전 로그인의 세션이 있으면 지운다. 로그인 후 돌아갈 주소는 이 사이트 안의 2048바이트 이하 경로만 허용한다.
- 역할이 없는 사용자는 로그인 뒤 로그아웃 버튼이 있는 403 화면을 보고, 거부된 로그인은 subject와 함께 INFO 로그 한 줄로 남는다.
- `POST /logout`이 세션을 지우고 발급자의 로그아웃 주소로 보낸다. 세션이 이미 만료되었거나 세션 저장소에 연결할 수 없어도 쿠키를 지우고 발급자의 로그아웃 주소로 보낸다. `GET /logout`은 405다.
- 쿠키 `se_session`(브라우저 세션 동안), `se_login`(10분). 둘 다 `HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/`.
- 로그인이 필요한 화면: 홈(`/`, 사용자 이름과 역할, 로그아웃 버튼)과 `admin`만 볼 수 있는 감사 로그(`/audit`, 제목만). 로그인하지 않은 `GET`은 로그인 화면으로, 그 밖의 요청은 401로 답한다.
- 세션 저장소에 연결할 수 없으면 503 화면을 보여준다. `/healthz`는 세션 저장소를 읽지 않는다.
- 표준 라이브러리의 교차 출처 보호(`http.CrossOriginProtection`)로 다른 출처에서 보낸 상태 변경 요청을 403으로 막는다.
- 401, 405 오류 화면. 400과 401 화면에는 "다시 로그인" 링크가 있다.
- 결정 기록 0012 (교차 출처 보호로 CSRF 방어)
- `cmd/web`이 환경 변수(`WEB_BASE_URL`, `VALKEY_ADDR`, `OIDC_ISSUER_URL`, `OIDC_DISCOVERY_URL`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`)를 읽어 valkey 세션 저장소와 OIDC 로그인을 연결한다. 필수 변수가 없으면 그 이름을 오류에 적고 멈춘다. 콜백 주소와 로그아웃 뒤 주소는 `WEB_BASE_URL`에서 만들며, `WEB_BASE_URL`에는 `/` 말고 다른 경로를 둘 수 없다.
- web은 기동할 때 발급자의 discovery 문서를 2초 간격으로 최대 60초 동안 다시 읽고, 그래도 닿지 않으면 발급자 주소와 실제로 요청한 주소를 적은 오류로 끝난다. 밖으로 나가는 HTTP 요청의 제한 시간은 10초다.
- valkey가 멈추면 세션 읽기가 몇 번만 다시 시도한 뒤 실패해, 화면이 멈추지 않고 503 화면이 나온다.
- `compose.yaml`: postgres, valkey, Keycloak, web을 띄우는 로컬 개발 환경. Keycloak은 기동할 때 `deploy/keycloak/realm.json`(역할 `user`, `admin`, 개발용 사용자 3명, ID 토큰의 `roles` 클레임)을 가져온다. 포트는 `127.0.0.1`에만 열고, valkey는 `16379`로 연다. valkey는 메모리를 64MB로 제한하고 넘치면 지우지 않고 새 쓰기를 거부한다(`noeviction`).
- `deploy/web/Dockerfile`: 정적 링크한 web을 셸이 없는 최소 이미지에서 root가 아닌 사용자로 실행한다. compose에서는 읽기 전용 루트 파일 시스템, 모든 capability 제거, `no-new-privileges`를 적용한다.
- 로컬 실행 안내 `docs/local-run.md`

### 변경

- `web -h`는 사용법을 표준 출력에 쓰고 종료 코드 0으로 끝난다. 플래그 오류는 한 번만 출력한다.
- `make lint`, `make fmt`는 `bin/`에 도구가 없으면 먼저 설치한다.
- 종료 시 검토가 `go.mod` 변경을 확인하고, 결정 기록 없는 의존성 추가를 지적한다.

### 수정

- 이름이 `testdata`로 시작하는 패키지(예: `internal/testdatabase`)가 린트에서 빠지던 문제
- 저장소가 `testdata`라는 이름의 폴더 아래에 있으면 수정 hook이 모든 파일을 건너뛰던 문제
- 수정 hook이 상대 경로 입력을 현재 폴더 기준으로 해석하던 문제. 저장소 루트 기준으로 해석한다.
