# 로컬에서 띄우기

`compose.yaml`은 postgres, valkey, Keycloak, OpenBao, web, worker, PDF 변환기(`pdf`)를 한 번에 띄워 Keycloak으로 로그인해 볼 수 있게 한다. 이 파일과 `deploy/` 아래의 비밀번호와 시크릿은 모두 `dev-only-…` 형태의 가짜 값이며 로컬 개발에만 쓴다. 공유하거나 배포하는 환경에 그대로 쓰지 않는다.

## 띄우고 내리기

Docker Engine 26 이상과, 볼륨 마운트의 `subpath`를 지원하는 그에 맞는 Compose가 필요하다. worker와 web은 `openbao-secrets` 볼륨에서 각자의 폴더(`worker`, `web`)만 `volume.subpath`로 마운트하는데, 이전 Engine이나 이 옵션을 모르는 Compose에서는 worker와 web이 뜨지 않는다. 아래의 `docker compose`는 따로 설치한 `docker-compose`로 바꿔 써도 된다.

처음 띄우기 전에 OpenBao의 봉인 해제 키를 호스트에 만든다. 키는 Docker 볼륨 밖의 파일 `~/.config/secret-externalizer/openbao-unseal.key`(바꾸려면 `OPENBAO_UNSEAL_KEY_FILE`)에 있고, 이 파일이 없으면 compose가 openbao를 만들지 못한다(`bind source path does not exist`).

```sh
make openbao-unseal-key        # 한 번만. 이미 있으면 확인만 하고 그대로 둔다
docker compose up --build -d   # 처음에는 Keycloak이 realm을 가져오느라 30초쯤 걸린다
docker compose ps              # postgres, valkey, keycloak, openbao는 healthy, web과 worker는 running
docker compose logs -f web
docker compose down            # 데이터(postgres, OpenBao 볼륨)는 남긴다
docker compose down -v         # 볼륨까지 지운다. 다음 기동 때 realm을 새로 가져오고, OpenBao에 보관한 원본도 사라진다
```

`down -v` 뒤에도 호스트의 봉인 해제 키 파일은 남는다. 빈 저장소는 다음 기동 때 그 키로 다시 초기화되므로 그대로 써도 된다.

`deploy/postgres/init.sql`은 postgres 볼륨을 처음 만들 때만 실행된다. `se_app` 계정과 테스트용 데이터베이스(`secret_externalizer_test`)를 더하기 전에 만든 볼륨에는 이 둘이 없으므로, `docker compose down -v`로 볼륨을 지우고 다시 띄운다. 볼륨을 지우지 않으려면 `init.sql`의 `se_app` 이하 문장을 `docker compose exec -T postgres psql -U postgres`로 한 번 실행한다.

브라우저에서 `http://localhost:8080/`을 열면 Keycloak 로그인 화면으로 넘어간다.

## 작업 공간과 worker

worker는 등록한 프로젝트 폴더를 읽어 스캔한다. 프로젝트는 작업 공간 루트 아래의 폴더이므로, 루트로 쓸 폴더를 `SE_WORKSPACE`에 절대 경로로 준다. compose는 이 폴더를 worker 컨테이너에 두 번 마운트한다.

| 컨테이너 안의 경로 | 마운트 | 쓰는 것 |
|---|---|---|
| `/workspace` (`WORKSPACE_ROOT`) | 읽기 전용 | 스캔과 스캐너(betterleaks, opengrep) |
| `/workspace-rw` (`APPLY_WORKSPACE_ROOT`) | 읽기-쓰기 | 사용자가 확인한 적용과 그 점검 |

스캔은 스캐너에 읽기 전용 마운트의 경로만 넘기고, 사용자가 확인한 적용만 읽기-쓰기 마운트로 파일을 바꾼다. 두 마운트가 막는 것은 스캔과 스캐너가 넘겨받은 경로로 쓰는 일이다. 스캐너(betterleaks, opengrep)는 같은 컨테이너에서 같은 사용자로 돌므로, 잘못 만들어졌거나 바뀐 스캐너는 `/workspace-rw`를 절대 경로로 열어 쓸 수 있다. 이를 줄이는 것은 `deploy/worker/scanners.lock`의 체크섬으로 고정한 스캐너 판본과, 스캔 한 번이 작업 공간에 아무것도 쓰지 않는다는 확인이다. 스캔을 다른 컨테이너나 사용자로 나누는 일은 남은 일이다(`docs/decisions/0032-apply-order-and-resuming.md`, `docs/next-steps.md`).

```sh
export SE_WORKSPACE=$HOME/work     # 프로젝트 폴더들이 들어 있는 폴더
docker compose up --build -d
```

- `SE_WORKSPACE`가 없으면 compose가 안내 문구와 함께 멈춘다. worker를 띄우지 않는 명령(`ps`, `down`)에도 compose 파일을 읽을 때 같은 오류가 나므로 셸에 계속 설정해 둔다.
- macOS에서는 Docker가 공유하는 경로(보통 홈 폴더 아래)여야 한다. `/tmp` 같은 경로는 컨테이너에서 빈 폴더로 보인다.
- 프로젝트를 등록할 때의 경로는 `SE_WORKSPACE` 기준의 상대 경로다. 예를 들어 `$HOME/work/shop`을 스캔하려면 `shop`으로 등록한다.
- worker는 `SE_UID`와 `SE_GID`로 정한 사용자로 실행된다. 기본값은 둘 다 65532다. 둘 다 숫자여야 하고, root(0)로는 실행하지 않는다(`openbao-secrets`가 거부해 worker가 뜨지 않는다). Linux 호스트의 Docker(네이티브)에서는 bind mount가 호스트 소유권을 그대로 유지하므로, 스캔할 파일과 폴더를 그 사용자가 읽을 수 있어야 하고, 적용하려면 쓸 수 있어야 한다. 작업 공간의 주인으로 실행하려면 `SE_UID=$(id -u) SE_GID=$(id -g) docker compose up -d`처럼 띄운다. worker가 적용으로 쓴 파일의 소유자는 worker의 사용자다. 읽을 수 없는 파일과 폴더(예: `0700` 폴더, `0600` 개인키나 `.env`)는 스캔에서 오류 없이 건너뛰고 건너뛴 수를 센다. macOS의 colima나 Docker Desktop에서는 소유권이 바뀌어 보이므로 이 문제가 생기지 않는다.
- `openbao-secrets`는 뜰 때마다 worker의 secret-id(`worker/secret-id`)와 그 폴더의 소유자를 `SE_UID`로 맞춘다(web의 것은 65532 그대로). `SE_UID`를 바꾸면 `docker compose up -d`가 `openbao-secrets`를 다시 실행해 이미 있는 볼륨의 소유자도 바꾼다.
- worker는 큐에 쌓인 스캔을 하나씩 실행한다. 파일마다 UTF-8이나 EUC-KR 텍스트로 읽고, betterleaks, opengrep, 키 파일 탐지기를 돌려 찾은 값을 항목으로 저장한다. postgres에는 값의 지문, 가린 미리보기, 위치, 가린 주변 줄만 남고, 비밀 값의 원본은 OpenBao에 보관한다. 고객정보는 원본을 보관하지 않고 주변 줄도 남기지 않는다. 실패하면 화면에는 정해진 사유만 보이고 자세한 오류는 `docker compose logs worker`에 남는다.
- 스캔 하나는 30분을 넘기면 `scan took longer than the time limit`으로 실패한다. 20MiB가 넘는 파일, 바이너리 파일, UTF-8도 EUC-KR도 아닌 파일은 텍스트로 스캔하지 않고 건너뛴 수에 센다. 경로가 UTF-8이 아니거나 제어 문자가 든 파일도 건너뛴 수에 센다. 키 저장소(`.jks`, `.p12` 등)는 바이너리라도 키 파일 항목이 된다.
- 스캔이 성공하면 worker 로그(`docker compose logs worker`)에 `scan finished` 기록이 하나 남는다. 스캔 ID(`scan_id`), 스캔이 목록에 넣은 일반 파일 수(`files`, 텍스트로 읽지 못하거나 경로를 저장할 수 없어 건너뛴 파일도 센다), 건너뛴 수(`skipped`), 버린 건수(`dropped`), 항목 수(`findings`), 텍스트로 읽지 못해 건너뛴 파일에 스캐너가 낸 결과 수(`results_in_skipped_files`), 도구별로 파일을 일부만 분석했다고 알린 오류 수(`scanner_errors`, opengrep의 파일별 10초 시간 초과나 부분 파싱 등)가 들어 있다. 건너뛴 파일이 있으면 `scan skipped files` 기록에 처음 50개의 상대 경로(`paths`)와 전체 수(`total`)가 남는다. 경로의 제어 문자와 깨진 UTF-8은 `U+FFFD`로 바뀐다. 읽지 못한 폴더는 수만 `skipped`에 들어가고 경로는 남지 않는다. 건너뛴 파일의 이름(경로순 처음 200개, UTF-8이 아니거나 제어 문자가 든 경로는 빼고 수에만 센다)과 도구별 스캐너 오류 수는 데이터베이스에도 저장되어 스캔 화면의 "검사하지 못한 것"에 보인다.
- 프로젝트 폴더 맨 위에 `.gitleaksignore`나 `.betterleaksignore`가 있으면 스캔하지 않고 `project has a scanner ignore file (.gitleaksignore or .betterleaksignore); remove it and scan again`으로 실패한다. betterleaks가 이 파일에 적힌 결과를 숨기고, 이를 끄는 옵션이 없기 때문이다. 작업 폴더에서 파일을 지우거나 이름을 바꾼 뒤 다시 스캔한다. 하위 폴더에 있는 같은 이름의 파일은 betterleaks가 읽지 않으므로 상관없다.
- EUC-KR 파일은 스캔하는 동안 UTF-8 사본을 worker의 `/tmp`(tmpfs, 1GiB)에 두고 스캔이 끝나면 지운다. EUC-KR 파일이 1GiB 가까이 되는 프로젝트를 스캔하려면 `compose.yaml`에서 크기를 늘린다. tmpfs는 호스트 메모리를 쓴다.
- OpenBao에 닿지 않으면 스캔은 `the secret store could not be reached; see the worker log`로 실패하고 postgres에는 항목을 하나도 저장하지 않는다. 그 실행이 실패하기 전에 OpenBao에 이미 보관한 원본은 남아 있다가, 그 프로젝트의 다음 스캔이 성공하면 그 스캔에 없는 것이 지워진다.
- 커밋 해시는 프로젝트 폴더가 git 저장소 안에 있을 때만 기록한다. 저장소가 아니거나 커밋이 없으면 비워 둔다.
- worker가 스캔 중에 멈추면 임대(2분)가 끝난 뒤 다시 실행한다. 세 번 멈추면 스캔이 `failed`로 끝난다.
- worker는 스캔과 함께 적용의 점검(`apply-check`)과 적용(`apply`) 작업도 처리한다. 둘 다 `/workspace-rw`의 프로젝트 폴더를 git으로 확인하고, 저장소를 바꾸는 git 명령은 실행하지 않는다. `APPLY_WORKSPACE_ROOT`가 없으면 모든 점검이 `workspace-read-only`로, git이 없으면 `git-missing`으로 막힌다.
- compose의 worker는 `restart: unless-stopped`다. worker가 적용 중에 죽으면 다시 뜬 worker가 임대(2분)가 끝난 뒤 그 작업을 이어 간다. 이어 가는 적용과 다음 점검은 먼저 기록과 디스크를 맞추고(이미 바꾼 파일은 적용된 것으로 센다), 저장이 확인된 값만 소스에서 지운다. 세 번 멈추면 적용이 `failed`로 끝난다.
- 적용은 파일을 같은 폴더의 임시 파일(`<이름>.se-apply-<난수 16자리>.tmp`)에 쓴 뒤 바꿔 넣는다. 중간에 죽어 남은 임시 파일은 그 프로젝트의 다음 점검이나 적용이 지운다. 도구는 스스로 만들었다고 기록한 임시 파일만 지운다.

## 스캔을 끝까지 해 보기

예제 프로젝트(값은 모두 가짜다)로 등록부터 판정까지 확인한다.

```sh
export SE_WORKSPACE=$HOME/work                  # 홈 폴더 아래의 폴더
mkdir -p $SE_WORKSPACE
cp -R internal/ruleset/testdata/legacy-shop $SE_WORKSPACE/
docker compose up --build -d
```

1. `dev-user`로 로그인하고 `/projects/new`에서 이름 `legacy-shop`, 경로 `legacy-shop`으로 등록한다.
2. 프로젝트 화면에서 "스캔 시작"을 누른다. 스캔 화면은 끝날 때까지 3초마다 새로 고쳐진다.
3. 끝나면 스캔 화면에 다음이 보인다. 스캔 화면은 요약만 보이고, 찾은 항목 하나하나는 판정 화면(`/projects/{id}/triage`)에서 본다.
   - 상태, 요청한 사람의 이름과 시각(UTC), 커밋 해시(예제는 git 저장소가 아니므로 "없음"), 규칙 버전, 도구 버전, 찾은 파일 수(읽지 못해 건너뛴 파일도 센다)
   - 분류별 항목 수와 `찾은 항목 N개(위치 M곳)`
   - 프로젝트의 마지막 성공한 스캔이면 "판정하기 · 미판정 N" 버튼. 이후에 성공한 스캔이 있으면 버튼 대신 "이후 스캔이 있습니다" 안내와 그 스캔의 링크
   - "검사하지 못한 것": 건너뛴 파일(펼침 상자 안에 경로 목록, 200개를 넘으면 "외 N개"), 위치가 맞지 않아 버린 결과, 도구별 스캐너 오류. 각각 0이 아닐 때만 보인다.
   - 스캔이 대기 중이거나 실행 중일 때 성공한 이전 스캔이 있으면, 지난 스캔 기준으로 판정할 수 있다는 안내와 판정 화면 링크가 보인다.
4. "판정하기"를 누르면 판정 화면이 열린다. 왼쪽 목록은 마지막 성공한 스캔의 묶음을 한 쪽에 50줄씩 보이고(기본 필터는 미판정), 줄을 고르면 오른쪽에 위치, 가린 주변 줄, 이름 입력란과 판정 버튼이 보인다. 고객정보는 파일과 종류마다 한 줄이고 값과 주변 줄이 없다. 판정하면 그 줄이 미판정 목록에서 빠지고 다음 줄이 골라진다.
5. 프로젝트 화면의 스캔 목록에서 이전 스캔을 본다. 예제 파일에서 값을 지우고 다시 스캔하면 판정 화면에서 그 항목만 사라지고, 나머지 묶음은 판정과 이름을 그대로 유지한다.
6. 끝나면 `$SE_WORKSPACE/legacy-shop`을 지운다. OpenBao에는 프로젝트의 마지막 스캔이 찾은 비밀 값의 원본이 남는다. 같은 프로젝트를 다시 스캔하면 그 스캔에 없는 값의 원본이 정리된다.

스캔 결과 화면과 판정 화면은 postgres에 저장된 가린 값만 보여 준다. 원본 값은 `admin`이 판정 화면에서 "원본 보기"를 누를 때만 보인다(아래).

## 적용을 끝까지 해 보기

적용은 git 작업본에서만 된다. 예제 폴더를 커밋이 있는 저장소로 만들고, EUC-KR 파일과 git이 추적하지 않는 로그를 더한다(값은 모두 가짜다).

```sh
cp -R internal/ruleset/testdata/legacy-shop "$SE_WORKSPACE/legacy-shop"
cd "$SE_WORKSPACE/legacy-shop"
printf '# \354\243\274\353\254\270 \354\204\244\354\240\225\r\njdbc.password=dev-only-fake-euckr-password\r\n' | iconv -f UTF-8 -t EUC-KR > conf/euckr.properties
cp logs/app-2026-11-01.log local/debug.log   # local/은 .gitignore에 있다: 추적되지 않는 고객정보
git init -q && git config core.autocrlf false
git add -A && git -c user.name=dev -c user.email=dev@example.invalid commit -q -m "sample"
```

- `git config core.autocrlf false`를 빼지 않는다. 호스트의 git에 `core.autocrlf`가 켜져 있으면 CRLF 파일이 LF로 커밋되고, 그 설정이 없는 worker 컨테이너의 git은 그 파일을 "커밋되지 않은 변경"으로 본다. 실제 프로젝트에서 같은 일이 생기면 그 파일 이름이 점검 화면의 "커밋되지 않은 변경"에 나온다.
- worker가 작업본에 쓸 수 있어야 한다. Linux 호스트에서는 `SE_UID=$(id -u) SE_GID=$(id -g) docker compose up -d`로 띄운다.

1. 프로젝트를 등록하고 스캔한 뒤 판정한다. 외부화와 삭제·마스킹으로 판정한 묶음만 적용 대상이다.
2. 프로젝트 화면의 "적용"(`/projects/{id}/apply`)에서 "조건 점검"을 누른다. worker가 소스를 바꾸지 않고 조건(git 작업본, 저장소 설정, 커밋되지 않은 변경, manifest, 최신 스캔, 적용할 판정)을 확인하고, 바뀔 위치와 건너뛸 위치를 사유와 함께 보인다.
3. git이 추적하지 않는 파일의 고객정보는 "복구 불가"에 따로 나온다. 동의란을 체크해야 그 파일을 가린다. 체크하지 않으면 그 파일만 건너뛴다.
4. "적용"을 누르면 결과 화면(`/applies/{id}`)으로 넘어간다. 끝나면 확인 스캔이 자동으로 돌고, 외부화한 값이 다시 발견되었는지 알린다.
5. 작업본에서 확인한다. `git diff`에는 판정한 위치만 있다. `.sh` 파일에는 `${EXT_이름}`, 그 밖에는 `${ext.이름}`이 들어가고, 통째로 옮긴 키 파일은 삭제로 보이며, `secrets-manifest.yml`이 생긴다. 도구는 커밋하지 않는다. 되돌리려면 `git checkout .`을 쓴다. OpenBao에 저장된 값은 남는다.
6. 판정 화면의 "적용됨" 탭에 묶음이 저장 경로와 함께 보인다. Java 리터럴에 들어간 placeholder에는 "코드 수정 필요"가 붙는다. 값을 코드로 읽게 고치는 일은 사람이 한다.

적용 중에는 그 프로젝트의 스캔 시작과 판정이 거부된다. 도구가 바꾼 파일은 커밋하지 않아도 다음 점검을 막지 않지만, 그 파일을 손으로 더 고치면 "커밋되지 않은 변경"으로 막힌다.

### 저장된 값을 확인하기

루트 토큰은 초기화 뒤 폐기되므로 worker의 AppRole로 로그인해 읽는다. 값은 base64로 저장되어 있고, 디코딩하면 소스에 적혀 있던 글자 그대로다. 이 명령은 값을 터미널에 출력하므로 로컬 개발 환경의 가짜 값에만 쓴다.

```sh
docker compose exec -e BAO_ADDR=https://127.0.0.1:8200 -e BAO_CACERT=/openbao/secrets/tls/ca.crt openbao sh -c '
  T=$(bao write -field=token auth/approle/login role_id=se-worker secret_id="$(cat /openbao/secrets/worker/secret-id)")
  BAO_TOKEN=$T bao read -format=json secret/data/projects/1/ext.db.main.password'
```

경로는 `secret/data/projects/<프로젝트 ID>/ext.<이름>`이고 manifest의 `store`와 같다. worker는 이 경로를 지울 수 없다(`bao delete`는 `permission denied`).

## 원본 보기와 감사 로그

1. `dev-admin`으로 로그인하고 위의 예제 프로젝트를 스캔한 뒤 판정 화면(`/projects/{id}/triage`)을 연다.
2. 비밀 값 묶음의 상세 맨 위에 "원본 보기" 버튼이 있다. `admin`에게만, 마지막 스캔이 원본을 보관한 묶음에만 보인다. 고객정보에는 없다.
3. 누르면 web이 먼저 감사 기록을 남기고, 그다음 OpenBao에서 원본을 읽어 그 응답에만 넣는다. 검은 테두리 상자에 값과 연 사람, 시각이 보이고 "닫기"를 누르면 값 없이 같은 상세로 돌아간다. 값은 저장하지 않고 캐시하지 않는다(`Cache-Control: no-store`). 텍스트가 아닌 값(UTF-8이 아니거나 NUL이 든 값)은 크기만 보인다.
4. OpenBao에 원본이 없으면 "보관된 원본이 없습니다. 다시 스캔하면 보관됩니다.", OpenBao에 닿지 않으면 "비밀 저장소에 연결할 수 없습니다.", OpenBao가 읽기를 거부했으면 "비밀 저장소에서 원본을 읽지 못했습니다."가 보인다. 두 경우에도 열람 시도는 감사 기록에 남는다. web은 OpenBao가 없어도 기동하고 다른 화면은 그대로 쓸 수 있다.
5. 머리 띠의 "감사 로그"(`/audit`)에서 판정, 묶음 옮기기, 원본 보기가 최신순으로 한 쪽에 100줄씩 보인다. 시각은 UTC이고, 내용에는 묶음과 항목 번호, 판정한 묶음의 종류(고객정보는 파일 경로도), 이름(`ext.…`), 보류 사유, 메모만 있고 값은 없다. "더 보기"가 다음 쪽(`/audit?before=<마지막 줄의 ID>`)을 연다.

## 리포트와 PDF

1. 성공한 스캔의 화면, 프로젝트 화면의 스캔 목록, 적용 결과 화면의 확인 스캔 옆에 "리포트" 링크가 있다(`/scans/{id}/report`). `user`와 `admin` 모두 볼 수 있다. 성공하지 않은 스캔의 리포트 주소는 스캔 화면으로 돌아간다.
2. 리포트는 열 때마다 만든다. 판정은 리포트를 만든 시각의 것이고, 외부화·삭제한 항목은 그 스캔이 요청되기 전에 적용된 것이다. 그래서 적용 뒤의 확인 스캔 리포트에 방금 외부화한 항목이 실린다. 지난 스캔의 리포트는 머리에 "이후 스캔이 있습니다"와 마지막 스캔 리포트의 링크를 보인다.
3. 리포트에는 값, 미리보기, 지문, 주변 줄이 없다. 절마다 100줄까지만 싣고 "외 N줄"을 적는다. 건수는 언제나 전체를 센다. 판정 화면에서 전체 목록을 볼 수 있다.
4. "PDF로 내보내기"를 누르면 같은 내용의 A4 PDF(`report-project-<프로젝트 ID>-scan-<스캔 ID>.pdf`)를 받는다. 예제 프로젝트에서 13~14쪽이고 1초 안팎이 걸린다. `pdf`가 멈춰 있으면 503 "PDF 변환 서비스에 연결할 수 없습니다. 잠시 뒤 다시 시도해 주세요." 화면이 나오고, 다른 화면은 그대로 쓸 수 있다. `docker compose start pdf` 뒤 다시 된다. web은 변환을 한 번에 하나만 보낸다. 다른 사람의 변환이 도는 동안 누르면 최대 20초 기다리고, 그래도 자리가 나지 않으면 503 "다른 PDF를 만드는 중입니다. 잠시 뒤 다시 시도해 주세요." 화면이 나온다. PDF에는 다른 화면으로 가는 링크가 없다.
5. 바닥글은 `secret-externalizer | scan #N | 쪽 / 전체`이고 왼쪽에 붙는다. 프로젝트 이름은 본문 첫머리에 있다.

## 대시보드

1. 머리 띠의 "대시보드"(`/dashboard`)를 연다. `user`와 `admin` 모두 볼 수 있다.
2. 타일의 수는 묶음 수이고, 프로젝트마다 마지막 성공한 스캔의 리포트 요약과 같다. 남은 항목은 미판정, 보류, 적용 대기를 더한 것이고 오탐은 들지 않는다. 여러 프로젝트를 볼 때는 프로젝트마다의 수를 더한다. 증감은 지난 성공한 스캔과의 차이다.
3. "남은 항목 추이"의 막대 하나는 성공한 스캔 하나다. 새것부터 30개까지 싣는다. 막대에 마우스를 올리면 수가 보이고, "표로 보기"에 같은 숫자의 표가 있다. 전체 프로젝트를 볼 때 막대의 높이는 그 스캔이 끝난 때의 프로젝트마다 마지막 스캔의 수를 더한 값이다. 판정은 지금의 것이므로, 오탐으로 판정한 묶음은 지난 막대에서도 빠진다.
4. 프로젝트와 기간으로 거른다(`/dashboard?project=<ID>&period=<all|7d|30d|90d>`). 기간은 추이에만 적용되고, 프로젝트 표는 언제나 모든 프로젝트를 보인다. 그 기간에 끝난 스캔이 없으면 "이 기간에 끝난 스캔이 없습니다."가 나온다.
5. 대시보드에는 값, 미리보기, 지문, 주변 줄이 없다. 차트는 서버가 그린 SVG이고 스크립트를 쓰지 않는다.
6. 예제 프로젝트 둘(스캔 여덟 개)에서 서버 응답은 2~13ms였다.

## 이미지를 다시 빌드할 때

- web과 worker는 같은 마이그레이션 파일을 가지고 뜰 때 스키마를 확인한다. 새 마이그레이션이 든 web이 먼저 뜨면, 그 파일이 없는 이전 worker 이미지는 다시 시작할 때 `schema_migration records versions [N] that this binary has no migration file for`로 뜨지 않는다(이미 떠 있던 worker는 계속 돌지만 재시작하면 멈춘다). 마이그레이션이 바뀐 커밋을 받으면 web과 worker를 함께 다시 빌드한다.
- Docker VM의 메모리가 2GB(colima 기본값)이면 Go 이미지 빌드가 매우 느리다. 전체 스택이 떠 있는 채 web을 빌드하자 90분 넘게 끝나지 않고 Keycloak이 메모리 부족으로 죽은 일이 있었고, `pdf-test`와 `openbao-test`를 멈춘 채 worker 하나를 빌드하는 데 약 16분이 걸렸다. 빌드 전에 `test` 프로필의 서비스를 멈추고, 이미지를 하나씩 빌드한 뒤(`docker compose build worker`) `docker compose up -d --no-deps --no-build worker`로 바꿔 띄운다. 가능하면 VM 메모리를 4GB 이상으로 늘린다(`colima stop && colima start --memory 4`).

## OpenBao

worker는 스캔에서 찾은 값의 지문을 OpenBao transit으로 계산하고, 판정 전의 원본을 OpenBao KV v2에 보관한다. 설정은 `deploy/openbao/config.hcl`과 봉인만 담은 `deploy/openbao/seal-static.hcl`에 있고, 이유는 `docs/decisions/0017-openbao-setup.md`와 `docs/decisions/0067-openbao-seal-key-on-host-and-tls.md`에 있다.

| 서비스 | 내용 |
|---|---|
| `openbao-secrets` | 한 번 실행되고 끝난다. `openbao-secrets` 볼륨에 AppRole secret-id(`worker/secret-id`, `web/secret-id`)를 난수로, OpenBao의 TLS 인증서(`tls/`)와 그 CA(`worker/ca.crt`, `web/ca.crt`)를 만든다. 이미 있는 파일은 그대로 두고, worker의 파일과 그 폴더의 소유자만 매번 `SE_UID`(기본 65532)로 맞춘다. 볼륨에 예전의 봉인 해제 키(`unseal/`)가 남아 있으면 `make openbao-unseal-key`를 실행하라는 메시지로 멈춘다 |
| `openbao` | 원본을 보관한다. 호스트에 포트를 열지 않고 TLS만 받으며, 내부 네트워크 `bao`의 worker(쓰기)와 web(원본 보기의 읽기)만 쓴다. 데이터는 `openbao-data` 볼륨에, 봉인 해제 키는 호스트 파일에 있고 compose가 그 파일을 openbao에만 `/run/secrets/openbao-unseal-key`로 넘긴다 |
| `openbao-test` | `test` 프로필에서만 뜬다. 통합 테스트용 `-dev` 서버이며 `127.0.0.1:18200`에 열린다. 메모리에만 저장하고 루트 토큰은 `dev-only-openbao-test-root-token`이다 |

- 처음 뜰 때 OpenBao가 스스로 초기화(self-init)한다. transit 키 `fingerprint`, KV v2 마운트 `secret`, AppRole `worker`와 `web`, 정책, 표준 출력 audit device를 만든다. 루트 토큰은 초기화가 끝나면 폐기되어 남지 않는다.
- 다시 띄우면 호스트 파일의 키로 봉인이 저절로 풀린다. `docker compose restart openbao` 뒤에도 그대로 쓸 수 있다.
- 키 파일을 잃으면 `openbao-data`의 값을 읽을 수 없다. 적용을 한 환경에서는 키 파일을 백업한다. 키 파일과 볼륨을 함께 가진 사람은 모든 값을 읽을 수 있으므로 백업도 볼륨과 따로 둔다.
- 키 파일은 `0700` 폴더 안의 `0644` 파일이다. Linux 호스트에서는 bind mount가 호스트의 소유자와 권한을 그대로 보이므로, 컨테이너의 openbao 사용자(uid 100)가 읽으려면 파일이 모두에게 읽기 가능해야 한다. 호스트의 다른 사용자는 폴더에서 막힌다. `make openbao-unseal-key`는 다른 사용자가 들어갈 수 있는 폴더를 거부한다.
- web과 worker는 `https://openbao:8200`으로 붙고 `BAO_CA_FILE`의 CA만 믿는다. 컨테이너 안에서 `bao` 명령을 쓸 때는 `-address=https://127.0.0.1:8200 -ca-cert=/openbao/secrets/tls/ca.crt`(또는 `BAO_ADDR`, `BAO_CACERT`)를 준다.
- `seal-static.hcl`의 static 봉인은 개발자 PC 한 대를 위한 것이다. 공유하거나 배포하는 환경에서는 이 파일 대신 키 관리 서비스에 기댄 자동 봉인 해제(transit, 클라우드 KMS, HSM 등)를 쓰고, 그 환경의 CA로 만든 인증서를 같은 경로에 둔다.
- audit 기록은 `docker compose logs openbao`에 나온다. 요청의 값은 HMAC으로 가려져 있다.
- `config.hcl`의 `initialize` 블록은 첫 기동 때만 실행된다. 고친 뒤 반영하려면 두 볼륨을 지우고 다시 띄운다. 보관한 원본도 모두 사라진다.

  ```sh
  docker compose rm -sf openbao openbao-secrets
  docker volume rm secret-externalizer_openbao-data secret-externalizer_openbao-secrets
  docker compose up -d openbao
  ```

- self-init의 요청 하나가 거부되면 OpenBao는 초기화를 끝내지 못하고 멈춘다(`docker compose logs openbao`에 `failed to initialize`). 이때 저장소는 초기화된 것으로 남아 다시 띄워도 정책과 AppRole이 없으므로, 위의 세 명령으로 두 볼륨을 지우고 다시 띄운다. 새 KV v2 마운트는 저장소를 올리는 동안 요청을 거부하므로 `config.hcl`은 마운트를 맨 처음에 만들고 버전 수 설정(`secret/config`)을 맨 끝에 보낸다. 이 순서를 바꾸지 않는다.

### 봉인 해제 키를 볼륨 밖으로 옮긴 뒤

이전에 만든 `openbao-secrets` 볼륨에는 봉인 해제 키가 `unseal/key`로 들어 있다. 새 compose로 띄우기 전에 한 번 `make openbao-unseal-key`를 실행한다. 스크립트가 볼륨의 키를 호스트 파일로 복사하고, 파일을 다시 읽어 볼륨의 것과 같은지 확인한 뒤에만 볼륨에서 지운다. 같은 키이므로 OpenBao의 데이터는 그대로 열린다. 그 뒤 `docker compose up -d openbao`가 `openbao-secrets`를 다시 실행해 TLS 인증서를 만들고, web과 worker는 새 이미지로 다시 띄운다(`docker compose up -d --build web worker`).

- 호스트 파일과 볼륨의 키가 다르면 아무것도 바꾸지 않고 멈춘다. `openbao-data`를 연 키를 호스트 파일 자리에 두고 다시 실행한다.
- `openbao-data` 볼륨은 있는데 어디에도 키가 없으면 새 키를 만들지 않고 멈춘다. 새 키로는 그 저장소를 열 수 없다.
- 적용은 외부화한 값을 `secret/data/projects/<프로젝트 ID>/ext.<이름>`에 쓴다. 이미 값이 있으면 저장된 바이트를 다시 읽어 지문을 비교하고, 다를 때만 check-and-set으로 새 버전을 쓴다. 이전 값은 이전 버전에 남는다. worker 정책은 이 경로를 쓰고 읽을 수만 있고 지우거나 메타데이터를 볼 수 없다. KV v2 마운트는 경로마다 버전을 100개 남긴다. 값 하나는 16MiB까지 저장하고, 그보다 큰 값(큰 키 파일)은 `too-large`로 건너뛰어 소스에 남는다. OpenBao는 요청 하나를 32MiB까지 받는다(`docs/decisions/0035-externalized-values-in-openbao.md`).

### 정책을 바꾼 뒤(계획 5)

worker 정책에 `secret/data/projects/*`의 `read`가 더해졌고, KV v2 마운트가 경로마다 버전을 100개 남기도록 self-init이 `secret/config`를 쓴다. 계획 5 이전에 만든 볼륨에는 이전 정책과 기본값(10개)이 들어 있으므로, `openbao-data`, `openbao-secrets`를 지우고 다시 띄운 뒤(`docker compose down -v`, 또는 위의 세 명령 뒤에 `docker compose up -d --force-recreate web worker`) 프로젝트를 다시 스캔한다. secret-id가 새로 만들어지므로 web과 worker도 다시 시작해야 한다.

> **경고:** `down -v`로 볼륨을 지우는 것은 **적용을 한 번도 하지 않은 환경에서만** 안전하다. 적용을 한 번이라도 한 환경에서는 `openbao-data` 볼륨에, git이 추적하지 않는 파일(키 파일, 무시된 설정 파일)에서 빼낸 값의 유일한 사본이 있을 수 있다. 그 파일은 적용이 지웠거나 자리표시자로 바꾸었으므로, 볼륨을 지우면 값이 영영 사라진다. 그런 환경에서 정책을 바꾸는 방법은 아직 없다(`docs/next-steps.md`).

## valkey

valkey는 세션과 진행 중인 로그인 시도를 가진다. TLS만 받고 ACL 사용자로만 쓸 수 있다(`docs/decisions/0066-valkey-auth-and-tls.md`).

| 서비스 | 내용 |
|---|---|
| `valkey-secrets` | 한 번 실행되고 끝난다. `valkey-secrets` 볼륨에 사설 CA와 valkey의 서버 인증서(`valkey/tls.crt`, `valkey/tls.key`, `valkey/ca.crt`), ACL 파일(`valkey/users.acl`), web의 비밀번호와 CA 인증서(`web/password`, `web/ca.crt`)를 만든다. 하나라도 없으면 모두 새로 만들고, 모두 있으면 그대로 둔다. CA의 개인 키는 남기지 않는다. 스크립트는 `deploy/valkey/make-secrets.sh`다 |
| `valkey` | 호스트에 포트를 열지 않고, 내부 네트워크 `valkey`의 web만 쓴다. 사용자는 web의 `se-web`(세션과 로그인 시도 키, web이 쓰는 명령만), 상태 확인의 `health`(`PING`만)이고 `default`는 꺼져 있다 |
| `valkey-test` | `test` 프로필에서만 뜬다. 통합 테스트용이며 평문으로 `127.0.0.1:16379`에 열린다. 비밀번호는 `dev-only-valkey-test-password`다 |

- 인증서는 10년짜리이고 갱신하지 않는다. 비밀번호나 인증서를 바꾸려면 볼륨을 지우고 valkey와 web을 다시 띄운다. 모든 사람이 다시 로그인해야 하는 것 말고 잃는 것은 없다.

  ```sh
  docker compose rm -sf valkey valkey-secrets
  docker volume rm secret-externalizer_valkey-secrets
  docker compose up -d --force-recreate valkey web
  ```

- 이 변경 전에 띄운 스택에서는 `docker compose up -d`가 `valkey-secrets`를 처음 실행하고 valkey와 web을 다시 만든다. web 이미지도 다시 빌드해야 한다(`VALKEY_USERNAME`, `VALKEY_PASSWORD_FILE`이 없으면 이전 이미지는 그 설정을 읽지 않고 평문으로 붙다가 실패한다).

## PDF 변환기

리포트의 PDF는 web이 `pdf` 서비스(Gotenberg 9.9.1의 Chromium 전용 이미지)에 HTML, 스타일시트, 글꼴을 한 요청으로 보내 만든다. 설정과 이유는 `docs/decisions/0043-pdf-converter-setup.md`에 있다.

| 서비스 | 내용 |
|---|---|
| `pdf` | 호스트에 포트를 열지 않는다. 바깥으로 나가는 길이 없는 내부 네트워크 `pdf`에만 붙고, 그 네트워크에는 web만 함께 붙는다. web은 `http://pdf:3000`으로 부른다. Chromium은 요청으로 받은 파일(`/tmp` 아래)만 열고 JavaScript를 실행하지 않는다 |
| `pdf-test` | `test` 프로필에서만 뜬다. `pdf`와 같은 이미지와 옵션이고, 네트워크 `test`에 붙어 `127.0.0.1:13000`에 열린다 |

## 개발용 계정

| 사용자 | 비밀번호 | 역할 | 볼 수 있는 화면 |
|---|---|---|---|
| `dev-user` | `dev-only-user-password` | `user` | 홈, 프로젝트와 스캔 |
| `dev-admin` | `dev-only-admin-password` | `admin` | 홈, 프로젝트와 스캔, 판정 화면의 원본 보기, 감사 로그(`/audit`) |
| `dev-norole` | `dev-only-norole-password` | 없음 | 로그인 뒤 403. 이 화면의 로그아웃 버튼으로 Keycloak 세션을 끝내고 다른 계정으로 로그인한다 |

Keycloak 관리 콘솔(`http://localhost:8081/admin/`)의 계정은 `dev-only-admin` / `dev-only-admin-password`다. 이 계정은 `master` realm이 처음 만들어질 때만 생긴다.

realm 설정은 `deploy/keycloak/realm.json`에 있다. Keycloak은 기동할 때 이 파일을 가져오지만 이미 있는 realm은 덮어쓰지 않는다. 파일을 고친 뒤에는 `docker compose down -v`로 볼륨을 지우고 다시 띄운다.

## 포트

모든 포트는 `127.0.0.1`에만 열린다.

| 서비스 | 호스트 주소 | 용도 |
|---|---|---|
| web | `http://localhost:8080` | 화면 |
| keycloak | `http://localhost:8081` | 로그인 화면, 관리 콘솔 |
| postgres | `127.0.0.1:15432` | 통합 테스트. 컨테이너끼리는 `postgres:5432`로 쓴다 |
| valkey-test | `127.0.0.1:16379` | 통합 테스트. `test` 프로필에서만 뜬다. 로컬에 설치한 redis가 6379를 쓰는 경우가 많아 16379로 연다 |
| openbao-test | `127.0.0.1:18200` | 통합 테스트. `test` 프로필에서만 뜬다 |
| pdf-test | `http://127.0.0.1:13000` | 통합 테스트. `test` 프로필에서만 뜬다 |

`openbao`는 호스트에 포트를 열지 않는다. web과 worker만 내부 네트워크 `bao`에서 TLS로 `openbao:8200`에 접속한다. `pdf`도 포트를 열지 않고, web만 내부 네트워크 `pdf`에서 `pdf:3000`으로 접속한다. `valkey`도 포트를 열지 않고, web만 내부 네트워크 `valkey`에서 TLS로 `valkey:6379`에 접속한다.

## 네트워크

서비스는 필요한 연결만 나르는 네트워크에 붙는다. 어느 서비스도 Docker 네트워크 밖으로 연결하지 못한다(`docs/decisions/0065-compose-networks.md`).

| 네트워크 | 종류 | 붙는 서비스 |
|---|---|---|
| `edge` | bridge, NAT 끔. 호스트 포트를 연다 | web, keycloak, postgres |
| `db` | internal | postgres, worker |
| `valkey` | internal | valkey, web |
| `bao` | internal | openbao, web, worker |
| `pdf` | internal | pdf, web |
| `test` | bridge, NAT 끔. 호스트 포트를 연다 | valkey-test, openbao-test, pdf-test (`test` 프로필) |

- worker와 스캐너는 postgres와 OpenBao에만 닿고 바깥 이름도 풀지 못한다. 스캐너를 올리거나 바꿀 때 바깥 연결이 필요한 기능(규칙 내려받기, 버전 확인 등)을 켜면 스캔이 실패한다.
- `openbao-secrets`와 `valkey-secrets`는 네트워크 없이(`network_mode: none`) 뜬다.
- 네트워크 구성을 바꾼 `compose.yaml`을 처음 받으면 `docker compose up -d`가 컨테이너를 새 네트워크로 다시 만든다. 이전의 `secret-externalizer_default` 네트워크는 남아 있으면 `docker network rm secret-externalizer_default`로 지운다.

## 발급자 주소

브라우저는 Keycloak을 `http://localhost:8081`로, web 컨테이너는 `http://keycloak:8080`으로 부른다. Keycloak은 `KC_HOSTNAME=http://localhost:8081`과 `KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true`로 띄운다. 그래서 토큰의 발급자와 로그인, 로그아웃 주소는 언제나 `localhost:8081`이고, web이 `keycloak:8080`에서 읽은 discovery 문서의 토큰, 키 주소는 `keycloak:8080`이다. web에는 `OIDC_ISSUER_URL`(토큰의 발급자)과 `OIDC_DISCOVERY_URL`(web이 닿는 주소)을 따로 준다.

web은 기동할 때 discovery 문서를 2초 간격으로 최대 60초 동안 다시 읽는다. 그동안 Keycloak이 뜨지 않으면 오류로 끝난다.

## web 환경 변수

| 이름 | 필수 | 내용 |
|---|---|---|
| `WEB_ADDR` | | 듣는 주소. 기본값 `:8080` |
| `WEB_BASE_URL` | 예 | 브라우저에서 보는 주소. 콜백 주소(`/auth/callback`)와 로그아웃 뒤 주소(`/`)를 여기서 만든다. `/` 말고 다른 경로, 쿼리, 프래그먼트는 둘 수 없다 |
| `VALKEY_ADDR` | 예 | 예 `valkey:6379`. TLS를 쓰면 valkey의 인증서를 이 주소의 호스트 이름으로 확인한다 |
| `VALKEY_USERNAME` | 예 | valkey ACL 사용자. compose에서는 `se-web` |
| `VALKEY_PASSWORD_FILE` | 예 | 그 사용자의 비밀번호가 든 파일. 앞뒤 공백은 뗀다. compose에서는 `valkey-secrets` 볼륨의 `web` 폴더를 `/run/secrets/valkey`에 읽기 전용으로 마운트하고 `/run/secrets/valkey/password`를 쓴다. 비밀번호는 로그와 오류 메시지에 남지 않는다 |
| `VALKEY_CA_FILE` | | valkey의 인증서를 서명한 CA 인증서(PEM). 주면 TLS로 붙고 이 파일의 인증서만 믿는다. 비우면 평문으로 붙고 시작할 때 `valkey connection is not encrypted` 경고를 남긴다. compose에서는 `/run/secrets/valkey/ca.crt`. 공유하거나 배포하는 환경에서는 반드시 준다 |
| `DATABASE_URL` | 예 | postgres 주소. 예 `postgres://se_app:dev-only-se-app-password@postgres:5432/secret_externalizer`. 로그와 오류 메시지에 남지 않는다. web은 기동할 때 마이그레이션을 실행한다 |
| `OIDC_ISSUER_URL` | 예 | 토큰의 발급자. 예 `http://localhost:8081/realms/secret-externalizer` |
| `OIDC_DISCOVERY_URL` | | web이 발급자에 닿는 주소. 비우면 `OIDC_ISSUER_URL`을 쓴다. 여기서 읽은 discovery 문서의 `issuer`가 `OIDC_ISSUER_URL`과 다르면 web이 기동하지 않는다 |
| `OIDC_CLIENT_ID` | 예 | `secret-externalizer-web` |
| `OIDC_CLIENT_SECRET` | 예 | 로그와 오류 메시지에 남지 않는다 |
| `BAO_ADDR` | 예 | OpenBao 주소. `http://` 또는 `https://`로 시작하고 경로가 없어야 한다. compose에서는 `https://openbao:8200`. `http://`이면 시작할 때 `openbao connection is not encrypted` 경고를 남긴다. web은 기동할 때 OpenBao에 닿지 않아도 되고, 원본 보기를 누를 때만 연결한다 |
| `BAO_CA_FILE` | 아니요 | OpenBao의 인증서가 이어져야 하는 CA 인증서(PEM) 파일. 주면 `BAO_ADDR`가 `https://`여야 하고 이 CA만 믿는다. 주지 않은 `https://` 주소는 시스템의 루트 인증서로 확인한다. compose에서는 `/run/secrets/openbao/ca.crt` |
| `BAO_ROLE_ID` | 예 | AppRole role-id. compose에서는 `se-web`. 이 역할의 정책은 `secret/data/pending/*` 읽기만 허용한다 |
| `BAO_SECRET_ID_FILE` | 예 | AppRole secret-id가 든 파일. 앞뒤 공백은 뗀다. compose에서는 `openbao-secrets` 볼륨의 `web` 폴더만 `/run/secrets/openbao`에 읽기 전용으로 마운트하고 `/run/secrets/openbao/secret-id`를 쓴다. secret-id는 로그와 오류 메시지에 남지 않는다 |

## worker 환경 변수

| 이름 | 필수 | 내용 |
|---|---|---|
| `DATABASE_URL` | 예 | web과 같은 postgres 주소. worker도 기동할 때 마이그레이션을 실행한다 |
| `WORKSPACE_ROOT` | 예 | 스캔이 읽는 작업 공간 루트. 절대 경로이고 폴더여야 한다. compose에서는 읽기 전용 마운트 `/workspace` |
| `APPLY_WORKSPACE_ROOT` | | 적용이 파일을 바꾸는 작업 공간 루트. `WORKSPACE_ROOT`와 같은 폴더를 읽기-쓰기로 마운트한 경로다. 절대 경로이고 폴더여야 한다. 비우면 적용의 모든 점검이 `workspace-read-only`로 막힌다. compose에서는 `/workspace-rw` |
| `BAO_ADDR` | 예 | OpenBao 주소. `http://` 또는 `https://`로 시작하고 경로가 없어야 한다. compose에서는 `https://openbao:8200`. `http://`이면 시작할 때 `openbao connection is not encrypted` 경고를 남긴다 |
| `BAO_CA_FILE` | 아니요 | OpenBao의 인증서가 이어져야 하는 CA 인증서(PEM) 파일. 주면 `BAO_ADDR`가 `https://`여야 하고 이 CA만 믿는다. 주지 않은 `https://` 주소는 시스템의 루트 인증서로 확인한다. compose에서는 `/run/secrets/openbao/ca.crt` |
| `BAO_ROLE_ID` | 예 | AppRole role-id. compose에서는 `se-worker` |
| `BAO_SECRET_ID_FILE` | 예 | AppRole secret-id가 든 파일. 앞뒤 공백은 뗀다. compose에서는 `openbao-secrets` 볼륨의 `worker` 폴더만 `/run/secrets/openbao`에 읽기 전용으로 마운트하고 `/run/secrets/openbao/secret-id`를 쓴다. secret-id는 로그와 오류 메시지에 남지 않는다 |
| `BETTERLEAKS_BIN` | 예 | betterleaks 실행 파일의 절대 경로. compose에서는 `/usr/local/bin/betterleaks` |
| `OPENGREP_BIN` | 예 | opengrep 실행 파일의 절대 경로. compose에서는 `/usr/local/bin/opengrep` |
| `RULES_DIR` | 예 | 탐지 규칙 폴더의 절대 경로. `betterleaks.toml` 파일과 `opengrep` 폴더가 있어야 한다. compose에서는 `/rules` |
| `SCAN_TEMP_DIR` | 예 | 스캐너의 HOME(`home`)과 TMPDIR(`tmp`), 스캔마다의 EUC-KR 사본(`copies/<스캔 ID>`)을 두는 폴더의 절대 경로. 없으면 worker가 만든다. tmpfs 위에 둔다. compose에서는 `/tmp/scan` |
| `GIT_BIN` | | 커밋 해시와 적용 전의 작업본 상태를 읽는 git 실행 파일. 비우면 `/usr/bin/git`이 있을 때 그것을 쓰고, 없으면 커밋 해시를 기록하지 않고 적용의 모든 점검이 `git-missing`으로 막힌다 |

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

규칙을 고치면 `internal/ruleset/testdata`의 두 예제로 확인한다. `legacy-shop`은 스캔과 적용 테스트가 함께 쓰는 예제 프로젝트이고, `rule-cases`는 규칙이 찾아야 하는 모양과 건드리지 말아야 하는 모양을 하나씩 둔 사례 모음이다. 값은 모두 가짜다. 찾아야 하는 것은 예제와 같은 이름의 `legacy-shop.json`, `rule-cases.json`에 `{file, line, category, kind, value}`로 적혀 있고(`value`는 잡아야 하는 값의 정확한 글자), 규칙 테스트는 실제 스캐너로 예제를 스캔해 이 목록과 정확히 같은지 본다. 같은 결과가 두 번 나와도 차이로 보고, 목록에 없는 줄이 나와도 차이로 본다(찾지 말아야 하는 사례는 목록에 없는 것으로 확인한다). `legacy-shop`은 `data`, `build.xml` 같은 이름의 폴더 아래에 두고 스캔해도 결과가 같은지도 본다. 새 사례는 `rule-cases`에 더한다. `legacy-shop`을 고치면 스캔과 적용 테스트의 결과도 달라진다.

```sh
make scanners                                   # 처음 한 번
go test -count=1 -v -run 'TestRules|TestVersion' ./internal/ruleset/
```

- 규칙 테스트는 `BETTERLEAKS_BIN`, `OPENGREP_BIN`이 가리키는 실행 파일을 쓰고, 없으면 저장소의 `bin/`에서 찾는다. 둘 다 없으면 "run make scanners" 안내와 함께 건너뛴다. `bin/`에 스캐너가 있으면 `make check`에서도 실행된다.
- `make test-integration`은 두 변수를 `bin/`의 실행 파일로 넘긴다. 다른 실행 파일을 쓰려면 `make test-integration BETTERLEAKS_BIN=… OPENGREP_BIN=…`처럼 준다.
- 예제의 `local/` 폴더는 예제 자신의 `.gitignore`에 적혀 있어 이 저장소에서도 무시된다. 이미 올라간 파일은 그대로 추적되지만, 이 폴더에 새 파일을 더할 때는 `git add -f`로 올린다.

## 통합 테스트

valkey가 필요한 테스트는 `TEST_VALKEY_ADDR`와 `TEST_VALKEY_PASSWORD`가, postgres가 필요한 테스트는 `TEST_DATABASE_URL`이, OpenBao가 필요한 테스트는 `TEST_BAO_ADDR`가, PDF 변환기가 필요한 테스트는 `TEST_PDF_URL`이 없으면 건너뛴다. `make test-integration`은 넷 다 요구한다. PDF 테스트는 `test` 프로필의 `pdf-test`(`http://127.0.0.1:13000`)를 쓴다. OpenBao 테스트는 원본을 보관하는 `openbao`가 아니라 `test` 프로필의 `openbao-test`를 쓰고, 테스트가 개발용 루트 토큰으로 transit, KV, AppRole을 직접 만든다. compose의 서비스로 실행하려면 다음과 같이 한다. 새 셸에서는 compose 명령마다 `SE_WORKSPACE`가 설정되어 있어야 한다. 없으면 compose가 파일을 읽다가 멈춘다(worker를 띄우지 않는 명령도 같다).

실제 스캐너를 쓰는 테스트(`internal/scan`의 `TestHandle_RealScanners…`, `internal/ruleset`의 규칙 테스트 등)는 `bin/`에 스캐너가 없으면 실패하지 않고 건너뛴다. 통합 테스트를 돌리기 전에 `make scanners`를 먼저 실행하고, `-v` 출력에서 이 테스트가 `SKIP`이 아닌지 확인한다.

```sh
export SE_WORKSPACE=$HOME/work     # 값은 존재하는 폴더면 된다
docker compose up -d postgres
docker compose --profile test up -d valkey-test openbao-test pdf-test
make scanners                      # 처음 한 번. 없으면 실제 스캐너 테스트가 건너뛰어진다
TEST_VALKEY_ADDR=127.0.0.1:16379 \
TEST_VALKEY_PASSWORD=dev-only-valkey-test-password \
TEST_DATABASE_URL=postgres://se_app:dev-only-se-app-password@127.0.0.1:15432/secret_externalizer_test \
TEST_BAO_ADDR=http://127.0.0.1:18200 \
TEST_PDF_URL=http://127.0.0.1:13000 \
make test-integration
```

Chromium이 든 `pdf`와 `pdf-test`는 쉬고 있을 때 각각 200MB 안팎을 쓰고, 컨테이너마다 1GB 한도가 있다. Docker VM의 메모리가 2GB(colima 기본값)인 기계에서 전체 스택과 `pdf-test`를 함께 띄운 채 `make test-integration`이 통과했다. VM 메모리가 그보다 작거나 다른 컨테이너가 더 떠 있어 `openbao-test`가 메모리 부족으로 죽으면(`docker inspect`의 `OOMKilled`, `internal/apply`가 `store-failed`나 연결 거부로 실패) `docker compose stop pdf`로 `pdf`를 멈추고 `openbao-test`를 다시 띄운 뒤 돌리거나 VM 메모리를 늘린다.

valkey 테스트는 `test` 프로필의 `valkey-test`를 쓴다. 평문이고 비밀번호는 `dev-only-valkey-test-password`다. 테스트는 그 `default` 사용자로 붙는다.

valkey는 디스크에 아무것도 남기지 않는다. valkey를 다시 띄우면 모든 세션이 사라지고 다시 로그인해야 한다. 메모리는 64MB로 제한하며, 다 차면 기존 키를 지우지 않고 새 쓰기만 거부한다(`noeviction`). 이때 새 로그인은 503 화면을 보고, 이미 로그인한 세션은 그대로 쓸 수 있다.

테스트는 실행마다 `test_<난수>` 스키마를 만들어 쓰고 끝나면 지운다.
