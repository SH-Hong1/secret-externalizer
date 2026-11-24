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
| 5 | 적용 | 완료 (`docs/superpowers/plans/2026-11-20-05-apply.md`) |
| 6 | 리포트와 PDF | 완료 (`docs/superpowers/plans/2026-11-20-06-report-and-pdf.md`) |
| 7 | 대시보드 | 완료 (`docs/superpowers/plans/2026-11-23-07-dashboard.md`) |

- [ ] **지금 상태(2026-11-23): 계획 1~7을 모두 끝냈다. 남은 것은 아래의 남은 일이다.** 새 계획은 없다. 2026-11-23에 한 파일의 같은 값을 설정 키마다 나누는 변경을 했다(결정 기록 0053). 2026-11-24에 화면에 없는 묶음이 오탐 원본의 정리를 막던 것을 고쳤다(결정 기록 0054). 남은 일 가운데 무엇을 먼저 할지는 사용자가 정한다. 남은 일의 첫 번째로 스캔부터 확인 스캔까지 도는 통합 테스트를 두었다(결정 기록 0049). 스캔 규칙의 알려진 누락과 오탐은 고쳤다(결정 기록 0052). 계획 5, 6, 7에서 사용자 대신 정한 것은 결정 기록 0041, 0044, 0047에 모았다. 이 기계의 Docker VM은 2GB라 이미지를 빌드하는 동안 Keycloak이 메모리 부족으로 죽을 수 있다(`docs/local-run.md`의 "이미지를 다시 빌드할 때").
- [x] 계획 3을 Subagent 방식으로 구현한다면, 새 종료 hook(`stop-review.sh`)이 하위 에이전트의 결과를 기다리는 동안 턴 종료를 막지 않는지 첫 Task에서 확인한다. 막으면 구현을 멈추고 hook부터 고친다. 2026-11-19에 계획 3 Task 1에서 확인했다. 하위 에이전트가 도는 동안 턴이 막히지 않고 끝났다.
- [x] 계획 2 전 정리(PR #2)가 병합되었는지 확인한다. 2026-11-18에 병합되었다.
- [x] `cmd/worker`를 만들면 `AGENTS.md` "Project layout"의 진입점 문장에 추가한다. 계획 2는 worker를 계획 3으로 미루는 안으로 작성했다. 2026-11-19에 계획 3 Task 4에서 `cmd/worker`를 만들고 진입점 문장에 추가했다.
- [x] 계획 2의 "검토 때 정해 주실 것" 7개 항목에 사용자의 답을 받고, 그에 맞게 계획을 고친 뒤 구현한다. Task 1~7을 구현했다.
- [x] 계획 2 Task 7의 확인 표(1~9)를 Chrome에서 확인한다. 2026-11-18에 Chromium으로 확인했다: `dev-user`로 로그인하면 이름과 역할이 있는 홈이 나오고, `/audit`은 `dev-user`에 403, `dev-admin`에 200이며, 로그아웃은 Keycloak 확인 화면을 거쳐 로그인 화면으로 돌아오고, web 로그에 비밀 값이 없었다.
- [x] 계획 2의 PR을 만들고 검토를 받는다. PR #3이 2026-11-18에 병합되었다.
- [x] Task 6/8 전에 `ParsePath`가 NUL 외의 제어 문자(줄바꿈 등)도 거부하게 한다. 제외 경로가 스캐너 설정과 제외 목록에 들어가면 줄바꿈으로 줄을 끼워 넣을 수 있다. 2026-11-19에 `0x20` 미만과 `0x7f`를 모두 `ErrInvalidPath`로 거부하게 했다.
- [x] worker의 uid(65532)를 설정할 수 있게 한다. 바꾸려면 `deploy/openbao/make-secrets.sh`가 만드는 secret-id 파일의 소유자도 함께 바꿔야 한다. Linux 호스트에서 읽을 수 없는 파일을 건너뛰고 세는 동작과, uid 65532가 읽을 수 있어야 한다는 요구 사항은 `docs/local-run.md`에 적었고, 건너뛰는 동작은 Task 6과 10에서 구현한다. 2026-11-20에 계획 5 Task 11에서 compose의 worker를 `SE_UID`, `SE_GID`(기본 65532)로 실행하게 하고, `openbao-secrets`가 뜰 때마다 worker의 secret-id와 그 폴더의 소유자를 `SE_UID`로 맞추게 했다(root와 숫자가 아닌 값은 거부).
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

## 설정 키에서 남은 일

설정 키로 항목을 나누는 변경(`docs/decisions/0053-finding-key.md`)에서 알려진 한계로 남긴 것이다.

- [x] 실제 스택에서 확인한다. 2026-11-24에 새 이미지로 확인했다: 환경 셋의 계정과 비밀번호가 묶음 여섯으로 나뉘고 상세와 옮기기 대상에 키가 보이며, 추천 이름은 `ext.db.local.main.password`, `ext.db.dev.main.password`, `ext.db.prod.main.password`였다. dev 비밀번호를 local 묶음으로 옮긴 뒤 줄을 밀고 다시 스캔해도 판정과 옮긴 상태가 남았고, 적용은 묶음마다 다른 placeholder와 저장 경로를 썼으며 web과 worker 로그에 값이 없었다.
- [ ] 실제로 같은 비밀인데 키가 다른 자리(`jdbc.password`, `jdbc.slave.password`)는 처음에 둘로 나오고 사람이 합친다.
- [ ] 키 이름을 바꾸면 새 항목이고 미판정의 새 묶음이다. 판정을 다시 하거나 옛 묶음으로 옮겨야 한다.
- [ ] 키를 읽을 수 있는지는 스캔마다 달라질 수 있다. 스캐너가 그 파일의 결과를 버렸거나 다른 파일에서 키와 겹치는 값을 새로 찾으면 그 스캔에서는 키 없는 미판정 항목이 보이고 원래 묶음은 판정 화면에서 빠진다. 다음에 키를 읽는 스캔에서 돌아온다.
- [ ] 키를 읽지 못한 자리(Java 문자열 인자, shell의 위치 인자, 고객정보가 있을 수 있는 파일)와 두 줄 이상인 값은 파일마다 항목 하나다.
- [ ] XML은 글자 수준으로 읽는다. 주석 안의 `<bean>`, 여러 줄에 걸친 태그에서 `id`가 다른 줄에 있는 경우는 키가 덜 정확할 수 있다.
- [ ] XML에서 태그의 `name="…"` 속성은 값이 같은 태그의 다른 속성에 있어도 키가 된다(`<bean id="ds" name="alias" p:password="…">`의 키는 `ds.alias`).
- [ ] bean이 아닌 XML에서 Tomcat의 `<Resource name="jdbc/live" … password="…"/>`처럼 `name`에 `/`가 있으면 한 줄에 쓴 태그의 키는 비어 있고(`/`는 키 글자가 아니다), 속성을 줄마다 나눠 쓴 태그의 키는 `password`다. 같은 파일의 두 `Resource`는 나뉘지 않는다. `<beans:bean>`처럼 접두사가 붙은 bean의 `id`는 키에 붙지 않는다.
- [ ] 규칙이 구성 요소로 나누지 않은 Oracle thin URL(`jdbc:oracle:thin:계정/비밀번호@호스트`)에서 직접 규칙이 찾은 계정의 키는 `thin`이고 비밀번호의 키는 비어 있다. 안쪽의 자기 닫힘 bean이 속성 값 안에 `>`를 가지면 그 `id`가 바깥 bean의 뒤쪽 속성 키에 붙는다(`docs/decisions/0053-finding-key.md`의 "알려진 한계").
- [ ] 스캔의 어느 파일에서든 찾은 4바이트 이상의 값이 키의 일부와 같으면(비밀번호 `pass`, 계정 `admin`, `user`, `root` 등) 그 글자를 품은 키는 모두 비어 있다. 그 파일의 환경별 자리는 다시 항목 하나가 되고, 화면에는 이유가 보이지 않는다(`docs/decisions/0053-finding-key.md`의 "알려진 한계").
- [x] 읽어서 의심한 것이고 확인하지 않았다: 키 없는 미판정 묶음이 판정 화면에서 빠진 뒤에도 `SettledFingerprints`가 그 묶음을 세어 임시 원본이 OpenBao에 남는다. 2026-11-24에 테스트로 재현했다. 키와 상관없이 마지막 스캔이 구성원을 찾지 못한 모든 묶음에서 생기는 문제였고, `SettledFingerprints`가 마지막 성공한 스캔이 구성원을 찾은 묶음만 세게 고쳤다(`docs/decisions/0054-settled-counts-only-bundles-on-screen.md`).
- [ ] 판을 올린 뒤 첫 스캔에서 한 파일에 키가 여럿이던 판정된 묶음은 첫 키만 판정을 이어받는다. 나머지는 다시 판정해야 한다.
- [ ] YAML 목록 항목 아래의 같은 값은 키가 목록 위의 경로를 포함하지 않아 나뉘지 않을 수 있다.

## 계획 7에서 남은 일

계획 7을 구현하고 확인하는 동안 나온 것 가운데 아직 고치지 않은 것이다(`docs/decisions/0047-decisions-made-while-building-plan-7.md`).

- [ ] 추이의 판정은 지금의 것이다. 묶음을 오탐으로 판정하면 지난 막대에서도 그 묶음이 빠지고, 오탐을 되돌리면 다시 든다. 그때의 판정으로 본 추이가 필요하면 스캔별 건수를 저장해야 한다(`docs/decisions/0045-what-the-dashboard-counts.md`).
- [ ] `db.Store.Dashboard`는 프로젝트마다 `readTotals`를 두 번(마지막 성공한 스캔과 그 앞) 부르고 스캔을 찾는 쿼리도 프로젝트마다 따로 낸다. 예제(프로젝트 둘)에서는 2~13ms이지만 프로젝트가 수백 개면 느려진다. 한 쿼리로 묶는다.
- [ ] 추이는 새것부터 스캔 30개(`dashboard.MaxPoints`)까지만 싣는다. 그 앞은 "이전 스캔 N개는 싣지 않았습니다"로만 알린다.
- [ ] 기간은 추이에만 걸린다. 타일, 분류별, 규칙, 프로젝트 표는 언제나 지금 상태다. 기간 안의 증감이나 그 기간에 내린 오탐 판정은 볼 수 없다.
- [ ] 차트에 확대, 범례 끄기, 기간 끌어 고르기가 없고 자동으로 새로 고치지 않는다. 대시보드의 PDF 내보내기도 없다.
- [ ] 프로젝트 표의 "마지막 스캔" 시각은 스캔을 요청한 시각이고, 추이의 막대와 표는 끝난 시각이다. 대기가 길었던 스캔(예제의 #8)은 두 시각이 18분 다르다.
- [ ] 스캔의 순서를 정하는 규칙이 둘이다. 마지막 성공한 스캔(`basis`)과 추이의 점은 (끝난 시각, ID)로 고르고(`db.Store.Dashboard`, `dashboard.Build`는 받은 점의 순서를 따른다), 증감의 앞 스캔은 `readProjectStates`가 `max(id)`로 ID가 `basis`보다 작은 스캔 가운데서 고른다. 프로젝트 하나에 스캔이 한 번에 하나만 돌므로 지금은 갈리지 않는다. worker를 늘리기 전에 앞 스캔도 (끝난 시각, ID)로 맞춘다.
- [ ] 큰 눈금에서 건수가 작은 조각은 높이가 0에 가깝게 그려져 보이지 않는다(막대의 `<title>`과 표에는 수가 있다). 조각의 최소 높이를 둘지 정한다(`docs/decisions/0046-chart-drawn-on-the-server.md`).
- [ ] 화면이 780px보다 좁으면 차트가 틀 안에서 가로로 스크롤되고, 처음에는 가장 오래된 막대가 보인다. 가장 새 막대를 보려면 오른쪽으로 밀어야 한다. 스크롤 틀(`chart-scroll`, `table-wrap`)은 키보드로 초점을 줄 수 없다.
- [ ] 스캔한 프로젝트가 하나도 없으면 타일과 함께 분류별, 규칙 절도 보이지 않는다.
- [ ] 기간 필터의 실제 확인은 예제 스캔의 `finished_at`을 잠시 옮겼다가 되돌려서 했다. 스캔부터 대시보드의 저장소 메서드까지는 `TestIntegration_ScanApplyRescan`이 돈다(2026-11-23, `docs/decisions/0049-end-to-end-test-and-empty-checks.md`). 기간 필터와 화면은 그 테스트가 지나지 않는다.

## 계획 6에서 남은 일

계획 6을 구현하고 확인하는 동안 나온 것 가운데 아직 고치지 않은 것이다(`docs/decisions/0044-decisions-made-while-building-plan-6.md`).

- [ ] 리포트는 저장하지 않고 열 때마다 만들므로, 지난 스캔의 리포트도 판정은 지금의 것이다. 그때의 판정으로 보려면 `audit_event`를 되감아야 하는데 묶음 옮기기 때문에 정확히 되돌릴 수 없다. 시점을 고정한 사본은 PDF뿐이다(`docs/decisions/0042-what-a-scan-report-shows.md`).
- [ ] 리포트는 절마다 100줄(`report.MaxRows`)까지만 싣는다. 2GB Docker VM에서 PDF 변환기(메모리 한도 1GB)가 모든 한도까지 채운 리포트를 인쇄할 수 있는 줄 수가 100이었다(150은 세 번에 한 번, 200과 300은 실패). 메모리가 더 큰 호스트에서는 `report.MaxRows`를 올리고 `TestReportPDF_PrintsAtEveryCap`으로 확인할 수 있다. 화면과 PDF가 같은 한도를 쓴다.
- [ ] PDF 바닥글에 프로젝트 이름이 없다(바닥글은 ASCII만 쓴다). 또 `text-align: right`가 먹지 않아 바닥글이 왼쪽에 붙는다. 고치려면 web 이미지를 다시 빌드해 실제 변환기로 확인해야 한다.
- [x] PDF 변환의 동시 요청 한도가 없다. 여러 사람이 큰 리포트를 함께 내보내면 변환기가 메모리 한도 안에서 실패해(400) 500 화면이 나온다. web에 동시 변환 수의 한도(세마포어)를 둔다. 2026-11-23에 web이 변환을 한 번에 하나만 보내게 했다. 겹친 요청은 변환 제한 20초 안에서 기다리고, 자리가 나지 않으면 503 "다른 PDF를 만드는 중입니다" 화면을 본다(`docs/decisions/0050-one-conversion-at-a-time-and-no-links-in-the-pdf.md`).
- [ ] PDF 한 건의 시간 예산(세션 읽기, DB 5초, 변환 20초)이 web의 `WriteTimeout` 30초에 가깝다. 둘 다 제한까지 걸리면 응답을 쓰기 전에 연결이 끊긴다(0044). 변환 자리를 기다리는 시간은 변환 20초 안에 들어 예산은 그대로다(0050).
- [ ] 네트워크는 `pdf`만 나뉘었다. 나머지는 "계획 3에서 남은 일"의 compose 네트워크 항목에 있다.
- [x] 계획 7이 추이를 그리려면 스캔별 건수의 스냅숏이 필요한지 정한다. 지금의 요약은 판정을 리포트를 만든 시각의 것으로 세므로, 지난 스캔의 숫자가 나중의 판정에 따라 바뀐다. 2026-11-23에 계획 7에서 스냅숏을 만들지 않기로 정했다. 추이의 한 점은 그 스캔이 찾은 묶음 가운데 지금 오탐이 아닌 것의 수다(`docs/decisions/0045-what-the-dashboard-counts.md`).
- [ ] 고객정보 한 줄이 여러 묶음을 묶을 때 그 줄의 상태는 묶음 가운데 하나라도 적용된 것을 따르고, 적용 표시("이후 적용됨", "적용됨 · 다시 발견")가 보류 사유보다 앞선다. 한 줄의 묶음 일부만 적용되면 나머지의 상태가 가려진다.
- [ ] `db.Store.Report`가 한 스냅숏(repeatable read)에서 읽는다는 성질을 붙잡는 테스트가 없다. 요약의 숫자와 목록이 같은 시점의 것이라는 것은 지금 코드의 `readOnly` 한 번으로만 지켜진다.
- [ ] 작업을 돌려받아 다시 대기 중인 적용 실행(시작 시각도 끝난 시각도 없음)의 `applied` 항목은 어느 스캔의 리포트에도 실리지 않고 줄의 적용 표시에도 들지 않는다. 그 실행이 끝나면 시각이 생겨 실린다.
- [x] 내보낸 PDF에는 살아 있는 링크 주석이 있고 대상이 `file:///…`이다. 2026-11-23에 인쇄용 문서에서 세 종류의 링크(이후 스캔의 리포트, 스캔 화면의 "검사하지 못한 것", 이력의 "적용 #N")를 뺐다. 문장과 표시는 남고 링크 글은 빠진다. 실제 변환기가 만든 PDF에 링크 주석이 없는지 `TestReportPDF_NoLinkAnnotations`가 본다(`docs/decisions/0050-one-conversion-at-a-time-and-no-links-in-the-pdf.md`).
- [ ] `db.Report`는 스캔의 모든 줄과 모든 변경을 읽고 `report.Build`가 나중에 절마다 100개로 자른다(SQL에서 제한하는 것은 이력뿐이다). 묶음이 수만 개인 프로젝트에서는 리포트가 보이는 것보다 훨씬 많이 읽는다. 두 쿼리를 SQL에서 제한하고 전체 건수는 따로 센다.
- [ ] 요약의 증감은 묶음 수만 보이고 위치 수의 증감은 보이지 않는다.
- [ ] PDF의 판정 이력 표에서 좁은 칸의 이름과 사람이 글자 중간에서 줄바꿈된다(`ext.db.main8.pass` / `word`, `dev-` / `admin`). 읽을 수는 있다.
- [ ] `report.Build`의 외부화 항목은 묶음의 분류, 종류, 저장 경로를 그 이름의 첫 변경에서 가져온다. 외부화 항목은 이름의 바이트 순서로 정렬되어 `${EXT_…}`가 `${ext.…}`보다 앞선다. `TestBuild_Caps`는 자르고 난 뒤의 자리 순서를 확인하지 않는다.
- [ ] `db.Store.Report`의 작은 정리: 건수를 셀 때도 `run_mark`를 계산하고, 판정 이력 쿼리가 전체 줄 수를 줄마다 되풀이한다. `report.Data`와 `reportFound`의 주석 줄바꿈이 고르지 않다.
- [ ] pdf 컨테이너에 `memswap_limit`이 없다. VM에 스왑이 있으면 메모리 한도를 넘어 스왑을 쓸 수 있다.
- [ ] web이 새 마이그레이션을 적용한 뒤 이전 worker 이미지가 다시 시작하면 `schema_migration records versions [N] …`로 뜨지 않고 `restart: unless-stopped`로 계속 다시 시작한다. 계획 6 Task 6에서 실제로 일어났다(worker를 다시 빌드해 풀었다). 화면에는 스캔이 "대기"로만 보인다. 배포 순서를 문서로만 막고 있으므로(`docs/local-run.md`의 "이미지를 다시 빌드할 때"), web이 worker의 상태를 보이거나 worker가 같은 오류로 멈춘 것을 알리는 방법을 정한다.
- [ ] 2GB Docker VM에서 web 이미지 빌드가 다른 서비스와 겹치면 VM이 메모리 부족으로 멈추고 Keycloak이 죽었다(90분 넘게 끝나지 않음). worker 하나만 빌드해도 약 16분 걸렸다. VM 메모리를 늘리거나, Go 빌드를 호스트에서 하고 이미지는 결과만 복사하는 방법을 정한다. 2026-11-23에는 `make test-integration` 전체를 로컬 스택(web, worker, Keycloak)이 떠 있는 채로 돌리자 `openbao-test`가 메모리 부족으로 죽었고(종료 코드 137), `TestReportPDF_PrintsAtEveryCap`이 변환기의 503/400으로 실패했다. `openbao-test`를 다시 띄워 `internal/apply`를, 그것을 멈추고 `internal/web`의 PDF 테스트를 따로 돌리면 통과한다.
- [ ] "확인 스캔의 리포트 요약이 판정 화면 탭과 같다"는 확인 스캔이 마지막 스캔일 때만 확인할 수 있다. Task 6에서는 마지막 스캔(#7, #8)으로 확인했다.

## 계획 5에서 남은 일

계획 5를 구현하는 동안 나온 것 가운데 아직 고치지 않은 것이다.

- [ ] 볼륨을 지우지 않고 OpenBao 정책을 바꾸는 방법을 둔다. self-init은 빈 저장소에서 한 번만 실행되므로 지금은 `config.hcl`의 정책을 고치면 `openbao-data`, `openbao-secrets`를 지워야 하는데, 적용을 한 번이라도 한 환경에서는 그 볼륨에 추적되지 않는 파일에서 빼낸 값의 유일한 사본이 있다(`docs/local-run.md`의 "정책을 바꾼 뒤").
- [ ] 스캔을 적용과 다른 컨테이너나 다른 사용자로 나눈다. 지금은 betterleaks와 opengrep이 worker 컨테이너에서 worker와 같은 사용자로 돌고, 그 컨테이너에 `/workspace-rw`가 읽기-쓰기로 마운트되어 있어, 잘못 만들어졌거나 바뀐 스캐너가 절대 경로로 소스를 바꿀 수 있다. 지금 이를 줄이는 것은 `deploy/worker/scanners.lock`의 체크섬과 "스캔이 작업 공간에 아무것도 쓰지 않는다"는 확인뿐이다(`docs/decisions/0032-apply-order-and-resuming.md`).
- [ ] OpenBao에 저장되는 외부화 값은 소스에 적힌 글자 그대로다. XML의 `&amp;`, properties와 Java의 `\` 이스케이프, YAML의 따옴표 안 이스케이프를 풀지 않는다. 값을 쓰는 쪽(배포 스크립트, 설정 주입)이 파일 종류를 보고 풀어야 한다. 풀어 저장할지 정한다(`docs/decisions/0035-externalized-values-in-openbao.md`).
- [ ] git은 `objects/info/alternates`가 작업 공간 밖을 가리켜도 그 객체를 읽는다. 읽기만 하고 도구가 받는 것은 경로와 표시뿐이지만, git 폴더가 작업 공간 안에 있어야 한다는 규칙의 예외다(`docs/decisions/0033-git-work-tree-inspection.md`).
- [ ] macOS의 git은 파일 이름을 NFC로 보고하므로 디스크에 NFD로 저장된 이름이 다른 바이트로 돌아올 수 있다. 결과는 "커밋되지 않은 변경"이나 "추적되지 않음" 쪽으로 기울고, Linux의 worker에는 해당하지 않는다(`docs/decisions/0033-git-work-tree-inspection.md`).
- [x] `apply_run.project_id`에는 걸린 상태(`checking`, `queued`, `running`)의 부분 유일 인덱스 말고 인덱스가 없다. "프로젝트의 마지막 실행"을 읽는 쿼리가 실행이 쌓이면 느려지므로 새 마이그레이션으로 인덱스를 더한다. 2026-11-20에 계획 6 Task 2에서 마이그레이션 `0008`로 `apply_run (project_id, id DESC)`와 `audit_event (project_id, id DESC)` 인덱스를 더했다.
- [ ] git이 추적하지 않고 사람이 손으로 만든 `secrets-manifest.yml`은 적용이 다시 쓴다(항목은 남고 모양은 쓰는 모양으로 바뀐다). git에 사본이 없으므로 원래 모양으로 되돌릴 수 없다(`docs/decisions/0034-manifest-format.md`).
- [ ] 죽은 뒤 다음 맞추기 전에 사람이 파일을 고치면, 그 파일의 항목은 `interrupted`로 닫히는데 디스크에는 placeholder가 남는다. 그 파일을 커밋하기 전까지 다음 적용은 "커밋되지 않은 변경"으로 막힌다(`docs/decisions/0032-apply-order-and-resuming.md`).
- [ ] 임시 파일을 지우지 못한 뒤 manifest가 하드 링크된 상태로 남으면, 이후의 manifest 쓰기가 모두 `ErrHardLinked`로 실패한다. 사람이 링크를 풀어야 한다(`docs/decisions/0037-file-rewrite-on-os-root.md`).
- [ ] `BeginFile`이나 `EndFile`에서 데이터베이스가 잠깐 오류를 내도 실행은 그대로 실패로 끝난다. 다시 시도하지 않으므로 사람이 적용을 다시 요청해야 한다(`docs/decisions/0032-apply-order-and-resuming.md`).
- [ ] 확장자가 `.sh`가 아닌 셸 스크립트(`bin/start`, `.bash`, `.ksh`)는 `${ext.…}` 표기로 바뀌어 셸에서 쓸 수 없다. 작은따옴표 안의 셸 문자열에 들어간 placeholder는 펼쳐지지 않는데 이를 알리지 않는다(`docs/decisions/0039-apply-plan-rules.md`).
- [ ] 16MiB 값 하나를 다룰 때 worker의 최대 메모리는 대략 100~150MB이고, compose의 worker 컨테이너에는 메모리 한도가 없다. `prepared`는 모든 대상 파일의 바이트를 한꺼번에 들고 있다(`docs/decisions/0035-externalized-values-in-openbao.md`).
- [ ] OpenBao HTTP 클라이언트의 전체 제한 시간이 10초라서, 느린 호스트에서는 큰 키 파일이 `store-failed`가 된다. 거부일 뿐 값이 사라지지는 않는다(`docs/decisions/0035-externalized-values-in-openbao.md`).
- [x] 실제 스캐너의 결과를 적용에 넣어 보는 자동 테스트가 없다. 2026-11-20에 계획 5 Task 15에서 실제 스택으로 손으로 확인했고(확인 표 21개), 그때 placeholder를 다시 찾는 결함과 OpenBao 초기화 순서 결함이 나왔다. 스캔부터 적용, 확인 스캔까지 도는 통합 테스트를 둔다. 2026-11-23에 `TestIntegration_ScanApplyRescan`(`internal/apply`)을 두었다. 실제 스캐너, postgres, OpenBao, git으로 스캔 → 추천 판정 → 점검 → 적용 → 확인 스캔 → 대시보드를 돈다(`docs/decisions/0049-end-to-end-test-and-empty-checks.md`).
- [ ] manifest가 계획의 대상 파일이고 그 값이 따옴표 없는 문자열이면, 치환한 placeholder를 manifest 읽기가 받지 않아 manifest 쓰기가 실패한다. 사람이 manifest를 고쳐야 다음 적용이 이어 쓴다(`docs/decisions/0032-apply-order-and-resuming.md`).
- [ ] 외부화한 값과 같은 값이 규칙이 보고하지 않은 자리에 있으면 그대로 남는다. 예제에서는 `build.xml`의 `scp` 대상 호스트, Java의 `setUsername("…")`과 JDBC URL 안의 호스트가 남았다. 그 줄은 다른 항목의 주변 줄로 DB(`location`)에도 저장된다. 비밀번호, 토큰, 키, 고객정보는 남지 않았다.
- [ ] 글자 그대로 `${...}` 모양인 실제 비밀 값은 스캔이 보고하지 않는다. 버린 placeholder 값은 같은 파일의 다른 항목 주변 줄에서 여전히 `****`로 가려진다(`docs/decisions/0026-connection-string-parts.md`).
- [ ] 외부화한 값을 도구의 화면에서 다시 볼 수 없다. 화면은 저장 경로만 보인다. 값은 worker의 AppRole로 OpenBao에서 읽는다(`docs/local-run.md`).
- [ ] KV v2는 경로마다 버전을 100개까지만 남긴다. 같은 이름에 값을 101번 바꿔 쓰면 가장 오래된 버전이 사라진다.
- [ ] manifest의 줄 번호는 적용한 때의 것이다. 이후에 파일을 고치면 어긋나고, 다음 적용이 그 항목을 다시 쓸 때만 고쳐진다.
- [ ] 바꿔 넣기(`rename`) 직전의 해시 확인과 `rename` 사이에 다른 프로세스가 파일을 고치면 그 변경이 덮어써진다. 틈은 짧지만 0이 아니다(`docs/decisions/0037-file-rewrite-on-os-root.md`).
- [ ] 적용 화면은 점검 전에 DB만 보고 "적용할 판정이 있음: 통과"라고 하는데, 남은 외부화 묶음이 worker가 언제나 건너뛰는 것(소스 파일 안의 키 파일 경로 등)뿐이면 점검 뒤에는 같은 조건이 실패한다. 2026-11-23에 점검 쪽은 고쳤다: 파일을 읽은 뒤 건너뛰기로 바뀐 것뿐일 때도 점검이 `nothing-to-apply`로 막고(전에는 `ready`였다), 문장은 "적용할 수 있는 항목이 없습니다."다(`docs/decisions/0049-end-to-end-test-and-empty-checks.md`). 점검 전 화면의 "통과"는 그대로다.
- [ ] 조건을 통과하지 못한 점검의 "점검하지 못함" 위치 수는 어느 조건까지 확인했는지에 따라 조금씩 다르게 나온다(예제에서 81~83).
- [x] 적용 중에도 판정 화면은 판정 버튼을 그대로 보이고, 누르면 409와 안내가 나온다. 버튼을 누를 수 없게 한다. 2026-11-23에 점검이나 적용이 진행 중이면 판정 화면의 상세를 잠그게 했다(버튼, 보류 사유, 메모, 이름을 누를 수 없고 옮기기 양식이 없다). 화면을 연 뒤에 적용이 시작되면 전처럼 409다(`docs/decisions/0051-apply-screen-guards.md`).
- [x] `ConfirmApply`는 점검이 복구 불가 파일을 보여 주지 않았어도 동의 값을 받는다. 지금은 handler만 막는다. 2026-11-23에 저장소가 그런 실행에서는 동의를 세지 않게 했다(`allow_irreversible`은 false, 감사 기록은 "no"). 요청을 거부하지는 않는다(0051).
- [x] 점검 뒤에 요청된 스캔이 실패했거나 아직 대기 중이면 적용 화면은 점검이 낡았다는 안내 없이 "적용" 단추를 보이고, 누르면 409와 "판정이나 스캔이 바뀌었습니다"가 나온다. 화면이 먼저 알리고, 문장도 "새 스캔이 있습니다"로 나눈다. 2026-11-23에 확인했다: 화면은 이미 그 두 경우에 적용 단추를 막고 실패한 조건을 보였다(테스트로 붙잡았다). 열어 둔 화면에서 적용을 눌렀을 때의 문장만 "점검한 뒤에 새 스캔이 있습니다"(`ErrScanFollowed`)와 "판정이 바뀌었습니다"로 나누고, 점검 전 화면의 낡은 점검 안내도 둘로 나눴다(0051).
- [ ] 자리 표시자인 구성 요소가 있고 값인 구성 요소가 없는 연결 문자열은 URL 항목 하나로도 남지 않고 통째로 빠진다. `Split`이 모르는 매개변수에 든 비밀 값(모든 구성 요소가 자리 표시자인 호스트 뒤의 `?apiToken=…` 등)은 보고되지 않는다(`docs/decisions/0026-connection-string-parts.md`).
- [ ] 오탐으로 판정한 묶음의 상세에는 원본 보기도 "보관된 원본이 없습니다" 안내도 보이지 않는다. 안내는 미판정 묶음에만 보인다. 계획 5의 확인 표 16은 "보관된 원본이 없습니다"를 기대했다. 둘 중 하나로 맞춘다.
- [ ] 대기 중에 막는 조건으로 실패한 적용 실행의 결과 화면은 실패 사유만 보이고, 어느 조건이 어느 파일에서 막혔는지(`Blockers`)는 보이지 않는다. 다시 점검하면 적용 화면이 보여 준다.
- [ ] 바꾸던 중에 멈춘 파일이 있는 실행의 확인 스캔이 값을 다시 찾지 못하면, 적용 결과 화면은 그 파일이 확인에 들지 않을 수 있다고 보이지만 스캔 화면은 "외부화한 값이 모두 사라졌습니다"를 보인다. 스캔 화면은 실행의 항목을 읽지 않는다(`docs/decisions/0040-confirmation-scan-and-settled-originals.md`).

## 계획 4에서 남은 일

계획 4가 하지 않고 넘긴 것과 검토에서 나온 것 가운데 아직 고치지 않은 것이다. 해당하는 계획을 쓸 때 함께 정한다.

- [x] 계획 5: 오탐으로 판정한 값의 임시 원본을 OpenBao에서 지운다. 2026-11-20에 계획 5 Task 12에서 했다. 적용이 끝난 뒤의 확인 스캔이 오탐으로 확정된 값의 원본을 다시 보관하지 않는다(`docs/decisions/0040-confirmation-scan-and-settled-originals.md`).
- [x] 계획 5: 한 파일 안에 같은 값이 두 번 나오면 따로 이름을 붙일 수 없다. 2026-11-23에 항목을 "지문, 파일, 설정 키"로 식별하게 했다. 키가 다르면 항목과 묶음이 나뉘고 키가 같거나 읽지 못한 자리는 그대로 항목 하나다(`docs/decisions/0053-finding-key.md`).
- [ ] 계획 5: 호스트, 포트, 계정은 항목마다 묶음 하나로 시작하고 사용자가 손으로 합친다. 스캔이 설정 키를 저장하게 되었지만(0053) 같은 키끼리 자동으로 합치는 일은 하지 않았다.
- [ ] 4바이트보다 짧은 접속 정보 구성 요소(포트 `22`, 계정 `sa`)는 그 파일의 자기 자리에서만 가린다. 규칙이 보고하지 않은 다른 자리의 같은 URL에서는 보일 수 있다(`docs/decisions/0026-connection-string-parts.md`).
- [ ] JDBC 매개변수는 이름만 보고 비밀번호로 정하므로 `passwordCharacterEncoding` 같은 설정 값도 비밀번호 항목이 된다(`docs/decisions/0026-connection-string-parts.md`).
- [ ] htmx를 2.0.11로 고정했다. 4.x가 나와 있으므로 올릴 때 `hx-` 속성과 설정(`htmx-config`)이 그대로 동작하는지 확인한다.
- [ ] 판정 화면의 상태 탭은 묶음 수를 센다. 고객정보는 파일과 종류마다 한 줄로 묶어 보이므로, 한 줄이 "삭제·마스킹" 탭에서 2 이상으로 셀 수 있다.
- [x] 프로젝트 화면과 스캔 화면은 건수만 필요한데 판정 목록 한 쪽(`Entries`)을 읽는다. 건수만 세는 저장소 메서드를 둔다. 2026-11-23에 계획 7 Task 3에서 `db.Store.VerdictCounts`를 만들고 프로젝트 화면, 스캔 화면, 적용된 묶음 탭이 그것을 쓰게 했다.
- [x] 계획 5(2026-11-20에 계획 5 Task 8에서 했다. 계획을 세울 때 묶음마다 다시 확인하고, 프론트엔드 구성원이 있는 묶음은 통째로 건너뛴다. `docs/decisions/0039-apply-plan-rules.md`): 적용은 묶음마다 `Check`를 다시 하고, 다시 나타난 구성원 때문에 허용되지 않게 된 판정은 적용하지 않는다. 판정은 마지막 스캔이 찾은 구성원만으로 확인하므로, 사라진 JSP 구성원이 있는 묶음을 외부화로 판정한 뒤 그 값이 다시 나타나면 외부화 묶음에 프론트엔드 구성원이 생긴다(`docs/decisions/0027-bundle-is-the-unit-of-a-verdict.md`의 "알려진 한계").
- [ ] 판정 화면 목록 줄은 shell 파일에서만 찾은 묶음에도 이름을 `ext.<이름>`으로 보인다. 상세는 `EXT_…`로 보인다. 목록도 묶음의 파일 종류에 맞춘다.
- [ ] htmx로 보낸 원본 보기 요청이 실패하면(세션 만료, 403, 500) 화면에 아무 변화가 없다. 오류를 보이거나 일반 POST로 넘어가게 한다.
- [x] `NewServer`가 위치 매개변수 열한 개를 받고 그중 넷이 같은 저장소다. 계획 5가 더 넣기 전에 묶는다. 2026-11-20에 계획 5 Task 13에서 저장소를 `web.Stores` 하나로 묶었다.

## 계획 3에서 남은 일

계획 3의 검토에서 나온 것 가운데 아직 고치지 않은 것이다. 해당하는 계획을 쓸 때 함께 정한다.

- [x] 규칙의 알려진 누락: `.properties`의 `jdbc.pass=`와 공백으로 구분한 형태, YAML 블록 스칼라(`password: >`), `sshpass -e`와 `SSHPASS=`, `setRequestProperty("Cookie", …)`, `settings.xml`의 `<username>`, `<constructor-arg name="password">`, `dbPassword` 같은 속성 이름, 외국인등록번호(7번째 자리 5~8), 하이픈 없는 전화번호. 2026-11-23에 아홉 가지를 모두 규칙에 넣고 사례 예제 `internal/ruleset/testdata/rule-cases`로 붙잡았다(`docs/decisions/0052-rule-gaps-closed.md`).
- [x] 규칙의 알려진 오탐: `TOKEN_HEADER = "X-Auth-Token"` 같은 상수, `MSG_PWD` 같은 메시지 상수, SpEL `#{…}` 값, DB가 아닌 pool bean의 `url`/`user`, 원격 명령의 `sed -i`. 2026-11-23에 다섯 가지를 모두 고쳤다(0052).
- [ ] 규칙을 고친 뒤에 남은 누락(0052의 "알려진 한계"): 두 줄 이상인 YAML 블록 스칼라 비밀번호, 목록 항목(`- password: >`)의 블록 스칼라, `p:dbPassword` 같은 `p:` 속성(`p:password`만 찾는다), `settings.xml`의 `<proxy>` 안 계정과 비밀번호, 하이픈 없는 지역 전화번호, 이름에 `pool`만 있고 class가 알려진 데이터 소스가 아닌 bean의 `url`/`user`, 변수 이름이 `…Header`, `…Name`, `…Url`처럼 끝나는데 실제 비밀 값인 Java 문자열, 글자 그대로 `#{…}` 모양인 비밀 값.
- [ ] 규칙을 고친 뒤에 남은 오탐(0052): 메시지 묶음(`messages*.properties`)의 `label.password=비밀번호`, `changePassword=…` 같은 줄(키 끝이 넓어져 전보다 늘 수 있다), 따옴표와 `user@` 없이 쓴 원격 명령의 `-i`(`ssh host sed -i …`), 로그에서 다른 뜻의 10~11자리 숫자가 `01`로 시작하는 경우.
- [ ] worker가 둘 이상일 때: 오래된 임시 원본 정리가 다음 스캔의 `PutPending`과 겹치면 `has_original = true`인데 원본이 없는 항목이 생길 수 있다. worker를 늘리기 전에 삭제 직전에 최신 스캔을 다시 확인하게 한다. 적용도 worker 하나를 전제로 한다. 임대를 잃은 뒤 한 실행을 두 worker가 함께 잡으면 기록이 틀릴 수 있다(파일은 바뀌었는데 항목이 `interrupted`). 값은 사라지지 않는다(`docs/decisions/0038-apply-file-records.md`).
- [x] compose의 worker에 `restart:` 정책이 없다. 하나뿐인 worker가 죽으면 스캔이 "실행 중"으로 남고 그 프로젝트의 새 스캔을 막는다. 2026-11-20에 계획 5 Task 11에서 `restart: unless-stopped`를 두었다. 다시 뜬 worker가 임대가 끝난 작업을 이어 간다.
- [ ] compose의 모든 서비스가 한 네트워크를 쓴다. web이 `openbao:8200`에 닿을 수 있고(자격 증명은 없다) worker와 스캔 도구가 바깥으로 나갈 수 있다. 네트워크를 나눈다. 2026-11-21에 계획 6 Task 4에서 `pdf`만 내부 네트워크 `pdf`(web과 pdf만 붙음)로 나눴다. 나머지 서비스는 여전히 기본 네트워크 하나다.
- [x] 큰 값: 지문 요청은 건수(500)로만 나누고 크기로 나누지 않는다. 20MiB에 가까운 키 파일은 OpenBao의 요청 한도를 넘을 수 있고, 그때 실패 사유가 "secret store could not be reached"로 나온다. 빈 키 파일(빈 HMAC 입력)도 확인하지 않았다. 2026-11-20에 계획 5 Task 4에서 지문 요청을 값의 바이트 합 8MiB로도 나누게 했다. 요청 한도는 32MiB이고 20MiB 값 하나(본문 약 26.7MiB)는 혼자 한 요청으로 통과하며, 빈 입력도 HMAC을 돌려주는 것을 `openbao-test`에서 확인했다(`docs/decisions/0035-externalized-values-in-openbao.md`).
- [ ] PEM 키가 CRLF 파일에 있으면 betterleaks가 본문을 디코딩한 결과를 하나 더 보고해 그 파일의 주변 줄이 빠지고 버린 건수가 늘 수 있다(실제 DER 본문에서도 그런지는 확인하지 않았다).
- [ ] 스캐너의 `HOME`/`TMPDIR`(`/tmp/scan` 아래 캐시)은 지우지 않는다. 컨테이너가 아닌 곳에서 worker를 강제 종료하면 `copies/<스캔 ID>`도 남는다.
- [ ] `SaveResult`는 항목마다 DB를 두 번 왕복한다. 항목이 수만 건이면 묶어서 넣는다.
- [ ] 파일을 여는 코드가 세 군데(`scan.readOriginal`, `textfile.readLimited`, `keyfile.readHead`)이고 안전 수준이 다르다. `O_NOFOLLOW`/`O_NONBLOCK`을 쓰는 하나로 모은다.
- [ ] betterleaks를 올릴 때 다시 확인할 것: 무시 파일을 루트에서만 읽는지, `--x-ignore-semgrepignore-files`(opengrep)가 남아 있는지.
- [ ] 프로젝트별 제외 경로는 opengrep에 넘기지 않고 결과를 받은 뒤 걸러낸다. 제외한 큰 폴더도 스캐너는 읽는다.
- [ ] 그 밖의 작은 정리: 결과가 EUC-KR 원본 실행에서만 버려졌을 때 주변 줄을 불필요하게 빼는 것, 미리보기와 건너뛴 파일 경로의 유니코드 형식 문자(경로는 제어 문자만 거른다).
