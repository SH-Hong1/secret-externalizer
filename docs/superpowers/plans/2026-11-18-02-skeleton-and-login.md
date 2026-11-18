# 골격과 로그인 Implementation Plan (계획 2/7)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `docker compose up`으로 띄운 뒤 Keycloak으로 로그인하면 역할(`user`, `admin`)에 따라 볼 수 있는 빈 화면이 갈리는 web 서버 골격을 만든다.

**Architecture:** `cmd/web`의 `run`이 환경 변수에서 설정을 읽어 valkey 클라이언트, OIDC 인증기, 세션 저장소를 만들고 `web.NewServer`에 넘긴다. OIDC 흐름은 `internal/auth`, 세션과 로그인 시도 보관은 `internal/session`, HTTP 처리는 `internal/web`이 맡는다. `internal/web`은 자신이 쓰는 인터페이스를 직접 정의하므로 handler 테스트는 손으로 쓴 fake로 돌고, 실제 Keycloak과 valkey는 compose에서만 필요하다.

**Tech Stack:** Go 1.27 (`net/http` `ServeMux`, `html/template`, `log/slog`, `http.CrossOriginProtection`), `github.com/coreos/go-oidc/v3`, `golang.org/x/oauth2`, `github.com/valkey-io/valkey-go`, `github.com/google/go-cmp`(테스트), Keycloak, valkey, postgres, docker compose

**Spec:** `docs/superpowers/specs/2026-11-18-secret-externalizer-design.md` (5절 "구성", 7절 "인증과 권한", 10절 2단계). 결정 기록 0001, 0005.

## 검토 때 정해 주실 것

설계 문서가 정하지 않아 이 계획에서 채운 내용이다. 각 항목의 첫 번째가 이 계획이 따르는 안이다.

1. **`cmd/worker`는 계획 3으로 미룬다.** worker가 하는 일은 작업 큐에서 스캔을 받아 실행하는 것인데, 작업 큐와 스캔이 계획 3에 있다. 지금 만들면 아무 일도 하지 않는 바이너리와 컨테이너가 생긴다. 다른 안: 이 계획에서 빈 worker를 만들어 compose 구성만 먼저 맞춘다.
2. **화면은 스타일 없는 최소 HTML로 만든다.** tailwindcss 빌드와 htmx는 시각 디자인을 확정하는 계획 4에서 넣는다. 다른 안: 이 계획에서 tailwind standalone CLI 빌드까지 넣는다.
3. **앱의 postgres 연결은 계획 3으로 미룬다.** 이 계획에는 앱이 저장할 테이블이 없다. compose의 postgres는 Keycloak이 쓰고, 앱용 데이터베이스는 비어 있는 채로 만들어 둔다. 로그인 자체는 감사 로그에 남기지 않는다(설계 7절의 감사 대상은 판정, 적용, 원본 보기다).
4. **세션 쿠키는 항상 `Secure`다.** 로컬에서 `http://localhost`로 쓸 때 Chrome과 Firefox는 `Secure` 쿠키를 받지만 Safari는 받지 않는다. 다른 안: compose에 TLS를 끝내는 프록시 컨테이너를 추가해 `https://localhost`로 쓴다.
5. **세션 수명은 유휴 30분, 최대 8시간이다.** 로그인 시도(state, nonce, PKCE verifier)는 10분 동안 유효하다.
6. **로그아웃은 앱 세션을 지운 뒤 Keycloak의 로그아웃 주소로 보낸다.** ID 토큰을 세션에 보관하지 않으므로 Keycloak이 로그아웃 확인 화면을 한 번 보여준다. 다른 안: ID 토큰을 세션에 보관해 확인 화면 없이 로그아웃한다.
7. **`admin`은 `user`가 할 수 있는 일을 모두 할 수 있다.** Keycloak에서 `admin`만 받은 사용자도 일반 화면을 쓴다. 두 역할이 모두 없는 사용자는 로그인이 거부된다.

## Global Constraints

- `AGENTS.md`의 모든 규칙을 따른다. 작업 하나를 끝낼 때마다 `make check`가 통과해야 한다.
- 코드, 주석, 커밋 메시지는 영문, `CHANGELOG.md`와 `docs/` 아래는 한글로 쓴다.
- 의존성을 추가할 때마다 `docs/decisions/`에 결정 기록을 쓴다. 버전은 구현 시점의 최신 안정 버전을 확인해 `go.mod`에 고정한다.
- 라이브러리와 Keycloak의 API, 설정 이름은 기억에 의존하지 말고 구현 시점의 공식 문서로 확인한다.
- 컨테이너 이미지는 태그와 digest로 고정한다. compose가 호스트에 여는 포트는 모두 `127.0.0.1`에만 묶는다.
- OIDC는 Authorization Code + PKCE(S256) 흐름이다.
- 세션은 valkey에 두고 쿠키에는 세션 ID만 담는다. 쿠키는 `HttpOnly`, `Secure`, `SameSite=Lax`다.
- 역할은 `user`와 `admin` 두 가지다.
- 상태를 바꾸는 요청은 `POST`이고 CSRF 방어를 거친다.
- 클라이언트 시크릿, 세션 ID, 토큰은 로그, 오류 메시지, 템플릿에 나오지 않는다. 이런 값을 담는 타입은 `String()`과 `LogValue()`가 가린 값을 돌려준다.
- 저장소에 넣는 개발용 계정과 시크릿은 가짜임이 분명한 값(`dev-only-...`)을 쓴다.

## Review Focus

1. 콜백의 `state`가 없거나, 다르거나, 이미 한 번 쓴 것이면 400으로 끝나고 세션이 만들어지지 않는다. (Task 6)
2. `return_to`가 `//evil.example`, `https://evil.example`, `/\evil.example`처럼 바깥을 가리키면 로그인 뒤 `/`로 보낸다. (Task 6)
3. 두 역할이 모두 없는 사용자는 403 화면을 보고 세션이 만들어지지 않는다. (Task 4, Task 6)
4. valkey에 닿지 않을 때 요청이 panic이나 로그인 반복으로 빠지지 않고 503 화면으로 끝나며, 오류는 한 번만 로그에 남는다. (Task 6)
5. Keycloak에서 사용자가 로그인을 취소해 콜백에 `error`가 붙어 오면 다시 로그인할 수 있는 화면을 보여준다. (Task 6)
6. 만료된 세션의 쿠키로 요청하면 쿠키를 지우고 로그인으로 보낸다. (Task 6)
7. web이 Keycloak보다 먼저 뜨면 정해진 시간 동안 다시 시도하고, 끝내 닿지 않으면 원인을 알리며 종료한다. (Task 7)

## File Structure

| 파일 | 책임 |
|---|---|
| `cmd/web/main.go` | `main`과 `run`. 신호 처리, 의존성 조립, 서버 기동과 종료 |
| `cmd/web/config.go` | 플래그와 환경 변수를 `config` 값으로 읽는다 |
| `internal/web/server.go` | `NewServer`. middleware를 씌운 전체 handler를 만든다 |
| `internal/web/routes.go` | 모든 경로 등록 |
| `internal/web/middleware.go` | panic 복구, 요청 로그, 보안 헤더, 세션 읽기, 로그인과 역할 요구 |
| `internal/web/render.go` | 템플릿을 한 번 파싱하고 버퍼에 그린 뒤 쓴다. 오류 화면 |
| `internal/web/handlers_auth.go` | 로그인 시작, 콜백, 로그아웃 |
| `internal/web/handlers_page.go` | 상태 확인, 홈, 감사 로그 자리 |
| `internal/web/templates/*.html` | 화면 틀, 홈, 감사 로그 자리, 오류 |
| `internal/auth/user.go` | `Role`, `User`, 클레임에서 역할을 뽑는 함수 |
| `internal/auth/secret.go` | `ClientSecret` |
| `internal/auth/attempt.go` | 로그인 시도 한 건의 state, nonce, verifier |
| `internal/auth/oidc.go` | `Authenticator`. 인증 주소 생성, 코드 교환과 ID 토큰 검증 |
| `internal/session/store.go` | valkey에 세션과 로그인 시도를 저장한다 |
| `compose.yaml` | postgres, valkey, keycloak, web |
| `deploy/web/Dockerfile` | web 이미지 |
| `deploy/keycloak/realm.json` | realm, 역할, client, 개발용 사용자 |
| `deploy/postgres/init.sql` | Keycloak용 데이터베이스와 계정 |
| `docs/local-run.md` | 로컬에서 띄우고 로그인하는 방법 |
| `docs/decisions/0010` ~ `0013` | OIDC 라이브러리, valkey 클라이언트, CSRF 방어 방식, 세션 설계 |

---

### Task 1: 설정, 서버 기동과 종료, 상태 확인

**Files:**
- Create: `cmd/web/config.go`, `cmd/web/config_test.go`, `internal/web/server.go`, `internal/web/routes.go`, `internal/web/middleware.go`, `internal/web/handlers_page.go`, `internal/web/server_test.go`
- Modify: `cmd/web/main.go`, `cmd/web/main_test.go`

**Interfaces:**
- Produces:
  - `type config struct { addr, baseURL, valkeyAddr string; oidc auth.Config }` (`cmd/web`). 이 작업에서는 `addr`만 쓰고 나머지 필드는 Task 7에서 채운다.
  - `func loadConfig(args []string, getenv func(string) string) (config, error)`
  - `func NewServer(logger *slog.Logger) http.Handler` (`internal/web`). Task 6에서 인자가 늘어난다.
  - `GET /healthz`는 200과 본문 `ok`를 돌려준다.

환경 변수: `WEB_ADDR`(기본값 `:8080`).

- [ ] **Step 1: 실패하는 테스트 작성**

`cmd/web/config_test.go`

| 테스트 | 입력 | 기대 |
|---|---|---|
| `TestLoadConfig_Defaults` | 환경 변수 없음 | `addr == ":8080"` |
| `TestLoadConfig_Addr` | `WEB_ADDR=127.0.0.1:9000` | `addr == "127.0.0.1:9000"` |

`internal/web/server_test.go` (패키지 `web_test`)

| 테스트 | 요청 | 기대 |
|---|---|---|
| `TestHealthz` | `GET /healthz` | 200, 본문 `ok` |
| `TestSecurityHeaders` | `GET /healthz` | `Content-Security-Policy: default-src 'self'; frame-ancestors 'none'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer` |
| `TestUnknownPath` | `GET /nope` | 404 |
| `TestRequestLog` | `GET /healthz` | 로그 한 줄에 `method`, `path`, `status`, `duration_ms`가 있다. 쿠키와 쿼리 문자열은 없다 |

`cmd/web/main_test.go`

```go
func TestRun_ServesUntilContextIsCancelled(t *testing.T) {
	// run을 goroutine에서 WEB_ADDR=127.0.0.1:0으로 띄운다.
	// stderr를 io.Pipe로 받아 "listening" 로그 줄의 addr 속성을 읽는다.
	// GET http://<addr>/healthz 가 200인지 확인한다.
	// context를 취소하고, run이 nil을 돌려주는지 채널로 기다린다.
}
```

- [ ] **Step 2: 실패 확인** — `go test ./cmd/web ./internal/web`. 기대: `loadConfig`, `web.NewServer`가 없어 컴파일 실패.

- [ ] **Step 3: 구현**

- `run`은 `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)`로 context를 감싼다.
- `http.Server`의 제한 시간: `ReadHeaderTimeout` 5초, `ReadTimeout` 15초, `WriteTimeout` 30초, `IdleTimeout` 120초.
- context가 취소되면 `Shutdown`을 10초 제한으로 호출한다. `http.ErrServerClosed`는 오류로 보지 않는다.
- 로그는 `slog.NewJSONHandler(stderr, nil)`. 포트를 연 뒤 `logger.InfoContext(ctx, "listening", "addr", listener.Addr().String())`를 남긴다.
- middleware 순서(바깥에서 안으로): panic 복구, 요청 로그, 보안 헤더.
- 기존 `-version`, `-h` 동작과 테스트는 그대로 둔다.

- [ ] **Step 4: 통과 확인** — `make check`

- [ ] **Step 5: 커밋** — `feat(web): serve healthz with graceful shutdown`

---

### Task 2: 템플릿과 화면 그리기

**Files:**
- Create: `internal/web/render.go`, `internal/web/render_test.go`, `internal/web/templates/layout.html`, `internal/web/templates/error.html`
- Modify: `internal/web/server.go`, `internal/web/routes.go`

**Interfaces:**
- Produces (`internal/web`, 비공개):
  - `type renderer struct`, `func newRenderer() *renderer`. `embed.FS`의 템플릿을 모두 파싱하고, 실패하면 panic한다.
  - `func (rd *renderer) page(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, name string, data any)`
  - `func (rd *renderer) fail(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, err error)`. `error.html`을 그리고, 500 이상이면 `err`를 한 번 로그에 남긴다. 화면에는 `err`의 내용을 쓰지 않는다.

오류 화면 문구:

| 상태 | 문구 |
|---|---|
| 400 | 요청을 처리할 수 없습니다. 처음부터 다시 로그인해 주세요. |
| 403 | 이 화면을 볼 권한이 없습니다. |
| 404 | 화면을 찾을 수 없습니다. |
| 500 | 처리 중 문제가 생겼습니다. 잠시 뒤 다시 시도해 주세요. |
| 503 | 세션 저장소에 연결할 수 없습니다. 잠시 뒤 다시 시도해 주세요. |

- [ ] **Step 1: 실패하는 테스트 작성** (`render_test.go`, 패키지 `web`. 비공개 `renderer`를 직접 부른다)

| 테스트 | 기대 |
|---|---|
| `TestRenderer_FailHidesError` | `fail(…, 500, errors.New("dial tcp 10.0.0.5"))`의 본문에 `10.0.0.5`가 없고 500 문구가 있다. 로그에는 오류가 한 줄 있다 |
| `TestRenderer_UnknownTemplate` | 없는 템플릿 이름으로 `page`를 부르면 500 화면이 나오고, 본문 앞부분에 다른 화면의 조각이 섞이지 않는다 |
| `TestUnknownPath`(Task 1의 것을 고친다) | 404 본문에 404 문구가 있고 `Content-Type`이 `text/html; charset=utf-8`이다 |

- [ ] **Step 2: 실패 확인**
- [ ] **Step 3: 구현** — `bytes.Buffer`에 그린 뒤 성공했을 때만 헤더와 본문을 쓴다. `layout.html`에는 `lang="ko"`와 제목, 본문 블록만 둔다.
- [ ] **Step 4: 통과 확인** — `make check`
- [ ] **Step 5: 커밋** — `feat(web): render pages through a buffer with error pages`

---

### Task 3: 사용자, 역할, 시크릿 타입

**Files:**
- Create: `internal/auth/user.go`, `internal/auth/secret.go`, `internal/auth/attempt.go`, `internal/auth/user_test.go`, `internal/auth/secret_test.go`, `internal/auth/attempt_test.go`

**Interfaces:**
- Produces:

```go
type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

type User struct {
	Subject string
	Name    string
	Roles   []Role
}

// Has reports whether u may do what r allows. RoleAdmin includes RoleUser.
func (u User) Has(r Role) bool

// ErrNoRole means the identity provider gave the user neither role.
var ErrNoRole = errors.New("user has no role in this application")

// rolesFromClaim keeps the known roles in names and drops the rest.
func rolesFromClaim(names []string) ([]Role, error) // ErrNoRole when none is known

type ClientSecret string
func (ClientSecret) String() string        // "[redacted]"
func (ClientSecret) LogValue() slog.Value  // "[redacted]"
func (s ClientSecret) Reveal() string

type Attempt struct{ State, Nonce, Verifier, ReturnTo string }
func NewAttempt(returnTo string) (Attempt, error)
func (Attempt) LogValue() slog.Value // redacts State, Nonce, Verifier
```

- [ ] **Step 1: 실패하는 테스트 작성**

| 테스트 | 입력 | 기대 |
|---|---|---|
| `TestUser_Has` (표) | `{user}` | `Has(user)` 참, `Has(admin)` 거짓 |
| | `{admin}` | 둘 다 참 |
| | 역할 없음 | 둘 다 거짓 |
| `TestRolesFromClaim` (표) | `["user","offline_access"]` | `[user]` |
| | `["admin","user"]` | `[admin user]` |
| | `["offline_access"]`, `nil` | `ErrNoRole` (`errors.Is`) |
| `TestClientSecret_Redacted` | `ClientSecret("dev-only-not-a-secret")` | `fmt.Sprintf("%v %s %+v", …)`와 `slog` 출력 어디에도 원문이 없다. `Reveal()`은 원문이다 |
| `TestNewAttempt` | 두 번 호출 | `State`, `Nonce`, `Verifier`가 서로 다르고 각각 43자 이상이다. `ReturnTo`는 넘긴 값이다 |

- [ ] **Step 2: 실패 확인**
- [ ] **Step 3: 구현** — `State`와 `Nonce`는 `crypto/rand` 32바이트를 base64url로, `Verifier`는 `oauth2.GenerateVerifier()`로 만든다.
- [ ] **Step 4: 통과 확인** — `make check`
- [ ] **Step 5: 커밋** — `feat(auth): add user, role, and redacted secret types`

---

### Task 4: OIDC 인증기

**Files:**
- Create: `internal/auth/oidc.go`, `internal/auth/oidc_test.go`, `internal/auth/fakeidp_test.go`, `docs/decisions/0010-go-oidc-and-oauth2.md`
- Modify: `go.mod`, `CHANGELOG.md`

**Interfaces:**
- Consumes: Task 3의 `User`, `ClientSecret`, `rolesFromClaim`, `ErrNoRole`
- Produces:

```go
type Config struct {
	IssuerURL    string       // the issuer as it appears in tokens and in the browser
	DiscoveryURL string       // where this process reaches the issuer; IssuerURL when empty
	ClientID     string
	ClientSecret ClientSecret
	RedirectURL  string       // <base URL>/auth/callback
	LogoutReturn string       // <base URL>/
}

func NewAuthenticator(ctx context.Context, cfg Config, client *http.Client) (*Authenticator, error)
func (a *Authenticator) AuthCodeURL(state, nonce, verifier string) string
func (a *Authenticator) Exchange(ctx context.Context, code, verifier, nonce string) (User, error)
func (a *Authenticator) LogoutURL() string
```

- 요청 범위: `openid profile`.
- 역할은 ID 토큰의 `roles` 클레임(문자열 배열)에서 읽는다. Keycloak이 이 클레임을 넣도록 Task 7의 realm 설정이 맞춘다.
- `Name`은 `preferred_username`, `Subject`는 `sub`.
- `DiscoveryURL`이 `IssuerURL`과 다르면 `oidc.InsecureIssuerURLContext`로 발급자 이름은 `IssuerURL`로 고정한 채 `DiscoveryURL`에서 설정을 읽는다. 컨테이너 안에서는 `keycloak:8080`, 브라우저에서는 `localhost:8081`로 닿는 구성을 위한 것이다.
- `LogoutURL`은 discovery 문서의 `end_session_endpoint`에 `client_id`와 `post_logout_redirect_uri`를 붙인다.

- [ ] **Step 1: 테스트용 가짜 발급자 작성** (`fakeidp_test.go`)

`httptest.Server`로 discovery 문서, JWKS, 토큰 엔드포인트를 제공한다. 테스트마다 RSA 키를 새로 만들고, 토큰 엔드포인트는 테스트가 지정한 클레임으로 서명한 ID 토큰을 돌려준다. 받은 `code_verifier`와 클라이언트 인증 정보를 기록해 테스트가 확인할 수 있게 한다.

- [ ] **Step 2: 실패하는 테스트 작성** (`oidc_test.go`)

| 테스트 | 조건 | 기대 |
|---|---|---|
| `TestAuthCodeURL` | — | 쿼리에 `response_type=code`, `code_challenge_method=S256`, `code_challenge`, 넘긴 `state`와 `nonce`, `redirect_uri`, `scope=openid profile`이 있다 |
| `TestExchange_ReturnsUser` | `roles: ["admin"]` | `User{Subject, Name, Roles: [admin]}` (`cmp.Diff`). 발급자가 받은 `code_verifier`가 넘긴 값이다 |
| `TestExchange_NonceMismatch` | 토큰의 `nonce`가 다르다 | 오류 |
| `TestExchange_WrongAudience` | `aud`가 다른 client다 | 오류 |
| `TestExchange_Expired` | `exp`가 과거다 | 오류 |
| `TestExchange_NoRole` | `roles` 없음 | `errors.Is(err, ErrNoRole)` |
| `TestExchange_ErrorHidesSecret` | 토큰 엔드포인트가 401 | 오류 문자열에 클라이언트 시크릿이 없다 |
| `TestNewAuthenticator_SeparateDiscoveryURL` | `IssuerURL`은 닿지 않는 주소, `DiscoveryURL`은 가짜 발급자 | 생성에 성공하고, 발급자 이름이 `IssuerURL`인 토큰을 받아들인다 |
| `TestLogoutURL` | — | `end_session_endpoint`에 `client_id`와 `post_logout_redirect_uri`가 붙어 있다 |

- [ ] **Step 3: 실패 확인**
- [ ] **Step 4: 구현** — `client`는 `oidc.ClientContext`와 `oauth2.HTTPClient` context 값으로 넘긴다. 결정 기록 0010에 go-oidc와 oauth2를 고른 이유를 쓴다(ID 토큰 서명과 클레임 검증을 직접 구현하지 않는다).
- [ ] **Step 5: 통과 확인** — `make check`
- [ ] **Step 6: 커밋** — `feat(auth): authenticate with OIDC authorization code and PKCE`

---

### Task 5: 세션 저장소

**Files:**
- Create: `internal/session/store.go`, `internal/session/store_test.go`, `docs/decisions/0011-valkey-go-client.md`, `docs/decisions/0013-session-design.md`
- Modify: `go.mod`, `Makefile`, `CHANGELOG.md`

**Interfaces:**
- Consumes: `auth.User`, `auth.Attempt`
- Produces:

```go
const (
	IdleTimeout     = 30 * time.Minute
	AbsoluteTimeout = 8 * time.Hour
	AttemptTimeout  = 10 * time.Minute
)

type ID string
func NewID() (ID, error)             // 32 bytes from crypto/rand, base64url
func (ID) String() string            // "[redacted]"
func (ID) LogValue() slog.Value      // "[redacted]"
func (id ID) Reveal() string         // for the cookie value only

type Session struct {
	User      auth.User
	ExpiresAt time.Time
}

var ErrNotFound = errors.New("session not found")

func NewStore(client valkey.Client, now func() time.Time) *Store
func (s *Store) Create(ctx context.Context, u auth.User) (ID, error)
func (s *Store) Get(ctx context.Context, id ID) (Session, error)
func (s *Store) Delete(ctx context.Context, id ID) error
func (s *Store) PutAttempt(ctx context.Context, a auth.Attempt) (ID, error)
func (s *Store) TakeAttempt(ctx context.Context, id ID) (auth.Attempt, error)
```

- valkey 키는 ID 원문이 아니라 SHA-256 값이다: `session:<hex>`, `attempt:<hex>`. valkey의 내용을 읽을 수 있어도 쿠키를 만들 수 없게 한다.
- 값은 JSON이다. 세션에는 `subject`, `name`, `roles`, `expires_at`만 담는다. 토큰은 담지 않는다.
- `Get`은 남은 수명을 `min(IdleTimeout, ExpiresAt까지 남은 시간)`으로 다시 건다. `ExpiresAt`이 지났으면 지우고 `ErrNotFound`.
- `TakeAttempt`는 읽으면서 지운다(`GETDEL`). 두 번째 호출은 `ErrNotFound`.
- 없는 키는 `ErrNotFound`, 연결 실패는 그 밖의 오류로 구분된다.

- [ ] **Step 1: 실패하는 테스트 작성** (`store_test.go`)

환경 변수 `TEST_VALKEY_ADDR`가 없으면 `t.Skip("set TEST_VALKEY_ADDR to run against valkey")`. 테스트마다 무작위 ID를 쓰므로 서로 간섭하지 않는다. 시각은 주입한 `now`로 움직인다.

| 테스트 | 기대 |
|---|---|
| `TestStore_CreateThenGet` | 저장한 `User`가 그대로 나온다 (`cmp.Diff`). `ExpiresAt`은 `now + 8h` |
| `TestStore_GetUnknown` | `ErrNotFound` |
| `TestStore_Delete` | 지운 뒤 `Get`은 `ErrNotFound`. 없는 ID를 지워도 오류가 아니다 |
| `TestStore_AbsoluteTimeout` | `now`를 8시간 1분 뒤로 옮기면 `ErrNotFound` |
| `TestStore_IdleTTL` | `Create` 직후 키의 TTL이 30분 이하, 29분 이상이다. `now`를 7시간 50분 뒤로 옮겨 `Get`하면 TTL이 10분 이하다 |
| `TestStore_KeyIsNotTheID` | valkey에 `session:<ID 원문>` 키가 없다 |
| `TestStore_AttemptIsTakenOnce` | 첫 `TakeAttempt`는 저장한 값, 두 번째는 `ErrNotFound` |
| `TestStore_Unreachable` | 닫힌 포트를 가리키는 클라이언트로 `Get`하면 오류이고 `errors.Is(err, ErrNotFound)`가 거짓이다. 이 테스트는 valkey 없이도 돈다 |
| `TestID_Redacted` | `fmt`와 `slog` 출력에 원문이 없다 |

- [ ] **Step 2: 실패 확인** — `docker run --rm -d -p 127.0.0.1:6379:6379 <고정한 valkey 이미지>`로 valkey를 띄우고 `TEST_VALKEY_ADDR=127.0.0.1:6379 go test ./internal/session`
- [ ] **Step 3: 구현** — `Makefile`에 `test-integration` 타깃을 추가한다: `TEST_VALKEY_ADDR`를 요구하고 `go test -race ./...`를 실행한다. 결정 기록 0011(valkey-go를 고른 이유), 0013(키 해시, 수명, 토큰을 담지 않는 이유)을 쓴다.
- [ ] **Step 4: 통과 확인** — valkey를 띄운 상태의 `make test-integration`, 띄우지 않은 상태의 `make check`(건너뛴 테스트가 있어도 통과)
- [ ] **Step 5: 커밋** — `feat(session): store sessions and login attempts in valkey`

---

### Task 6: 로그인 흐름, 권한 확인, 화면

**Files:**
- Create: `internal/web/handlers_auth.go`, `internal/web/handlers_auth_test.go`, `internal/web/fakes_test.go`, `internal/web/templates/home.html`, `internal/web/templates/audit.html`, `docs/decisions/0012-cross-origin-protection-for-csrf.md`
- Modify: `internal/web/server.go`, `internal/web/routes.go`, `internal/web/middleware.go`, `internal/web/handlers_page.go`, `internal/web/server_test.go`, `CHANGELOG.md`

**Interfaces:**
- Consumes: `auth.User`, `auth.Role`, `auth.Attempt`, `auth.NewAttempt`, `auth.ErrNoRole`, `session.ID`, `session.Session`, `session.ErrNotFound`
- Produces (`internal/web`):

```go
type Authenticator interface {
	AuthCodeURL(state, nonce, verifier string) string
	Exchange(ctx context.Context, code, verifier, nonce string) (auth.User, error)
	LogoutURL() string
}

type SessionStore interface {
	Create(ctx context.Context, u auth.User) (session.ID, error)
	Get(ctx context.Context, id session.ID) (session.Session, error)
	Delete(ctx context.Context, id session.ID) error
}

type AttemptStore interface {
	PutAttempt(ctx context.Context, a auth.Attempt) (session.ID, error)
	TakeAttempt(ctx context.Context, id session.ID) (auth.Attempt, error)
}

func NewServer(logger *slog.Logger, authn Authenticator, sessions SessionStore, attempts AttemptStore) http.Handler
```

경로:

| 경로 | 조건 | 동작 |
|---|---|---|
| `GET /healthz` | 없음 | `ok` |
| `GET /login` | 없음 | 로그인 시도를 저장하고 쿠키 `se_login`을 심은 뒤 발급자의 인증 주소로 303. 쿼리 `return_to`를 시도에 담는다 |
| `GET /auth/callback` | 없음 | 아래 "콜백 처리" |
| `POST /logout` | 로그인 | 세션을 지우고 쿠키를 지운 뒤 `LogoutURL()`로 303 |
| `GET /{$}` | 로그인 | 홈. 사용자 이름과 역할, 로그아웃 버튼을 보여준다 |
| `GET /audit` | `admin` | "감사 로그" 제목만 있는 빈 화면 |

쿠키: `se_session`(수명은 브라우저 세션), `se_login`(`Max-Age` 600). 둘 다 `Path=/`, `HttpOnly`, `Secure`, `SameSite=Lax`.

콜백 처리 순서:

1. 쿼리에 `error`가 있으면 로그인 시도를 지우고 400 화면에 "다시 로그인" 링크를 보여준다.
2. `se_login` 쿠키로 `TakeAttempt`. 없으면 400.
3. 쿼리의 `state`와 시도의 `State`를 `subtle.ConstantTimeCompare`로 비교한다. 다르면 400.
4. `Exchange`. `auth.ErrNoRole`이면 403, 그 밖의 오류는 400으로 보여주고 오류를 로그에 남긴다.
5. 세션을 만들고 `se_session`을 심고 `se_login`을 지운 뒤, 검증한 `ReturnTo`로 303.

`return_to` 검증: `/`로 시작하고, 두 번째 글자가 `/`나 `\`가 아니며, `url.Parse` 결과에 호스트와 스킴이 없어야 한다. 아니면 `/`.

middleware 순서(바깥에서 안으로): panic 복구, 요청 로그, 보안 헤더, `http.CrossOriginProtection`, 세션 읽기. 로그인 요구와 역할 요구는 경로별로 `routes.go`에서 씌운다.

- 세션 읽기: `se_session`이 있으면 `Get`한다. `ErrNotFound`면 쿠키를 지우고 로그인하지 않은 것으로 본다. 그 밖의 오류는 503 화면.
- 로그인 요구: 사용자가 없으면 `GET`은 `/login?return_to=<요청 경로와 쿼리>`로 303, 그 밖의 메서드는 401.
- 역할 요구: `User.Has`가 거짓이면 403 화면.
- 사용자는 비공개 키 타입으로 요청 context에 싣는다.

- [ ] **Step 1: fake 작성** (`fakes_test.go`) — 메모리 맵 기반의 `fakeSessions`, `fakeAttempts`와 고정 값을 돌려주는 `fakeAuthenticator`. 각각 지정한 오류를 돌려주게 할 수 있다.

- [ ] **Step 2: 실패하는 테스트 작성** (`handlers_auth_test.go`, 패키지 `web_test`)

| 테스트 | 조건 | 기대 |
|---|---|---|
| `TestHome_RedirectsToLogin` | 쿠키 없이 `GET /?x=1` | 303, `Location: /login?return_to=%2F%3Fx%3D1` |
| `TestLogin_StartsAttempt` | `GET /login?return_to=/audit` | 303으로 fake의 인증 주소. `se_login` 쿠키의 속성이 위와 같다. 저장된 시도의 `ReturnTo`가 `/audit` |
| `TestCallback_CreatesSession` | 올바른 `state` | 303 `/audit`, `se_session` 쿠키 속성 확인, `se_login` 삭제, 세션 1건 |
| `TestCallback_StateMismatch` | 다른 `state` | 400, 세션 0건 |
| `TestCallback_NoAttemptCookie` | `se_login` 없음 | 400, 세션 0건 |
| `TestCallback_Replay` | 같은 콜백을 두 번 | 두 번째는 400, 세션 1건 |
| `TestCallback_ProviderError` | `?error=access_denied` | 400, 본문에 `/login` 링크, 세션 0건 |
| `TestCallback_NoRole` | `Exchange`가 `auth.ErrNoRole` | 403, 세션 0건 |
| `TestCallback_ReturnTo` (표) | `//evil.example`, `https://evil.example`, `/\evil.example`, 빈 값 | 모두 303 `/` |
| | `/audit?page=2` | 303 `/audit?page=2` |
| `TestHome_ShowsUser` | `user` 세션 | 200, 본문에 이름과 `user`. 이름에 `<script>`를 넣어도 이스케이프된다 |
| `TestAudit_ForbiddenForUser` | `user` 세션 | 403 |
| `TestAudit_AllowedForAdmin` | `admin` 세션 | 200 |
| `TestSession_Expired` | 저장소에 없는 `se_session` | 303 `/login…`, `se_session` 삭제 쿠키 |
| `TestSession_StoreDown` | `Get`이 연결 오류 | 503 화면, 로그에 오류 1줄, `Location` 없음 |
| `TestLogout` | 세션이 있는 `POST /logout`, `Sec-Fetch-Site: same-origin` | 303으로 `LogoutURL()`, 세션 0건, `se_session` 삭제 쿠키 |
| `TestLogout_CrossOrigin` | `Sec-Fetch-Site: cross-site` | 403, 세션이 남아 있다 |
| `TestLogout_Get` | `GET /logout` | 405 |
| `TestLogs_HaveNoSecrets` | 위 흐름 전체를 돌린 로그 | 세션 ID, `state`, `code`가 없다 |

- [ ] **Step 3: 실패 확인**
- [ ] **Step 4: 구현** — 결정 기록 0012에 토큰 방식 대신 표준 라이브러리의 `http.CrossOriginProtection`을 쓰는 이유를 쓴다(추가 의존성이 없고, 폼마다 토큰을 넣지 않아도 된다. 지원 브라우저는 `Sec-Fetch-Site`나 `Origin`을 보낸다).
- [ ] **Step 5: 통과 확인** — `make check`
- [ ] **Step 6: 커밋** — `feat(web): log in through OIDC and gate pages by role`

---

### Task 7: 조립, compose, 실제 로그인 확인

**Files:**
- Create: `compose.yaml`, `deploy/web/Dockerfile`, `deploy/keycloak/realm.json`, `deploy/postgres/init.sql`, `.dockerignore`, `docs/local-run.md`
- Modify: `cmd/web/main.go`, `cmd/web/config.go`, `cmd/web/config_test.go`, `cmd/web/main_test.go`, `CHANGELOG.md`, `docs/next-steps.md`

**Interfaces:**
- Consumes: `auth.NewAuthenticator`, `session.NewStore`, `web.NewServer`

환경 변수:

| 이름 | 필수 | 내용 |
|---|---|---|
| `WEB_ADDR` | | 기본값 `:8080` |
| `WEB_BASE_URL` | 예 | 브라우저에서 보는 주소. 예 `http://localhost:8080` |
| `VALKEY_ADDR` | 예 | 예 `valkey:6379` |
| `OIDC_ISSUER_URL` | 예 | 예 `http://localhost:8081/realms/secret-externalizer` |
| `OIDC_DISCOVERY_URL` | | 예 `http://keycloak:8080/realms/secret-externalizer` |
| `OIDC_CLIENT_ID` | 예 | `secret-externalizer-web` |
| `OIDC_CLIENT_SECRET` | 예 | |

compose 구성:

| 서비스 | 내용 |
|---|---|
| `postgres` | 볼륨에 데이터 보관. `init.sql`이 `keycloak` 데이터베이스와 계정을 만든다. 앱용 데이터베이스 `secret_externalizer`는 비워 둔다. 호스트 포트 없음 |
| `valkey` | 호스트 포트 `127.0.0.1:6379` (통합 테스트용) |
| `keycloak` | postgres를 쓰고 `realm.json`을 기동 때 import한다. 브라우저 주소는 `http://localhost:8081`, 컨테이너 사이 주소는 `http://keycloak:8080`. 두 주소에서 모두 같은 발급자 이름이 나오도록 hostname 설정을 맞춘다 |
| `web` | `deploy/web/Dockerfile`로 빌드. 호스트 포트 `127.0.0.1:8080`. 읽기 전용 루트 파일 시스템, 모든 capability 제거, `no-new-privileges`, root가 아닌 사용자 |

realm 설정: realm `secret-externalizer`, realm 역할 `user`와 `admin`, client `secret-externalizer-web`(confidential, 표준 흐름만, PKCE S256 필수, redirect URI `http://localhost:8080/auth/callback`, 로그아웃 뒤 주소 `http://localhost:8080/`), realm 역할을 ID 토큰의 `roles` 클레임에 넣는 mapper, 개발용 사용자 `dev-user`(역할 `user`), `dev-admin`(역할 `admin`), `dev-norole`(역할 없음). 비밀번호와 client 시크릿은 `dev-only-…` 형태의 값이다.

web 이미지: 여러 단계 빌드, 정적 링크(`CGO_ENABLED=0`), `-trimpath`, `-ldflags "-X main.version=<값>"`, 실행 단계는 셸이 없는 최소 이미지.

- [ ] **Step 1: 실패하는 테스트 작성**

| 테스트 | 조건 | 기대 |
|---|---|---|
| `TestLoadConfig_MissingRequired` (표) | 필수 변수를 하나씩 뺀다 | 오류 메시지에 그 변수 이름이 있다 |
| `TestLoadConfig_DerivedURLs` | `WEB_BASE_URL=http://localhost:8080/` | `RedirectURL == "http://localhost:8080/auth/callback"`, `LogoutReturn == "http://localhost:8080/"` |
| `TestLoadConfig_BadBaseURL` | `WEB_BASE_URL=localhost` | 오류 |
| `TestRun_ErrorHidesClientSecret` | 발급자가 닿지 않는 주소 | 오류 문자열과 stderr에 시크릿이 없다 |
| `TestRun_RetriesDiscovery` | 처음 두 번은 503, 그 뒤 정상인 가짜 발급자 | `run`이 기동해 `listening`을 남긴다 |
| `TestRun_GivesUpOnDiscovery` | 계속 503 | 제한 시간 뒤 오류. 메시지에 발급자 주소가 있다 |

`TestRun_ServesUntilContextIsCancelled`(Task 1)는 가짜 발급자와 `TEST_VALKEY_ADDR`가 필요해지므로, valkey가 없으면 건너뛰게 고친다.

- [ ] **Step 2: 실패 확인**
- [ ] **Step 3: 구현**

- `run`은 설정을 읽고, valkey 클라이언트를 만들고, 발급자 설정을 읽어 인증기를 만든다. 발급자에 닿지 않으면 2초 간격으로 최대 60초 동안 다시 시도한다(`time.Timer` 하나를 다시 건다). 테스트가 간격과 제한을 줄일 수 있도록 `run`이 부르는 비공개 함수의 인자로 둔다.
- 밖으로 나가는 `http.Client`의 제한 시간은 10초다.
- 종료할 때 valkey 클라이언트를 닫는다.

- [ ] **Step 4: 통과 확인** — `make check`, valkey를 띄운 상태의 `make test-integration`

- [ ] **Step 5: compose로 실제 확인** — `docker compose up --build -d` 뒤 브라우저(Chrome)에서 확인한다.

| 순서 | 동작 | 기대 |
|---|---|---|
| 1 | `http://localhost:8080/` | Keycloak 로그인 화면으로 넘어간다 |
| 2 | `dev-user`로 로그인 | 홈에 `dev-user`와 `user`가 보인다 |
| 3 | `http://localhost:8080/audit` | 403 화면 |
| 4 | 로그아웃 | Keycloak의 확인 화면을 거쳐 다시 로그인 화면이 나온다 |
| 5 | `dev-admin`으로 로그인 뒤 `/audit` | "감사 로그" 화면 |
| 6 | `dev-norole`로 로그인 | 403 화면. `/`로 가면 다시 로그인으로 넘어간다 |
| 7 | 로그인한 채 `docker compose stop valkey` 뒤 새로 고침 | 503 화면. `docker compose start valkey` 뒤에는 다시 로그인으로 넘어간다 |
| 8 | `docker compose logs web` | 세션 ID, 시크릿, 토큰이 없다 |
| 9 | `docker compose restart web` | 10초 안에 정상 종료하고 다시 뜬다. 로그인 상태가 유지된다 |

- [ ] **Step 6: 문서** — `docs/local-run.md`에 띄우는 방법, 개발용 계정, Safari 제약을 쓴다. `CHANGELOG.md`에 추가 내용을 쓴다. `docs/next-steps.md`의 계획 표와 남은 항목을 고친다.

- [ ] **Step 7: 커밋** — `feat: run web with keycloak, valkey, and postgres under compose`
