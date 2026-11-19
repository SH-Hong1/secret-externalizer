# 프로젝트 등록과 스캔 Implementation Plan (계획 3/7)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 로그인한 사용자가 작업 루트 아래의 폴더를 프로젝트로 등록하고 스캔을 시작하면, worker가 betterleaks와 opengrep으로 탐지한 항목이 원본 값 없이 postgres에 저장되고 화면에 목록으로 보인다.

**Architecture:** web은 프로젝트와 스캔 요청을 postgres에 쓰고, `cmd/worker`가 postgres의 작업 큐에서 스캔을 가져와 실행한다. worker는 파일을 직접 읽어 인코딩을 판별하고, 스캐너를 `exec.CommandContext`로 실행해 표준 출력의 JSON을 메모리에서만 읽는다. 값의 지문은 OpenBao transit의 HMAC으로 계산하고, 비밀 값의 원본은 OpenBao KV의 임시 경로에 두며, postgres에는 지문, 가린 미리보기, 위치, 가린 주변 줄만 저장한다. web은 소스와 OpenBao에 접근하지 않는다.

**Tech Stack:** Go 1.27, `github.com/jackc/pgx/v5`, `golang.org/x/text`, postgres 18, OpenBao 2.7(transit, KV v2, AppRole), betterleaks 1.10, opengrep 2.0, docker compose

**Spec:** `docs/superpowers/specs/2026-11-18-secret-externalizer-design.md` (3절 "대상", 5절 "구성", 6절의 "프로젝트 등록"부터 "스캔 간 항목 식별"까지와 "OpenBao 사용 범위", 10절 3단계). 결정 기록 0001, 0002, 0003.

## 검토 때 정해 주실 것

설계 문서가 정하지 않았거나 만드는 사람에게 맡긴 내용이다. 각 항목의 첫 번째가 이 계획이 따르는 안이다.

1. **계획은 하나로 두고 PR을 둘로 나눈다.** Task 1~4(postgres, 프로젝트, 작업 큐, worker)가 끝나면 "스캔을 요청하면 worker가 파일 수만 세고 끝내는" 상태로 한 번 PR을 올린다. Task 5~11(OpenBao, 스캐너, 규칙, 탐지 항목)이 두 번째 PR이다. 다른 안: 계획 문서를 3a, 3b로 나눈다.
2. **postgres 드라이버는 pgx v5, 마이그레이션 실행기와 작업 큐는 직접 만든다.** 마이그레이션은 `embed`한 SQL 파일을 번호순으로 실행하는 작은 코드이고, 작업 큐는 `FOR UPDATE SKIP LOCKED`와 임대 시간(lease)으로 만든다. 다른 안: tern(sprig, cobra가 딸려 온다), River(모듈 8개와 자체 스키마가 추가되고 아직 1.0 전이다).
3. **web과 worker는 postgres 계정 하나(`se_app`)를 같이 쓰고, 둘 다 시작할 때 마이그레이션을 실행한다.** advisory lock으로 한쪽만 실행된다. 다른 안: 계정을 둘로 나누고 worker만 마이그레이션한다.
4. **web은 등록할 때 경로의 모양만 검사하고, 실제 폴더 확인은 worker가 스캔할 때 한다.** web에는 소스가 마운트되지 않기 때문이다. 없는 폴더나 작업 루트 밖을 가리키는 심볼릭 링크는 스캔이 실패로 끝나면서 사유가 화면에 보인다. 다른 안: 등록할 때 worker에 확인 작업을 보내고 결과를 기다린다.
5. **이 계획에서 소스는 worker에 읽기 전용으로 마운트한다.** 쓰기는 계획 5(적용)에서 연다.
6. **OpenBao는 static seal과 self-init으로 자동화한다.** 봉인 해제 키와 AppRole의 secret-id는 처음 띄울 때 `openbao-secrets` 컨테이너가 난수로 만들어 볼륨에 둔다. 저장소에는 넣지 않는다. 저장소(storage)는 pebbledb다(2.7에서 `file`이 없어졌다). self-init은 처음 한 번만 실행되므로, 계획 4와 5에서 쓸 web 읽기 권한과 외부화 값 경로(`projects/`)의 정책도 설계 문서의 "OpenBao 사용 범위" 표대로 지금 만든다. 다른 안: 다른 개발용 값처럼 `dev-only-...` 고정 값을 compose에 적는다. 이 도구는 개발자 PC에서 실제 원본 값을 보관하므로 따르지 않았다.
7. **OpenBao 클라이언트는 `net/http`로 직접 만든다.** 쓰는 API가 로그인, HMAC, KV 쓰기·목록·삭제뿐이다. 다른 안: `github.com/openbao/openbao/api/v2`(직접 의존 13개).
8. **지문은 OpenBao transit 키 하나(`fingerprint`)의 버전 1로 고정해 계산한다.** 키를 회전하면 HMAC이 달라져 스캔 간 식별이 끊기므로 회전하지 않는다. 키는 모든 프로젝트가 같이 쓴다. 다른 프로젝트의 과거 판정을 제안에 쓰려면(설계 6절 "판정") 같은 값의 지문이 같아야 한다.
9. **고객정보도 지문을 저장한다.** 오탐으로 판정한 고객정보가 다음 스캔에서 다시 올라오지 않으려면 스캔 간 식별이 필요하다. 지문은 OpenBao의 키 없이는 계산할 수 없는 HMAC이고, 원본과 주변 줄은 어디에도 저장하지 않는다. 다른 안: 고객정보는 "파일, 종류, 건수"로만 식별한다. 줄이 바뀌면 판정이 유지되지 않는다.
10. **EUC-KR 파일은 worker가 UTF-8로 바꾼 사본을 tmpfs에 만들어 스캔한다.** 스캐너 둘 다 인코딩을 바꿔 읽지 않아, 그대로 두면 EUC-KR 파일의 한글(주소 키워드)을 찾지 못한다. 사본은 메모리에만 있고 스캔이 끝나면 지운다. 다른 안: 원본 그대로만 스캔하고, EUC-KR 파일에서는 ASCII로 된 값만 찾는다.
11. **고객정보 규칙은 로그와 샘플 데이터에만 적용한다.** 확장자 `log, csv, tsv, txt, sql, dat, json`과 폴더 `log(s), sample(s), data, dump` 아래다. `.java`나 `pom.xml`의 이메일 주소까지 잡으면 오탐이 대부분이 된다. 프로젝트별로 고객정보 경로를 더하는 기능은 넣지 않는다.
12. **개인키와 인증서 파일은 worker 안의 Go 검출기가 찾는다.** betterleaks는 바이너리 파일(`.jks`, `.p12`)을 건너뛴다. PEM 머리말과 확장자로 찾고, 파일 하나가 항목 하나다.
13. **접속 정보 묶음(JDBC URL, `user@host`)은 이 계획에서 값 하나로 저장한다.** 구성 요소별로 나누는 일은 placeholder를 정하는 계획 4, 5에서 한다.
14. **화면은 계획 2처럼 스타일 없는 최소 HTML이다.** 스캔이 진행 중인 화면은 3초마다 새로 고친다(`<meta http-equiv="refresh">`). htmx는 계획 4에서 넣는다.
15. **스캐너 버전과 체크섬은 `deploy/worker/scanners.lock`에 고정한다.** worker 이미지와 `make scanners`가 같은 파일을 읽는다. `AGENTS.md`의 "Tool versions are pinned in `Makefile`" 문장에 이 파일을 더한다. 다른 안: Makefile에 두고 compose에 build arg로 넘긴다.
16. **스캔이 실패하면 자동으로 다시 하지 않는다.** 사유를 보고 사용자가 다시 시작한다. worker가 스캔 중에 죽은 경우만 임대 시간이 지난 뒤 다시 실행하고, 세 번째에도 끝나지 않으면 실패로 표시한다.

## 구현 전에 확인할 사실

조사에서 공식 문서나 소스로 확인하지 못했거나 실행해 보지 않은 내용이다. 해당 Task의 첫 단계에서 확인하고, 다르면 계획을 고친 뒤 진행한다.

| 내용 | Task |
|---|---|
| self-init의 `request`로 transit, KV v2, AppRole, 정책, audit device를 만들 수 있고, role-id와 secret-id를 파일에서 읽어 지정할 수 있다 | 5 |
| HMAC만 허용하는 정책이 `path "transit/hmac/fingerprint" { capabilities = ["update"] }`로 충분하다 | 5 |
| opengrep 2.0의 단일 바이너리가 worker 이미지에서 root가 아닌 사용자, 읽기 전용 루트 파일시스템으로 실행된다 | 8 |
| opengrep의 `--x-ignore-semgrepignore-files`(내부용 표시)가 `test/`, `build/`, `vendor/` 기본 제외를 끈다 | 8 |
| 파일 일부를 읽지 못했을 때 opengrep의 종료 코드와 `errors[]` 내용 | 8 |
| betterleaks와 opengrep의 macOS용 배포 파일 이름 | 8 |
| 조사 보고서의 opengrep 규칙 예시는 실행해 보지 않은 것이다 | 9 |

## Global Constraints

- `AGENTS.md`의 모든 규칙을 따른다. 작업 하나를 끝낼 때마다 `make check`가 통과해야 한다.
- 코드, 주석, 커밋 메시지는 영문, `CHANGELOG.md`와 `docs/` 아래는 한글로 쓴다.
- 의존성을 추가할 때마다 `docs/decisions/`에 결정 기록을 쓴다(0015부터). 버전은 구현 시점의 최신 안정 버전을 확인해 고정한다.
- 라이브러리, OpenBao, 스캐너의 API와 옵션 이름은 구현 시점의 공식 문서와 소스로 확인한다.
- 컨테이너 이미지는 태그와 digest로 고정한다. compose가 호스트에 여는 포트는 모두 `127.0.0.1`에만 묶는다. 실제 원본을 보관하는 OpenBao는 호스트에 포트를 열지 않는다.
- 원본 값은 postgres, valkey, 로그, 오류 메시지, 스캔 실패 사유, 템플릿 어디에도 남지 않는다. 원본 값을 담는 타입은 `String()`, `Format`, `LogValue()`가 가린 값을 돌려준다.
- 스캐너의 표준 출력과 표준 오류에는 평문 값이 있을 수 있다. 파일에 쓰지 않고, 로그와 오류에 넣지 않는다.
- 고객정보는 원본과 주변 줄을 저장하지 않는다. 종류, 위치, 지문만 저장한다.
- 입력에서 온 경로는 심볼릭 링크까지 풀어 작업 루트 안인지 확인한 뒤 쓴다. 파일 트리를 돌 때 심볼릭 링크는 따라가지 않는다.
- 외부 명령은 `exec.CommandContext`와 인자 슬라이스로 실행한다.
- 비밀 값의 미리보기는 앞 4자와 `****`다. 8자 미만이면 `****`만 쓴다.
- 기본 제외 경로는 `.git`, `target`, `build`, `node_modules`다. 프로젝트별 제외 경로를 더할 수 있다.
- 스캔 대상은 현재 작업본의 파일이다. git이 추적하지 않는 파일과 `.gitignore` 대상 파일도 포함한다.
- 스캔 기록에 커밋 해시, 도구 버전, 규칙 버전을 남긴다.
- 스캔 간 식별은 "프로젝트, 지문, 파일 경로"다.
- 테스트 예제 소스의 값은 가짜임이 분명해야 한다(`dev-only-fake-...`, `010-0000-0000`).

## Review Focus

1. 주변 줄에 다른 탐지 값이 있으면 그 값도 가려진다. 한 줄에 값이 둘이면 둘 다 가려진다. (Task 7)
2. 스캐너가 알려 준 위치의 파일 내용이 값과 다르면 그 항목을 저장하지 않고 건수만 남긴다. CRLF 파일, EUC-KR 파일, 여러 줄에 걸친 값에서 위치가 맞는다. (Task 6, 10)
3. 프로젝트 경로가 심볼릭 링크로 작업 루트 밖을 가리키면 스캔이 실패하고 파일을 하나도 읽지 않는다. 프로젝트 안의 심볼릭 링크는 건너뛴다. (Task 4, 6)
4. OpenBao에 닿지 않으면 스캔은 실패로 끝나고 탐지 항목이 하나도 저장되지 않는다. 실패 사유와 로그에 값이 없다. (Task 10)
5. 스캐너가 0이 아닌 코드로 끝나거나 JSON이 깨졌을 때, 표준 출력과 표준 오류의 내용이 오류 메시지와 로그에 들어가지 않는다. (Task 8)
6. worker가 스캔 중에 죽으면 임대 시간이 지난 뒤 다른 실행이 이어받고, 같은 스캔의 항목이 두 번 저장되지 않는다. 세 번 죽으면 스캔이 실패로 표시된다. (Task 3, 4)
7. 한 프로젝트에 스캔을 연달아 요청해도 진행 중인 스캔은 하나다. (Task 3)
8. 20MB를 넘는 파일, 바이너리 파일, UTF-8도 EUC-KR도 아닌 파일은 건너뛰고 건수가 스캔 화면에 보인다. (Task 6, 11)

## File Structure

| 파일 | 책임 |
|---|---|
| `internal/db/db.go`, `migrate.go`, `migrations/*.sql` | pgx 풀 열기, 마이그레이션 실행. 이 패키지 밖에는 SQL이 없다 |
| `internal/db/project.go`, `scan.go`, `job.go`, `finding.go` | 프로젝트, 스캔 기록, 작업 큐, 탐지 항목의 쿼리 |
| `internal/project/project.go` | `Project`, 경로 모양 검사(`ParsePath`), 작업 루트 안인지 확인(`Resolve`) |
| `internal/scan/scan.go` | `ID`, `Status`, `Scan`, `Raw`, `Scanner` 인터페이스 |
| `internal/scan/runner.go` | 스캔 한 건의 실행: 순회, 스캐너 실행, 위치 확인, 병합, 지문, 원본 보관, 저장 |
| `internal/job/job.go` | `Job` 타입, 작업을 가져와 실행하는 루프, 임대 연장. 다른 내부 패키지를 가져오지 않는다 |
| `internal/bao/client.go` | OpenBao 클라이언트: AppRole 로그인, 지문 계산, 임시 원본 쓰기·목록·삭제 |
| `internal/textfile/textfile.go`, `walk.go` | 인코딩 판별, UTF-8 변환, 위치 변환, 줄 읽기, 트리 순회 |
| `internal/finding/finding.go`, `mask.go` | `Category`, `Kind`, `Finding`, `Location`, 미리보기, 주변 줄 가리기 |
| `internal/betterleaks`, `internal/opengrep`, `internal/keyfile` | `scan.Scanner` 구현 셋 |
| `internal/web/handlers_project.go`, `handlers_scan.go`, 템플릿 | 프로젝트 목록·등록·상세, 스캔 요청·상태·탐지 목록 |
| `cmd/worker/main.go`, `config.go` | worker의 `main`과 `run` |
| `rules/betterleaks.toml`, `rules/opengrep/*.yml` | 탐지 규칙 |
| `internal/ruleset/ruleset.go`, `testdata/legacy-shop/`, `testdata/expected.json` | 규칙 버전 계산, 규칙 테스트와 그 예제 소스, 기대 결과 |
| `deploy/worker/Dockerfile`, `scanners.lock`, `fetch-scanners.sh` | worker 이미지, 스캐너 버전과 체크섬, 내려받기 |
| `deploy/openbao/config.hcl`, `deploy/openbao/make-secrets.sh` | OpenBao 설정(static seal, self-init), 키와 secret-id 생성 |
| `docs/decisions/0015`~ | pgx, 마이그레이션과 작업 큐, OpenBao 구성과 클라이언트, x/text와 인코딩 처리, 스캐너 실행 방식 |

---

## PR 1: postgres, 프로젝트, 작업 큐, worker

### Task 1: postgres 연결과 마이그레이션

**Files:**
- Create: `internal/db/db.go`, `internal/db/migrate.go`, `internal/db/migrations/0001_init.sql`, `internal/db/db_test.go`, `docs/decisions/0015-pgx.md`, `docs/decisions/0016-own-migrations-and-queue.md`
- Modify: `cmd/web/config.go`, `cmd/web/main.go`, 두 테스트, `compose.yaml`, `deploy/postgres/init.sql`, `Makefile`, `docs/local-run.md`

**Interfaces:**
- Produces (`internal/db`):
  - `type DSN string`. `String`, `Format`, `LogValue`가 `[redacted]`를 돌려준다.
  - `func Open(ctx context.Context, dsn DSN) (*Store, error)`. 풀을 만들고 `Ping`한다. 오류에 DSN이 들어가지 않는다.
  - `func (s *Store) Migrate(ctx context.Context) error`, `func (s *Store) Close()`
- web 환경 변수 `DATABASE_URL`(필수). `run`이 `Open`, `Migrate`를 부른 뒤 서버를 띄운다.

마이그레이션 규칙: 파일 이름은 `NNNN_이름.sql`이고 번호순으로 실행한다. 실행한 번호는 `schema_migration(version, applied_at)`에 남긴다. 전체를 `pg_advisory_lock`으로 감싸고, 파일 하나가 트랜잭션 하나다.

compose: `init.sql`에 `se_app` 계정과 테스트용 데이터베이스 `secret_externalizer_test`를 더한다. postgres를 `127.0.0.1:15432`에 연다(통합 테스트용). `init.sql`은 볼륨을 처음 만들 때만 실행되므로, 기존 볼륨은 `docker compose down -v`로 지우고 다시 띄운다는 안내를 `docs/local-run.md`에 쓴다.

- [ ] **Step 1: 실패하는 테스트 작성**

| 테스트 | 기대 |
|---|---|
| `TestDSN_Redacted` | `%v`, `%+v`, `%#v`, `%s`, `slog`, `json.Marshal` 어디에도 비밀번호가 없다 |
| `TestOpen_ErrorHidesDSN` | 닿지 않는 주소로 `Open`하면 오류에 비밀번호가 없다 |
| `TestMigrate_AppliesOnce` | 두 번 불러도 `schema_migration`에 줄이 하나씩이다 |
| `TestMigrate_Concurrent` | `Store` 둘이 동시에 `Migrate`해도 둘 다 nil이고 각 마이그레이션이 한 번만 실행된다 |
| `TestLoadConfig_RequiresDatabaseURL`(`cmd/web`) | 없으면 오류에 `DATABASE_URL`이 있다 |

postgres가 필요한 테스트는 `TEST_DATABASE_URL`이 없으면 `t.Skip`한다. 테스트마다 `CREATE SCHEMA test_<난수>`를 만들고 `search_path`를 그쪽으로 잡은 풀을 돌려주는 도우미 `newTestStore(t)`를 `internal/db/export_test.go`에 둔다. `make test-integration`은 `TEST_DATABASE_URL`도 요구한다.

- [ ] **Step 2: 실패 확인**
- [ ] **Step 3: 구현** — `0001_init.sql`은 이 Task에서 비어 있지 않아야 하므로 Task 2의 `project` 표를 여기서 만든다.
- [ ] **Step 4: 통과 확인** — `make check`, `make test-integration`
- [ ] **Step 5: 커밋** — `feat(db): open postgres and run embedded migrations`

---

### Task 2: 프로젝트 등록

**Files:**
- Create: `internal/project/project.go`, `internal/project/project_test.go`, `internal/db/project.go`, `internal/db/project_test.go`, `internal/web/handlers_project.go`, `internal/web/handlers_project_test.go`, `internal/web/templates/projects.html`, `project_new.html`, `project.html`
- Modify: `internal/web/server.go`, `routes.go`, `fakes_test.go`, `home.html`, `cmd/web/main.go`

**Interfaces:**
- Produces (`internal/project`):
  - `type ID int64`
  - `type Project struct { ID ID; Name, Path string; Excludes []string; CreatedBy string; CreatedAt time.Time }`
  - `func ParsePath(raw string) (string, error)`. 모양만 본다. 실패는 `ErrInvalidPath`.
  - `func Resolve(root, rel string) (string, error)`. 심볼릭 링크를 풀어 `root` 안의 폴더인지 확인한다. 실패는 `ErrOutsideRoot`, `ErrNotDirectory`.
- Produces (`internal/db`): `CreateProject(ctx, project.Project) (project.ID, error)`(이름이나 경로가 겹치면 `ErrDuplicate`), `Project(ctx, project.ID) (project.Project, error)`(없으면 `ErrNotFound`), `Projects(ctx) ([]project.Project, error)`
- Produces (`internal/web`): `type ProjectStore interface`(위 세 메서드). `NewServer`에 인자가 하나 늘어난다.

경로: `GET /projects`, `GET /projects/new`, `POST /projects`, `GET /projects/{id}`. 모두 로그인이 필요하다.

`project` 표: `id`, `name`(유일, 1~100자), `path`(유일), `excludes text[]`, `created_by`(OIDC subject), `created_at`.

- [ ] **Step 1: 실패하는 테스트 작성**

`ParsePath`

| 입력 | 기대 |
|---|---|
| `shop`, `team/shop`, `.` | 그대로 |
| `shop/`, `./shop`, `team//shop` | `shop`, `shop`, `team/shop` |
| 빈 문자열, `/abs`, `..`, `a/../../b`, `a\b`, NUL이 든 문자열, 1024바이트 초과 | `ErrInvalidPath` |

`Resolve` (`t.TempDir`에 폴더와 심볼릭 링크를 만든다)

| 상황 | 기대 |
|---|---|
| 루트 아래의 폴더 | 풀린 절대 경로 |
| 루트 밖을 가리키는 심볼릭 링크 | `ErrOutsideRoot` |
| 루트 안의 다른 폴더를 가리키는 심볼릭 링크 | 풀린 절대 경로 |
| 없는 경로, 파일 | `ErrNotDirectory` |
| 루트 자체가 심볼릭 링크 | 정상 동작 |

handler (손으로 쓴 fake `ProjectStore`)

| 테스트 | 기대 |
|---|---|
| `TestProjects_RequiresLogin` | 세션 없이 `GET /projects`는 로그인으로 보낸다 |
| `TestProjectCreate_Valid` | `POST /projects` 뒤 `/projects/{id}`로 303, 저장된 `CreatedBy`가 세션의 subject다 |
| `TestProjectCreate_InvalidPath` | `../x`는 400, 폼이 입력값과 함께 다시 보이고 저장되지 않는다 |
| `TestProjectCreate_Duplicate` | `ErrDuplicate`는 409와 안내 문구 |
| `TestProjectCreate_Excludes` | 여러 줄 입력이 줄마다 `ParsePath`를 거쳐 저장된다. 빈 줄은 버린다 |
| `TestProjectCreate_CrossOrigin` | 다른 출처의 `POST`는 403 |
| `TestProject_NotFound` | 없는 ID, 숫자가 아닌 ID는 404 |
| `TestProject_EscapesName` | 이름 `<script>`가 이스케이프되어 나온다 |

- [ ] **Step 2: 실패 확인**
- [ ] **Step 3: 구현**
- [ ] **Step 4: 통과 확인** — `make check`, `make test-integration`
- [ ] **Step 5: 커밋** — `feat: register projects under the workspace root`

---

### Task 3: 스캔 요청과 작업 큐

**Files:**
- Create: `internal/scan/scan.go`, `internal/job/job.go`(타입만), `internal/db/scan.go`, `internal/db/job.go`, 두 테스트, `internal/db/migrations/0002_scan_and_job.sql`, `internal/web/handlers_scan.go`, `handlers_scan_test.go`, `templates/scan.html`
- Modify: `internal/web/server.go`, `routes.go`, `fakes_test.go`, `templates/project.html`

**Interfaces:**
- Produces (`internal/scan`):
  - `type ID int64`
  - `type Status string`: `StatusQueued`, `StatusRunning`, `StatusSucceeded`, `StatusFailed`
  - `type Scan struct { ID ID; ProjectID project.ID; Status Status; RequestedBy string; RequestedAt, StartedAt, FinishedAt time.Time; CommitHash, RulesVersion string; ToolVersions map[string]string; FailureReason string; Files, Skipped, Findings int }`
- Produces (`internal/job`): `type Job struct { ID int64; ScanID int64; Attempts int }`. `ScanID`가 `scan.ID`가 아닌 것은 `scan`이 `job`을 가져오기 때문이다(`db`는 `job`, `scan`, `project`를 가져오고, `scan`은 `job`을 가져온다).
- Produces (`internal/db`):
  - `RequestScan(ctx, project.ID, requestedBy string) (scan.ID, error)`. 스캔 기록과 작업을 한 트랜잭션에 넣는다. 진행 중인 스캔이 있으면 그 ID와 `ErrScanActive`를 돌려준다.
  - `Scan(ctx, scan.ID) (scan.Scan, error)`, `ScansByProject(ctx, project.ID) ([]scan.Scan, error)`
  - `ClaimJob(ctx, workerID string, lease time.Duration) (job.Job, error)`. 없으면 `job.ErrNoJob`.
  - `ExtendJob(ctx, jobID int64, workerID string, lease time.Duration) error`. 임대를 잃었으면 `job.ErrLeaseLost`.
  - `StartScan(ctx, scan.ID) error`, `FailScan(ctx, jobID int64, workerID string, id scan.ID, reason string) error`
- `SaveResult`(성공 기록)는 Task 10에서 만든다. Task 4는 이 Task에서 만드는 `FinishEmptyScan(ctx, jobID, workerID, scan.ID, files, skipped int) error`를 쓰고, Task 10에서 이를 지운다.

경로: `POST /projects/{id}/scans`, `GET /scans/{id}`.

`job` 표: `id`, `scan_id`, `state`(`queued`, `running`, `done`, `failed`), `attempts`, `locked_by`, `locked_until`, `created_at`. `scan` 표에는 "프로젝트당 `queued`나 `running`은 하나"인 부분 유일 인덱스를 둔다.

`ClaimJob`은 한 트랜잭션에서 다음을 한다.

1. 임대 시간이 지났고 `attempts`가 3인 작업을 `failed`로 바꾸고, 그 스캔을 사유 `worker stopped during the scan three times`로 실패 처리한다.
2. `queued`이거나 임대 시간이 지난 `running` 작업 하나를 `FOR UPDATE SKIP LOCKED`로 잡아 `running`, `attempts+1`, `locked_by`, `locked_until`을 쓴다.

- [ ] **Step 1: 실패하는 테스트 작성**

| 테스트 | 기대 |
|---|---|
| `TestRequestScan_CreatesScanAndJob` | 스캔은 `queued`, 작업은 `queued`다 |
| `TestRequestScan_ActiveScan` | 두 번째 호출은 첫 스캔의 ID와 `ErrScanActive`. goroutine 둘이 동시에 불러도 스캔은 하나다 |
| `TestClaimJob_OneWinner` | 작업 하나를 goroutine 열이 동시에 잡으면 하나만 성공하고 나머지는 `ErrNoJob` |
| `TestClaimJob_ReclaimsExpiredLease` | 임대 0초로 잡은 작업을 다른 worker가 다시 잡고 `Attempts`가 2다 |
| `TestClaimJob_GivesUpAfterThree` | 세 번 잡힌 뒤 임대가 지나면 스캔이 `failed`이고 사유가 위 문구다 |
| `TestExtendJob_LeaseLost` | 다른 worker가 가져간 작업을 연장하면 `ErrLeaseLost` |
| `TestFailScan_OnlyByLeaseHolder` | 임대를 잃은 worker의 `FailScan`은 `ErrLeaseLost`이고 스캔 상태가 바뀌지 않는다 |
| `TestScanCreate_Redirects`(handler) | `POST /projects/{id}/scans` 뒤 `/scans/{id}`로 303. `ErrScanActive`여도 진행 중인 스캔으로 303 |
| `TestScan_RefreshesWhileActive`(handler) | `queued`, `running`이면 본문에 3초 새로 고침이 있고, 끝난 스캔에는 없다 |
| `TestScan_ShowsFailureReason`(handler) | 실패 사유가 이스케이프되어 보인다 |

- [ ] **Step 2: 실패 확인**
- [ ] **Step 3: 구현**
- [ ] **Step 4: 통과 확인** — `make check`, `make test-integration`
- [ ] **Step 5: 커밋** — `feat: request scans through a postgres job queue`

---

### Task 4: worker와 빈 스캔

**Files:**
- Create: `cmd/worker/main.go`, `config.go`, 두 테스트, `internal/job/run.go`, `run_test.go`, `internal/scan/runner.go`, `runner_test.go`, `deploy/worker/Dockerfile`
- Modify: `compose.yaml`, `AGENTS.md`("Project layout"의 진입점 문장에 `cmd/worker`), `docs/next-steps.md`, `docs/local-run.md`, `CHANGELOG.md`

**Interfaces:**
- Produces (`internal/job`):
  - `type Queue interface { ClaimJob(…); ExtendJob(…) }`(Task 3의 시그니처 그대로). `ErrNoJob`과 `ErrLeaseLost`는 `job`에 두고 `db`가 그것을 돌려준다.
  - `type Handler func(ctx context.Context, j Job) error`
  - `type Options struct { WorkerID string; Poll, Lease, Heartbeat time.Duration }`
  - `func Run(ctx context.Context, logger *slog.Logger, q Queue, h Handler, opts Options) error`. `ctx`가 취소되면 실행 중인 작업의 `ctx`도 취소하고, 그 작업이 돌아온 뒤 nil을 돌려준다.
- Produces (`internal/scan`):
  - `type Runner struct`, `func NewRunner(store Store, root string, …) *Runner`, `func (r *Runner) Handle(ctx context.Context, j job.Job) error`. `Store`는 `scan`이 정의하는 인터페이스이고 `db.Store`가 구현한다.
  - 이 Task의 `Handle`: `StartScan`, 프로젝트 조회, `project.Resolve`, 파일 수 세기(Task 6 전이므로 `filepath.WalkDir`로 일반 파일만), `FinishEmptyScan`. 오류가 나면 `FailScan`에 사유를 쓴다.

worker 설정: `DATABASE_URL`, `WORKSPACE_ROOT`(필수, 절대 경로이고 폴더여야 한다). 기본값: 확인 간격 2초, 임대 2분, 연장 30초. `run`의 시그니처와 종료 방식은 `cmd/web`과 같다.

실패 사유 문구(화면에 그대로 보인다. 경로 말고는 입력에서 온 내용을 넣지 않는다):

| 상황 | 사유 |
|---|---|
| `ErrOutsideRoot` | `project path points outside the workspace root` |
| `ErrNotDirectory` | `project path is not a directory in the workspace` |
| 그 밖의 오류 | `scan could not finish; see the worker log` (오류 자체는 worker 로그에 한 번 남긴다) |

compose의 `worker`: 포트 없음, `user: "65532:65532"`, `read_only: true`, `cap_drop: [ALL]`, `no-new-privileges`, `/tmp`는 tmpfs, `${SE_WORKSPACE:?…}:/workspace:ro`. `SE_WORKSPACE`가 없으면 compose가 안내 문구와 함께 멈춘다. 이미지는 git이 든 고정 버전의 alpine이다(커밋 해시를 읽는 데 쓰고, 계획 5의 적용 조건 확인에도 필요하다).

- [ ] **Step 1: 실패하는 테스트 작성**

| 테스트 | 기대 |
|---|---|
| `TestRun_HandlesJob` | fake 큐의 작업 하나가 handler에 전달된다 |
| `TestRun_ExtendsLease` | handler가 도는 동안 `ExtendJob`이 불린다(주입한 짧은 간격, 채널로 기다린다) |
| `TestRun_CancelsHandlerWhenLeaseLost` | `ExtendJob`이 `ErrLeaseLost`를 돌려주면 handler의 `ctx`가 취소된다 |
| `TestRun_StopsOnCancel` | `ctx`를 취소하면 실행 중인 handler가 끝난 뒤 nil을 돌려준다 |
| `TestRun_SurvivesClaimError` | `ClaimJob` 오류는 한 번 로그에 남고 루프가 계속 돈다 |
| `TestHandle_CountsFiles` | 파일 3개인 폴더의 스캔이 `succeeded`, `Files == 3` |
| `TestHandle_OutsideRoot` | 밖을 가리키는 심볼릭 링크 프로젝트는 `failed`와 위 사유. 링크가 가리키는 폴더의 파일을 열지 않는다 |
| `TestHandle_FailureReasonHidesError` | 저장소 오류 `dial tcp 10.0.0.5`가 사유에 없다 |
| `TestRun_Worker`(`cmd/worker`) | `-version`, `-h`, 필수 환경 변수 누락 오류 |

- [ ] **Step 2: 실패 확인**
- [ ] **Step 3: 구현** — 루프의 대기는 `time.Timer`를 다시 맞춰 쓴다. handler와 임대 연장은 `errgroup`으로 묶는다.
- [ ] **Step 4: 통과 확인** — `make check`, `make test-integration`
- [ ] **Step 5: compose 확인**

| 번호 | 동작 | 기대 |
|---|---|---|
| 1 | `SE_WORKSPACE` 없이 `docker compose up` | 안내 문구와 함께 멈춘다 |
| 2 | 예제 폴더를 `SE_WORKSPACE`로 주고 띄운 뒤 프로젝트 등록, 스캔 시작 | 몇 초 안에 스캔 화면이 `succeeded`와 파일 수를 보여준다 |
| 3 | 없는 경로로 등록하고 스캔 | `failed`와 사유 |
| 4 | 스캔 중 `docker compose kill worker` 뒤 다시 띄운다 | 2분쯤 뒤 스캔이 다시 실행되어 끝난다 |

- [ ] **Step 6: 커밋과 PR** — `feat(worker): run queued scans from the job queue`. 여기서 PR 1을 올린다.

---

## PR 2: OpenBao, 스캐너, 탐지 항목

### Task 5: OpenBao 구성과 클라이언트

**Files:**
- Create: `deploy/openbao/config.hcl`, `deploy/openbao/make-secrets.sh`, `internal/bao/client.go`, `client_test.go`, `integration_test.go`, `docs/decisions/0017-openbao-setup-and-client.md`
- Modify: `compose.yaml`, `cmd/worker/config.go`, `main.go`, `Makefile`, `docs/local-run.md`

**Interfaces:**
- Produces (`internal/bao`):
  - `type SecretID string`(가린다), `type Credentials struct { RoleID string; SecretID SecretID }`
  - `func New(addr string, creds Credentials, hc *http.Client) (*Client, error)`. `hc`에 제한 시간이 없으면 거부한다.
  - `func (c *Client) Fingerprints(ctx context.Context, values [][]byte) ([]string, error)`. 입력 순서대로, 소문자 16진수 64자.
  - `func (c *Client) PutPending(ctx context.Context, projectID int64, fingerprint string, value []byte) error`
  - `func (c *Client) ListPending(ctx context.Context, projectID int64) ([]string, error)`. 없으면 빈 목록.
  - `func (c *Client) DeletePending(ctx context.Context, projectID int64, fingerprint string) error`. 모든 버전과 메타데이터를 지운다. 없는 것을 지워도 nil.

OpenBao의 구성:

| 항목 | 값 |
|---|---|
| 저장소 | `storage "pebbledb"`, 이름 있는 볼륨 `openbao-data` |
| 봉인 | `seal "static"`, 키는 볼륨 `openbao-secrets`의 파일 |
| transit | 마운트 `transit`, 키 `fingerprint`, 회전하지 않는다. 요청마다 `key_version: 1`, `sha2-256` |
| KV v2 | 마운트 `secret`. 임시 원본은 `pending/<프로젝트 ID>/<지문>`, 본문은 `{"value": "<base64>"}` |
| AppRole `worker` | `transit/hmac/fingerprint` update, `secret/data/pending/*`와 `secret/data/projects/*` create·update, `secret/metadata/pending/*` list·delete |
| AppRole `web` | `secret/data/pending/*`와 `secret/data/projects/*` read. 이 계획에서 web은 OpenBao에 접속하지 않는다 |
| 토큰 | TTL 1시간. 클라이언트는 처음 쓸 때 로그인하고, 403을 받으면 한 번 다시 로그인해 재시도한다 |
| audit device | 표준 출력으로 내보내는 file 장치 |

`openbao-secrets` 컨테이너는 볼륨에 파일이 없을 때만 봉인 해제 키(32바이트)와 두 역할의 secret-id를 난수로 만든다. worker는 `BAO_ADDR`, `BAO_ROLE_ID`, `BAO_SECRET_ID_FILE`을 읽는다.

통합 테스트는 실제 원본이 든 OpenBao를 쓰지 않는다. compose의 `test` 프로필에 `-dev` 모드의 `openbao-test`를 `127.0.0.1:18200`에 두고, 테스트가 개발용 루트 토큰으로 transit, KV, AppRole을 직접 만든다. `TEST_BAO_ADDR`이 없으면 건너뛴다.

- [ ] **Step 1: "구현 전에 확인할 사실"의 Task 5 항목을 공식 문서로 확인한다.** self-init으로 안 되는 것이 있으면 그 부분만 한 번 실행되는 설정 컨테이너로 옮기고 결정 기록에 쓴다.
- [ ] **Step 2: 실패하는 테스트 작성** (`httptest`로 만든 가짜 OpenBao)

| 테스트 | 기대 |
|---|---|
| `TestFingerprints_Request` | `POST /v1/transit/hmac/fingerprint`, 본문에 `key_version: 1`과 base64 입력. `vault:v1:<base64>` 응답이 16진수 64자로 바뀐다 |
| `TestFingerprints_WrongVersion` | 응답이 `vault:v2:`면 오류 |
| `TestFingerprints_Batches` | 값 1,200개가 500개씩 나뉘어 요청되고 순서가 유지된다 |
| `TestClient_LogsInOnce` | 호출 세 번에 로그인은 한 번 |
| `TestClient_ReLoginOn403` | 403 뒤 다시 로그인해 성공한다. 두 번째도 403이면 오류 |
| `TestClient_ErrorsHideValues` | 500 응답 본문에 값이 있어도 오류 메시지와 `%+v`에 값, secret-id, 토큰이 없다 |
| `TestDeletePending_Missing` | 404는 nil |
| `TestNew_RequiresTimeout` | 제한 시간 없는 클라이언트는 거부 |
| `TestIntegration_RoundTrip` | 실제 OpenBao에서 같은 값의 지문이 두 번 같고, 쓰기·목록·삭제가 된다. `worker` 역할의 토큰으로 `secret/data/pending/…` 읽기는 403이다 |

- [ ] **Step 3: 실패 확인**
- [ ] **Step 4: 구현**
- [ ] **Step 5: 통과 확인** — `make check`, `make test-integration`. `docker compose up` 뒤 `docker compose restart openbao`를 해도 봉인이 풀린 채로 뜨는지 확인한다.
- [ ] **Step 6: 커밋** — `feat(bao): fingerprint values and hold pending originals in OpenBao`

---

### Task 6: 파일 읽기, 인코딩, 위치

**Files:**
- Create: `internal/textfile/textfile.go`, `walk.go`, 두 테스트, `internal/textfile/testdata/`(EUC-KR, CRLF, BOM 예제), `docs/decisions/0018-encoding-detection.md`

**Interfaces:**
- Produces (`internal/textfile`):
  - `type Encoding int`(1부터): `UTF8`, `EUCKR`
  - `type File struct { Encoding Encoding; Text []byte; Hash string }`. `Text`는 BOM을 뗀 UTF-8, `Hash`는 원래 바이트의 SHA-256(16진수).
  - `func Read(path string, maxBytes int64) (File, error)`. 실패는 `ErrTooLarge`, `ErrBinary`, `ErrUnknownEncoding`.
  - `func (f File) Position(offset int) (line, col int)`. `Text`의 바이트 위치를 1부터 세는 줄과 문자 단위 열로 바꾼다.
  - `func (f File) Offset(line, byteCol int) (int, error)`. 줄과 그 줄 안의 1부터 세는 바이트 열을 `Text`의 바이트 위치로 바꾼다.
  - `func (f File) Lines(first, last int) []string`. 줄 끝의 `\r`은 뗀다. 범위를 벗어난 부분은 잘라 낸다.
  - `func Walk(root string, excludes []string, fn func(rel string, d fs.DirEntry) error) error`. 일반 파일만 넘긴다. 심볼릭 링크와 기본 제외 경로, `excludes`는 건너뛴다.

판별 순서: UTF-8 BOM이면 UTF-8. UTF-16 BOM이면 `ErrUnknownEncoding`. 처음 8,000바이트에 NUL이 있으면 `ErrBinary`. `utf8.Valid`면 UTF-8. 아니면 `korean.EUCKR`로 바꾸고, 결과에 `U+FFFD`가 있으면 `ErrUnknownEncoding`.

- [ ] **Step 1: 실패하는 테스트 작성**

| 테스트 | 기대 |
|---|---|
| `TestRead_UTF8`, `_UTF8BOM`, `_EUCKR` | `Encoding`이 맞고 `Text`가 기대한 UTF-8이다. `Hash`는 원래 바이트의 해시다 |
| `TestRead_CP949ExtendedHangul` | `똠방각하`(EUC-KR 기본 범위 밖)가 들어 있는 CP949 파일이 그대로 읽힌다 |
| `TestRead_Binary`, `_UTF16`, `_Garbage`, `_TooLarge` | 각각의 오류 |
| `TestPosition_Table` | 한글 뒤의 위치는 열이 문자 단위다. CRLF 파일에서 줄 번호가 맞다. 마지막 줄에 줄바꿈이 없어도 된다 |
| `TestOffset_RoundTrip` | `Offset`으로 얻은 위치를 `Position`에 넣으면 같은 줄이 나온다. 없는 줄은 오류 |
| `TestLines_StripsCR`, `_Clamps` | |
| `TestWalk_SkipsSymlinksAndExcludes` | 파일과 폴더의 심볼릭 링크, `.git`, `target`, `build`, `node_modules`, 프로젝트 제외 경로가 빠진다. `src/build.xml`처럼 이름만 비슷한 파일은 남는다 |
| `TestWalk_UnreadableFile` | 읽기 권한이 없는 폴더는 오류가 아니라 건너뛴다 |

- [ ] **Step 2: 실패 확인**
- [ ] **Step 3: 구현**
- [ ] **Step 4: 통과 확인** — `make check`
- [ ] **Step 5: 커밋** — `feat(textfile): read legacy sources as UTF-8 with positions`

---

### Task 7: 탐지 항목 타입과 가리기

**Files:**
- Create: `internal/finding/finding.go`, `mask.go`, 두 테스트

**Interfaces:**
- Produces (`internal/finding`):
  - `type Category string`: `CategoryPartnerSession`(`partner-session`), `CategoryStorage`(`storage-credential`), `CategorySSH`(`ssh-credential`), `CategoryCustomerData`(`customer-data`), `CategoryOther`(`other-secret`). `func ParseCategory(string) (Category, error)`.
  - `type Kind string`. 고객정보: `phone`, `rrn`, `email`, `card`, `address`. 비밀 값: `password`, `token`, `account`, `host`, `url`, `private-key`, `key-file`, `other`.
  - `type Detection struct { Tool, Rule string }`
  - `type Span struct { Start, End int }`(`Text`의 바이트 위치, 끝은 포함하지 않는다)
  - `type Location struct { StartLine, StartCol, EndLine, EndCol int; Context []string; ContextFirstLine int; FileHash string; Detections []Detection }`
  - `type Finding struct { Fingerprint, File string; Category Category; Kind Kind; Preview string; HasOriginal bool; Locations []Location }`
  - `func Preview(c Category, k Kind, value string) string`
  - `func Context(f textfile.File, target Span, all []Span) (lines []string, first int)`. 값이 있는 줄과 앞뒤 2줄을 돌려주되, `all`의 모든 구간을 `****`로 바꾼다. 한 줄은 200자에서 자른다.

`Preview`의 규칙:

| 분류 | 결과 |
|---|---|
| 비밀 값, 8자 이상 | 앞 4자 + `****` |
| 비밀 값, 8자 미만 | `****` |
| `key-file`, `private-key` | `****` |
| `phone` | 숫자를 가리고 형식만 남긴다. 맨 앞 묶음만 보인다: `010-****-****` |
| `rrn` | `******-*******` |
| `email` | `****@****` |
| `card` | `****-****-****-****` |
| `address` | `****` |

고객정보의 `Location.Context`는 항상 비어 있다. 이 규칙은 Task 10의 조립이 지키고 Task 10의 테스트가 확인한다.

- [ ] **Step 1: 실패하는 테스트 작성**

| 테스트 | 기대 |
|---|---|
| `TestPreview_Table` | 위 표. 한글 값은 문자 단위로 센다 |
| `TestPreview_NeverContainsDigitsOfCustomerData` | 전화번호, 주민등록번호, 카드번호 입력의 결과에 맨 앞 묶음 말고는 입력의 숫자가 없다 |
| `TestContext_MasksTarget` | 값이 `****`로 바뀌고 줄의 나머지는 그대로다 |
| `TestContext_MasksNeighbours` | 앞 줄과 같은 줄의 다른 구간도 가려진다 |
| `TestContext_MultiLineValue` | 여러 줄에 걸친 값(PEM)은 첫 줄에 `****` 하나만 남고 가운데 줄은 빈 줄이 된다 |
| `TestContext_OverlappingSpans` | 겹치는 구간 둘이 `****` 하나로 합쳐진다 |
| `TestContext_TruncatesAfterMasking` | 5,000자짜리 한 줄에서 200자 뒤에 있는 값이 잘린 결과에 남지 않는다. 가린 뒤에 자른다 |
| `TestContext_FileEdges` | 첫 줄과 마지막 줄에서 범위를 벗어나지 않는다 |
| `TestParseCategory_Unknown` | 모르는 문자열은 오류 |

- [ ] **Step 2: 실패 확인**
- [ ] **Step 3: 구현**
- [ ] **Step 4: 통과 확인** — `make check`
- [ ] **Step 5: 커밋** — `feat(finding): mask previews and surrounding lines`

---

### Task 8: 스캐너 실행과 출력 읽기

**Files:**
- Create: `internal/betterleaks/betterleaks.go`, `internal/opengrep/opengrep.go`, `internal/keyfile/keyfile.go`, 각 테스트와 `testdata/*.json`, `deploy/worker/scanners.lock`, `deploy/worker/fetch-scanners.sh`, `docs/decisions/0019-scanner-invocation.md`
- Modify: `internal/scan/scan.go`, `deploy/worker/Dockerfile`, `Makefile`(`make scanners`), `AGENTS.md`("Formatting and tooling"의 버전 고정 문장)

**Interfaces:**
- Produces (`internal/scan`):
  - `type Value []byte`. `String`, `Format`, `LogValue`, `MarshalJSON`이 `[redacted]`를 돌려준다.
  - `type Raw struct { Tool, Rule string; Category finding.Category; Kind finding.Kind; File string; Offset, EndOffset int; Line, ByteCol int; Value Value }`. `File`은 스캔한 폴더 기준 상대 경로다. 스캐너가 바이트 위치를 주면 `Offset`, `EndOffset`을 채우고 `Value`는 비운다(opengrep). 줄과 열만 주면 `Offset`은 -1이고 `Line`, `ByteCol`, `Value`를 채운다(betterleaks). `key-file`은 `Offset` 0, `EndOffset` -1이다(파일 전체).
  - `type Scanner interface { Name() string; Version(ctx context.Context) (string, error); Scan(ctx context.Context, dir string) ([]Raw, error) }`
  - `var ErrScannerFailed`. 스캐너 오류는 도구 이름과 종료 코드만 담는다.
- Produces: `betterleaks.New(bin, config string) *Scanner`, `opengrep.New(bin, rulesDir string, excludes []string) *Scanner`, `keyfile.New() *Scanner`

실행 인자:

| 도구 | 인자 |
|---|---|
| betterleaks | `dir <dir> --no-banner --config <config> --report-format json --report-path - --exit-code 3 --max-target-megabytes 20 --timeout 1500 --log-level warn`. 종료 코드 0과 3이 정상이다 |
| opengrep | `scan --config <rulesDir> --json --quiet --no-git-ignore --x-ignore-semgrepignore-files --max-target-bytes 20MB --timeout 10 --exclude <제외 경로>… <dir>`. 종료 코드 0이 정상이다 |

- 실행 환경은 `PATH`, `HOME`, `TMPDIR`만 넘긴다. `HOME`과 `TMPDIR`은 tmpfs다.
- 표준 출력은 파이프로 읽고 256MiB를 넘으면 실패한다. 표준 오류는 버린다.
- betterleaks의 위치는 값이 아니라 일치한 구간(`Match`)을 가리킨다. `Match` 안에서 `Secret`이 처음 나오는 곳을 찾아 줄과 바이트 열을 옮긴다. `Match`에 줄바꿈이 있으면 줄도 옮긴다.
- opengrep은 `start.offset`, `end.offset`만 쓴다. `extra.lines`와 `metavars`는 읽지 않는다. `errors[]`는 건수만 센다.
- 분류와 종류는 규칙에서 온다. betterleaks는 규칙의 `tags`(`category:…`, `kind:…`), opengrep은 `metadata.category`, `metadata.kind`다. 태그가 없는 betterleaks 기본 규칙은 `other-secret`, `other`이고, 규칙 ID에 `private-key`가 있으면 `ssh-credential`, `private-key`다.
- `keyfile`: 확장자 `jks, p12, pfx, keystore, ppk`, 이름 `id_rsa, id_dsa, id_ecdsa, id_ed25519`, 그리고 첫 4KiB에 `-----BEGIN … PRIVATE KEY-----`나 `-----BEGIN CERTIFICATE-----`가 있는 파일. 분류 `ssh-credential`, 종류 `key-file`.

`scanners.lock`에는 도구마다 버전과, 운영체제·아키텍처별 파일 이름과 SHA-256을 적는다. `fetch-scanners.sh <os> <arch> <dir>`가 내려받아 체크섬을 확인한다. opengrep은 체크섬 파일을 배포하지 않으므로 릴리스의 자산 digest를 적는다.

- [ ] **Step 1: "구현 전에 확인할 사실"의 Task 8 항목을 확인한다.** `make scanners`로 받은 바이너리와 worker 이미지 안에서 직접 실행해 본다.
- [ ] **Step 2: 실패하는 테스트 작성**

출력 읽기(바이너리 없이 `testdata`의 JSON으로)

| 테스트 | 기대 |
|---|---|
| `TestParse_Betterleaks` | 규칙, 분류, 종류, 파일, 줄, 바이트 열, 값이 맞다 |
| `TestParse_Betterleaks_SecretInsideMatch` | `Match`가 `password = "abc"`, `Secret`이 `abc`면 열이 `abc`의 시작이다 |
| `TestParse_Betterleaks_MultiLineMatch` | `Secret`이 `Match`의 둘째 줄에 있으면 줄이 하나 늘고 열이 그 줄 기준이다 |
| `TestParse_Betterleaks_SecretNotInMatch` | 그 항목은 버리고 `dropped`가 1이다 |
| `TestParse_Opengrep` | `Offset`, `EndOffset`이 맞고 `Value`가 비어 있다. `extra.lines`의 내용이 결과 어디에도 없다 |
| `TestParse_Opengrep_MissingMetadata` | 분류가 없는 규칙의 결과는 오류다(규칙 파일의 잘못) |
| `TestParse_Truncated`, `_NotJSON` | `ErrScannerFailed`. 오류 메시지에 입력의 내용이 없다 |
| `TestValue_Redacted` | `%v`, `%s`, `%q`, `%x`, `%#v`, `slog`, `json.Marshal`, 그리고 `Raw`를 `%+v`로 찍었을 때 값이 없다 |

실행(가짜 스캐너 스크립트를 `t.TempDir`에 만들어 `bin`으로 준다)

| 테스트 | 기대 |
|---|---|
| `TestScan_NonZeroExit` | `ErrScannerFailed`. 가짜 스캐너가 표준 출력과 표준 오류에 쓴 `dev-only-fake-leak`이 오류에 없다 |
| `TestScan_Betterleaks_ExitCode3` | 정상으로 본다 |
| `TestScan_OutputTooLarge` | 한도를 넘으면 실패하고 프로세스가 끝난다 |
| `TestScan_ContextCancelled` | `ctx`를 취소하면 프로세스가 끝나고 `ctx.Err()`를 감싼 오류가 온다 |
| `TestScan_Env` | 가짜 스캐너가 본 환경 변수가 `PATH`, `HOME`, `TMPDIR`뿐이다 |

`keyfile`

| 테스트 | 기대 |
|---|---|
| `TestKeyfile_Table` | `deploy/id_rsa`, `certs/server.pem`, `conf/store.jks`는 찾고, `README.md`와 본문에 `PRIVATE KEY`라는 말만 있는 `.java`는 찾지 않는다 |

- [ ] **Step 3: 실패 확인**
- [ ] **Step 4: 구현**
- [ ] **Step 5: 통과 확인** — `make check`
- [ ] **Step 6: 커밋** — `feat: run betterleaks, opengrep, and the key file detector`

---

### Task 9: 탐지 규칙과 예제 소스

**Files:**
- Create: `rules/betterleaks.toml`, `rules/opengrep/spring.yml`, `java.yml`, `shell.yml`, `build.yml`, `internal/ruleset/ruleset.go`, `ruleset_test.go`, `internal/ruleset/testdata/legacy-shop/…`, `internal/ruleset/testdata/expected.json`
- Modify: `deploy/worker/Dockerfile`(`/rules`로 복사), `Makefile`

**Interfaces:**
- Consumes: Task 8의 세 스캐너. `rules/`에는 규칙 파일만 두고 Go 코드는 두지 않는다. 테스트는 저장소 루트의 `rules/`를 상대 경로로 읽는다.
- Produces (`internal/ruleset`): `func Version(dir string) (string, error)`. 규칙 파일들의 이름과 내용으로 계산한 SHA-256의 앞 12자다. 스캔 기록의 `RulesVersion`이 된다.

처음 넣는 규칙:

| 도구 | 규칙 | 분류, 종류 |
|---|---|---|
| betterleaks | 기본 규칙 전체(`[extend] useDefault = true`) | `other-secret` (개인키는 `ssh-credential`) |
| betterleaks | `customer-phone`, `customer-rrn`, `customer-email`, `customer-card`, `customer-address` | `customer-data`와 각 종류. 검토 항목 11의 경로에만 적용 |
| betterleaks | `partner-session-cookie`(`JSESSIONID=`, `Cookie:` 머리말의 값), `partner-bearer-token` | `partner-session`, `token` |
| opengrep | Spring XML `<property name="password|username|url" value="…"/>`(dataSource bean) | `storage-credential` |
| opengrep | `.properties`와 `application*.yml`의 `*.password`, `*.username`, `jdbc*.url`, `*.host` | `storage-credential` |
| opengrep | Java: 이름에 `passw`, `secret`, `token`이 든 변수에 문자열 리터럴 대입, `setPassword("…")` | `other-secret`, `password` |
| opengrep | shell: `sshpass -p`, `ssh`와 `scp`의 `user@host`, `-i <키 파일>` | `ssh-credential` |
| opengrep | `settings.xml`의 `<server>` 안 `<password>`, `pom.xml`과 `build.xml`의 비밀번호 속성 | `storage-credential` |

`customer-address`는 `시|도`, `구|군`, `동|읍|면|로|길`이 차례로 나오는 한 줄을 찾는다. 오탐과 누락이 많을 것을 전제로 한다(설계 5절).

예제 소스 `legacy-shop`은 설계 3절 "탐지 대상 파일" 표의 종류마다 파일이 하나 이상 있다(JSP의 토큰은 betterleaks 기본 규칙이 찾는다): `.java`, Spring XML, `.properties`, `application-prod.yml`, `pom.xml`, `settings.xml`, `build.xml`, `deploy.sh`, 개인키 파일, JSP, 로그, 샘플 CSV. Spring XML 하나와 로그 하나는 EUC-KR과 CRLF로 저장한다. `test/` 폴더와 `.gitignore`에 적힌 폴더에도 값을 하나씩 둔다. `expected.json`은 `{file, line, category, kind}`의 목록이다.

- [ ] **Step 1: "구현 전에 확인할 사실"의 Task 9 항목을 확인한다.**
- [ ] **Step 2: 실패하는 테스트 작성** — 스캐너 바이너리(`BETTERLEAKS_BIN`, `OPENGREP_BIN`, 없으면 저장소의 `bin/`)가 없으면 `t.Skip`한다.

| 테스트 | 기대 |
|---|---|
| `TestRules_ExpectedFindings` | `legacy-shop`을 세 스캐너로 스캔한 결과의 `{file, line, category, kind}` 집합이 `expected.json`과 같다. 차이는 `cmp.Diff`로 보인다. EUC-KR 파일은 Task 6으로 UTF-8로 바꾼 사본을 스캔한다 |
| `TestRules_IgnoredAndTestDirsAreScanned` | `test/` 아래와 `.gitignore` 대상 폴더의 값이 두 도구 모두에서 나온다 |
| `TestRules_CustomerDataOnlyInDataFiles` | `.java`의 이메일 주소는 고객정보로 나오지 않는다 |
| `TestRules_EveryRuleHasCategory` | 직접 쓴 규칙마다 분류와 종류가 있고 `ParseCategory`를 통과한다(바이너리 없이 규칙 파일만 읽는다) |
| `TestVersion_ChangesWithContent` | 규칙 파일의 한 글자를 바꾸면 버전이 달라지고, 파일 순서와는 무관하다 |

- [ ] **Step 3: 실패 확인** — `make scanners` 뒤 `make test-integration`
- [ ] **Step 4: 규칙과 예제 소스를 쓴다.**
- [ ] **Step 5: 통과 확인** — `make check`, `make test-integration`
- [ ] **Step 6: 커밋** — `feat(rules): detect credentials and customer data in legacy sources`

---

### Task 10: 스캔 조립과 저장

**Files:**
- Create: `internal/db/finding.go`, `finding_test.go`, `internal/db/migrations/0003_finding.sql`
- Modify: `internal/scan/runner.go`, `runner_test.go`, `internal/db/scan.go`(`FinishEmptyScan` 삭제), `cmd/worker/main.go`, `config.go`, `compose.yaml`

**Interfaces:**
- Consumes: Task 5~9
- Produces (`internal/scan`):
  - `type Fingerprinter interface { Fingerprints(ctx, values [][]byte) ([]string, error) }`
  - `type Originals interface { PutPending(…); ListPending(…); DeletePending(…) }`(Task 5의 시그니처)
  - `type Result struct { CommitHash, RulesVersion string; ToolVersions map[string]string; Files, Skipped, Dropped int; Findings []finding.Finding }`
  - `func NewRunner(store Store, fp Fingerprinter, originals Originals, scanners []Scanner, opts Options) *Runner`. `Options`는 작업 루트, 규칙 버전, 임시 폴더, 파일 크기 한도(20MiB), 제한 시간(30분)이다.
- Produces (`internal/db`):
  - `SaveResult(ctx, jobID int64, workerID string, id scan.ID, r scan.Result) error`. 한 트랜잭션에서 임대를 확인하고, 항목을 "프로젝트, 지문, 파일"로 upsert하고, 위치를 넣고, 스캔을 `succeeded`로 바꾼다. 임대를 잃었으면 `ErrLeaseLost`이고 아무것도 저장되지 않는다.
  - `FindingsByScan(ctx, scan.ID) ([]finding.Finding, error)`

표: `finding(id, project_id, fingerprint, file_path, category, kind, preview, has_original, first_scan_id, last_scan_id)`에 `(project_id, fingerprint, file_path)` 유일. `location(id, finding_id, scan_id, start_line, start_col, end_line, end_col, context text[], context_first_line, file_hash, detections jsonb)`.

`Handle`의 순서:

1. `StartScan`, 프로젝트 조회, `project.Resolve`.
2. `git -c safe.directory=* -C <dir> rev-parse HEAD`로 커밋 해시를 읽는다. git 저장소가 아니면 빈 문자열이다.
3. `textfile.Walk`로 파일 목록을 만들고 파일마다 `textfile.Read`한다. 읽지 못한 파일은 `Skipped`에 센다. EUC-KR 파일은 `Text`를 `<임시 폴더>/<스캔 ID>/<상대 경로>`에 쓴다.
4. 스캐너마다 프로젝트 폴더와 사본 폴더를 스캔한다. 파일 목록에 없는 파일의 결과는 버린다.
5. `Raw`마다 `Text`의 구간을 정한다. `Offset`이 -1이면 `File.Offset`으로 구하고, 그 자리의 내용이 `Value`와 같은지 확인한다. 다르면 버리고 `Dropped`에 센다. `key-file`의 값은 파일의 원래 바이트 전체다.
6. 값 전체를 `Fingerprints`로 보낸다.
7. "파일, 시작 위치, 지문"이 같은 `Raw`를 위치 하나로 합치고 `Detections`를 모은다. "지문, 파일"이 같은 위치를 항목 하나로 모은다.
8. 미리보기와 주변 줄을 만든다. 주변 줄을 가릴 때는 그 파일의 모든 구간을 쓴다. 고객정보는 주변 줄을 만들지 않는다.
9. 고객정보가 아닌 항목의 값을 지문별로 한 번씩 `PutPending`한다.
10. `SaveResult`.
11. `ListPending`에서 이번 결과에 없는 지문을 `DeletePending`한다. 이 단계의 실패는 로그에만 남기고 스캔은 성공으로 둔다. 다음 스캔이 다시 정리한다.
12. 성공하든 실패하든 사본 폴더를 지운다.

분류가 서로 다른 규칙이 같은 위치의 같은 값을 잡으면 `storage-credential`, `ssh-credential`, `partner-session`, `customer-data`, `other-secret` 순으로 앞의 것을 쓴다.

- [ ] **Step 1: 실패하는 테스트 작성** (fake `Scanner`, `Fingerprinter`, `Originals`, `Store`. 예제 폴더는 `t.TempDir`)

| 테스트 | 기대 |
|---|---|
| `TestHandle_MergesTools` | 두 도구가 같은 값을 잡으면 항목 하나, 위치 하나, `Detections` 둘 |
| `TestHandle_SameValueTwoFiles` | 지문이 같은 항목 둘(파일별) |
| `TestHandle_SameValueTwiceInFile` | 항목 하나, 위치 둘 |
| `TestHandle_ValueMismatchDropped` | 위치의 내용이 `Value`와 다르면 저장하지 않고 `Dropped == 1` |
| `TestHandle_CRLFAndEUCKR` | CRLF이면서 EUC-KR인 파일에서 한글 뒤에 있는 값의 줄과 열이 맞고, 주변 줄이 올바른 한글이다 |
| `TestHandle_EUCKRCopyRemoved` | 성공, 스캐너 실패, `ctx` 취소 뒤 모두 임시 폴더가 비어 있다 |
| `TestHandle_CustomerDataHasNoContextNoOriginal` | 고객정보 항목은 `Context`가 비고 `HasOriginal`이 false이며 `PutPending`이 불리지 않는다. 미리보기에 원래 숫자가 없다 |
| `TestHandle_ContextMasksOtherFindings` | 비밀번호 줄의 주변 줄에 있는 다른 항목의 값이 가려진다 |
| `TestHandle_KeyFile` | 항목 하나, 미리보기 `****`, `PutPending`의 값이 파일의 원래 바이트다 |
| `TestHandle_FingerprintFails` | 스캔은 `failed`이고 `SaveResult`와 `PutPending`이 불리지 않는다. 사유에 값이 없다 |
| `TestHandle_PutPendingFails` | 스캔은 `failed`이고 `SaveResult`가 불리지 않는다 |
| `TestHandle_ScannerFails` | 스캔은 `failed`, 사유는 `betterleaks could not finish; see the worker log` |
| `TestHandle_DeletesStalePending` | `ListPending`에만 있는 지문이 `DeletePending`된다. 삭제가 실패해도 스캔은 `succeeded` |
| `TestHandle_SkippedFiles` | 20MiB를 넘는 파일, 바이너리, UTF-16 파일이 `Skipped`에 세어지고 스캔은 성공한다 |
| `TestHandle_ResultOutsideFileListIgnored` | 스캐너가 제외 경로나 심볼릭 링크 너머의 파일을 보고하면 버린다 |
| `TestHandle_NoRawValueInLogs` | 위 경우들을 도는 동안 로그 버퍼 전체에 예제 값이 한 번도 나오지 않는다 |
| `TestSaveResult_SecondScanKeepsFinding`(db) | 같은 "프로젝트, 지문, 파일"은 두 번째 스캔에서 같은 `finding.id`이고 `last_scan_id`만 바뀐다. 줄 번호가 달라져도 같다 |
| `TestSaveResult_LeaseLost`(db) | 임대를 잃은 worker의 저장은 `ErrLeaseLost`이고 `finding`, `location`에 줄이 없다 |
| `TestSaveResult_Twice`(db) | 같은 스캔을 두 번 저장하려 하면 두 번째는 `ErrLeaseLost`다(작업이 이미 `done`) |

- [ ] **Step 2: 실패 확인**
- [ ] **Step 3: 구현**
- [ ] **Step 4: 통과 확인** — `make check`, `make test-integration`
- [ ] **Step 5: 커밋** — `feat(scan): store findings without their values`

---

### Task 11: 탐지 목록 화면과 실제 확인

**Files:**
- Modify: `internal/web/handlers_scan.go`, `handlers_scan_test.go`, `templates/scan.html`, `templates/project.html`, `fakes_test.go`, `docs/local-run.md`, `docs/next-steps.md`, `CHANGELOG.md`

**Interfaces:**
- Consumes: `db.FindingsByScan`
- `GET /scans/{id}`가 보여주는 것:
  - 상태, 요청한 사람과 시각, 커밋 해시, 도구 버전, 규칙 버전, 읽은 파일 수, 건너뛴 파일 수, 위치가 맞지 않아 버린 건수
  - 분류별 건수
  - 비밀 값 항목: 분류, 종류, 파일과 줄, 미리보기, 탐지한 도구와 규칙, 가린 주변 줄
  - 고객정보: 파일과 종류별로 한 줄. 건수와 첫 줄 번호, 형식 미리보기만 보인다
- `GET /projects/{id}`에 스캔 목록(시각, 상태, 항목 수)이 보인다.

- [ ] **Step 1: 실패하는 테스트 작성**

| 테스트 | 기대 |
|---|---|
| `TestScan_ListsFindings` | 항목의 파일, 줄, 미리보기, 규칙이 보인다 |
| `TestScan_GroupsCustomerData` | 같은 파일의 전화번호 37건이 한 줄과 건수 37로 보인다 |
| `TestScan_EscapesContext` | 주변 줄의 `<script>`가 이스케이프된다 |
| `TestScan_ShowsSkippedAndDropped` | 0이 아니면 건수가 보인다 |
| `TestScan_Empty` | 항목이 없으면 "탐지된 항목이 없습니다."가 보인다 |

- [ ] **Step 2: 실패 확인**
- [ ] **Step 3: 구현**
- [ ] **Step 4: 통과 확인** — `make check`, `make test-integration`
- [ ] **Step 5: compose에서 확인** — `internal/ruleset/testdata/legacy-shop`을 복사한 폴더를 `SE_WORKSPACE` 아래에 두고 스캔한다.

| 번호 | 확인 | 기대 |
|---|---|---|
| 1 | 스캔 화면 | `expected.json`의 항목이 모두 보이고 미리보기는 가려져 있다 |
| 2 | `docker compose logs web worker`에서 예제 값을 검색 | 없다 |
| 3 | postgres의 `finding`, `location`, `scan`, `job` 전체에서 예제 값을 검색(`pg_dump`의 결과에 `grep`) | 없다 |
| 4 | worker 컨테이너의 `/tmp` | 스캔이 끝난 뒤 비어 있다 |
| 5 | 예제의 값 하나를 지우고 다시 스캔 | 그 항목이 새 스캔에 없고, 나머지 항목의 `finding.id`는 그대로다 |
| 6 | `docker compose stop openbao` 뒤 스캔 | `failed`와 사유. 항목이 저장되지 않는다 |
| 7 | 전체를 내렸다가 다시 띄운다(`down` 뒤 `up`, `-v` 없이) | OpenBao가 봉인이 풀린 채 뜨고, 다시 스캔하면 지문이 이전과 같아 `finding.id`가 유지된다 |
| 8 | `dev-user`로 스캔 화면 | 원본 값을 볼 방법이 없다 |

- [ ] **Step 6: 커밋과 PR** — `feat(web): list the findings of a scan`. `docs/next-steps.md`의 계획 3을 "구현 완료, PR 검토 대기"로 바꾸고 PR 2를 올린다.
