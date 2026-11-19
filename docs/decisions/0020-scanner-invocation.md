# 스캐너를 실행하는 방식과 출력을 읽는 방식

- 날짜: 2026-11-19
- 상태: 확정

## 배경

worker는 betterleaks와 opengrep을 외부 명령으로 실행하고(0002), 키와 인증서 파일은 Go로 직접 찾는다. 스캐너의 표준 출력과 표준 오류에는 찾은 값이 평문으로 들어 있을 수 있다. 계획 문서의 실행 인자는 도구를 실행해 보지 않고 쓴 것이어서, 구현 전에 두 도구의 현재 안정 버전을 내려받아 실제로 실행해 확인했다.

확인한 사실(darwin/arm64 호스트와, worker와 같은 alpine 3.24.4 이미지에서 uid 65532, 읽기 전용 루트, `/tmp`만 tmpfs, 네트워크 없음으로 실행):

- betterleaks 1.10.1(최신 안정판. 2.0.0은 아직 rc), opengrep 2.0.0(최신 안정판). 두 도구 모두 linux용 실행 파일이 정적 링크라 alpine(musl)에서 그대로 실행된다. worker의 기반 이미지는 바꾸지 않았다.
- betterleaks: JSON 보고서는 결과의 배열이다(결과가 없으면 `[]`). `File`은 넘긴 폴더가 절대 경로면 절대 경로다. `StartLine`, `StartColumn`은 `Match`가 시작하는 곳이고, 열은 1부터 세는 **바이트** 열이다(같은 줄 앞에 한글이 있을 때 확인, UTF-8 BOM도 3바이트로 센다). 여러 줄에 걸친 일치는 `Match`에 줄바꿈이 그대로 들어 있고 `StartLine`/`StartColumn`은 첫 줄 기준이다. 결과가 있으면 `--exit-code`로 준 코드, 없으면 0, 설정 파일 오류 같은 자체 오류는 1로 끝난다. 읽을 수 없는 파일은 표준 오류에 경고만 쓰고 건너뛴다.
- betterleaks는 스캔 대상 폴더의 `.gitleaksignore`/`.betterleaksignore`를 `-i` 값과 상관없이 읽어 결과를 숨긴다. 끄는 옵션이 없다. 대상 폴더의 `.gitleaks.toml`은 `--unsafe-auto-load-config` 없이는 읽지 않는다. 줄 끝의 `gitleaks:allow` 주석도 결과를 숨긴다(`--ignore-gitleaks-allow`로 끈다). jar 같은 압축 파일은 바이너리로 보고 건너뛴다. HOME 아래 `.cache`에 정규식 엔진(wazero) 컴파일 캐시를 쓰며, HOME에 쓸 수 없어도 스캔은 된다.
- opengrep: `start.offset`, `end.offset`은 **바이트** 오프셋이다(앞줄과 같은 줄에 한글이 있을 때 확인). `col`도 바이트 기준이다. 결과의 `extra.lines`와 `extra.metavars`에는 일치한 원문이 있고, `errors[].message`에도 소스 원문이 들어 있다.
- opengrep은 규칙 파일의 경로를 규칙 ID 앞에 붙인다(`private.tmp...rules.java-password-field`). `--no-rewrite-rule-ids`를 주면 규칙에 쓴 ID 그대로다.
- opengrep의 종료 코드: 일부 파일이 구문 분석에 실패해도(`PartialParsing`, `level: warn`) 0이고 `errors[]`에만 남는다. 읽을 수 없는 파일은 `errors[]`에도 없이 조용히 건너뛴다(`--x-ls-long`에서만 `insufficient_permissions`로 보인다). 규칙 파일이 잘못되었으면 7(`errors[]`에 code 5 `Invalid YAML` 또는 code 7 `Missing config`)이다.
- opengrep의 기본 제외: 아무 옵션 없이 실행하면 `test/`, `vendor/`, `build/`(기본 `.semgrepignore`)와 `.gitignore` 대상이 빠진다. `--x-ignore-semgrepignore-files`가 앞의 셋을, `--no-git-ignore`가 `.gitignore` 대상을 다시 넣는 것을 확인했다. 줄 끝의 `nosemgrep` 주석은 결과를 숨긴다(`--disable-nosem`으로 끈다).
- opengrep의 `--exclude` 패턴은 gitignore 문법이고 프로젝트 루트에 고정된다. 스캔 폴더가 git 저장소의 하위 폴더이면 프로젝트 루트는 저장소 루트라서 `/main`이 스캔 폴더의 `main`을 가리키지 않는다. `--project-root=<폴더>`를 주면 그 폴더가 루트가 되고(버전 관리 없는 폴더로 본다) 패턴이 그 폴더 기준이 된다. `\`로 `*`, `?`, `[`, `]`, 공백을 이스케이프할 수 있다.
- opengrep은 `/tmp`, HOME에 아무것도 쓰지 않았다. 버전 확인 등 네트워크 접속 없이 동작한다.
- 배포 파일: betterleaks는 `betterleaks_<버전>_<linux|darwin>_<arm64|x64>.tar.gz`와 `checksums.txt`, opengrep은 실행 파일 그대로 `opengrep_linux_aarch64`, `opengrep_linux_x86`, `opengrep_osx_arm64`, `opengrep_osx_x86`이고 체크섬 파일은 없다(서명 `.sig`, `.cert`는 있다). GitHub 릴리스 API가 주는 자산 digest를 썼고, betterleaks는 `checksums.txt`와 digest가 같다.

## 결정

### 버전 고정과 내려받기

- `deploy/worker/scanners.lock`에 도구, 버전, 운영체제·아키텍처(Go 이름), SHA-256, 주소를 한 줄씩 적는다. linux와 darwin의 arm64, amd64를 모두 적는다.
- `deploy/worker/fetch-scanners.sh <os> <arch> <폴더>`가 받고 확인한다. curl은 https만 허용한다. 모든 파일의 체크섬이 맞아야 하나라도 설치하고, 하나라도 다르면 아무것도 설치하지 않고 실패한다.
- `make scanners`는 호스트 플랫폼의 실행 파일을 `bin/`에, worker `Dockerfile`은 `TARGETOS`/`TARGETARCH`에 맞는 실행 파일을 `/usr/local/bin`에 넣는다. 내려받기용 단계에만 curl을 설치하고 최종 이미지에는 넣지 않는다.
- 스크립트 테스트는 `deploy/worker/fetch-scanners_test.sh`이고 `make scanners-test`로 `make check`에 들어간다. 가짜 curl로 실행하므로 내려받지 않는다.

### 실행 인자

betterleaks:

```
dir --no-banner --config=<config> --report-format=json --report-path=- --exit-code=3
    --max-target-megabytes=20 --timeout=1500 --log-level=warn --ignore-gitleaks-allow -- <dir>
```

종료 코드 0과 3이 정상이다.

opengrep:

```
scan --config=<rulesDir> --json --quiet --no-git-ignore --x-ignore-semgrepignore-files
     --no-rewrite-rule-ids --disable-nosem --project-root=<dir> --max-target-bytes=20MB --timeout=10
     --exclude=.git/ --exclude=target/ --exclude=build/ --exclude=node_modules/
     --exclude=/<프로젝트 제외 경로>… -- <dir>
```

종료 코드 0만 정상이다.

- 계획의 표에 더한 것: betterleaks의 `--ignore-gitleaks-allow`, opengrep의 `--no-rewrite-rule-ids`, `--disable-nosem`, `--project-root`, 그리고 두 도구의 `--` 구분자. 값은 `--옵션=값` 한 인자로 넘긴다.
- 기본 제외 디렉터리는 끝에 `/`를 붙여 같은 이름의 파일은 제외하지 않는다(`textfile.Walk`와 같다). 프로젝트 제외 경로는 앞에 `/`를 붙여 스캔 폴더 기준으로 고정하고 gitignore의 특수 문자를 이스케이프한다.
- betterleaks에는 제외 옵션을 주지 않는다. 이후 스캔 실행기가 자기 파일 목록에 없는 파일의 결과를 버린다.

### 실행

- `internal/scanexec`가 두 도구의 실행을 한 곳에서 맡는다. `exec.CommandContext`와 인자 슬라이스로 실행하고 셸을 거치지 않는다.
- 환경 변수는 `PATH`, `HOME`, `TMPDIR` 셋만 넘긴다. 값은 `run`에서 읽어 생성자로 받는다(`scanexec.Env`).
- 표준 출력은 메모리에만 받고 256MiB를 넘으면 프로세스를 죽이고 실패한다. 표준 오류는 null 장치로 보낸다. 파일에 쓰지 않는다.
- 스캐너는 자기 프로세스 그룹에서 실행한다. `ctx`가 끝나거나 출력이 한도를 넘으면 그룹 전체를 죽이고, 스캐너가 끝나면 종료 코드와 상관없이 그룹에 남은 프로세스를 모두 죽인다. 스캐너가 끝난 뒤 5초 안에 표준 출력이 닫히지 않으면(남은 프로세스가 쥐고 있으면) 파이프를 닫고, 그때까지 읽은 출력은 버리고 실패로 본다. 종료 코드가 정상(betterleaks의 3 포함)이어도 같다.
- 오류에는 도구 이름과 종료 코드 또는 정해진 사유("output too large", "output is not a valid report" 등)만 넣는다. `*exec.ExitError`나 JSON 디코더의 오류처럼 출력의 내용을 담을 수 있는 오류는 감싸지 않는다. `ctx`가 끝나서 멈춘 경우는 `ctx.Err()`를 감싼다.

### 출력 읽기

- betterleaks: `Match` 안에서 `Secret`이 처음 나오는 곳으로 줄과 바이트 열을 옮긴다. `Match`에 줄바꿈이 있으면 그 앞의 줄바꿈 수만큼 줄을 더하고 열은 마지막 줄바꿈 뒤부터 센다. `Secret`이 `Match`에 없거나 비어 있으면 버린다. 분류와 종류는 규칙의 `category:…`, `kind:…` 태그에서 오고, 둘 다 없으면 규칙 ID에 `private-key`가 있을 때 `ssh-credential`/`private-key`, 아니면 `other-secret`/`other`다. 하나만 있거나 알 수 없는 이름이면 규칙 파일의 잘못으로 보고 스캔이 실패한다.
- opengrep: `check_id`, `path`, `start.offset`, `end.offset`, `extra.metadata.category`, `extra.metadata.kind`만 읽는다. `extra.lines`, `extra.metavars`, `errors[]`의 내용은 디코딩하지 않고 `errors[]`는 건수만 센다. `metadata`의 분류나 종류가 없거나 알 수 없으면 스캔이 실패한다. 오프셋이 없거나 거꾸로면 버린다.
- 두 도구 모두 결과의 경로를 스캔 폴더 기준 상대 경로로 바꾸고, 폴더 밖을 가리키면 버린다.
- 버린 결과의 수(`Dropped`)와 opengrep의 오류 수(`Errors`)는 `scan.Counts`로 돌려준다. `scan.Scanner` 인터페이스의 `Scan`은 계획대로 두고, 각 스캐너에 `ScanWithCounts(ctx, dir) ([]scan.Raw, scan.Counts, error)`를 더했다. 테스트와 이후의 실행기는 `Parse(out, dir) ([]scan.Raw, scan.Counts, error)`나 `ScanWithCounts`로 건수를 얻는다.

### 키 파일

`internal/keyfile`은 `textfile.Walk`로 일반 파일만 돌며, 이름(`id_rsa`, `id_dsa`, `id_ecdsa`, `id_ed25519`), 확장자(`jks`, `p12`, `pfx`, `keystore`, `ppk`, 대소문자 무시), 또는 첫 4KiB 안의 한 줄 전체가 `-----BEGIN … PRIVATE KEY-----`나 `-----BEGIN CERTIFICATE-----`인 파일을 찾는다. 결과는 파일 전체(`Offset` 0, `EndOffset` -1)이고 값을 담지 않는다. 읽을 수 없는 파일은 건너뛴다. `Version`은 고정 문자열 `1`이고 찾는 기준이 바뀌면 올린다.

## 이유

- 스캔 대상 프로젝트가 스스로 결과를 숨길 수 없어야 한다. `.gitignore` 대상과 `test/`, `vendor/`를 스캔하기로 한 것(계획의 전역 제약)과 같은 이유로, 주석(`gitleaks:allow`, `nosemgrep`)으로 숨기는 기능도 끈다.
- `--project-root`가 없으면 제외 경로의 기준이 스캔 폴더가 아니라 그 폴더를 담은 git 저장소의 루트가 되어, 프로젝트 제외 경로가 엉뚱한 곳을 가리킨다.
- `--no-rewrite-rule-ids`가 없으면 규칙 ID에 규칙 폴더의 절대 경로가 들어가 환경마다 ID가 달라진다.
- 출력을 파일로 받으면(`--report-path`, `--output`) tmpfs라도 평문 값이 파일로 남는다. 파이프로 받으면 메모리에만 있다.
- 그룹 단위로 죽이지 않으면 스캐너가 띄운 자식 프로세스가 취소 뒤에도 남는다. `WaitDelay`가 없으면 표준 출력을 쥔 자식 때문에 worker가 멈출 수 있다.
- 버린 결과 수를 `Scan`의 반환값에 넣으려면 계획의 인터페이스를 바꿔야 한다. 이후 실행기에서 건수가 필요한 곳은 구체 타입의 `ScanWithCounts`를 쓰면 되므로 인터페이스는 그대로 둔다.
- 키 파일 탐지에서 표식을 줄 전체로 요구해야 "PRIVATE KEY"라는 말이 들어간 소스나 문자열 리터럴 안의 표식이 키 파일로 잡히지 않는다. 소스 안에 박힌 키는 betterleaks 규칙이 값으로 찾는다.

## 검토한 다른 방법

- `cmd.Stdout`에 한도가 있는 버퍼를 주고 exec의 `WaitDelay`에 맡기기: 종료 코드가 0이 아니면 `Wait`가 `*ExitError`만 돌려주고 `ErrWaitDelay`를 버려서(Go 1.27 `os/exec`의 `Wait`), 파이프가 강제로 닫혔는지 알 수 없다. 그래서 파이프를 `os.Pipe`로 직접 만들고, 끝(EOF)을 직접 기다린다. `StdoutPipe`는 `Wait`가 파이프를 닫으므로 같은 판단을 할 수 없다.
- opengrep의 `--x-ignore-semgrepignore-files` 대신 빈 `.semgrepignore`를 `--semgrepignore-filename`으로 지정: 내부용 옵션을 피할 수 있지만 대상 폴더의 `.semgrepignore`는 여전히 읽힌다. 내부용 옵션은 실행 때 경고만 내고 동작하므로 쓰고, 버전을 올릴 때 확인한다.
- betterleaks 2.0.0-rc: 정식 버전이 아니어서 쓰지 않는다.
- worker 기반 이미지를 glibc 이미지로 바꾸기: 두 실행 파일이 정적 링크라 필요 없다.

## 남은 일

- betterleaks는 스캔 폴더의 `.gitleaksignore`/`.betterleaksignore`를 끌 수 없다. 스캔 실행기가 스캐너에 UTF-8 사본 폴더를 넘길 때 이 두 파일은 사본에 넣지 않는다(실행기를 만드는 계획에서 처리한다).
