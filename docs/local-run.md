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
- worker는 큐에 쌓인 스캔을 하나씩 실행한다. 파일마다 UTF-8이나 EUC-KR 텍스트로 읽고, betterleaks, opengrep, 키 파일 탐지기를 돌려 찾은 값을 항목으로 저장한다. postgres에는 값의 지문, 가린 미리보기, 위치, 가린 주변 줄만 남고, 비밀 값의 원본은 OpenBao에 보관한다. 고객정보는 원본을 보관하지 않고 주변 줄도 남기지 않는다. 실패하면 화면에는 정해진 사유만 보이고 자세한 오류는 `docker compose logs worker`에 남는다.
- 스캔 하나는 30분을 넘기면 `scan took longer than the time limit`으로 실패한다. 20MiB가 넘는 파일, 바이너리 파일, UTF-8도 EUC-KR도 아닌 파일은 텍스트로 스캔하지 않고 건너뛴 수에 센다. 키 저장소(`.jks`, `.p12` 등)는 바이너리라도 키 파일 항목이 된다.
- 프로젝트 폴더 맨 위에 `.gitleaksignore`나 `.betterleaksignore`가 있으면 스캔하지 않고 `project has a scanner ignore file (.gitleaksignore or .betterleaksignore); remove it and scan again`으로 실패한다. betterleaks가 이 파일에 적힌 결과를 숨기고, 이를 끄는 옵션이 없기 때문이다. 작업 폴더에서 파일을 지우거나 이름을 바꾼 뒤 다시 스캔한다. 하위 폴더에 있는 같은 이름의 파일은 betterleaks가 읽지 않으므로 상관없다.
- EUC-KR 파일은 스캔하는 동안 UTF-8 사본을 worker의 `/tmp`(tmpfs, 1GiB)에 두고 스캔이 끝나면 지운다. EUC-KR 파일이 1GiB 가까이 되는 프로젝트를 스캔하려면 `compose.yaml`에서 크기를 늘린다. tmpfs는 호스트 메모리를 쓴다.
- OpenBao에 닿지 않으면 스캔은 `the secret store could not be reached; see the worker log`로 실패하고 아무것도 저장하지 않는다.
- 커밋 해시는 프로젝트 폴더가 git 저장소 안에 있을 때만 기록한다. 저장소가 아니거나 커밋이 없으면 비워 둔다.
- worker가 스캔 중에 멈추면 임대(2분)가 끝난 뒤 다시 실행한다. 세 번 멈추면 스캔이 `failed`로 끝난다.

## 스캔을 끝까지 해 보기

예제 프로젝트(값은 모두 가짜다)로 등록부터 탐지 목록까지 확인한다.

```sh
export SE_WORKSPACE=$HOME/work                  # 홈 폴더 아래의 폴더
mkdir -p $SE_WORKSPACE
cp -R internal/ruleset/testdata/legacy-shop $SE_WORKSPACE/
docker compose up --build -d
```

1. `dev-user`로 로그인하고 `/projects/new`에서 이름 `legacy-shop`, 경로 `legacy-shop`으로 등록한다.
2. 프로젝트 화면에서 "스캔 시작"을 누른다. 스캔 화면은 끝날 때까지 3초마다 새로 고쳐진다.
3. 끝나면 스캔 화면에 다음이 보인다.
   - 상태, 요청한 사람과 시각(UTC), 커밋 해시(예제는 git 저장소가 아니므로 "없음"), 규칙 버전, 도구 버전, 읽은 파일 수
   - 분류별 건수와 `발견 N건`. 위치가 맞지 않아 버린 건수와 건너뛴 파일 수는 0이 아닐 때만 보인다.
   - 비밀 값 항목마다 분류, 종류, 파일과 줄, 가린 미리보기(`dev-****`처럼 앞 네 글자만), 탐지한 도구와 규칙, 줄 번호가 붙은 가린 주변 줄. 값이 있는 줄에는 `>` 표시가 붙는다. 주변 줄을 저장하지 않은 항목은 그 사실이 적혀 있다.
   - 고객정보는 파일과 종류별로 한 줄(건수, 첫 줄, 형식 미리보기)이고 주변 줄은 없다.
4. 프로젝트 화면의 스캔 목록에서 이전 스캔을 본다. 예제 파일에서 값을 지우고 다시 스캔하면 그 항목만 사라지고 나머지 항목은 같은 항목으로 이어진다.
5. 끝나면 `$SE_WORKSPACE/legacy-shop`을 지운다. OpenBao에는 프로젝트의 마지막 스캔이 찾은 비밀 값의 원본이 남는다. 같은 프로젝트를 다시 스캔하면 그 스캔에 없는 값의 원본이 정리된다.

스캔 결과 화면은 postgres에 저장된 가린 값만 보여 주므로 화면에서 원본 값을 볼 방법은 없다.

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
| `BETTERLEAKS_BIN` | 예 | betterleaks 실행 파일의 절대 경로. compose에서는 `/usr/local/bin/betterleaks` |
| `OPENGREP_BIN` | 예 | opengrep 실행 파일의 절대 경로. compose에서는 `/usr/local/bin/opengrep` |
| `RULES_DIR` | 예 | 탐지 규칙 폴더의 절대 경로. `betterleaks.toml` 파일과 `opengrep` 폴더가 있어야 한다. compose에서는 `/rules` |
| `SCAN_TEMP_DIR` | 예 | 스캐너의 HOME(`home`)과 TMPDIR(`tmp`), 스캔마다의 EUC-KR 사본(`copies/<스캔 ID>`)을 두는 폴더의 절대 경로. 없으면 worker가 만든다. tmpfs 위에 둔다. compose에서는 `/tmp/scan` |
| `GIT_BIN` | | 커밋 해시를 읽는 git 실행 파일. 비우면 `/usr/bin/git`이 있을 때 그것을 쓰고, 없으면 커밋 해시를 기록하지 않는다 |

실행 파일이나 규칙 폴더가 없으면 worker가 기동하지 않는다. 기동할 때 규칙 버전을 계산해 로그에 남긴다.

확인 간격(2초), 임대(2분), 연장 간격(30초), 스캔 제한 시간(30분), 파일 크기 한도(20MiB)는 환경 변수로 바꾸지 않는다.

## Safari 제약

세션 쿠키(`se_session`, `se_login`)는 언제나 `Secure`다. Chrome과 Firefox는 `http://localhost`를 안전한 출처로 보고 `Secure` 쿠키를 받지만 Safari는 받지 않는다. 그래서 Safari에서는 쿠키가 저장되지 않아 로그인이 이어지지 않는다(이 동작은 Safari에서 직접 확인하지 않았다). 로컬 확인은 Chrome이나 Firefox로 한다.

`curl`로 확인할 때도 같은 점을 알아 둔다. 이 저장소에서 확인한 curl 8.7.1은 `localhost`를 안전한 곳으로 보고 쿠키 파일(`-c`, `-b`)의 `Secure` 쿠키를 `http://localhost`로 보냈다. 이렇게 하지 않는 curl이라면 `-H 'Cookie: se_session=…'`로 쿠키를 직접 넣는다.

## 스캐너 실행 파일

worker는 betterleaks와 opengrep을 외부 명령으로 실행한다. 두 도구의 버전과 운영체제·아키텍처별 내려받기 주소, SHA-256 체크섬은 `deploy/worker/scanners.lock`에 고정되어 있다. worker 이미지는 빌드할 때 이 파일대로 이미지의 플랫폼에 맞는 실행 파일을 받아 `/usr/local/bin`에 넣는다.

호스트에서 스캐너를 직접 실행해 보려면 현재 플랫폼의 실행 파일을 `bin/`에 받는다. lint 도구가 있는 폴더와 같다.

```sh
make scanners      # bin/betterleaks, bin/opengrep
bin/betterleaks version
bin/opengrep --version
```

- `deploy/worker/fetch-scanners.sh <os> <arch> <폴더>`가 내려받고 체크섬을 확인한다. 하나라도 맞지 않으면 아무것도 설치하지 않고 실패한다.
- 이 스크립트의 테스트(`make scanners-test`)는 `make check`에 들어 있고, 실제로 내려받지 않는다.
- 버전을 올릴 때는 그 도구의 모든 줄을 바꾸고, 체크섬은 릴리스 페이지(betterleaks는 `checksums.txt`, opengrep은 자산의 digest)에서 가져온다.
- opengrep 실행 파일은 플랫폼마다 160MB가 넘는다.

## 탐지 규칙

두 스캐너가 쓰는 규칙은 저장소 루트의 `rules/`에 있다. worker 이미지는 이 폴더를 `/rules`로 복사한다.

- `rules/betterleaks.toml`: betterleaks 기본 규칙을 켜 두고(`useDefault = true`) 고객정보(전화번호, 주민등록번호, 이메일, 카드번호, 주소)와 제휴사 세션(쿠키, Bearer 토큰) 규칙을 더한다. 고객정보 규칙은 확장자가 `log`, `csv`, `tsv`, `txt`, `sql`, `dat`, `json`인 파일과 `log`, `logs`, `sample`, `samples`, `data`, `dump` 폴더 아래의 파일에만 적용한다.
- `rules/opengrep/*.yml`: Spring XML의 dataSource, `.properties`와 `application*.yml`의 접속 정보, Java 문자열 리터럴, 셸 스크립트의 ssh 인자, Maven과 Ant 설정의 비밀번호를 찾는다.
- 규칙마다 분류와 종류를 적는다(betterleaks는 `tags`의 `category:`, `kind:`, opengrep은 `metadata`의 `category`, `kind`). 규칙은 값만 잡는다. 따옴표, 키 이름, XML 태그는 잡은 범위에 넣지 않는다.
- 규칙 버전(`internal/ruleset.Version`)은 `rules/` 아래 파일들의 이름과 내용으로 계산한다. 한 글자만 바꿔도 버전이 바뀐다.

규칙을 고치면 예제 프로젝트 `internal/ruleset/testdata/legacy-shop`으로 확인한다. 이 프로젝트의 값은 모두 가짜다. 찾아야 하는 것은 `internal/ruleset/testdata/expected.json`에 `{file, line, category, kind, value}`로 적혀 있고(`value`는 잡아야 하는 값의 정확한 글자), 규칙 테스트는 실제 스캐너로 예제를 스캔해 이 목록과 정확히 같은지 본다. 같은 결과가 두 번 나와도 차이로 본다. 예제를 `data`, `build.xml` 같은 이름의 폴더 아래에 두고 스캔해도 결과가 같은지도 본다.

```sh
make scanners                                   # 처음 한 번
go test -count=1 -v -run 'TestRules|TestVersion' ./internal/ruleset/
```

- 규칙 테스트는 `BETTERLEAKS_BIN`, `OPENGREP_BIN`이 가리키는 실행 파일을 쓰고, 없으면 저장소의 `bin/`에서 찾는다. 둘 다 없으면 "run make scanners" 안내와 함께 건너뛴다. `bin/`에 스캐너가 있으면 `make check`에서도 실행된다.
- `make test-integration`은 두 변수를 `bin/`의 실행 파일로 넘긴다. 다른 실행 파일을 쓰려면 `make test-integration BETTERLEAKS_BIN=… OPENGREP_BIN=…`처럼 준다.
- 예제의 `local/` 폴더는 예제 자신의 `.gitignore`에 적혀 있어 이 저장소에서도 무시된다. 이미 올라간 파일은 그대로 추적되지만, 이 폴더에 새 파일을 더할 때는 `git add -f`로 올린다.

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
