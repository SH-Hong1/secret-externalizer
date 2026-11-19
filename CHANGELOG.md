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
- `internal/db`: postgres 연결 풀(pgx), 내장 마이그레이션, 프로젝트·스캔·작업 저장소. 접속 주소는 `db.DSN` 타입으로 다뤄 로그와 오류 메시지에 남지 않는다.
- 프로젝트 등록: 작업 공간 루트 아래의 폴더를 이름과 상대 경로로 등록한다. 등록할 때는 경로의 모양(빈 값, 절대 경로, 루트 밖으로 나가는 `..` 등)만 확인하고, 폴더가 실제로 있는지와 심볼릭 링크를 풀어도 루트 안인지는 worker가 스캔할 때 확인해 어긋나면 스캔을 `failed`로 끝낸다.
- 프로젝트 이름과 경로, 제외 경로는 앞뒤 공백을 지운 뒤 확인한다. 일반 파일을 거쳐 가는 경로(`file.txt/sub`)는 `ErrNotDirectory`로 구분한다.
- 스캔 요청과 상태 화면: 프로젝트마다 진행 중인 스캔은 하나만 둘 수 있고, 상태 화면은 스캔이 끝날 때까지 스스로 새로 고친다.
- `cmd/worker`: 큐에 쌓인 스캔을 하나씩 실행한다(스캔 방식은 아래 항목). 작업은 postgres에 임대로 잡으며(2분, 30초마다 연장), worker가 멈추면 임대가 끝난 뒤 다시 실행하고 세 번 멈추면 스캔을 `failed`로 끝낸다(경합 상황에서도 네 번째 시도는 내주지 않는다). 실패 사유는 정해진 문구만 화면에 보이고 오류 내용은 worker 로그에만 남는다.
- `deploy/worker/Dockerfile`과 compose의 `worker`: git이 든 alpine 이미지를 root가 아닌 사용자로 실행하고 작업 공간(`SE_WORKSPACE`)을 읽기 전용으로 마운트한다. `SE_WORKSPACE`가 없으면 compose가 안내와 함께 멈춘다.
- OpenBao: compose에 `openbao-secrets`(봉인 해제 키와 AppRole secret-id를 난수로 한 번 만든다), `openbao`(static 봉인, 첫 기동 때 self-init), 통합 테스트용 `openbao-test`(`test` 프로필)를 더했다. `openbao`는 지문을 계산하는 transit 키 `fingerprint`와 판정 전 원본을 두는 KV v2 `secret`을 담고, 다시 띄워도 봉인이 저절로 풀린다. 호스트에 포트를 열지 않는다. 결정 기록 0017, 0018.
- worker가 스캔을 실제로 실행한다. 파일을 UTF-8 또는 EUC-KR 텍스트로 읽고(결정 기록 0019), `rules/`의 규칙으로 betterleaks와 opengrep을 실행하고 키 파일 탐지기를 더해(결정 기록 0020) 찾은 값을 항목으로 저장한다. EUC-KR 파일은 스캔하는 동안 worker의 tmpfs(`/tmp`)에 둔 UTF-8 사본으로 스캔하고 끝나면 지운다. `make scanners`가 `bin/`에 스캐너를 받는다.
- 항목은 값을 저장하지 않는다. postgres에는 지문, 가린 미리보기, 위치, 비밀 값의 가린 주변 줄만 남고, 고객정보는 주변 줄도 원본도 남기지 않는다. 로그와 샘플 데이터 파일(확장자 `log`, `out`, `csv`, `tsv`, `txt`, `sql`, `dat`, `json`이거나, `app.log.1`, `catalina.out-20261101`처럼 회전된 로그이거나, `log`, `logs`, `sample`, `samples`, `data`, `dump` 폴더 아래의 파일)과 고객정보가 탐지된 파일에서는 비밀 값도 주변 줄 없이 목록에 오른다. 주변 줄에서는 그 스캔이 어느 파일에서든 탐지한 값(4바이트 이상, 키 파일 전체는 제외)을 그 값이 나오는 모든 자리에서 가린다. 비밀 값의 원본은 OpenBao에 보관한다. 같은 값을 다시 찾으면 항목의 `id`가 유지된다. 결정 기록 0021, 0024.
- NUL이나 깨진 텍스트가 든 파일 때문에 스캔이 실패하지 않는다. UTF-8 BOM으로 시작하는 파일도 NUL과 UTF-8 유효성을 확인해 맞지 않으면 건너뛰고, 주변 줄의 NUL, 탭을 뺀 제어 문자, 깨진 UTF-8은 `U+FFFD`로 바꿔 저장한다. 경로가 UTF-8이 아니거나 제어 문자가 든 파일은 건너뛴 파일로 센다.
- worker가 스캔을 마칠 때마다 요약 기록(`scan finished`)을 로그에 남긴다. 읽은 파일, 건너뛴 파일, 버린 건수, 항목 수와 함께 도구별 스캐너 오류 수(파일을 일부만 분석한 경우)와 건너뛴 파일에 스캐너가 낸 결과 수를 담는다. 건너뛴 파일이 있으면 그 상대 경로를 처음 50개까지 전체 수와 함께 따로 기록한다(`scan skipped files`).
- 스캔 화면에 탐지 목록이 보인다. 상태, 요청한 사람과 시각(UTC), 커밋 해시, 도구와 규칙 버전, 읽은 파일 수, 분류별 건수, 비밀 값 항목(분류, 종류, 파일과 줄, 가린 미리보기, 탐지한 도구와 규칙, 가린 주변 줄)을 보여 주고, 고객정보는 파일과 종류별로 한 줄(건수, 첫 줄, 형식 미리보기)로 묶어 보여 준다. 건너뛴 파일과 위치가 맞지 않아 버린 건수는 0이 아닐 때만 설명과 함께 보인다. 프로젝트 화면에는 그 프로젝트의 스캔 목록(시각, 상태, 항목 수)이 보인다.
- 스캔이 `failed`로 끝나는 경우: OpenBao에 닿지 않을 때, 스캐너가 끝내지 못했을 때, 30분 제한 시간을 넘겼을 때, 프로젝트 폴더 맨 위에 betterleaks 무시 파일(`.gitleaksignore`, `.betterleaksignore`)이 있을 때. 실패 사유는 정해진 문구만 화면에 보인다.
- worker 환경 변수 `BAO_ADDR`, `BAO_ROLE_ID`, `BAO_SECRET_ID_FILE`, `BETTERLEAKS_BIN`, `OPENGREP_BIN`, `RULES_DIR`, `SCAN_TEMP_DIR`(필수)와 `GIT_BIN`(선택).
- worker가 정상 종료(SIGTERM)할 때 잡고 있던 작업을 돌려주고, 임대를 연장하지 못한 채 임대 시간이 지나면 스스로 멈춘다. 결정 기록 0022.
- 스캔이 JDBC URL을 구성 요소로 나눠 저장한다. `jdbc:mysql://계정:비밀번호@호스트:포트/DB`, `?user=…&password=…` 매개변수(이름에 `password`, `pwd`, `secret`, `token`이 든 매개변수의 값도 비밀번호로 나눈다), SQL Server의 `;user=…;password=…`, Oracle의 `계정/비밀번호@호스트:포트:SID`와 `@//호스트:포트/서비스`에서 계정, 비밀번호, 호스트, 포트가 각각 항목이 되고, URL 전체는 항목으로 남지 않는다. DB 이름과 경로는 항목이 아니다. `${...}` 자리 표시자인 구성 요소는 빼고, 형식을 모르거나 나눌 구성 요소가 없는 URL(`jdbc:h2:mem:test` 등)과 인증 영역 뒤에 `@`가 있는 URL은 지금처럼 URL 항목 하나로 남는다. 항목의 종류에 `port`(화면 표시 "포트")를 더했다(마이그레이션 `0004_port_kind.sql`). 결정 기록 0026.

### 변경

- web은 `DATABASE_URL`을 필수로 읽고 기동할 때 마이그레이션을 실행한다.
- `web -h`는 사용법을 표준 출력에 쓰고 종료 코드 0으로 끝난다. 플래그 오류는 한 번만 출력한다.
- `make lint`, `make fmt`는 `bin/`에 도구가 없으면 먼저 설치한다.
- 종료 시 검토가 `go.mod` 변경을 확인하고, 결정 기록 없는 의존성 추가를 지적한다.
- 종료 시 규칙 검토를 `agent` hook 대신 `.claude/hooks/stop-review.sh`(command hook)가 실행한다. 스크립트가 변경분을 만들어 hook을 끈 `claude -p` 세션에 넘기고, 위반 목록을 받아 막을지 정한다. Go 소스, `go.mod`, 템플릿, 마이그레이션 파일이 바뀌지 않았거나 이미 승인한 변경분이면 검토하지 않는다. 한 번 되돌려 보낸 턴에서는 Security 절 위반만 다시 막는다. 검토기가 실행되지 않으면 막지 않고 메시지를 남긴다.
- 실행 중인 백그라운드 작업이 있으면 종료 hook이 `make check`와 규칙 검토를 실행하지 않는다. 그 작업이 끝나 턴이 다시 종료될 때 실행한다.
- 결정 기록 0014 (종료 시 규칙 검토를 command hook으로)

### 수정

- 이름이 `testdata`로 시작하는 패키지(예: `internal/testdatabase`)가 린트에서 빠지던 문제
- 저장소가 `testdata`라는 이름의 폴더 아래에 있으면 수정 hook이 모든 파일을 건너뛰던 문제
- 수정 hook이 상대 경로 입력을 현재 폴더 기준으로 해석하던 문제. 저장소 루트 기준으로 해석한다.
- 하위 에이전트의 결과를 기다리는 동안 종료 시 검토가 "계획이 끝나지 않았다"는 이유로 턴 종료를 반복해서 막던 문제
- 종료 시 검토가 Bash 권한이 거부되어 변경분을 보지 못한 채 판정하던 문제
- 프로젝트 이름에 제어 문자나 깨진 UTF-8이 있으면 500이 나던 문제. 400과 함께 양식을 다시 보여준다.
- web의 데이터베이스 호출이 요청마다 5초 제한을 받는다. 데이터베이스가 멈추면 화면이 멈추지 않고 500 화면이 나온다.
- web과 worker가 데이터베이스에 연결할 때 3초 제한을 받는다. DSN에 `connect_timeout`이 있으면 그 값을 쓴다. 응답하지 않는 주소에 연결하다가 기동이 멈추지 않는다.
- `Migrate`가 바이너리가 모르는 스키마(적용 기록에 파일이 없는 버전, 이미 적용된 최고 버전보다 낮은 미적용 파일)를 만나면 아무것도 적용하지 않고 버전을 적은 오류로 멈춘다.
- 프로젝트 경로와 제외 경로에 제어 문자(줄바꿈 등)가 있으면 등록할 때 거부한다.
