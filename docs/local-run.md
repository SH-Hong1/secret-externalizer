# 로컬에서 띄우기

`compose.yaml`은 postgres, valkey, Keycloak, OpenBao, web, worker를 한 번에 띄워 Keycloak으로 로그인해 볼 수 있게 한다. 이 파일과 `deploy/` 아래의 비밀번호와 시크릿은 모두 `dev-only-…` 형태의 가짜 값이며 로컬 개발에만 쓴다. 공유하거나 배포하는 환경에 그대로 쓰지 않는다.

## 띄우고 내리기

Docker와 Compose v2 이상이 필요하다. 아래의 `docker compose`는 따로 설치한 `docker-compose`로 바꿔 써도 된다.

```sh
docker compose up --build -d   # 처음에는 Keycloak이 realm을 가져오느라 30초쯤 걸린다
docker compose ps              # postgres, valkey, keycloak, openbao는 healthy, web과 worker는 running
docker compose logs -f web
docker compose down            # 데이터(postgres, OpenBao 볼륨)는 남긴다
docker compose down -v         # 볼륨까지 지운다. 다음 기동 때 realm을 새로 가져오고, OpenBao에 보관한 원본과 키도 사라진다
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
- worker는 uid 65532로 실행된다. Linux 호스트의 Docker(네이티브)에서는 bind mount가 호스트 소유권을 그대로 유지하므로, 스캔할 파일과 폴더를 uid 65532가 읽을 수 있어야 한다. 읽을 수 없는 파일과 폴더(예: `0700` 폴더, `0600` 개인키나 `.env`)는 오류 없이 건너뛰고 건너뛴 수를 센다. macOS의 colima나 Docker Desktop에서는 소유권이 바뀌어 보이므로 이 문제가 생기지 않는다.
- worker는 큐에 쌓인 스캔을 하나씩 실행한다. 지금은 일반 파일 수를 세어 스캔 화면에 보여 줄 뿐이다. 실패하면 화면에는 정해진 사유만 보이고 자세한 오류는 `docker compose logs worker`에 남는다.
- worker가 스캔 중에 멈추면 임대(2분)가 끝난 뒤 다시 실행한다. 세 번 멈추면 스캔이 `failed`로 끝난다.

## OpenBao

worker는 스캔에서 찾은 값의 지문을 OpenBao transit으로 계산하고, 판정 전의 원본을 OpenBao KV v2에 보관한다. 설정은 `deploy/openbao/config.hcl`에 있고, 이유는 `docs/decisions/0017-openbao-setup.md`에 있다.

| 서비스 | 내용 |
|---|---|
| `openbao-secrets` | 한 번 실행되고 끝난다. `openbao-secrets` 볼륨에 봉인 해제 키(`unseal/key`)와 AppRole secret-id(`worker/secret-id`, `web/secret-id`)를 난수로 만든다. 이미 있는 파일은 그대로 둔다 |
| `openbao` | 원본을 보관한다. 호스트에 포트를 열지 않는다. 데이터는 `openbao-data` 볼륨에 있다 |
| `openbao-test` | `test` 프로필에서만 뜬다. 통합 테스트용 `-dev` 서버이며 `127.0.0.1:18200`에 열린다. 메모리에만 저장하고 루트 토큰은 `dev-only-openbao-test-root-token`이다 |

- 처음 뜰 때 OpenBao가 스스로 초기화(self-init)한다. transit 키 `fingerprint`, KV v2 마운트 `secret`, AppRole `worker`와 `web`, 정책, 표준 출력 audit device를 만든다. 루트 토큰은 초기화가 끝나면 폐기되어 남지 않는다.
- 다시 띄우면 볼륨의 키로 봉인이 저절로 풀린다. `docker compose restart openbao` 뒤에도 그대로 쓸 수 있다.
- audit 기록은 `docker compose logs openbao`에 나온다. 요청의 값은 HMAC으로 가려져 있다.
- `config.hcl`의 `initialize` 블록은 첫 기동 때만 실행된다. 고친 뒤 반영하려면 두 볼륨을 지우고 다시 띄운다. 보관한 원본도 모두 사라진다.

  ```sh
  docker compose rm -sf openbao openbao-secrets
  docker volume rm secret-externalizer_openbao-data secret-externalizer_openbao-secrets
  docker compose up -d openbao
  ```

- 봉인 해제 키가 데이터와 같은 Docker 볼륨 영역에 있으므로, 이 구성은 개발자 PC 한 대에서만 쓴다.

## 개발용 계정

| 사용자 | 비밀번호 | 역할 | 볼 수 있는 화면 |
|---|---|---|---|
| `dev-user` | `dev-only-user-password` | `user` | 홈, 프로젝트와 스캔 |
| `dev-admin` | `dev-only-admin-password` | `admin` | 홈, 프로젝트와 스캔, 감사 로그(`/audit`) |
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
| openbao-test | `127.0.0.1:18200` | 통합 테스트. `test` 프로필에서만 뜬다 |

`openbao`는 호스트에 포트를 열지 않는다. worker만 compose 네트워크 안에서 `openbao:8200`으로 접속한다.

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
| `BAO_ADDR` | 예 | OpenBao 주소. compose에서는 `http://openbao:8200` |
| `BAO_ROLE_ID` | 예 | AppRole role-id. compose에서는 `se-worker` |
| `BAO_SECRET_ID_FILE` | 예 | AppRole secret-id가 든 파일. 앞뒤 공백은 뗀다. compose에서는 `openbao-secrets` 볼륨의 `worker` 폴더만 `/run/secrets/openbao`에 읽기 전용으로 마운트하고 `/run/secrets/openbao/secret-id`를 쓴다. secret-id는 로그와 오류 메시지에 남지 않는다 |

확인 간격(2초), 임대(2분), 연장 간격(30초)은 환경 변수로 바꾸지 않는다.

## Safari 제약

세션 쿠키(`se_session`, `se_login`)는 언제나 `Secure`다. Chrome과 Firefox는 `http://localhost`를 안전한 출처로 보고 `Secure` 쿠키를 받지만 Safari는 받지 않는다. 그래서 Safari에서는 쿠키가 저장되지 않아 로그인이 이어지지 않는다(이 동작은 Safari에서 직접 확인하지 않았다). 로컬 확인은 Chrome이나 Firefox로 한다.

`curl`로 확인할 때도 같은 점을 알아 둔다. 이 저장소에서 확인한 curl 8.7.1은 `localhost`를 안전한 곳으로 보고 쿠키 파일(`-c`, `-b`)의 `Secure` 쿠키를 `http://localhost`로 보냈다. 이렇게 하지 않는 curl이라면 `-H 'Cookie: se_session=…'`로 쿠키를 직접 넣는다.

## 통합 테스트

valkey가 필요한 테스트는 `TEST_VALKEY_ADDR`가, postgres가 필요한 테스트는 `TEST_DATABASE_URL`이, OpenBao가 필요한 테스트는 `TEST_BAO_ADDR`가 없으면 건너뛴다. `make test-integration`은 셋 다 요구한다. OpenBao 테스트는 원본을 보관하는 `openbao`가 아니라 `test` 프로필의 `openbao-test`를 쓰고, 테스트가 개발용 루트 토큰으로 transit, KV, AppRole을 직접 만든다. compose의 서비스로 실행하려면 다음과 같이 한다. 새 셸에서는 compose 명령마다 `SE_WORKSPACE`가 설정되어 있어야 한다. 없으면 compose가 파일을 읽다가 멈춘다(worker를 띄우지 않는 명령도 같다).

```sh
export SE_WORKSPACE=$HOME/work     # 값은 존재하는 폴더면 된다
docker compose up -d valkey postgres
docker compose --profile test up -d openbao-test
TEST_VALKEY_ADDR=127.0.0.1:16379 \
TEST_DATABASE_URL=postgres://se_app:dev-only-se-app-password@127.0.0.1:15432/secret_externalizer_test \
TEST_BAO_ADDR=http://127.0.0.1:18200 \
make test-integration
```

valkey는 디스크에 아무것도 남기지 않는다. valkey를 다시 띄우면 모든 세션이 사라지고 다시 로그인해야 한다. 메모리는 64MB로 제한하며, 다 차면 기존 키를 지우지 않고 새 쓰기만 거부한다(`noeviction`). 이때 새 로그인은 503 화면을 보고, 이미 로그인한 세션은 그대로 쓸 수 있다.

테스트는 실행마다 `test_<난수>` 스키마를 만들어 쓰고 끝나면 지운다.
