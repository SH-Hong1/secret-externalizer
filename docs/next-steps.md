# 이어서 할 일

이 문서는 세션이 바뀌어도 남은 일이 빠지지 않도록 적어 둔 목록이다. `CLAUDE.md`가 이 문서를 불러오므로 이 폴더에서 연 세션은 항상 이 목록을 읽는다.

- 항목을 끝내면 체크 표시를 하고, 결과를 한 줄로 적는다.
- 절 하나가 모두 끝나면 그 절을 지운다.
- 목록이 비면 이 문서와 `CLAUDE.md`의 `@docs/next-steps.md` 줄을 함께 지운다.

## 준비

- 이 폴더(`secret-externalizer`)에서 세션을 연다. 상위 폴더에서 열면 hook 설정이 적용되지 않는다.
- `jq`가 설치되어 있어야 한다.
- 종료 시 규칙 검토는 `claude` 명령을 실행한다. `claude`가 `PATH`에 있고 로그인되어 있어야 한다. 없으면 검토 없이 턴이 끝나고 그 사실이 메시지로 보인다.
- `bin/`은 git에 올라가지 않는다. 새로 받은 저장소에서는 `make tools`를 먼저 실행한다.

## 다음 계획

설계 문서: `docs/superpowers/specs/2026-11-18-secret-externalizer-design.md`

| 번호 | 계획 | 상태 |
|---|---|---|
| 1 | 개발 규칙과 검증 hook | 완료 |
| 2 | 골격과 로그인 | 완료 (`docs/superpowers/plans/2026-11-18-02-skeleton-and-login.md`) |
| 3 | 프로젝트 등록과 스캔 | 완료 (`docs/superpowers/plans/2026-11-19-03-project-and-scan.md`) |
| 4 | 화면 디자인 시안과 판정 화면 | 완료 (`docs/superpowers/plans/2026-11-19-04-design-and-triage.md`) |
| 5 | 적용 | |
| 6 | 리포트와 PDF | |
| 7 | 대시보드 | |

- [ ] **지금 상태(2026-11-20): 계획 4를 두 PR로 구현했다. PR 1은 #9(`feat/verdict-screen`, Task 1~5), PR 2는 `feat/triage-screen`(Task 6~10)이다. 다음은 계획 5(적용)이다.** 계획 5의 계획 문서는 `docs/superpowers/plans/`에 한글로 쓰고, 사용자의 검토를 받은 뒤 구현한다. 계획 5가 이어받을 일은 아래 "계획 4에서 남은 일"에 있다.
- [x] 계획 3을 Subagent 방식으로 구현한다면, 새 종료 hook(`stop-review.sh`)이 하위 에이전트의 결과를 기다리는 동안 턴 종료를 막지 않는지 첫 Task에서 확인한다. 막으면 구현을 멈추고 hook부터 고친다. 2026-11-19에 계획 3 Task 1에서 확인했다. 하위 에이전트가 도는 동안 턴이 막히지 않고 끝났다.
- [x] 계획 2 전 정리(PR #2)가 병합되었는지 확인한다. 2026-11-18에 병합되었다.
- [x] `cmd/worker`를 만들면 `AGENTS.md` "Project layout"의 진입점 문장에 추가한다. 계획 2는 worker를 계획 3으로 미루는 안으로 작성했다. 2026-11-19에 계획 3 Task 4에서 `cmd/worker`를 만들고 진입점 문장에 추가했다.
- [x] 계획 2의 "검토 때 정해 주실 것" 7개 항목에 사용자의 답을 받고, 그에 맞게 계획을 고친 뒤 구현한다. Task 1~7을 구현했다.
- [x] 계획 2 Task 7의 확인 표(1~9)를 Chrome에서 확인한다. 2026-11-18에 Chromium으로 확인했다: `dev-user`로 로그인하면 이름과 역할이 있는 홈이 나오고, `/audit`은 `dev-user`에 403, `dev-admin`에 200이며, 로그아웃은 Keycloak 확인 화면을 거쳐 로그인 화면으로 돌아오고, web 로그에 비밀 값이 없었다.
- [x] 계획 2의 PR을 만들고 검토를 받는다. PR #3이 2026-11-18에 병합되었다.
- [x] Task 6/8 전에 `ParsePath`가 NUL 외의 제어 문자(줄바꿈 등)도 거부하게 한다. 제외 경로가 스캐너 설정과 제외 목록에 들어가면 줄바꿈으로 줄을 끼워 넣을 수 있다. 2026-11-19에 `0x20` 미만과 `0x7f`를 모두 `ErrInvalidPath`로 거부하게 했다.
- [ ] worker의 uid(65532)를 설정할 수 있게 한다. 바꾸려면 `deploy/openbao/make-secrets.sh`가 만드는 secret-id 파일의 소유자도 함께 바꿔야 한다. Linux 호스트에서 읽을 수 없는 파일을 건너뛰고 세는 동작과, uid 65532가 읽을 수 있어야 한다는 요구 사항은 `docs/local-run.md`에 적었고, 건너뛰는 동작은 Task 6과 10에서 구현한다.
- [x] worker가 정상 종료(SIGTERM)할 때 잡고 있던 작업을 풀어 준다(`queued`, `attempts - 1`, `locked_by`로 확인). 2026-11-19에 `ReleaseJob`을 만들고, 종료로 멈춘 handler 뒤에 `job.Run`이 작업과 스캔을 `queued`로 돌려주게 했다(`docs/decisions/0022-job-release-and-local-lease.md`).
- [x] DB 장애로 임대 연장이 계속 실패할 때, worker가 자기 임대 만료 시각을 넘기면 handler를 취소한다. 2026-11-19에 `job.Run`이 마지막 성공한 임대 요청부터 `Lease`가 지나면 handler를 취소하고 작업은 돌려주지 않게 했다(`docs/decisions/0022-job-release-and-local-lease.md`).
- [x] web의 DB 호출에 제한 시간이 없다. postgres가 멈추면 화면이 멈춘다. 2026-11-19에 DB를 부르는 화면에 요청마다 5초 제한을 걸고(`cmd/web`의 `databaseLimit`, 풀 대기도 이 제한 안에 든다), `db.Open`에 연결 제한 시간 3초를 두었다(DSN에 `connect_timeout`이 있으면 그 값). worker의 쿼리는 작업의 context를 그대로 쓴다.
- [x] 프로젝트 이름에 NUL이나 깨진 UTF-8이 들어오면 400이 아니라 500이 난다. Task 11에서 고친다. 이름에 제어 문자와 깨진 UTF-8이 있으면 400과 양식 다시 보이기로 거부한다.
- [x] `Migrate`가 스키마 어긋남을 알아채지 못한다(바이너리가 모르는 버전이 적용되어 있거나, 높은 번호 뒤에 낮은 번호 파일이 추가된 경우). 마이그레이션이 파일 중간에 실패하는 경우와 잘못된 파일 이름의 테스트도 `0003`을 넣을 때 함께 쓴다. 2026-11-19에 `Migrate`가 두 경우 모두 버전을 밝힌 오류로 아무것도 적용하지 않고 멈추게 했고, 파일 중간 실패와 잘못된 파일 이름의 테스트를 함께 넣었다.
- [ ] `ErrNotFound`, `ErrDuplicate`, `ErrScanActive`를 `db`에서 `project`, `scan`으로 옮길지 Task 10에서 runner를 다시 쓸 때 정한다.
- [ ] Task 10에서 `SaveResult`는 `closeJob`과 같은 트랜잭션 경로를 써서 "항목이 두 번 저장되지 않는다"가 한 트랜잭션의 성질로 남게 한다.
- [ ] 공유하거나 배포하는 환경에 올리기 전에 web이 valkey에 인증하게 한다(비밀번호와 TLS). 지금은 compose 네트워크 안에서 인증 없이 붙는다.
- [ ] 공유하거나 배포하는 환경에 올리기 전에 OpenBao의 봉인 방식을 바꾼다. 지금은 static 봉인의 키가 데이터와 같은 Docker 볼륨 영역(`openbao-secrets`)에 있어 저장 시 암호화가 볼륨에 접근할 수 있는 사람을 막지 못하고, 리스너에 TLS가 없다(`docs/decisions/0017-openbao-setup.md`).
- [ ] base64 등으로 인코딩된 비밀 값은 betterleaks가 디코딩해 찾아도 지금은 위치를 확인하지 못해 버리고(버린 건수에 센다) 그 파일의 주변 줄을 저장하지 않는다. 자리를 정하지 못한 결과가 있는 파일은 모두 같다. 인코딩된 구간을 항목으로 보고하는 일은 이후 계획에서 정한다.
- [ ] 한 브라우저에서 로그인을 동시에 두 번 시작하면 서로 깨진다. `se_login`이 쿠키 하나라서 두 번째 `/login`이 첫 번째 쿠키를 덮어쓰고, 첫 번째 콜백이 두 번째 시도를 꺼내 state 불일치로 400이 된다. 시도마다 쿠키 이름을 달리하는 방법 등을 이후 계획에서 정한다.
- [ ] 판정 화면에서 항목을 옮기는 요청이 worker의 스캔 결과 저장과 겹치면 드물게 스캔 저장이 실패한다(교착 상태로 중단되거나, 저장이 고른 `shared` 묶음의 마지막 구성원이 옮겨 가 묶음이 지워지면 외래 키 오류). 다음 스캔이 회복하지만, worker를 둘 이상 두거나 동시 사용이 많아지기 전에 처리한다(`docs/decisions/0027-bundle-is-the-unit-of-a-verdict.md`의 "알려진 한계").
- [x] 종료 hook(agent)을 점검한다. 계획 2를 Subagent 방식으로 구현하는 동안 두 가지가 보였다. 하위 에이전트의 결과를 기다리는 중에 "계획이 끝나지 않았다"는 이유로 턴 종료를 반복해서 막았는데, 이는 `review-prompt.md`에 없는 판단이고 한 번 되돌려 보낸 뒤에는 승인하라는 규칙과도 어긋난다. 또 가끔 Bash 권한이 거부되어 `git diff`를 보지 못한 채 답했다. 원인을 확인하고, 계획 3을 Subagent 방식으로 시작하기 전에 고친다. 2026-11-19에 고쳤다. 원인은 hook 입력에 마지막 응답과 백그라운드 작업 목록이 들어 있어 검토 에이전트가 그것으로 완료 여부를 판단한 것이고, `agent` hook의 도구 권한은 설정할 방법이 없었다. 검토를 `stop-review.sh`(command hook)로 옮겼다(`docs/decisions/0014-stop-review-as-command-hook.md`).
- [ ] 계획 3 이후의 계획 문서는 `docs/superpowers/plans/`에 한글로 쓰고, 사용자의 검토를 받은 뒤 구현한다.
- [x] 계획 4에 필요한 계획 3의 남은 일. 2026-11-20에 계획 4에서 정리했다: 판정 화면이 항목 ID로 항목을 가리키고, 목록을 한 쪽에 50줄씩 나누며, 마지막 성공한 스캔만 보여 `has_original`을 믿을 수 있고, 건너뛴 파일 이름과 스캐너 오류 수를 스캔 화면에 보이며, 주변 줄은 저장된 그대로 보이기로 했다(`docs/decisions/0028-reveal-is-the-one-place-a-value-is-shown.md`). 남은 base64 값과 제외 경로는 각자의 항목에 있다.

## 계획 4에서 남은 일

계획 4가 하지 않고 넘긴 것과 검토에서 나온 것 가운데 아직 고치지 않은 것이다. 해당하는 계획을 쓸 때 함께 정한다.

- [ ] 계획 5: 오탐으로 판정한 값의 임시 원본을 OpenBao에서 지운다. web은 읽기 권한만 있으므로 계획 5의 적용 작업이 임시 경로를 정리할 때 함께 한다(계획 4 문서의 결정 14).
- [ ] 계획 5: 한 파일 안에 같은 값이 두 번 나오면 따로 이름을 붙일 수 없다. 묶음을 나누는 단위가 파일(항목)이기 때문이다.
- [ ] 계획 5: 호스트, 포트, 계정은 항목마다 묶음 하나로 시작하고 사용자가 손으로 합친다. 같은 접속 정보끼리 자동으로 묶으려면 설정 키가 필요한데 스캔은 그것을 저장하지 않는다.
- [ ] 4바이트보다 짧은 접속 정보 구성 요소(포트 `22`, 계정 `sa`)는 그 파일의 자기 자리에서만 가린다. 규칙이 보고하지 않은 다른 자리의 같은 URL에서는 보일 수 있다(`docs/decisions/0026-connection-string-parts.md`).
- [ ] JDBC 매개변수는 이름만 보고 비밀번호로 정하므로 `passwordCharacterEncoding` 같은 설정 값도 비밀번호 항목이 된다(`docs/decisions/0026-connection-string-parts.md`).
- [ ] htmx를 2.0.11로 고정했다. 4.x가 나와 있으므로 올릴 때 `hx-` 속성과 설정(`htmx-config`)이 그대로 동작하는지 확인한다.
- [ ] 판정 화면의 상태 탭은 묶음 수를 센다. 고객정보는 파일과 종류마다 한 줄로 묶어 보이므로, 한 줄이 "삭제·마스킹" 탭에서 2 이상으로 셀 수 있다.
- [ ] 프로젝트 화면과 스캔 화면은 건수만 필요한데 판정 목록 한 쪽(`Entries`)을 읽는다. 건수만 세는 저장소 메서드를 둔다.
- [ ] 계획 5: 적용은 묶음마다 `Check`를 다시 하고, 다시 나타난 구성원 때문에 허용되지 않게 된 판정은 적용하지 않는다. 판정은 마지막 스캔이 찾은 구성원만으로 확인하므로, 사라진 JSP 구성원이 있는 묶음을 외부화로 판정한 뒤 그 값이 다시 나타나면 외부화 묶음에 프론트엔드 구성원이 생긴다(`docs/decisions/0027-bundle-is-the-unit-of-a-verdict.md`의 "알려진 한계").
- [ ] 판정 화면 목록 줄은 shell 파일에서만 찾은 묶음에도 이름을 `ext.<이름>`으로 보인다. 상세는 `EXT_…`로 보인다. 목록도 묶음의 파일 종류에 맞춘다.
- [ ] htmx로 보낸 원본 보기 요청이 실패하면(세션 만료, 403, 500) 화면에 아무 변화가 없다. 오류를 보이거나 일반 POST로 넘어가게 한다.
- [ ] `NewServer`가 위치 매개변수 열한 개를 받고 그중 넷이 같은 저장소다. 계획 5가 더 넣기 전에 묶는다.
- [ ] 이 브랜치(`feat/triage-screen`)를 빈 볼륨에서 띄우는 일(`docker compose down -v` 뒤 `up --build`)을 web에 OpenBao secret-id 마운트가 생긴 뒤로 해 보지 않았다.

## 계획 3에서 남은 일

계획 3의 검토에서 나온 것 가운데 아직 고치지 않은 것이다. 해당하는 계획을 쓸 때 함께 정한다.

- [ ] 규칙의 알려진 누락: `.properties`의 `jdbc.pass=`와 공백으로 구분한 형태, YAML 블록 스칼라(`password: >`), `sshpass -e`와 `SSHPASS=`, `setRequestProperty("Cookie", …)`, `settings.xml`의 `<username>`, `<constructor-arg name="password">`, `dbPassword` 같은 속성 이름, 외국인등록번호(7번째 자리 5~8), 하이픈 없는 전화번호.
- [ ] 규칙의 알려진 오탐: `TOKEN_HEADER = "X-Auth-Token"` 같은 상수, `MSG_PWD` 같은 메시지 상수, SpEL `#{…}` 값, DB가 아닌 pool bean의 `url`/`user`, 원격 명령의 `sed -i`.
- [ ] worker가 둘 이상일 때: 오래된 임시 원본 정리가 다음 스캔의 `PutPending`과 겹치면 `has_original = true`인데 원본이 없는 항목이 생길 수 있다. worker를 늘리기 전에 삭제 직전에 최신 스캔을 다시 확인하게 한다.
- [ ] compose의 worker에 `restart:` 정책이 없다. 하나뿐인 worker가 죽으면 스캔이 "실행 중"으로 남고 그 프로젝트의 새 스캔을 막는다.
- [ ] compose의 모든 서비스가 한 네트워크를 쓴다. web이 `openbao:8200`에 닿을 수 있고(자격 증명은 없다) worker와 스캔 도구가 바깥으로 나갈 수 있다. 네트워크를 나눈다.
- [ ] 큰 값: 지문 요청은 건수(500)로만 나누고 크기로 나누지 않는다. 20MiB에 가까운 키 파일은 OpenBao의 요청 한도를 넘을 수 있고, 그때 실패 사유가 "secret store could not be reached"로 나온다. 빈 키 파일(빈 HMAC 입력)도 확인하지 않았다.
- [ ] PEM 키가 CRLF 파일에 있으면 betterleaks가 본문을 디코딩한 결과를 하나 더 보고해 그 파일의 주변 줄이 빠지고 버린 건수가 늘 수 있다(실제 DER 본문에서도 그런지는 확인하지 않았다).
- [ ] 스캐너의 `HOME`/`TMPDIR`(`/tmp/scan` 아래 캐시)은 지우지 않는다. 컨테이너가 아닌 곳에서 worker를 강제 종료하면 `copies/<스캔 ID>`도 남는다.
- [ ] `SaveResult`는 항목마다 DB를 두 번 왕복한다. 항목이 수만 건이면 묶어서 넣는다.
- [ ] 파일을 여는 코드가 세 군데(`scan.readOriginal`, `textfile.readLimited`, `keyfile.readHead`)이고 안전 수준이 다르다. `O_NOFOLLOW`/`O_NONBLOCK`을 쓰는 하나로 모은다.
- [ ] betterleaks를 올릴 때 다시 확인할 것: 무시 파일을 루트에서만 읽는지, `--x-ignore-semgrepignore-files`(opengrep)가 남아 있는지.
- [ ] 프로젝트별 제외 경로는 opengrep에 넘기지 않고 결과를 받은 뒤 걸러낸다. 제외한 큰 폴더도 스캐너는 읽는다.
- [ ] 그 밖의 작은 정리: 결과가 EUC-KR 원본 실행에서만 버려졌을 때 주변 줄을 불필요하게 빼는 것, 미리보기와 건너뛴 파일 경로의 유니코드 형식 문자(경로는 제어 문자만 거른다).
