# 로컬에서 띄우기

`compose.yaml`은 postgres, valkey, Keycloak, web, worker를 한 번에 띄워 Keycloak으로 로그인해 볼 수 있게 한다. 이 파일과 `deploy/` 아래의 비밀번호와 시크릿은 모두 `dev-only-…` 형태의 가짜 값이며 로컬 개발에만 쓴다. 공유하거나 배포하는 환경에 그대로 쓰지 않는다.

## 띄우고 내리기

Docker와 Compose v2 이상이 필요하다. 아래의 `docker compose`는 따로 설치한 `docker-compose`로 바꿔 써도 된다.

```sh
docker compose up --build -d   # 처음에는 Keycloak이 realm을 가져오느라 30초쯤 걸린다
docker compose ps              # postgres, valkey, keycloak은 healthy, web은 running
docker compose logs -f web
docker compose down            # 데이터(postgres 볼륨)는 남긴다
docker compose down -v         # 볼륨까지 지운다. 다음 기동 때 realm을 새로 가져온다
```

`deploy/postgres/init.sql`은 postgres 볼륨을 처음 만들 때만 실행된다. `se_app` 계정과 테스트용 데이터베이스(`secret_externalizer_test`)를 더하기 전에 만든 볼륨에는 이 둘이 없으므로, `docker compose down -v`로 볼륨을 지우고 다시 띄운다. 볼륨을 지우지 않으려면 `init.sql`의 `se_app` 이하 문장을 `docker compose exec -T postgres psql -U postgres`로 한 번 실행한다.

브라우저에서 `http://localhost:8080/`을 열면 Keycloak 로그인 화면으로 넘어간다.

## 작업 공간과 worker

worker는 등록한 프로젝트 폴더를 읽어 스캔한다. 프로젝트는 작업 공간 루트 아래의 폴더이므로, 루트로 쓸 폴더를 `SE_WORKSPACE`에 절대 경로로 준다. compose가 이 폴더를 worker 컨테이너의 `/workspace`에 읽기 전용으로 마운트한다.

```sh
export SE_WORKSPACE=$HOME/work     # 프로젝트 폴더들이 들어 있는 폴더
docker compose up --build -d
```

- `SE_WORKSPACE`가 없으면 compose가 안내 문구와 함께 멈춘다. worker를 띄우지 않는 명령(`ps`, `down`)에도 compose 파일을 읽을 때 같은 오류가 나므로 셸에 계속 설정해 둔다.
- macOS에서는 Docker가 공유하는 경로(보통 홈 폴더 아래)여야 한다. `/tmp` 같은 경로는 컨테이너에서 빈 폴더로 보인다.
- 프로젝트를 등록할 때의 경로는 `SE_WORKSPACE` 기준의 상대 경로다. 예를 들어 `$HOME/work/shop`을 스캔하려면 `shop`으로 등록한다.
- worker는 큐에 쌓인 스캔을 하나씩 실행한다. 지금은 일반 파일 수를 세어 스캔 화면에 보여 줄 뿐이다. 실패하면 화면에는 정해진 사유만 보이고 자세한 오류는 `docker compose logs worker`에 남는다.
- worker가 스캔 중에 멈추면 임대(2분)가 끝난 뒤 다시 실행한다. 세 번 멈추면 스캔이 `failed`로 끝난다.

## 개발용 계정

| 사용자 | 비밀번호 | 역할 | 볼 수 있는 화면 |
|---|---|---|---|
| `dev-user` | `dev-only-user-password` | `user` | 홈 |
| `dev-admin` | `dev-only-admin-password` | `admin` | 홈, 감사 로그(`/audit`) |
| `dev-norole` | `dev-only-norole-password` | 없음 | 로그인 뒤 403. 이 화면의 로그아웃 버튼으로 Keycloak 세션을 끝내고 다른 계정으로 로그인한다 |

Keycloak 관리 콘솔(`http://localhost:8081/admin/`)의 계정은 `dev-only-admin` / `dev-only-admin-password`다. 이 계정은 `master` realm이 처음 만들어질 때만 생긴다.

realm 설정은 `deploy/keycloak/realm.json`에 있다. Keycloak은 기동할 때 이 파일을 가져오지만 이미 있는 realm은 덮어쓰지 않는다. 파일을 고친 뒤에는 `docker compose down -v`로 볼륨을 지우고 다시 띄운다.

## 포트

모든 포트는 `127.0.0.1`에만 열린다.

| 서비스 | 호스트 주소 | 용도 |
|---|---|---|
| web | `http://localhost:8080` | 화면 |
| keycloak | `http://localhost:8081` | 로그인 화면, 관리 콘솔 |
| valkey | `127.0.0.1:16379` | 통합 테스트. 로컬에 설치한 redis가 6379를 쓰는 경우가 많아 16379로 연다 |
| postgres | `127.0.0.1:15432` | 통합 테스트. 컨테이너끼리는 `postgres:5432`로 쓴다 |

## 발급자 주소

브라우저는 Keycloak을 `http://localhost:8081`로, web 컨테이너는 `http://keycloak:8080`으로 부른다. Keycloak은 `KC_HOSTNAME=http://localhost:8081`과 `KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true`로 띄운다. 그래서 토큰의 발급자와 로그인, 로그아웃 주소는 언제나 `localhost:8081`이고, web이 `keycloak:8080`에서 읽은 discovery 문서의 토큰, 키 주소는 `keycloak:8080`이다. web에는 `OIDC_ISSUER_URL`(토큰의 발급자)과 `OIDC_DISCOVERY_URL`(web이 닿는 주소)을 따로 준다.

web은 기동할 때 discovery 문서를 2초 간격으로 최대 60초 동안 다시 읽는다. 그동안 Keycloak이 뜨지 않으면 오류로 끝난다.

## web 환경 변수

| 이름 | 필수 | 내용 |
|---|---|---|
| `WEB_ADDR` | | 듣는 주소. 기본값 `:8080` |
| `WEB_BASE_URL` | 예 | 브라우저에서 보는 주소. 콜백 주소(`/auth/callback`)와 로그아웃 뒤 주소(`/`)를 여기서 만든다. `/` 말고 다른 경로, 쿼리, 프래그먼트는 둘 수 없다 |
| `VALKEY_ADDR` | 예 | 예 `valkey:6379` |
| `DATABASE_URL` | 예 | postgres 주소. 예 `postgres://se_app:dev-only-se-app-password@postgres:5432/secret_externalizer`. 로그와 오류 메시지에 남지 않는다. web은 기동할 때 마이그레이션을 실행한다 |
| `OIDC_ISSUER_URL` | 예 | 토큰의 발급자. 예 `http://localhost:8081/realms/secret-externalizer` |
| `OIDC_DISCOVERY_URL` | | web이 발급자에 닿는 주소. 비우면 `OIDC_ISSUER_URL`을 쓴다. 여기서 읽은 discovery 문서의 `issuer`가 `OIDC_ISSUER_URL`과 다르면 web이 기동하지 않는다 |
| `OIDC_CLIENT_ID` | 예 | `secret-externalizer-web` |
| `OIDC_CLIENT_SECRET` | 예 | 로그와 오류 메시지에 남지 않는다 |

## worker 환경 변수

| 이름 | 필수 | 내용 |
|---|---|---|
| `DATABASE_URL` | 예 | web과 같은 postgres 주소. worker도 기동할 때 마이그레이션을 실행한다 |
| `WORKSPACE_ROOT` | 예 | 작업 공간 루트. 절대 경로이고 폴더여야 한다. compose에서는 `/workspace` |

확인 간격(2초), 임대(2분), 연장 간격(30초)은 환경 변수로 바꾸지 않는다.

## Safari 제약

세션 쿠키(`se_session`, `se_login`)는 언제나 `Secure`다. Chrome과 Firefox는 `http://localhost`를 안전한 출처로 보고 `Secure` 쿠키를 받지만 Safari는 받지 않는다. 그래서 Safari에서는 쿠키가 저장되지 않아 로그인이 이어지지 않는다(이 동작은 Safari에서 직접 확인하지 않았다). 로컬 확인은 Chrome이나 Firefox로 한다.

`curl`로 확인할 때도 같은 점을 알아 둔다. 이 저장소에서 확인한 curl 8.7.1은 `localhost`를 안전한 곳으로 보고 쿠키 파일(`-c`, `-b`)의 `Secure` 쿠키를 `http://localhost`로 보냈다. 이렇게 하지 않는 curl이라면 `-H 'Cookie: se_session=…'`로 쿠키를 직접 넣는다.

## 통합 테스트

valkey가 필요한 테스트는 `TEST_VALKEY_ADDR`가, postgres가 필요한 테스트는 `TEST_DATABASE_URL`이 없으면 건너뛴다. `make test-integration`은 둘 다 요구한다. compose의 valkey와 postgres로 실행하려면:

```sh
docker compose up -d valkey postgres
TEST_VALKEY_ADDR=127.0.0.1:16379 \
TEST_DATABASE_URL=postgres://se_app:dev-only-se-app-password@127.0.0.1:15432/secret_externalizer_test \
make test-integration
```

valkey는 디스크에 아무것도 남기지 않는다. valkey를 다시 띄우면 모든 세션이 사라지고 다시 로그인해야 한다. 메모리는 64MB로 제한하며, 다 차면 기존 키를 지우지 않고 새 쓰기만 거부한다(`noeviction`). 이때 새 로그인은 503 화면을 보고, 이미 로그인한 세션은 그대로 쓸 수 있다.

테스트는 실행마다 `test_<난수>` 스키마를 만들어 쓰고 끝나면 지운다.
