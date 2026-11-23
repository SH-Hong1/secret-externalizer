# PDF 변환기의 구성과 제한

- 날짜: 2026-11-21
- 상태: 확정

## 배경

리포트의 PDF는 web이 완성한 HTML을 pdf 컨테이너가 Chromium으로 인쇄해 만든다(결정 기록 0006). 변환기는 사용자의 데이터를 담은 문서를 받으므로, 받은 것 말고는 아무것도 읽지 않고 어디에도 닿지 않아야 한다. 계획 6의 "구현 전에 확인할 사실" 가운데 Task 4의 항목을 2026-11-20~21에 다음과 같이 확인했다.

### 버전과 이미지

- Gotenberg의 공식 문서(gotenberg.dev의 설치, 설정, Chromium HTML 변환 쪽)는 9.x를 다룬다. Chromium만 든 이미지의 태그는 `gotenberg/gotenberg:9-chromium`이다.
- Docker Hub의 태그 목록에서 지금 안정 버전은 9.9.1(2026-11-20)이고, `9.9.1-chromium`, `9.9-chromium`, `9-chromium`, `latest-chromium`이 같은 digest `sha256:d722dfb3105c00f775125f13e3613ecbdb0040b12a7b8245c1d98ebafaefedd4`를 가리킨다. 이미지의 `org.opencontainers.image.version` 라벨과 `GET /version`도 `9.9.1`이다.
- 이 digest는 여러 아키텍처의 index다. `docker manifest inspect`로 linux/amd64(`sha256:ab825daa…`)와 linux/arm64(`sha256:a3797b79…`)가 들어 있음을 확인했다(386, ppc64le 등도 있다). 이 기계(arm64, colima)에서 arm64 이미지로 실행했다. compose에는 index digest를 고정한다.
- 이미지의 사용자는 `gotenberg`(uid 1001, gid 1001, 보조 그룹 0)이고 HOME은 `/home/gotenberg`다. `/usr/bin/curl`이 있어 healthcheck에 쓴다. LibreOffice는 없다(`/forms/libreoffice/convert`는 404).

### 변환 경로와 양식 필드

공식 문서와 `curl` 실험으로 확인했다.

- `POST /forms/chromium/convert/html`. 파일은 모두 양식 필드 `files`로 보내고 `index.html`이 있어야 한다(없으면 400). 다른 파일은 폴더 없이 이름만으로 참조한다(`href="app.css"`, `url("X.woff2")`). Gotenberg는 요청마다 `/tmp/<uuid>/<uuid>/`에 파일을 쓰고 `index.html`은 `<uuid>.html`로 바꿔 `file://`로 연다(디버그 로그 `file:///tmp/5aa0088b-…/7ce0e5bd-…/847fd976-….html`). UUID는 소문자 16진수다.
- `/tmp`에는 요청 폴더 말고도 Chromium의 것이 있다: `HOME=/tmp`이라 `/tmp/.config/chromium`, `/tmp/.cache/fontconfig`, `/tmp/org.chromium.Chromium.*`가 생기고, Chromium의 사용자 데이터 폴더도 요청 폴더와 같은 모양(`/tmp/<uuid>/<uuid>/`)이며 그 안에 `Local State`, `DevToolsActivePort`, `Default/` 등이 있다(`find /tmp`로 확인).
- Gotenberg 9.9.1은 허용 목록과 별개로, 인쇄 중인 요청의 폴더 밖을 가리키는 `file://` 주소를 스스로 막는다(로그의 `rule: file-prefix`). 허용 목록을 `^file:///tmp/`로 넓게 둔 컨테이너에서도 `/tmp/marker-root.txt`, `/tmp/.config/marker.txt`, 다른 요청 모양의 폴더(`/tmp/aaaaaaaa-…/bbbbbbbb-…/other.txt`), 그 아래 폴더의 파일이 모두 `file-prefix`로 막혔다.
- `header.html`과 `footer.html`은 따로 다루는 이름이다. 각각 쪽마다 위·아래 여백에 그려지고, `pageNumber`, `totalPages`(그리고 `title`, `date`, `url`) 클래스에 값이 채워진다. `header.html`이라는 파일을 보내면 본문이 아니라 쪽 머리에 그려지는 것을 PDF를 열어 확인했다. 머리글과 바닥글은 스타일시트와 파일을 읽지 않는다(문서).
- 9.9.1은 모르는 양식 필드를 400으로 거부한다(`form field 'bogusField' is unknown to this route`).
- 종이 크기는 `paperSize`(A0~A5, B4, B5, Letter, Legal, Tabloid) 필드가 있다. 그래서 계획대로 `paperWidth`·`paperHeight` 대신 `paperSize=A4`를 쓴다. 결과의 `MediaBox`는 `[0 0 595.92 841.92]`(A4)다.
- 여백 필드는 `marginTop`, `marginBottom`, `marginLeft`, `marginRight`이고 단위 없는 값은 인치이며 `in`, `mm`, `cm`, `pt`, `px`, `pc`를 붙일 수 있다. 클라이언트는 단위를 붙여 `0.6in`, `0.8in`을 보낸다.
- `printBackground`, `preferCssPageSize`의 이름이 그대로다.

### 글꼴

- 한글 문단과 `@font-face`(저장소의 `IBMPlexSansKR-1.1.0-Regular.woff2`)를 보내면 PDF에 `/FontName /AAAAAA+IBMPlexSansKR`가 들어간다. 바닥글은 보낸 글꼴을 쓰지 못해 `NotoSans-Regular`로 그려진다.
- 글꼴을 보내지 않으면 같은 한글이 이미지의 `NotoSansCJKkr-Regular`, `NotoSansCJKkr-Bold`로 그려진다. 그래서 PDF에 든 글꼴 이름으로 보낸 글꼴이 쓰였는지 가를 수 있다. Chromium이 쓰는 PDF는 글꼴 서술자를 압축하지 않아 바이트에서 이름을 찾을 수 있고(`pdffonts`는 이 기계에 없다), `TestConvert_Real`이 이것을 확인한다.
- 리포트가 보내는 파일은 woff2 다섯 개(약 4.3MB)와 HTML, CSS로 5MB쯤이다. 본문 한도의 기본값은 없음이다.

### 옵션과 시간

`gotenberg --help`(9.9.1)와 설정 문서로 확인했다. 환경 변수 이름은 옵션 이름을 대문자로 바꾸고 `-`를 `_`로 바꾼 것이다.

- 상태 확인은 `GET /health`이고 `{"status":"up","details":{"chromium":{"status":"up",…}}}`을 돌려준다.
- 기본 Chromium 거부 목록은 `^file:(?!//\/tmp/).*`로, 문서와 `--help`가 같다. 허용 목록(`--chromium-allow-list`)을 두면 거기에 맞지 않는 주소는 모두 막힌다(로그 `rule: allow-list`).
- `--chromium-auto-start`를 켜면 컨테이너가 뜬 뒤 약 2초에 health가 `up`이 되고 첫 변환이 0.29초, 끄면 첫 변환이 0.49초다(둘째부터는 0.2초).
- 요청 제한(`--api-timeout`)이 끝나면 Gotenberg는 **503**을 돌려준다(본문 `The request exceeded the time limit…`). 기다리기 조건(`waitForSelector`)이 제한 안에 끝나지 않을 때도 503이다. 504는 보지 못했다. 제한 15초에서 28MB HTML을 보내면 15.7초에 503이 왔고, 그 직후의 변환은 정상이었다. 본문 한도를 넘으면 413, 양식 오류는 400이다.
- 메모리가 모자라 Chromium의 렌더러가 죽으면 Gotenberg는 **400**과 `Chromium failed to print the PDF; this usually happens when the page is too large`를 돌려준다(3~8초). 메모리가 모자란 상태에서는 제한 15초를 지키지 못하기도 한다. 한 번은 19.3초에 503이 왔고, 메모리 한도가 없을 때는 75초 뒤에 응답 없이 연결이 끊겼다(로그 `hard timeout: context deadline exceeded`). 그래서 web은 Gotenberg의 제한에 기대지 않고 자기 제한(20초)을 따로 건다.

### 메모리

리포트 크기의 문서로 쟀다. 글꼴 woff2 다섯 개, CSS, 바닥글과 표 한 줄에 다섯 칸(한글, `code`, 굵은 글씨)인 HTML이다. 컨테이너의 cgroup 값(`/sys/fs/cgroup/memory.current`, `memory.peak`, `memory.events`의 `oom_kill`)과 `docker stats --no-stream`으로 쟀다. `memory.current`와 `memory.peak`에는 tmpfs와 다시 쓸 수 있는 파일 캐시가 들어 있고, 익명 메모리(`anon`)는 Chromium이 쉬고 있을 때 140~150MB다.

| 상태 | 결과 |
|---|---|
| 뜬 직후(한도 없음) | `docker stats` 188MB |
| 뜬 직후(`--memory 1g`) | current 371MB, peak 397MB |
| 1,000줄(약 1.1MB HTML, 21쪽) | 0.9~1.1초, peak 475~492MB |
| 3,000줄(55쪽 안팎) | 2.4~2.6초, peak 525MB |
| 6,000줄(6절 × 1,000줄, 계획의 최대 절 크기) | 한도 1g: 렌더러가 cgroup 안에서 죽고(`oom_kill` 1씩 증가) 400(3~8초) 또는 503(19.3초). 다른 서비스는 죽지 않았다. 바로 뒤의 1,000줄 변환은 정상 |
| 6,000줄, 한도 없음 | `docker stats` 730MB까지 오른 뒤 75초에 응답 없이 끊겼다. 2GB VM의 커널이 컨테이너 안 프로세스를 죽였다(`OOMKilled=true`) |
| 6,000줄, 한도 1g + `--chromium-max-memory=640MB` | 59초 동안 응답 없이 끊겼다. 한도만 둔 것보다 나빠 쓰지 않는다 |

tmpfs의 실제 사용량은 리포트 요청 하나에 6MB쯤이었다(`df -h /tmp`).

### 허용 주소 좁히기

허용 목록을 `^file:///tmp/<uuid>/<uuid>/[^/]+$`(UUID는 `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)로 둔 컨테이너에 표지 파일을 심고, 다음 주소를 iframe으로 여는 문서를 보냈다. PDF를 열어 보고 로그의 차단 규칙을 확인했다.

| 주소 | 결과 |
|---|---|
| `file:///tmp/marker-root.txt` | 막힘(allow-list), 내용 없음 |
| `file:///tmp/../etc/passwd` | Chromium이 `file:///etc/passwd`로 정규화, 막힘(allow-list) |
| `file:///tmp/<a>/<b>/../../marker-root.txt`, `%2e%2e` 형태 | `file:///tmp/marker-root.txt`로 정규화, 막힘(allow-list) |
| `file:///tmp/<a>/<b>/..%2f..%2fmarker-root.txt` | 막힘(file-prefix) |
| `file:///tmp/.config/marker.txt` | 막힘(allow-list) |
| 다른 요청 모양의 폴더 `file:///tmp/<a>/<b>/other.txt` | 막힘(file-prefix) |
| `file:///tmp/<a>/<b>/sub/deep.txt` | 막힘(allow-list) |
| 요청과 함께 보낸 `own.txt` | 열림, 내용이 PDF에 있다 |

같은 목록에서 리포트 크기 문서의 글꼴은 `IBMPlexSansKR`, `IBMPlexSansKR-Bold`, `D2Coding`으로 들어갔다. compose의 `pdf-test`를 이 설정으로 다시 띄운 뒤에도 같은 결과였다.

대소문자: `Footer.html`이라는 파일을 보내면 바닥글로 쓰이지 않고 본문만 인쇄된다(PDF로 확인).

### 컨테이너 제한

`read_only`, `/tmp` tmpfs, `cap_drop: ALL`, `no-new-privileges`를 모두 켜고 시작했다.

- 그대로는 Chromium이 뜨지 않는다. 오류는 `start chromium: … chrome failed to start: chrome_crashpad_handler: --database is required`다. 같은 설정에서 `read_only`만 빼면 뜬다. Chromium이 HOME(`/home/gotenberg`, 읽기 전용 루트에 있다) 아래에 프로필과 crash 데이터베이스를 만들기 때문이다.
- 제한을 풀지 않고 `HOME=/tmp`로 tmpfs를 가리키게 하자 네 제한을 모두 켠 채로 뜨고 변환했다. 그래서 풀어 준 제한은 없다.
- uid 1001이 `/tmp` tmpfs(권한 1777)에 쓸 수 있다. `/dev/shm`은 Docker 기본값(64MB)으로 충분했다.

### 네트워크

- `internal: true`인 `pdf` 네트워크에만 붙은 임시 컨테이너에서 `pdf:3000/health`는 200이었고, `example.com`, `postgres`, `openbao`, `valkey`, `keycloak`, `host.docker.internal`은 이름을 찾지 못했으며(curl 종료 코드 6), `1.1.1.1`, 이 네트워크의 게이트웨이(`172.19.0.1:8080`, `:15432`), colima 호스트 주소(`192.168.5.2:8080`)는 연결되지 않았다(종료 코드 7). `pdf` 컨테이너 안에서도 같았다.
- web 컨테이너의 네트워크 이름공간에서 `http://pdf:3000/health`는 200이었다. web은 `PDF_URL`과 `pdf` 네트워크를 더한 채로 지금의 바이너리로 뜨고 기존 화면이 그대로 동작했다(이 확인은 web이 `PDF_URL`을 읽기 전의 것이다).
- Chromium은 뜰 때 `update.googleapis.com`에 닿으려 하는데 Gotenberg의 프록시가 허용 목록으로 막는다(로그 `CONNECT blocked`).
- `<script>`가 바꾸려던 글자는 바뀌지 않았고, `file:///etc/passwd` 스타일시트, `file:///etc/hostname` iframe, 바깥 주소의 `<img>`는 모두 `outbound request blocked by the Chromium filters`로 막힌 채 PDF가 나왔다.

## 결정

compose의 `pdf` 서비스는 다음과 같다. 통합 테스트용 `pdf-test`(`test` 프로필)는 같은 이미지와 옵션을 쓰고, 기본 네트워크에 붙어 `127.0.0.1:13000`에 열린다.

| 설정 | 이유 |
|---|---|
| `gotenberg/gotenberg:9.9.1-chromium@sha256:d722dfb3…` | Chromium만 든 이미지. index digest라 amd64와 arm64 모두 같은 고정 값을 쓴다 |
| `--pdf-disable-routes`, `--pdf-disable-stages` | PDF 합치기, 서명 등과 변환 뒤 처리 단계를 끈다. 쓰는 것은 Chromium 변환 경로뿐이다 |
| `--chromium-disable-javascript` | 리포트 문서에는 스크립트가 없다. 들어가더라도 실행되지 않는다 |
| `--chromium-allow-list=^file:///tmp/<uuid>/<uuid>/[^/]+$` | 요청 폴더 바로 아래의 파일만 연다. 다른 `file://`(`/tmp` 아래의 Chromium 프로필과 캐시 포함)와 모든 `http(s)`가 막힌다. Gotenberg의 `file-prefix` 규칙이 다른 요청의 폴더를 막는다 |
| `--chromium-deny-private-ips`, `--chromium-deny-public-ips` | 목록을 빠져나간 주소가 있어도 어떤 호스트에도 닿지 않게 한 겹 더 둔다 |
| `--webhook-disable`, `--api-disable-download-from` | Gotenberg 자신이 바깥에 요청하는 두 기능을 끈다 |
| `--api-timeout=15s` | web의 변환 제한(20초)보다 짧아 Gotenberg가 먼저 503으로 답한다. 실측 15.7초 |
| `--api-body-limit=32MB` | 리포트 요청은 5MB쯤이다. 더 큰 요청은 413으로 거부한다 |
| `--chromium-auto-start` | 첫 내보내기가 Chromium 기동을 기다리지 않게 한다 |
| `HOME=/tmp` | Chromium의 프로필과 crash 데이터베이스를 tmpfs에 둔다. 이것으로 `read_only`를 풀지 않았다 |
| `networks: [pdf]`, 호스트 포트 없음 | 바깥과 다른 서비스에 닿지 못하고 web만 닿는다 |
| `read_only: true`, `tmpfs: /tmp:size=256m` | 요청의 파일과 Chromium 프로필만 메모리에 쓴다. 리포트 요청은 5MB쯤이고 실제 사용량은 6MB였다. tmpfs는 컨테이너의 메모리 한도에 함께 센다 |
| `mem_limit: 1g` | 너무 큰 문서에서 Chromium이 메모리를 다 쓰면 이 한도 안에서 죽고 그 변환만 실패한다. 한도가 없으면 호스트(VM)의 커널이 아무 서비스나 죽일 수 있다. 3,000줄 문서의 peak 525MB(tmpfs, 캐시 포함)에 여유를 두고, tmpfs가 다 차도(256MB) Chromium이 쓸 자리가 남게 했다. `pdf-test`도 같다. `docker inspect`의 `HostConfig.Memory`가 1073741824다 |
| `cap_drop: [ALL]`, `no-new-privileges:true` | 필요한 capability가 없다 |
| `restart: unless-stopped` | 변환기가 죽어도 web이 다시 쓸 수 있게 한다 |
| healthcheck `curl … /health` | Chromium까지 `up`인지 본다 |

`networks.pdf`는 `internal: true`이고, web은 `default`와 `pdf` 둘 다에 붙으며 `PDF_URL=http://pdf:3000`을 받고 `pdf`가 시작된 뒤에 뜬다(건강할 때까지 기다리지 않는다).

시간 제한은 Gotenberg 요청 제한 15초 < web의 변환 제한 20초(Task 5) < 클라이언트 `http.Client` 제한 25초(Task 5) < web `WriteTimeout` 30초 순서다.

클라이언트 `internal/pdf`는 503과 504, 연결 실패, 제한 시간 초과, 응답을 읽다가 끊긴 경우를 `ErrUnavailable`로, 그 밖의 상태 코드(400, 413, 500 등), PDF가 아닌 응답, 64MiB를 넘는 응답을 `ErrRejected`로 돌려준다. 오류에는 응답 본문을 넣지 않는다. `index.html`, `header.html`, `footer.html`과 겹치는 이름, 빈 이름, `.`과 `..`, 슬래시나 역슬래시, 제어 문자, 깨진 UTF-8이 든 이름은 보내지 않고 거부한다.

한글 글꼴은 pdf 이미지에 넣지 않고, 화면과 같은 woff2 파일을 요청마다 함께 보낸다.

## 이유

- 설계 8절의 [제안]은 "한글 글꼴은 pdf 이미지에 넣는다"였다. 그러면 공식 이미지 위에 이미지를 따로 빌드하고 고정해야 하고, 화면과 PDF의 글꼴 파일이 둘로 나뉘어 버전이 어긋날 수 있다. 요청마다 보내면 공식 이미지를 digest로 그대로 쓰고, 화면과 PDF가 같은 파일을 쓴다. 요청이 5MB쯤 커지지만 같은 호스트의 내부 네트워크라 변환 시간에 비해 작다. "pdf 컨테이너는 외부 자원을 받아오지 않는다"는 그대로 지켜진다.
- 이미지에 Noto Sans CJK KR이 있어 보낸 글꼴을 읽지 못해도 한글이 깨지지 않고 다른 글꼴로 조용히 그려진다. 그래서 확인은 화면이 아니라 PDF의 글꼴 이름으로 한다.
- "자기 요청의 폴더"는 Gotenberg의 `file-prefix` 규칙이 지킨다. 허용 목록은 그 위에 한 겹을 더해, 요청 폴더 모양이 아닌 `/tmp`의 모든 것(`HOME=/tmp` 때문에 생기는 Chromium 프로필, 캐시, 소켓)과 하위 폴더를 막는다. Chromium의 사용자 데이터 폴더는 요청 폴더와 모양이 같아 정규식만으로는 가를 수 없고, 그 바로 아래의 파일(`Local State` 등)은 `file-prefix` 규칙이 막는다.
- 메모리 한도는 `--chromium-max-memory`가 아니라 cgroup 한도로 둔다. 실험에서 `--chromium-max-memory`는 큰 문서를 더 오래 붙잡고 응답 없이 끊었고, cgroup 한도만 둔 쪽은 몇 초 안에 400으로 답했다.
- 리포트가 가질 수 있는 크기는 `docs/decisions/0042-what-a-scan-report-shows.md`의 "PDF로 인쇄할 수 있는 크기"가 정한다. 한도를 없애도 큰 문서가 인쇄되지 않고 다른 서비스를 위험하게 했으므로 메모리 한도를 높이지 않는다.
- 503을 `ErrUnavailable`로 두는 것은 Gotenberg가 제한 시간 초과와 Chromium 재시작 중을 503으로 알리기 때문이다. 사용자에게는 "잠시 뒤 다시 시도"가 맞는 안내다.

## 검토한 다른 방법

- 글꼴을 넣은 이미지를 빌드한다: 위 이유로 하지 않았다.
- CSS와 글꼴을 `data:` 주소로 HTML에 넣는다: HTML이 6MB가 되고 `template.CSS`로 바꿔야 한다(계획 6의 검토 항목 11).
- `read_only`를 끄거나 `/home/gotenberg`에 tmpfs를 더 둔다: `HOME=/tmp`만으로 되므로 필요 없다.
- 허용 목록을 `^file:///tmp/`로 둔다: `file-prefix` 규칙이 있어 다른 요청의 파일은 막히지만, 규칙 하나에만 기대게 된다. 좁힌 목록으로도 글꼴과 스타일시트가 그대로 읽혀 좁힌다.
- `--chromium-max-memory`: 위 실험에서 더 나빠 쓰지 않는다. `--chromium-max-concurrency`, `--chromium-max-queue-size`: 쓰는 곳이 web 하나이고 사용자가 적어 지금은 기본값을 둔다.
- 거부 목록만 바꾼다(`^(?!file:///tmp/)`): 허용 목록이 같은 일을 더 분명하게 하고, 허용 목록을 두면 그 밖의 주소는 모두 막힌다는 것을 로그로 확인했다.
