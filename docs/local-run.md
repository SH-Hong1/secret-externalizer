# 로컬에서 띄우기

`compose.yaml`은 postgres, valkey, Keycloak, web을 한 번에 띄워 Keycloak으로 로그인해 볼 수 있게 한다. 이 파일과 `deploy/` 아래의 비밀번호와 시크릿은 모두 `dev-only-…` 형태의 가짜 값이며 로컬 개발에만 쓴다. 공유하거나 배포하는 환경에 그대로 쓰지 않는다.

## 띄우고 내리기

Docker와 Compose v2 이상이 필요하다. 아래의 `docker compose`는 따로 설치한 `docker-compose`로 바꿔 써도 된다.

```sh
docker compose up --build -d   # 처음에는 Keycloak이 realm을 가져오느라 30초쯤 걸린다
docker compose ps              # postgres, valkey, keycloak은 healthy, web은 running
docker compose logs -f web
docker compose down            # 데이터(postgres 볼륨)는 남긴다
docker compose down -v         # 볼륨까지 지운다. 다음 기동 때 realm을 새로 가져온다
```

브라우저에서 `http://localhost:8080/`을 열면 Keycloak 로그인 화면으로 넘어간다.

## 개발용 계정

| 사용자 | 비밀번호 | 역할 | 볼 수 있는 화면 |
|---|---|---|---|
| `dev-user` | `dev-only-user-password` | `user` | 홈 |
| `dev-admin` | `dev-only-admin-password` | `admin` | 홈, 감사 로그(`/audit`) |
| `dev-norole` | `dev-only-norole-password` | 없음 | 로그인 뒤 403 |

Keycloak 관리 콘솔(`http://localhost:8081/admin/`)의 계정은 `dev-only-admin` / `dev-only-admin-password`다. 이 계정은 `master` realm이 처음 만들어질 때만 생긴다.

realm 설정은 `deploy/keycloak/realm.json`에 있다. Keycloak은 기동할 때 이 파일을 가져오지만 이미 있는 realm은 덮어쓰지 않는다. 파일을 고친 뒤에는 `docker compose down -v`로 볼륨을 지우고 다시 띄운다.

## 포트

모든 포트는 `127.0.0.1`에만 열린다.

| 서비스 | 호스트 주소 | 용도 |
|---|---|---|
| web | `http://localhost:8080` | 화면 |
| keycloak | `http://localhost:8081` | 로그인 화면, 관리 콘솔 |
| valkey | `127.0.0.1:16379` | 통합 테스트. 로컬에 설치한 redis가 6379를 쓰는 경우가 많아 16379로 연다 |
| postgres | 없음 | 컨테이너끼리만 쓴다 |

## 발급자 주소

브라우저는 Keycloak을 `http://localhost:8081`로, web 컨테이너는 `http://keycloak:8080`으로 부른다. Keycloak은 `KC_HOSTNAME=http://localhost:8081`과 `KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true`로 띄운다. 그래서 토큰의 발급자와 로그인, 로그아웃 주소는 언제나 `localhost:8081`이고, web이 `keycloak:8080`에서 읽은 discovery 문서의 토큰, 키 주소는 `keycloak:8080`이다. web에는 `OIDC_ISSUER_URL`(토큰의 발급자)과 `OIDC_DISCOVERY_URL`(web이 닿는 주소)을 따로 준다.

web은 기동할 때 discovery 문서를 2초 간격으로 최대 60초 동안 다시 읽는다. 그동안 Keycloak이 뜨지 않으면 오류로 끝난다.

## web 환경 변수

| 이름 | 필수 | 내용 |
|---|---|---|
| `WEB_ADDR` | | 듣는 주소. 기본값 `:8080` |
| `WEB_BASE_URL` | 예 | 브라우저에서 보는 주소. 콜백 주소(`/auth/callback`)와 로그아웃 뒤 주소(`/`)를 여기서 만든다 |
| `VALKEY_ADDR` | 예 | 예 `valkey:6379` |
| `OIDC_ISSUER_URL` | 예 | 토큰의 발급자. 예 `http://localhost:8081/realms/secret-externalizer` |
| `OIDC_DISCOVERY_URL` | | web이 발급자에 닿는 주소. 비우면 `OIDC_ISSUER_URL`을 쓴다 |
| `OIDC_CLIENT_ID` | 예 | `secret-externalizer-web` |
| `OIDC_CLIENT_SECRET` | 예 | 로그와 오류 메시지에 남지 않는다 |

## Safari 제약

세션 쿠키(`se_session`, `se_login`)는 언제나 `Secure`다. Chrome과 Firefox는 `http://localhost`를 안전한 출처로 보고 `Secure` 쿠키를 받지만 Safari는 받지 않는다. 그래서 Safari에서는 쿠키가 저장되지 않아 로그인이 이어지지 않는다(이 동작은 Safari에서 직접 확인하지 않았다). 로컬 확인은 Chrome이나 Firefox로 한다.

`curl`로 확인할 때도 같은 점을 알아 둔다. 이 저장소에서 확인한 curl 8.7.1은 `localhost`를 안전한 곳으로 보고 쿠키 파일(`-c`, `-b`)의 `Secure` 쿠키를 `http://localhost`로 보냈다. 이렇게 하지 않는 curl이라면 `-H 'Cookie: se_session=…'`로 쿠키를 직접 넣는다.

## 통합 테스트

valkey가 필요한 테스트는 `TEST_VALKEY_ADDR`가 없으면 건너뛴다. compose의 valkey로 실행하려면:

```sh
docker compose up -d valkey
TEST_VALKEY_ADDR=127.0.0.1:16379 make test-integration
```

valkey는 디스크에 아무것도 남기지 않는다. valkey를 다시 띄우면 모든 세션이 사라지고 다시 로그인해야 한다.
