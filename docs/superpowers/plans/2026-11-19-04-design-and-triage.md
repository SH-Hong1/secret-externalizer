# 화면 디자인 시안과 판정 화면 Implementation Plan (계획 4/7)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 확인받은 시안대로 화면에 스타일이 입혀지고, 로그인한 사용자가 프로젝트의 마지막 스캔이 찾은 값을 묶음 단위로 보면서 "외부화, 삭제·마스킹, 오탐, 보류"를 판정하고 placeholder 이름을 정하며, `admin`은 원본을 볼 수 있고 그 모든 일이 감사 로그에 남는다.

**Architecture:** 판정과 이름의 단위는 묶음(`bundle`)이다. 묶음은 한 프로젝트 안에서 지문이 같은 항목(`finding`)의 모음이고, worker가 스캔 결과를 저장할 때 규칙에 따라 새 항목을 묶음에 넣는다. web은 프로젝트의 마지막으로 성공한 스캔에 나온 묶음만 판정 화면에 그리고, 판정, 이름 변경, 묶음 옮기기, 원본 보기를 postgres의 `audit_event`에 같은 트랜잭션으로 남긴다. 화면은 `html/template`이 그린 쪽 전체가 기본이고, htmx는 목록에서 고른 묶음의 상세만 바꿔 끼운다. 스타일은 tailwindcss standalone CLI가 만든 CSS 파일 하나다.

**Tech Stack:** Go 1.27, `html/template`, htmx 2(정적 파일), tailwindcss 4 standalone CLI, postgres 18, OpenBao 2.7(KV v2 읽기), Claude design(Artifact의 Design 유형), frontend-design 스킬

**Spec:** `docs/superpowers/specs/2026-11-18-secret-externalizer-design.md` (6절의 "탐지 항목이 갖는 정보", "스캔 간 항목 식별", "판정", "placeholder", 7절 "인증과 권한", 8절 "판정 화면", 9절 "화면 디자인", 10절 4~5단계). 결정 기록 0003, 0005, 0012, 0021, 0023.

## 검토 때 정해 주실 것

설계 문서가 정하지 않았거나 계획 3이 이 계획으로 넘긴 내용이다. 각 항목의 첫 문장이 이 계획이 따르는 안이다. **[확정]** 은 2026-11-19에 사용자가 제안대로 정한 항목이다.

1. **[확정] 계획은 하나로 두고 PR을 둘로 나눈다.** PR 1은 Task 1~5(시안, 스타일, 접속 정보 나누기, 묶음과 판정 저장, 제안)이고 화면에서 달라지는 것은 스타일뿐이다. PR 2는 Task 6~10(판정 화면, 원본 보기, 감사 로그)이다.
2. **[확정] 이번에 시안을 만드는 화면은 이 계획이 만드는 화면이다.** 공통 틀(머리, 내비게이션), 로그인 전후와 오류, 프로젝트 목록·등록·상세, 스캔, 판정, 감사 로그다. 대시보드와 리포트는 같은 색과 글꼴 체계로 계획 6, 7에서 시안을 만든다. 다른 안: 대시보드와 리포트도 지금 한 장씩 만들어 전체 모양을 한 번에 확인한다.
3. **[확정] 판정 화면은 프로젝트 단위이고, 그 프로젝트의 마지막으로 성공한 스캔에 나온 값만 보여 준다.** 주소는 `/projects/{id}/triage`다. 이전 스캔의 `has_original`은 믿을 수 없고(다음 스캔이 원본을 지운다), 판정은 스캔이 아니라 값에 붙기 때문이다. 스캔 화면에는 요약만 남기고 탐지 목록은 판정 화면으로 옮긴다. 마지막 스캔이 아닌 스캔은 건수만 보인다.
4. **[확정] 묶음을 나누는 단위는 파일이다.** 묶음에서 떼어 낼 수 있는 것은 "한 파일 안의 그 값 전부"다. 스캔 간 식별이 "프로젝트, 지문, 파일 경로"라서 줄 번호가 밀려도 유지되는 가장 작은 단위가 파일이기 때문이다. 한 파일 안에 같은 값이 두 번 나오고 가리키는 대상이 다르면(같은 파일의 포트 `3306` 두 개) 둘은 같은 이름을 받는다. 다른 안: 위치 단위로 나눈다. 스캔 간에 위치를 식별하는 방법(설정 키, 순번)을 따로 만들어야 한다.
5. **[확정] 자동으로 묶는 규칙.** 비밀번호, 토큰, URL, 키처럼 값이 고유한 종류는 지문이 같으면 한 묶음이다. 호스트, 포트, 계정은 자동으로 묶지 않고 파일마다 묶음 하나로 시작하며, 사용자가 판정 화면에서 합친다. 설계는 "설정 키나 문맥이 같을 때만 자동으로 묶는다"고 했지만 스캔이 설정 키를 저장하지 않는다. JSP, JS, HTML 파일의 항목은 다른 파일의 항목과 묶지 않는다(외부화를 고를 수 없으므로). 고객정보는 항목마다 묶음 하나이고, 목록에서는 파일과 종류별로 한 줄로 모아 한 번에 판정한다.
6. **[확정] 접속 정보(JDBC URL, `user@host`)를 구성 요소로 나누는 일을 이 계획에서 한다.** worker가 스캔할 때 URL 하나를 호스트, 포트, 계정, 비밀번호 항목으로 나눠 저장하고, 나누는 데 성공하면 URL 통째 항목은 남기지 않는다. 계획 5로 미루면 이 계획에서 URL 통째에 내린 판정이 나뉘는 순간 모두 사라진다. 종류에 `port`를 더한다. DB 이름과 SID는 항목으로 만들지 않는다. 다른 안: 계획 5의 첫 Task로 미루고, 그때까지 URL 항목은 판정할 수 없게 막는다.
7. **[확정] placeholder 이름은 접두어 없이 한 가지 형태로 저장한다.** 저장하는 형태는 `db.main.password`(소문자와 숫자로 된 마디 2~5개를 `.`으로 이은 것, 100자 이하)이고, 화면과 이후의 적용은 파일 종류에 따라 `ext.db.main.password`(설정, XML, Java)와 `EXT_DB_MAIN_PASSWORD`(shell)로 바꿔 쓴다. 한 묶음이 `.yml`과 `.sh`에 걸쳐 있어도 이름은 하나다. 이름은 프로젝트 안에서 묶음마다 달라야 한다.
8. **[확정] 이름 제안은 `<분류>.<대상>.<항목>`이다.** 분류는 `db`, `ssh`, `partner`, `secret`, 항목은 종류(`password`, `token`, `account`, `host`, `port`, `url`, `key`)다. 대상은 주변 줄의 탐지된 줄에서 설정 키나 변수 이름을 읽어, 분류와 항목을 뜻하는 낱말을 뺀 나머지로 만든다(`order.datasource.password` → `order`). 남는 것이 없거나 주변 줄이 없으면 `main`이고, 이미 쓰인 이름이면 `main2`, `main3`으로 올린다. 제안은 입력란에 미리 채워질 뿐이고 사용자가 판정 버튼을 눌러야 저장된다.
9. **[확정] 과거 판정 제안.** 같은 지문의 다른 묶음(다른 프로젝트 포함)에 판정이 있으면 가장 최근 것을 "제안"으로 표시해 미리 고른다. 이것도 버튼을 눌러야 저장된다.
10. **[확정] 보류 사유는 세 가지에서 고른다.** "구조 변경 필요", "확인 필요", "나중에 처리"다. 모든 판정에 메모(500자 이하, 선택)를 적을 수 있다. 판정은 적용 전까지 바꾸거나 취소(미판정으로)할 수 있다.
11. **[확정] 분류별로 고를 수 있는 판정.** 비밀 값은 외부화, 오탐, 보류다. 고객정보는 삭제·마스킹, 오탐, 보류다. JSP, JS, HTML의 비밀 값은 오탐, 보류만이고 "보류: 구조 변경 필요"가 미리 골라져 있다.
12. **[확정] 주변 줄은 지금처럼 가린 채로 보여 주고 더 가리지 않는다.** 코드를 보면서 판정하는 것이 이 화면의 목적이다. 알고 받아들이는 위험: 어떤 규칙도 잡지 못한 텍스트(규칙이 놓친 형태의 비밀번호, 주석 속 연락처)가 소스 파일의 주변 줄로 postgres에 남고 `user` 역할에게 보인다. 로그, 샘플 데이터, 고객정보가 나온 파일에는 이미 주변 줄이 없다. 결정 기록으로 남긴다. 다른 안: 탐지된 줄 하나만 저장한다.
13. **[확정] 원본 보기는 `admin`의 응답에만 값을 쓴다.** `AGENTS.md` Security 절의 "비밀 값은 template에 나오지 않는다"에 예외를 하나 더한다. 원본 보기 요청의 응답 한 곳이다. 순서는 감사 기록 저장, OpenBao 읽기, 응답이고, 감사 기록을 저장하지 못하면 값을 읽지 않는다. 요청은 `POST`다(기록을 남기는 요청이고, 미리 불러오기나 캐시의 대상이 되지 않게 한다). 텍스트가 아닌 원본(키 저장소)은 크기만 보여 준다. 결정 기록으로 남긴다.
14. **[확정] 오탐으로 판정한 값의 임시 원본을 지우는 일은 계획 5에서 한다.** web은 OpenBao에 읽기 권한만 있고, 지우는 쪽은 worker다. 계획 5가 만드는 적용 작업이 OpenBao의 임시 경로를 정리할 때 함께 한다.
15. **[확정] 건너뛴 파일의 이름을 스캔 화면에 보여 준다.** 스캔 기록에 건너뛴 파일의 경로(처음 200개)와 도구별 스캐너 오류 건수를 저장하고, 스캔 화면에서 펼쳐 볼 수 있게 한다. 무엇이 검사되지 않았는지 모르면 "남은 항목 0"을 믿을 수 없다. 경로는 비밀 값이 아니다.
16. **[확정] 만든 CSS 파일을 저장소에 넣는다.** `make css`가 `internal/web/static/app.css`를 만들고, `make check`가 다시 만들어 본 결과와 같은지 확인한다. `go build`와 web 이미지 빌드는 지금 그대로다. 다른 안: 이미지 빌드 단계에서 CLI를 받아 만든다.
17. **[확정] 글꼴은 시안에서 정한다.** 웹 글꼴을 쓰면 OFL 등 재배포가 허용된 것만 정적 파일로 넣고(CDN 없음) 결정 기록에 라이선스를 적는다.

## 구현 전에 확인할 사실

실행해 보지 않았거나 공식 문서로 확인하지 못한 내용이다. 해당 Task의 첫 단계에서 확인하고, 다르면 계획을 고친 뒤 진행한다.

| 내용 | Task |
|---|---|
| tailwindcss 4 standalone CLI의 배포 파일 이름과 체크섬 파일, `--input`/`--output`/`--minify` 옵션, 템플릿 경로를 CSS의 `@source`로 지정하는 방법 | 2 |
| htmx 2가 지금의 CSP(`default-src 'self'`)에서 동작한다. `htmx-config`의 `includeIndicatorStyles: false`, `allowEval: false`가 인라인 스타일과 `eval`을 끈다 | 2 |
| `rules/opengrep/shell.yml`이 `user@host`를 어떤 종류(`kind`)의 값 하나로 보고하는지, JDBC URL 규칙이 URL 전체를 범위로 보고하는지 | 3 |
| betterleaks가 JDBC URL 안의 비밀번호를 따로 보고하는 경우, 나눈 비밀번호 항목과 "파일, 시작 위치, 지문"이 같아 한 위치로 합쳐진다 | 3 |
| web AppRole의 정책(`secret/data/pending/*` 읽기)으로 KV v2의 값을 읽을 수 있고, 없는 경로는 404로 온다 | 8 |

## Global Constraints

- `AGENTS.md`의 모든 규칙을 따른다. 작업 하나를 끝낼 때마다 `make check`가 통과해야 한다.
- 코드, 주석, 커밋 메시지는 영문, `CHANGELOG.md`와 `docs/` 아래는 한글로 쓴다.
- 의존성과 정적 자원(htmx, 글꼴)을 더할 때마다 `docs/decisions/`에 결정 기록을 쓴다(0025부터). 버전은 구현 시점의 최신 안정 버전을 확인해 `Makefile`에 체크섬과 함께 고정한다.
- 화면 구현은 Task 1에서 확인받은 시안을 따르고 frontend-design 스킬로 한다. 시안에 없는 화면 요소를 새로 만들 때는 시안의 색, 글꼴, 간격을 쓴다.
- 판정되지 않은 묶음은 어떤 경로로도 판정된 상태가 되지 않는다. 제안은 저장하지 않는다.
- 원본 값은 postgres, valkey, 로그, 오류 메시지, 감사 로그에 남지 않는다. 화면에 쓰는 곳은 원본 보기 응답 하나다.
- 고객정보는 종류, 파일, 줄 번호, 같은 파일 안의 건수, 형식 미리보기만 보여 준다. 원본 보기 대상이 아니다.
- 원본 보기는 `admin`만 한다. 볼 때마다 OpenBao에서 읽고 web에 캐시하지 않는다.
- 판정, 이름 변경, 묶음 옮기기, 원본 보기는 사용자와 시각과 함께 `audit_event`에 남는다.
- 상태를 바꾸는 요청은 `POST`이고 지금의 cross-origin 보호를 지난다. 모든 응답은 지금처럼 `Cache-Control: no-store`이고, 정적 파일만 예외다.
- CDN을 쓰지 않는다. CSP `default-src 'self'`를 넓히지 않는다. 인라인 스크립트와 인라인 스타일을 쓰지 않는다.
- 키보드 단축키를 두지 않는다. 목록은 눌러서 고르고 판정은 버튼으로 한다.
- htmx가 없어도(스크립트를 끈 브라우저) 모든 동작이 쪽 전체 새로 고침으로 된다.
- 병합된 마이그레이션 파일(`0001`~`0003`)은 고치지 않는다.
- 테스트 예제 값은 가짜임이 분명해야 한다(`dev-only-fake-...`, `010-0000-0000`, `db.invalid`).

## Review Focus

1. 주소에 다른 프로젝트의 묶음 ID나 항목 ID를 넣은 요청은 404이고 아무것도 바뀌지 않는다. (Task 4, 7)
2. 허용되지 않는 조합(고객정보에 외부화, JSP 항목에 외부화, 이름 없는 외부화, 이미 쓰인 이름, 형식이 틀린 이름, 사유 없는 보류)은 400이고 아무것도 저장되지 않으며 감사 기록도 없다. (Task 4, 7)
3. 판정한 뒤 다시 스캔해도 판정과 이름이 남는다. 새로 나타난 파일의 같은 값은 규칙에 따라 기존 묶음에 들어가고 그 묶음의 판정을 따른다. 사용자가 떼어 낸 항목은 다시 스캔해도 원래 묶음으로 돌아가지 않는다. (Task 4)
4. 원본 보기: `user` 역할은 403이다. 감사 기록 저장이 실패하면 OpenBao를 읽지 않는다. OpenBao에 닿지 않거나 원본이 없으면 값 없이 안내만 보인다. 값은 web 로그 어디에도 없다. (Task 8)
5. 묶음의 마지막 항목을 옮기면 빈 묶음이 사라지고 그 이름을 다시 쓸 수 있다. 지문이 다른 묶음으로는 옮길 수 없다. (Task 4, 7)
6. 성공한 스캔이 하나도 없는 프로젝트의 판정 화면은 오류가 아니라 안내를 보인다. 범위 밖 쪽 번호는 마지막 쪽을, 모르는 필터 값은 400을 준다. (Task 6)
7. 나누지 못하는 URL(모르는 형식, 호스트가 `${...}`인 것)은 통째로 한 항목으로 남는다. 나눈 구성 요소의 줄과 열이 EUC-KR 파일과 CRLF 파일에서 맞는다. (Task 3)
8. 묶음이 수천 개여도 판정 화면이 한 쪽에 50줄만 그린다. (Task 6)

## File Structure

| 파일 | 책임 |
|---|---|
| `docs/design/` | 확인받은 시안의 HTML과 색·글꼴·간격 표 (`README.md`) |
| `internal/web/static/` | `app.css`(만든 파일), `htmx.min.js`, 글꼴 |
| `internal/web/assets/app.css` | tailwind 입력 파일 (테마 값, 구성 요소 클래스) |
| `internal/web/static.go` | 정적 파일 handler, `asset` 템플릿 함수 |
| `internal/connstr/connstr.go` | 접속 문자열을 구성 요소의 범위로 나눈다 |
| `internal/triage/triage.go` | 묶음, 판정, 보류 사유, 허용 규칙 |
| `internal/triage/name.go` | placeholder 이름의 형식과 두 가지 표기 |
| `internal/triage/suggest.go` | 이름 제안 |
| `internal/audit/audit.go` | 감사 기록의 타입 |
| `internal/db/migrations/0004_port_kind.sql` | 종류에 `port` 추가 |
| `internal/db/migrations/0005_bundle_and_audit.sql` | `bundle`, `finding.bundle_id`, `audit_event`, 기존 항목의 묶음 만들기 |
| `internal/db/migrations/0006_scan_skips.sql` | `scan.skipped_files`, `scan.scanner_errors` |
| `internal/db/bundle.go`, `internal/db/audit.go` | 묶음과 감사 기록의 SQL |
| `internal/web/handlers_triage.go`, `triageview.go` | 판정 화면의 handler와 화면용 값 |
| `internal/web/handlers_original.go` | 원본 보기 |
| `internal/web/templates/triage.html`, `original.html` | 판정 화면, 원본 보기 조각 |
| 고치는 파일 | `internal/finding/finding.go`(ID, `KindPort`), `internal/scan/`(나누기, 건너뛴 파일), `internal/db/finding.go`(묶음 넣기), `internal/bao/client.go`(읽기), `internal/web/`의 `routes.go`·`server.go`·`render.go`·`scanview.go`·모든 템플릿, `cmd/web/`, `compose.yaml`, `Makefile`, `AGENTS.md`, `CHANGELOG.md`, `docs/local-run.md`, `docs/next-steps.md` |

---

## PR 1: 시안, 스타일, 판정의 자료

### Task 1: 화면 시안

코드를 쓰지 않는다. 이 Task가 끝나야 Task 2와 Task 6 이후를 시작한다. Task 3~5는 시안과 상관없어 시안 검토를 기다리는 동안 진행할 수 있다.

**Files:**
- Create: `docs/design/README.md`, `docs/design/*.html`

- [ ] **Step 1:** `.superpowers/brainstorm/` 아래의 배치 시안 `triage-layout.html`(B안: 목록 + 상세)과 설계 8절을 읽는다. 배치는 이미 확정이고, 이 Task가 정하는 것은 시각 디자인이다.
- [ ] **Step 2:** Claude design으로 다음 화면의 시안을 만든다. 값은 모두 가짜이고 가린 형태다.
  - 공통 틀: 머리(도구 이름, 프로젝트·감사 로그 내비게이션, 사용자 이름과 역할, 로그아웃), 오류 화면, 역할 없음 화면
  - 프로젝트 목록, 등록 양식(검증 오류가 보이는 상태 포함), 프로젝트 상세(스캔 목록, 스캔 시작, 판정 화면으로 가기)
  - 스캔: 진행 중, 성공(요약, 건너뛴 파일 펼침), 실패
  - 판정 화면의 상태 다섯 가지: 비밀 값 묶음(주변 줄, 같은 값의 다른 위치, 이름 입력, 판정 버튼), 고객정보 줄(파일과 종류, 건수, 줄 번호), JSP 항목(외부화 없음), 과거 판정 제안이 있는 묶음, 원본을 펼친 `admin`의 화면
  - 판정 화면의 빈 상태: 스캔 없음, 필터 결과 없음, 모두 판정함
  - 감사 로그
- [ ] **Step 3:** 사용자에게 시안의 주소를 주고 확인받는다. 고칠 점을 받아 고치고 다시 확인받는다.
- [ ] **Step 4:** 확인받은 시안의 HTML을 `docs/design/`에 저장하고, `README.md`에 색(역할별 이름과 값), 글꼴, 글자 크기 단계, 간격 단계, 판정 네 가지와 미판정의 색 표시를 표로 적는다. 글꼴을 정했으면 결정 기록을 쓴다.
- [ ] **Step 5:** 커밋한다. `docs: add the approved screen designs`

### Task 2: 정적 자원과 공통 틀

**Files:**
- Create: `internal/web/static.go`, `internal/web/static_test.go`, `internal/web/assets/app.css`, `internal/web/static/app.css`, `internal/web/static/htmx.min.js`, `docs/decisions/0025-tailwind-and-htmx.md`
- Modify: `Makefile`, `internal/web/render.go`, `internal/web/routes.go`, `internal/web/middleware.go`, `internal/web/templates/*.html`, `internal/web/scanview.go`, `CHANGELOG.md`

**Interfaces:**
- Produces: `GET /static/{file}`; 템플릿 함수 `asset(name string) string`(`/static/app.css?v=<SHA-256의 앞 8자리>`); 템플릿 블록 `layout`이 머리와 내비게이션을 그리고, 쪽 템플릿은 지금처럼 `title`, `head`, `body`를 정의한다. 로그인한 쪽의 데이터는 `User auth.User` 필드를 갖는다.

- [ ] **Step 1:** "구현 전에 확인할 사실"의 Task 2 항목 두 개를 확인한다. `Makefile`에 `TAILWIND_VERSION`, `HTMX_VERSION`과 체크섬을 고정하고, `make tools`가 `bin/tailwindcss`를 받아 체크섬을 확인하게 한다. `make css`(만들기), `make css-check`(다시 만들어 저장소의 파일과 비교)를 더하고 `check`에 `css-check`를 넣는다.
- [ ] **Step 2: 실패하는 테스트를 쓴다.** `static_test.go`:
  - `TestStatic`: `GET /static/app.css`는 200, `Content-Type: text/css; charset=utf-8`, `Cache-Control: public, max-age=31536000, immutable`. `GET /static/nope.css`와 `GET /static/../render.go`는 404. 로그인 없이 받는다.
  - `TestAsset`: `asset("app.css")`의 결과가 `/static/app.css?v=`로 시작하고 뒤가 16진수 8자리다. 없는 이름은 템플릿 실행 오류다.
  - `TestLayoutNavigation`: 로그인한 `dev-user`의 `/projects` 응답에 사용자 이름, "프로젝트" 링크, 로그아웃 양식이 있고 "감사 로그" 링크가 없다. `dev-admin`의 응답에는 있다.
  - `TestNoInlineScriptOrStyle`: 모든 템플릿 파일에 `<script>` 본문, `style=`, `<style`, `on[a-z]+=` 속성이 없다.
- [ ] **Step 3:** 테스트가 실패하는지 확인한다. `go test ./internal/web/ -run 'TestStatic|TestAsset|TestLayout|TestNoInline'`
- [ ] **Step 4:** `static.go`를 쓴다. `//go:embed static`으로 넣고, `handleStatic() http.Handler`가 파일 이름 하나만 받는다. `securityHeaders`의 `no-store`는 이 handler가 덮어쓴다. `parseTemplates`는 `template.New(...).Funcs`로 `asset`을 넣는다.
- [ ] **Step 5:** `layout.html`에 `<link rel="stylesheet">`, `<script src defer>`, `<meta name="htmx-config">`, 머리와 내비게이션을 넣고, 모든 쪽 템플릿을 시안대로 다시 쓴다(frontend-design 스킬). `assets/app.css`에 `docs/design/README.md`의 값을 테마로 옮긴다. `make css`로 `static/app.css`를 만든다.
- [ ] **Step 6:** `make check`가 통과하는지 확인한다. `docker compose up --build`로 띄워 Chromium에서 로그인, 프로젝트 목록, 등록, 스캔 화면이 시안과 같은지 보고, 콘솔에 CSP 위반이 없는지 확인한다.
- [ ] **Step 7:** `CHANGELOG.md`와 결정 기록 0025(standalone CLI, 만든 CSS를 저장소에 두는 이유, htmx를 쓰는 범위)를 쓰고 커밋한다. `feat(web): add the stylesheet, htmx and the shared page frame`

### Task 3: 접속 정보를 구성 요소로 나누기

**Files:**
- Create: `internal/connstr/connstr.go`, `internal/connstr/connstr_test.go`, `internal/db/migrations/0004_port_kind.sql`, `docs/decisions/0026-connection-string-parts.md`
- Modify: `internal/finding/finding.go`(`KindPort`, `ParseKind`, `secretPreview`), `internal/scan/locate.go` 또는 `merge.go`, `internal/web/scanview.go`(`kindLabel`), `rules/` 아래 규칙의 테스트 예제, `CHANGELOG.md`

**Interfaces:**
- Produces:

  ```go
  // Part is one component of a connection string. Span is a byte range in
  // the string that was split.
  type Part struct {
  	Kind finding.Kind // KindHost, KindPort, KindAccount, KindPassword
  	Span finding.Span
  }

  // Split returns the components of value, in order of position. It returns
  // nil when value is not a connection string it knows or has no component.
  func Split(value []byte) []Part
  ```

- [ ] **Step 1:** "구현 전에 확인할 사실"의 Task 3 항목 두 개를 규칙 파일과 실제 스캔으로 확인한다.
- [ ] **Step 2: 실패하는 테스트를 쓴다.** `TestSplit`은 표 형태이고 각 줄은 입력과 기대하는 `(종류, 잘라 낸 문자열)` 목록이다.

  | 입력 | 결과 |
  |---|---|
  | `jdbc:mysql://db.invalid:3306/shop` | host `db.invalid`, port `3306` |
  | `jdbc:mysql://db.invalid/shop?user=shop_app&password=dev-only-fake-pw` | host, account `shop_app`, password `dev-only-fake-pw` |
  | `jdbc:mysql://shop_app:dev-only-fake-pw@db.invalid:3306/shop` | account, password, host, port |
  | `jdbc:oracle:thin:@db.invalid:1521:ORCL` | host, port |
  | `jdbc:oracle:thin:shop_app/dev-only-fake-pw@db.invalid:1521:ORCL` | account, password, host, port |
  | `jdbc:oracle:thin:@//db.invalid:1521/svc` | host, port |
  | `jdbc:sqlserver://db.invalid:1433;databaseName=shop;user=sa;password=dev-only-fake-pw` | host, port, account, password |
  | `jdbc:postgresql://[::1]:5432/shop` | host `::1`, port |
  | `deploy@host.invalid` | account `deploy`, host `host.invalid` |
  | `deploy@host.invalid:2222` | account, host, port |
  | `deploy@host.invalid:/srv/app` | account, host |
  | `jdbc:mysql://${db.host}:${db.port}/shop` | 없음 |
  | `jdbc:mysql://${db.host}:3306/shop` | port만 |
  | `jdbc:h2:mem:test`, `not a url`, 빈 값 | 없음 |

  모든 결과에서 `value[Span.Start:Span.End]`가 기대한 문자열과 같고, 범위가 겹치지 않는다. `FuzzSplit`은 어떤 입력에도 패닉이 없고 범위가 입력 안에 있으며 UTF-8 글자 경계에 있음을 확인한다.
- [ ] **Step 3:** 실패를 확인하고 `Split`을 쓴다. 표준 라이브러리만 쓴다. `${`로 시작하는 구성 요소는 결과에서 뺀다.
- [ ] **Step 4: 러너의 실패하는 테스트를 쓴다.** `internal/scan`의 기존 러너 테스트에 더한다.
  - `application.yml`에 `url: jdbc:mysql://shop_app:dev-only-fake-pw@db.invalid:3306/shop`가 있으면 결과에 종류 `url`인 항목이 없고 `account`, `password`, `host`, `port` 항목이 그 파일에 하나씩 있으며, 각 위치의 시작 열이 원문에서 그 구성 요소가 시작하는 열이다.
  - 같은 내용의 EUC-KR 파일(앞줄에 한글 주석)과 CRLF 파일에서도 줄과 열이 같다.
  - `jdbc:h2:mem:test`는 종류 `url`인 항목 하나로 남는다.
  - 네 항목의 탐지 규칙(`Detections`)은 URL을 보고한 규칙이다.
  - 주변 줄에서 네 값이 모두 가려진다.
- [ ] **Step 5:** 값의 자리를 정한 뒤, 지문을 만들기 전에 종류가 `url`이거나 Step 1에서 확인한 ssh 대상 종류인 결과를 `connstr.Split`으로 나눈다. 나뉘면 원래 결과를 구성 요소 결과로 바꾸고(분류와 규칙은 그대로), 나뉘지 않으면 그대로 둔다. `0004_port_kind.sql`은 `finding`의 `kind` CHECK 제약을 `port`를 더한 것으로 바꾼다. `Preview`는 `port`를 다른 비밀 값 종류와 같이 다룬다.
- [ ] **Step 6:** `make check`와 `make test-integration`이 통과하는지 확인한다.
- [ ] **Step 7:** `CHANGELOG.md`와 결정 기록 0026(나누는 형식, 통째 항목을 남기지 않는 이유, DB 이름을 빼는 이유)을 쓰고 커밋한다. `feat(scan): split connection strings into their components`

### Task 4: 묶음과 판정 저장

**Files:**
- Create: `internal/triage/triage.go`, `internal/triage/name.go`, `internal/triage/triage_test.go`, `internal/audit/audit.go`, `internal/db/migrations/0005_bundle_and_audit.sql`, `internal/db/bundle.go`, `internal/db/audit.go`, `internal/db/bundle_test.go`, `docs/decisions/0027-bundle-is-the-unit-of-a-verdict.md`
- Modify: `internal/finding/finding.go`(`type ID int64`, `Finding.ID`), `internal/db/finding.go`(`FindingsByScan`이 ID를 채우고, `insertFinding`이 묶음을 정한다), `CHANGELOG.md`

**Interfaces:**
- Produces (`internal/triage`):

  ```go
  type BundleID int64

  type Verdict string // zero value: not decided
  const (
  	VerdictExternalize   Verdict = "externalize"
  	VerdictRedact        Verdict = "redact"
  	VerdictFalsePositive Verdict = "false-positive"
  	VerdictHold          Verdict = "hold"
  )

  type HoldReason string
  const (
  	HoldStructure HoldReason = "structure-change"
  	HoldConfirm   HoldReason = "needs-confirmation"
  	HoldLater     HoldReason = "later"
  )

  // Name is a placeholder name without its prefix, such as "db.main.password".
  type Name string
  func ParseName(s string) (Name, error) // ErrInvalidName
  func (n Name) Config() string          // "ext.db.main.password"
  func (n Name) Shell() string           // "EXT_DB_MAIN_PASSWORD"

  // Decision is what a user chose for a bundle. The zero Decision clears a verdict.
  type Decision struct {
  	Verdict Verdict
  	Reason  HoldReason
  	Name    Name
  	Note    string
  }

  type Member struct {
  	FindingID   finding.ID
  	File        string
  	HasOriginal bool
  	Locations   []finding.Location
  }

  type Bundle struct {
  	ID          BundleID
  	ProjectID   project.ID
  	Fingerprint string
  	Category    finding.Category
  	Kind        finding.Kind
  	Preview     string
  	Decision    Decision
  	DecidedBy   string
  	DecidedAt   time.Time
  	Members     []Member
  }

  func Frontend(file string) bool                    // .jsp .jspf .js .html .htm, 대소문자 무시
  func SharesBundle(k finding.Kind) bool             // host, port, account와 고객정보 종류는 false
  func (b Bundle) Allowed() []Verdict                // 검토 항목 11
  func (b Bundle) Check(d Decision) error            // ErrNotAllowed
  ```

- Produces (`internal/audit`): `type Actor struct{ Subject, Name string }`, `type Action string`(`verdict`, `move`, `reveal`), `type Event struct{ ID int64; At time.Time; Actor Actor; Action Action; ProjectID project.ID; BundleID triage.BundleID; FindingID finding.ID; Detail map[string]string }`. `Detail`에는 판정, 사유, 이름, 옮긴 곳의 묶음 ID만 들어간다. 메모는 `Detail`에 넣는다(값이 아니라 사용자가 쓴 글이다).
- Produces (`*db.Store`):

  ```go
  // LatestScan returns the project's newest succeeded scan, or ErrNotFound.
  LatestScan(ctx, projectID project.ID) (scan.Scan, error)
  // Entries returns one page of the triage list for scanID.
  Entries(ctx, projectID project.ID, scanID scan.ID, f triage.Filter) (triage.Page, error)
  Bundle(ctx, projectID project.ID, scanID scan.ID, id triage.BundleID) (triage.Bundle, error)
  CustomerGroup(ctx, projectID project.ID, scanID scan.ID, key triage.GroupKey) (triage.Group, error)
  Decide(ctx, projectID project.ID, ids []triage.BundleID, d triage.Decision, by audit.Actor) error
  MoveFinding(ctx, projectID project.ID, id finding.ID, to triage.BundleID, by audit.Actor) (triage.BundleID, error)
  UsedNames(ctx, projectID project.ID) ([]triage.Name, error)
  PastDecision(ctx, fingerprint string, except triage.BundleID) (triage.Decision, bool, error)
  ```

  `triage.Filter`는 `Category finding.Category`(빈 값은 전체), `Status string`(`undecided`, 판정 네 가지, `all`), `Page int`다. `triage.Page`는 `Entries []Entry`, `Total int`, `Page int`, `Pages int`, 그리고 필터와 상관없는 상태별 건수 `Counts map[Verdict]int`다. `triage.Entry`는 목록 한 줄이다: 비밀 값 묶음 하나(`Bundle BundleID`, 이름, 분류, 종류, 미리보기, 위치 수, 판정) 또는 고객정보 한 무리(`Group GroupKey{File string; Kind finding.Kind; Verdict Verdict}`, 건수, 묶음 수). `triage.Group`은 그 무리의 묶음 ID 목록, 건수, 줄 번호(처음 50개)다. 한 쪽은 50줄이고 순서는 분류, 파일, 첫 줄이다.

- [ ] **Step 1: `triage`의 실패하는 테스트를 쓴다.**
  - `TestParseName`: `db.main.password`, `ssh.deploy2.key`는 통과한다. 빈 값, 마디 하나(`password`), 여섯 마디, 대문자, `-`와 `_`, 숫자로 시작하는 마디, `ext.`으로 시작하는 이름, 101자는 `ErrInvalidName`이다.
  - `TestNameForms`: `db.main.password`의 `Config()`는 `ext.db.main.password`, `Shell()`은 `EXT_DB_MAIN_PASSWORD`다.
  - `TestAllowed`: 비밀 값 묶음은 `externalize, false-positive, hold`, 고객정보는 `redact, false-positive, hold`, 구성원 중 `Frontend` 파일이 하나라도 있는 비밀 값 묶음은 `false-positive, hold`다.
  - `TestCheck`: 허용되지 않은 판정, 이름 없는 외부화, 외부화가 아닌데 이름이 있는 것, 사유 없는 보류, 보류가 아닌데 사유가 있는 것, 500자를 넘거나 제어 문자가 든 메모는 `ErrNotAllowed`다. 빈 `Decision`은 언제나 통과한다.
- [ ] **Step 2:** 실패를 확인하고 `triage`와 `audit`을 쓴다.
- [ ] **Step 3: `db`의 실패하는 테스트를 쓴다.** 실행 중인 postgres가 없으면 건너뛴다(기존 `db` 테스트의 방식).
  - `TestSaveResultAssignsBundles`: 한 스캔 결과에 같은 지문의 비밀번호가 `a.yml`, `b.xml`, `c.jsp`에, 같은 지문의 호스트가 `a.yml`, `b.xml`에, 같은 전화번호가 `x.log`, `y.log`에 있으면 묶음은 비밀번호 2개(`a.yml`+`b.xml`, `c.jsp`), 호스트 2개, 전화번호 2개다.
  - `TestRescanKeepsDecisions`: 판정하고 이름을 준 뒤 같은 결과를 다시 저장하면 판정과 이름이 그대로다. 새 파일 `d.properties`에 같은 비밀번호가 더해지면 `a.yml`의 묶음에 들어간다. `MoveFinding`으로 `b.xml`을 떼어 낸 뒤 다시 저장해도 `b.xml`은 떼어 낸 묶음에 있다.
  - `TestDecide`: 저장한 값이 `Bundle`로 읽히고 `audit_event`에 한 줄이 생긴다. 다른 프로젝트 ID로 부르면 `ErrNotFound`이고 아무것도 바뀌지 않는다. ID 여러 개 중 하나가 없으면 전부 저장되지 않는다. 다른 묶음이 쓰는 이름은 `ErrDuplicate`다. `Check`에 걸리는 값은 `triage.ErrNotAllowed`다.
  - `TestMoveFinding`: `to`가 0이면 새 묶음을 만들고 그 ID를 돌려준다. 지문이 다른 묶음으로는 `triage.ErrNotAllowed`다. 마지막 구성원을 옮기면 원래 묶음 줄이 지워지고 그 이름을 다른 묶음에 줄 수 있다. 옮긴 항목은 옮겨 간 묶음의 판정을 따른다. `Frontend` 파일의 항목을 외부화로 판정된 묶음으로 옮기면 `triage.ErrNotAllowed`다.
  - `TestEntries`: 묶음 120개에서 `Page: 3`이 20줄, `Pages`가 3이다. `Page: 9`는 3쪽을 준다. 한 파일의 전화번호 37개는 한 줄(건수 37)이고, 그중 5개를 오탐으로 판정하면 두 줄(32, 5)이 된다. `scanID`에 없는 묶음(값이 사라진 것)은 나오지 않는다. 분류와 상태 필터가 줄을 거른다.
  - `TestPastDecision`: 다른 프로젝트의 같은 지문에 판정이 있으면 가장 최근 것을 주고, 없으면 `false`다.
  - `TestMigration0005Backfill`: `0004`까지 적용한 DB에 항목을 넣고 `0005`를 적용하면 모든 항목에 `bundle_id`가 있고 묶음 수가 `TestSaveResultAssignsBundles`의 규칙과 같다.
- [ ] **Step 4:** `0005_bundle_and_audit.sql`을 쓴다.

  ```sql
  CREATE TABLE bundle (
      id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
      project_id  bigint NOT NULL REFERENCES project (id),
      fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
      -- shared: every finding of the fingerprint joins it; frontend: the same
      -- for JSP, JS and HTML files; own: only what a user or the rule for
      -- common values put here.
      origin      text NOT NULL CHECK (origin IN ('shared', 'frontend', 'own')),
      verdict     text NOT NULL DEFAULT '' CHECK (verdict IN ('', 'externalize', 'redact', 'false-positive', 'hold')),
      hold_reason text NOT NULL DEFAULT '' CHECK (hold_reason IN ('', 'structure-change', 'needs-confirmation', 'later')),
      name        text CHECK (name ~ '^[a-z][a-z0-9]*(\.[a-z][a-z0-9]*){1,4}$'),
      note        text NOT NULL DEFAULT '' CHECK (char_length(note) <= 500),
      decided_by  text NOT NULL DEFAULT '',
      decided_at  timestamptz,
      CHECK ((verdict = 'externalize') = (name IS NOT NULL)),
      CHECK ((verdict = 'hold') = (hold_reason <> '')),
      UNIQUE (project_id, name)
  );
  CREATE UNIQUE INDEX bundle_one_auto ON bundle (project_id, fingerprint, origin) WHERE origin <> 'own';
  CREATE INDEX bundle_fingerprint ON bundle (fingerprint);
  ```

  `finding.bundle_id bigint REFERENCES bundle (id)`를 더하고, 기존 항목의 묶음을 만든 뒤 `NOT NULL`로 바꾸고 인덱스를 건다. `audit_event(id, at, actor_subject, actor_name, action, project_id, bundle_id, finding_id, detail jsonb)`는 `project`만 참조하고 묶음과 항목은 참조하지 않는다(묶음이 지워져도 기록은 남는다). `action`은 CHECK로 세 값에 묶는다.
- [ ] **Step 5:** `bundle.go`, `audit.go`를 쓰고 `insertFinding`을 고친다. 새 항목의 묶음은 고객정보이거나 `SharesBundle`이 false면 새 `own` 묶음, `Frontend` 파일이면 그 지문의 `frontend` 묶음, 그 밖에는 `shared` 묶음이다(없으면 만든다). 이미 있는 항목의 묶음은 건드리지 않는다. `Decide`와 `MoveFinding`은 한 트랜잭션에서 묶음을 잠그고(`FOR UPDATE`), `Check`로 확인하고, 바꾸고, 감사 기록을 쓴다.
- [ ] **Step 6:** `make check`와 `make test-integration`이 통과하는지 확인한다.
- [ ] **Step 7:** `CHANGELOG.md`와 결정 기록 0027(검토 항목 4, 5, 7의 결정과 이유)을 쓰고 커밋한다. `feat(triage): store bundles, verdicts and the audit trail`

### Task 5: 제안

**Files:**
- Create: `internal/triage/suggest.go`, `internal/triage/suggest_test.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: `triage.Bundle`, `triage.Name`, `Store.UsedNames`, `Store.PastDecision`
- Produces:

  ```go
  // Suggestion is what the tool proposes for a bundle. It is never stored.
  // Source says where the verdict came from: "rule" or "history".
  type Suggestion struct {
  	Decision Decision
  	Source   string
  }

  // SuggestName returns a name for b that is not in used.
  func SuggestName(b Bundle, used []Name) Name

  // Default returns the suggestion the rules give for b without looking at
  // earlier verdicts: a name for a secret, and a hold for a frontend file.
  func Default(b Bundle, used []Name) Suggestion
  ```

  web의 `Suggester` 인터페이스(Task 6)는 `Suggest(ctx context.Context, b triage.Bundle) (triage.Suggestion, error)` 하나다. 2차의 AI 제안은 이 인터페이스의 구현을 하나 더하는 것이다.

- [ ] **Step 1: 실패하는 테스트를 쓴다.** `TestSuggestName`은 표 형태다. 탐지된 줄은 `Members[0].Locations[0]`의 `Context`에서 `StartLine`에 해당하는 줄이다.

  | 분류, 종류 | 탐지된 줄 | 쓰인 이름 | 결과 |
  |---|---|---|---|
  | storage, password | `    password: Sh0p****` | 없음 | `db.main.password` |
  | storage, password | `order.datasource.password=****` | 없음 | `db.order.password` |
  | storage, account | `<property name="username" value="****"/>` | 없음 | `db.main.account` |
  | storage, password | `jdbc.readonly.passwd = ****` | 없음 | `db.readonly.password` |
  | storage, password | (주변 줄 없음) | `db.main.password` | `db.main2.password` |
  | storage, host | `spring.datasource.url=jdbc:mysql://****:****/shop` | 없음 | `db.main.host` |
  | ssh, password | `sshpass -p '****' ssh deploy@****` | 없음 | `ssh.main.password` |
  | ssh, key-file | (주변 줄 없음) | 없음 | `ssh.main.key` |
  | partner-session, token | `private static final String PARTNER_API_TOKEN = "eyJh****";` | 없음 | `partner.api.token` |
  | other-secret, other | `secretValue = "abcd****"` | 없음 | `secret.main.value` |

  결과는 언제나 `ParseName`을 통과한다(탐지된 줄에 한글이나 기호만 있어도). `TestDefault`: 비밀 값 묶음은 판정 없이 이름만 채운다. `Frontend` 파일 묶음은 `hold`와 `structure-change`, `Source: "rule"`이다. 고객정보 묶음은 빈 제안이다.
- [ ] **Step 2:** 실패를 확인하고 구현한다. 대상을 만드는 순서: 탐지된 줄에서 값 앞의 키(`=`, `:` 앞의 낱말이나 `name="..."`의 값, Java는 대입문의 변수 이름)를 찾고, `.`, `_`, `-`, 낙타 표기 경계로 낱말을 나누고, 소문자로 바꾸고, 분류를 뜻하는 낱말(`jdbc`, `db`, `database`, `datasource`, `spring`, `ssh`, `partner`)과 항목을 뜻하는 낱말(`password`, `passwd`, `pwd`, `pass`, `username`, `user`, `id`, `account`, `url`, `host`, `port`, `token`, `key`, `secret`)을 빼고, 남은 첫 낱말을 쓴다.
- [ ] **Step 3:** `make check`가 통과하는지 확인하고 커밋한다. `feat(triage): suggest placeholder names and default verdicts`
- [ ] **Step 4:** PR 1을 올린다. 본문에 검토 항목 1~17의 답과, 화면에서 달라지는 것이 스타일뿐임을 적는다.

---

## PR 2: 판정 화면, 원본 보기, 감사 로그

### Task 6: 판정 화면 (읽기)

**Files:**
- Create: `internal/web/handlers_triage.go`, `internal/web/triageview.go`, `internal/web/handlers_triage_test.go`, `internal/web/templates/triage.html`
- Modify: `internal/web/server.go`(`TriageStore`, `Suggester`), `internal/web/routes.go`, `internal/web/fakes_test.go`, `internal/web/templates/project.html`, `cmd/web/main.go`, `CHANGELOG.md`

**Interfaces:**
- Consumes: Task 4의 `Store` 메서드, Task 5의 `Suggestion`
- Produces: `GET /projects/{id}/triage?category=&status=&page=&sel=`. `sel`은 `b<묶음 ID>` 또는 `c.<종류>.<파일 경로>`다(고객정보 한 무리, 판정은 `status`를 따른다). `status`의 기본값은 `undecided`다. 요청 머리에 `HX-Request: true`와 `HX-Target: detail`이 있으면 `<section id="detail">` 조각만 돌려준다. `TriageStore`는 Task 4의 메서드 여덟 개를 갖는 인터페이스이고 `internal/web/server.go`에 둔다. `NewServer`의 매개변수에 `triage TriageStore`, `suggester Suggester`를 더한다.

- [ ] **Step 1: 실패하는 테스트를 쓴다.** 손으로 쓴 fake `TriageStore`를 쓴다.
  - `TestTriagePage`: 목록에 묶음마다 분류, 위치 수, 이름이나 첫 위치가 보인다. `sel`이 없으면 첫 줄이 골라진 상세가 함께 보인다. 상세에 파일과 줄, 탐지 도구와 규칙, 가린 주변 줄(탐지된 줄 표시), 같은 값의 다른 위치, 이름 입력란(제안한 이름), 허용된 판정 버튼만 있다.
  - `TestTriageCustomerData`: 고객정보 상세에 종류, 파일, 건수, 줄 번호, 형식 미리보기가 있고 주변 줄, 이름 입력란, 원본 보기 단추가 없다.
  - `TestTriageSuggestion`: 과거 판정이 있는 묶음의 상세에 "제안" 표시와 그 판정이 미리 골라져 있고, 목록의 상태는 여전히 미판정이다.
  - `TestTriageFragment`: `HX-Request`와 `HX-Target: detail`이 있으면 응답에 `<html`이 없고 `id="detail"`이 있다.
  - `TestTriageNoScan`: `LatestScan`이 `ErrNotFound`면 200과 "아직 성공한 스캔이 없습니다" 안내, 스캔 시작으로 가는 링크가 있다.
  - `TestTriageBadInput`: 모르는 `category`, `status`, 숫자가 아닌 `page`, 형식이 틀린 `sel`은 400이다. 없는 묶음의 `sel`은 404다. 없는 프로젝트는 404다.
  - `TestTriageEscapes`: 주변 줄과 파일 경로의 `<script>`가 이스케이프되어 나온다.
  - `TestTriageRequiresLogin`: 세션이 없으면 로그인으로 보낸다.
- [ ] **Step 2:** 실패를 확인한다.
- [ ] **Step 3:** handler와 템플릿을 시안대로 쓴다(frontend-design 스킬). 목록의 줄은 `<a href="?...&sel=...">`이고 `hx-get`, `hx-target="#detail"`, `hx-push-url="true"`를 갖는다. `Suggester`의 구현은 `cmd/web`이 조립한다: 과거 판정(`PastDecision`)이 있으면 그것을 쓰고, 없으면 `triage.Default`다. `Suggester`가 오류를 내면 제안 없이 그리고 오류를 한 번 로그에 남긴다.
- [ ] **Step 4:** 프로젝트 화면에 판정 화면으로 가는 링크와 상태별 건수를 넣는다.
- [ ] **Step 5:** `make check`가 통과하는지 확인하고 커밋한다. `feat(web): show the triage list and detail`

### Task 7: 판정하기, 묶음 옮기기

**Files:**
- Modify: `internal/web/handlers_triage.go`, `internal/web/handlers_triage_test.go`, `internal/web/routes.go`, `internal/web/templates/triage.html`, `CHANGELOG.md`

**Interfaces:**
- Produces:
  - `POST /projects/{id}/bundles/{bundle}/verdict` — 양식 값 `verdict`(빈 값은 판정 취소), `hold_reason`, `name`, `note`, `return`(돌아갈 필터와 쪽)
  - `POST /projects/{id}/customer-data/verdict` — 양식 값 `kind`, `file`, `from`(지금의 판정), `verdict`, `hold_reason`, `note`, `return`
  - `POST /projects/{id}/findings/{finding}/move` — 양식 값 `to`(`new` 또는 묶음 ID), `return`
  - 성공하면 303으로 판정 화면에 돌아간다. 판정한 뒤에는 같은 필터의 다음 줄이 골라지고, 옮긴 뒤에는 원래 묶음(없어졌으면 옮겨 간 묶음)이 골라진다. 실패(400)는 판정 화면을 다시 그리고 상세 위에 사유를 보인다.

- [ ] **Step 1: 실패하는 테스트를 쓴다.**
  - `TestVerdictSaves`: 외부화와 이름을 보내면 `Decide`가 그 묶음 ID, `Decision`, 로그인한 사용자의 `audit.Actor`로 한 번 불리고 303이다.
  - `TestVerdictRejected`: 표 형태. 형식이 틀린 이름, 모르는 `verdict`, 모르는 `hold_reason`은 `Decide`를 부르지 않고 400이다. `Decide`가 `triage.ErrNotAllowed`를 주면 400과 "이 항목에는 고를 수 없는 판정입니다", `db.ErrDuplicate`면 400과 "이미 쓰는 이름입니다"와 보낸 이름이 입력란에 남고, `db.ErrNotFound`면 404다.
  - `TestCustomerDataVerdict`: `CustomerGroup`이 준 묶음 ID 전부로 `Decide`를 한 번 부른다. 무리가 비어 있으면 404다.
  - `TestMove`: `to=new`는 `MoveFinding(…, 0, …)`, `to=17`은 17로 부른다. `ErrNotAllowed`는 400이다.
  - `TestReturnIsLocal`: `return`에 `https://evil.invalid`나 `//evil.invalid`를 넣어도 303의 `Location`이 `/projects/{id}/triage`로 시작한다.
  - `TestWritesNeedPost`: 세 주소의 `GET`은 405다. 다른 출처의 `POST`는 지금의 cross-origin 보호가 막는다.
- [ ] **Step 2:** 실패를 확인하고 handler를 쓴다. 양식 본문은 `http.MaxBytesReader`로 64KiB에 묶는다.
- [ ] **Step 3:** 상세 템플릿에 판정 양식, 판정 취소, 구성원마다 "따로 떼기"와 "다른 묶음으로"(같은 지문의 다른 묶음이 있을 때만)를 넣는다. 한 묶음이 설정 파일과 shell에 걸쳐 있으면 이름의 두 표기를 함께 보인다.
- [ ] **Step 4:** `make check`가 통과하는지 확인하고 커밋한다. `feat(web): record verdicts and move findings between bundles`

### Task 8: 원본 보기와 감사 로그

**Files:**
- Create: `internal/web/handlers_original.go`, `internal/web/handlers_original_test.go`, `internal/web/templates/original.html`, `docs/decisions/0028-reveal-is-the-one-place-a-value-is-shown.md`
- Modify: `internal/bao/client.go`, `internal/bao/client_test.go`, `internal/db/audit.go`, `internal/web/handlers_page.go`(`handleAudit`), `internal/web/templates/audit.html`, `internal/web/server.go`, `internal/web/routes.go`, `cmd/web/config.go`, `cmd/web/main.go`, `compose.yaml`, `AGENTS.md`, `docs/local-run.md`, `CHANGELOG.md`

**Interfaces:**
- Produces (`internal/bao`):

  ```go
  // Value holds an original read from OpenBao. fmt, slog and encoding/json
  // print a redacted form.
  type Value []byte
  func (v Value) Reveal() []byte

  // ErrNoOriginal means OpenBao holds no pending original at that path.
  var ErrNoOriginal = errors.New("no pending original")

  func (c *Client) ReadPending(ctx context.Context, projectID int64, fingerprint string) (Value, error)
  ```

- Produces (`*db.Store`): `RecordReveal(ctx, projectID project.ID, id triage.BundleID, by audit.Actor) error`, `AuditEvents(ctx, before int64, limit int) ([]audit.Event, error)`(최신순, `before`가 0이면 처음부터)
- Produces (web): `POST /projects/{id}/bundles/{bundle}/original`(`admin`), `GET /audit?before=`(`admin`, 한 쪽 100줄). web 인터페이스 `Originals { ReadPending(ctx, projectID int64, fingerprint string) (bao.Value, error) }`. web 환경 변수 `BAO_ADDR`, `BAO_ROLE_ID`, `BAO_SECRET_ID_FILE`(필수).

- [ ] **Step 1:** "구현 전에 확인할 사실"의 Task 8 항목을 실행 중인 OpenBao에서 확인한다.
- [ ] **Step 2: 실패하는 테스트를 쓴다.**
  - `bao`: `TestReadPending`은 `PutPending`으로 넣은 값을 그대로 읽고, 없는 지문은 `ErrNoOriginal`, 형식이 틀린 지문은 OpenBao를 부르지 않고 오류다. `TestValueRedacts`는 `%v`, `%s`, `%x`, `%#v`, `slog`, `json.Marshal` 어디에도 값이 나오지 않음을 확인한다.
  - `TestOriginalAdminOnly`: `dev-user`는 403이고 `RecordReveal`도 `ReadPending`도 불리지 않는다.
  - `TestOriginalOrder`: `RecordReveal`이 `ReadPending`보다 먼저 불린다. `RecordReveal`이 실패하면 `ReadPending`이 불리지 않고 500이다.
  - `TestOriginalShown`: 응답에 값이 있고, `Cache-Control: no-store`이며, 테스트의 로그 버퍼에 값이 없다. 값의 `<script>`는 이스케이프된다.
  - `TestOriginalUnavailable`: `ErrNoOriginal`이면 200과 "보관된 원본이 없습니다. 다시 스캔하면 보관됩니다", OpenBao 오류면 "비밀 저장소에 연결할 수 없습니다"이고 둘 다 값이 없다. 감사 기록은 남는다(보려고 한 사실).
  - `TestOriginalNotText`: UTF-8이 아니거나 NUL이 든 값은 "텍스트가 아닌 원본, N바이트"만 보인다.
  - `TestOriginalRefused`: 고객정보 묶음, 구성원 모두 `HasOriginal`이 false인 묶음, 마지막 스캔에 없는 묶음은 404이고 `RecordReveal`이 불리지 않는다.
  - `TestAuditPage`: `admin`에게 시각, 사용자, 동작, 프로젝트, 대상, 내용이 최신순으로 보이고 "더 보기" 링크의 `before`가 마지막 줄의 ID다. `dev-user`는 403이다.
- [ ] **Step 3:** 실패를 확인하고 구현한다. `original.html`은 조각이고 상세의 원본 자리에 끼워진다(`hx-post`, `hx-target`). htmx가 없으면 판정 화면 전체를 값과 함께 그린다. 값은 `string(v.Reveal())`로 이 handler 안에서만 꺼낸다.
- [ ] **Step 4:** `cmd/web`이 `bao.New`로 클라이언트를 만들어 넘긴다. `compose.yaml`의 web에 환경 변수 세 개와 `openbao-secrets` 볼륨의 web secret-id(읽기 전용)를 더하고, `depends_on`에 openbao를 넣는다. web이 기동할 때 OpenBao에 닿지 않아도 기동은 되고 원본 보기만 안내를 보인다.
- [ ] **Step 5:** `AGENTS.md` Security 절 첫 항목에 문장을 더한다.

  ```
  The value itself is written in one place: the response to an `admin`'s
  request to reveal an original, which reads it from OpenBao for that request,
  keeps no copy, and records the request in the audit trail before reading.
  ```

- [ ] **Step 6:** `make check`와 `make test-integration`이 통과하는지 확인한다.
- [ ] **Step 7:** `CHANGELOG.md`, `docs/local-run.md`(web의 새 환경 변수), 결정 기록 0028(검토 항목 12, 13)을 쓰고 커밋한다. `feat(web): let an admin reveal an original and show the audit trail`

### Task 9: 스캔 화면 정리와 건너뛴 파일

**Files:**
- Create: `internal/db/migrations/0006_scan_skips.sql`
- Modify: `internal/scan/scan.go`(`Scan`, `Result`), `internal/scan/runner.go`, `internal/db/scan.go`, `internal/db/finding.go`(`SaveResult`), `internal/web/handlers_scan.go`, `internal/web/scanview.go`, `internal/web/templates/scan.html`, 해당 테스트, `CHANGELOG.md`, `docs/local-run.md`

**Interfaces:**
- Produces: `scan.Result`와 `scan.Scan`에 `SkippedFiles []string`(상대 경로, 처음 200개, 경로순), `ScannerErrors map[string]int`(도구 이름별)를 더한다. `scan` 테이블에 `skipped_files text[] NOT NULL DEFAULT '{}'`, `scanner_errors jsonb NOT NULL DEFAULT '{}'`.

- [ ] **Step 1: 실패하는 테스트를 쓴다.**
  - 러너: 건너뛴 파일이 250개면 `Result.SkippedFiles`가 경로순 200개이고 `Skipped`는 250이다. 경로가 UTF-8이 아니거나 제어 문자가 든 파일은 `SkippedFiles`에 넣지 않는다(`Skipped`에는 센다). 스캐너가 오류 3건을 보고하면 `ScannerErrors["opengrep"]`이 3이다.
  - `db`: `SaveResult` 뒤에 `Scan`이 두 값을 그대로 돌려준다.
  - web `TestScanPage`: 건너뛴 파일 목록이 `<details>` 안에 있고, 250개 중 200개만 저장했으면 "외 50개"가 보인다. 스캐너 오류가 있으면 도구별 건수와 "일부 파일을 끝까지 분석하지 못했습니다"가 보인다. 탐지 표는 없고 분류별 건수와 판정 화면으로 가는 링크가 있다. 프로젝트의 마지막 성공한 스캔이 아니면 링크 대신 "이후 스캔이 있습니다" 안내가 있다. 실패한 스캔에도 요청한 사람과 시각이 보인다.
- [ ] **Step 2:** 실패를 확인하고 구현한다. 러너가 로그에 쓰려고 이미 모으는 값을 `Result`에 싣는다. 스캔 화면에서 `FindingsByScan`으로 그리던 표를 지우고 `rowsOf`, `secretRow`, `customerGroup` 등 쓰이지 않게 된 코드를 지운다. 건수의 단위는 "항목 N개(위치 M곳)"로 통일한다.
- [ ] **Step 3:** `make check`와 `make test-integration`이 통과하는지 확인하고 커밋한다. `feat(scan): keep the skipped file names and move the findings to the triage screen`

### Task 10: 실제 확인과 문서

**Files:**
- Modify: `docs/next-steps.md`, `docs/local-run.md`, `CHANGELOG.md`

- [ ] **Step 1:** `docker compose down -v` 뒤 `docker compose up --build`로 새로 띄우고, 계획 3의 예제 소스 프로젝트를 등록해 스캔한다.
- [ ] **Step 2:** Chromium에서 아래 표를 확인하고 결과를 PR 본문에 적는다.

  | 번호 | 확인 |
  |---|---|
  | 1 | 모든 화면이 `docs/design/`의 시안과 같고, 콘솔에 CSP 위반과 스크립트 오류가 없다 |
  | 2 | 판정 화면의 목록에서 줄을 누르면 상세만 바뀌고 주소가 바뀐다. 그 주소를 새 탭에 붙여 넣으면 같은 화면이 나온다 |
  | 3 | JDBC URL이 호스트, 포트, 계정, 비밀번호로 나뉘어 있고 각각 이름 제안이 다르다 |
  | 4 | 비밀번호 묶음에 외부화와 이름을 주면 목록에서 사라지고(미판정 필터) 다음 줄이 골라진다. 상태 필터 "외부화"에서 다시 보인다 |
  | 5 | 같은 이름을 다른 묶음에 주면 거부되고 입력한 이름이 남아 있다 |
  | 6 | 묶음에서 한 파일을 떼어 다른 이름을 주고, 다시 합친다 |
  | 7 | 고객정보 한 줄(수십 건)을 한 번에 삭제·마스킹으로 판정한다. 상세에 값과 주변 줄이 없다 |
  | 8 | JSP 항목에 외부화 버튼이 없고 "보류: 구조 변경 필요"가 미리 골라져 있다 |
  | 9 | 다시 스캔한 뒤 판정과 이름이 그대로다 |
  | 10 | `dev-user`에게 원본 보기 단추가 없고 주소를 직접 요청하면 403이다. `dev-admin`은 값을 본다 |
  | 11 | 감사 로그에 4~7과 10의 동작이 사용자, 시각과 함께 있고 값은 없다 |
  | 12 | `docker compose logs web`과 postgres의 `audit_event`, `bundle`에 예제 소스의 비밀 값이 없다(`grep`으로 확인) |
  | 13 | 스크립트를 끈 채로 2, 4, 6이 쪽 전체 새로 고침으로 된다 |
  | 14 | 스캔 화면에서 건너뛴 파일 이름이 펼쳐진다 |

- [ ] **Step 3:** `docs/next-steps.md`를 고친다. 표의 계획 4를 완료로 바꾸고, "계획 4에 필요한 계획 3의 남은 일"에서 끝난 것을 지우고, 이 계획이 계획 5로 넘기는 일(오탐의 임시 원본 지우기, 한 파일 안의 같은 값을 따로 이름 붙이기, 호스트·포트·계정의 자동 묶기)을 적는다.
- [ ] **Step 4:** `superpowers:requesting-code-review`로 브랜치 전체를 검토받고 PR 2를 올린다.

## 이 계획이 하지 않는 것

- 적용, manifest, OpenBao의 `projects/` 경로 쓰기, 확인 재스캔 (계획 5)
- 오탐으로 판정한 값의 임시 원본 지우기 (계획 5)
- 리포트의 판정 이력 화면 (계획 6). 자료는 `audit_event`에 쌓인다.
- 대시보드의 건수와 추이 (계획 7)
- base64로 인코딩된 값을 항목으로 보고하는 일, 규칙의 알려진 누락과 오탐 (`docs/next-steps.md`의 "계획 3에서 남은 일")
