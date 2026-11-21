# 리포트와 PDF Implementation Plan (계획 6/7)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 로그인한 사용자가 성공한 스캔의 리포트 화면에서 요약(찾은 항목, 외부화, 삭제·마스킹, 오탐, 보류, 미판정과 이전 스캔 대비 증감), 외부화한 항목, 삭제·마스킹한 고객정보, 구조 변경이 필요한 항목, 남은 항목, 오탐과 사유, 판정 이력을 보고, "PDF로 내보내기"를 누르면 같은 내용의 PDF를 받는다. 리포트와 PDF에는 값도, 미리보기도, 주변 코드도 없다.

**Architecture:** 리포트는 저장하지 않고 열 때마다 postgres에서 읽어 만든다. `db.Report`가 한 스냅숏 트랜잭션에서 스캔이 찾은 묶음(지금의 판정), 그 스캔이 요청되기 전에 적용된 항목(`apply_item`), 프로젝트의 판정 이력(`audit_event`)을 읽고, 순수 함수 `report.Build`가 절로 나누고 묶고 줄 수를 자른다. 화면과 PDF는 같은 템플릿 블록(`report`)을 쓴다. PDF는 web이 완성한 HTML, 스타일시트, 글꼴 파일을 한 번의 multipart 요청으로 pdf 컨테이너(Gotenberg의 Chromium)에 보내고 PDF 바이트를 돌려받는다. pdf 컨테이너는 바깥으로 나가는 길이 없는 내부 네트워크에만 있고 web만 그 네트워크에 함께 붙는다.

**Tech Stack:** Go 1.27, postgres 18, `html/template`, tailwindcss 4, Gotenberg 9(Chromium 경로만, 버전은 Task 4에서 확인), docker compose. 새 Go 의존성은 없다(`net/http`, `mime/multipart`로 Gotenberg를 부른다).

**Spec:** `docs/superpowers/specs/2026-11-18-secret-externalizer-design.md` (2절 실패 조건, 5절 컨테이너 표의 `pdf`, 7절, 8절 "리포트", 10절 7단계). 결정 기록 0006, 0023, 0028, 0029, 0036, 0040, 0041.

## 검토 때 정해 주실 것

설계 문서가 정하지 않은 내용이다. 각 항목의 첫 문장이 이 계획이 따르는 안이다. **[권고안으로 확정]** 은 2026-11-20의 지시("묻지 말고 권고안으로 정하고 문서로 남긴다")에 따라 작성자가 정한 항목이다. 구현하면서 바꾼 것은 결정 기록 0044에 모은다.

1. **[권고안으로 확정] 계획은 하나로 두고 PR을 둘로 나눈다.** PR 1은 Task 1~3(리포트 자료, 조회, 화면)이고 그것만으로 화면에서 리포트를 볼 수 있다. PR 2는 Task 4~6(pdf 컨테이너와 클라이언트, PDF 내보내기, 실제 확인)이다. 다른 안: PR 하나. 검토할 양이 한 번에 몰리고, 컨테이너 구성과 화면이 한 검토에 섞인다.
2. **[권고안으로 확정] 리포트는 저장하지 않고 열 때마다 만든다. 시점을 고정한 사본은 PDF다.** 리포트 머리와 PDF에 만든 시각을 적는다. 다른 안: 스캔이 끝날 때 리포트를 스냅숏으로 저장한다. 판정은 스캔이 끝난 뒤에 하므로 스캔 종료 시점의 스냅숏에는 판정도 적용도 없다. "언제 확정하는가"를 정할 사건이 없고 테이블이 는다.
3. **[권고안으로 확정] 스캔 N의 리포트가 보이는 것은 세 가지다.** (가) **찾은 것**: 스캔 N이 찾은 묶음, 판정은 리포트를 만든 시각의 것. (나) **적용한 것**: 그 프로젝트의 적용 실행 가운데 스캔 N이 요청되기 전에 시작된 실행(`coalesce(apply_run.started_at, apply_run.finished_at) < scan.requested_at`)과 스캔 N이 확인 스캔이면 그것이 확인하는 실행(`apply_run.id = scan.apply_id`)이 `applied`로 남긴 `apply_item` 전부(누적). (다) **판정 이력**: 그 프로젝트의 판정, 묶음 옮기기, 적용 확인 기록 전부. 스캔과 적용은 겹치지 않으므로(결정 기록 0036) "요청 전에 시작된 실행"은 "요청 전에 끝난 실행"이다. `started_at`은 worker가 종료하며 작업을 돌려줄 때 비워지고 그 실행이 다시 시작되지 못한 채 닫힐 수 있어 `finished_at`으로 메운다(`finished_at`만 쓰면 안 된다. 확인 스캔은 실행을 닫는 트랜잭션에서 요청되어 두 시각이 같다). 한 자리를 적용하고, 사람이 되돌리고, 다시 적용하면 `applied` 항목이 둘이라 그 자리가 두 번 실리고 두 번 센다(판정 화면의 "적용됨" 탭과 같은 셈이다). 그래서 마지막 성공한 스캔의 리포트가 프로젝트의 지금 상태를 말하는 문서이고, 적용 뒤의 확인 스캔 리포트에 방금 외부화한 항목이 실린다. 지난 스캔의 리포트는 머리에 "이후 스캔이 있습니다"와 마지막 스캔 리포트의 링크를 보인다. 다른 안: 스캔 N을 근거로 한 실행(`apply_run.scan_id = N`)만 싣는다. 그러면 확인 스캔의 리포트에 외부화한 항목이 하나도 없고, 공유할 문서가 "적용 전 스캔의 리포트"가 되는데 거기에는 사라졌는지 확인한 결과가 없다. 다른 안: 지난 스캔의 리포트는 판정도 그때의 것으로 되돌려 보인다. `audit_event`를 되감아야 하고 묶음 옮기기 때문에 정확히 되돌릴 수 없다.
4. **[권고안으로 확정] 리포트와 PDF에는 미리보기, 지문, 주변 코드를 싣지 않는다.** 설계 8절은 "값은 들어가지 않는다"이고, `AGENTS.md`는 미리보기와 지문을 "데이터베이스와 화면"에만 허용한다. PDF는 도구 밖으로 나가는 문서이고 화면 리포트는 PDF와 같은 내용이어야 하므로 둘 다 뺀다. 항목은 분류, 종류, 이름, 파일과 줄로 가리킨다. `report` 패키지의 타입에는 미리보기, 지문, 주변 줄을 담을 필드가 없다(테스트로 붙잡는다). 사용자가 쓴 사유(note)는 싣는다. `AGENTS.md`의 Security 절에 한 줄을 더한다.
5. **[권고안으로 확정] 건수의 단위는 묶음이고 위치 수를 함께 보인다.** 판정 화면의 탭과 같은 단위다. 고객정보는 값마다 묶음 하나이므로 묶음 수가 곧 찾은 값의 수다. 외부화와 삭제·마스킹은 `applied` 항목의 묶음 수(중복 없이)와 항목 수(위치)다.
6. **[권고안으로 확정] 요약의 숫자는 일곱 개다.** 찾은 항목, 외부화 완료, 삭제·마스킹 완료, 오탐, 보류, 미판정, 적용 대기(판정이 외부화나 삭제·마스킹인데 이 스캔이 아직 찾는 것). 찾은 항목 = 오탐 + 보류 + 미판정 + 적용 대기다. 증감은 같은 계산을 바로 앞의 성공한 스캔에 한 값과의 차이다(판정은 둘 다 지금의 것). 앞의 성공한 스캔이 없으면 증감을 보이지 않는다. 설계의 "탐지, 외부화, 삭제, 오탐, 보류"에 미판정과 적용 대기를 더한 것이다. 둘이 없으면 숫자의 합이 맞지 않는다.
7. **[권고안으로 확정] 찾은 묶음은 세 절 가운데 정확히 한 곳에 실린다.** 오탐이면 6절. 오탐이 아니고, 비밀 값 묶음의 구성원이 JSP·JS·HTML 파일에 있거나 보류 사유가 "구조 변경 필요"면 4절. 고객정보는 프론트엔드 파일에 있어도 삭제·마스킹할 수 있으므로(`triage.Bundle.Allowed`) 파일 종류로는 4절에 가지 않는다(판정 화면의 안내 "리포트의 구조 변경이 필요한 항목에 실립니다"와 같다). 나머지(미판정, 다른 사유의 보류, 적용 대기)는 5절이고, 그래서 5절의 제목은 설계의 "남은 미판정 항목"이 아니라 "남은 항목"이다. 줄마다 상태를 보인다: 미판정, 보류와 사유, "적용 전", "적용됨 · 다시 발견"(이 스캔 전에 적용했는데 이 스캔이 또 찾음), "이후 적용됨"(이 스캔 뒤의 실행이 바꿈. 마지막 스캔에서는 확인 스캔이 실패했거나 아직 돌지 않았을 때만 나온다).
8. **[권고안으로 확정] 고객정보는 어느 절에서든 파일, 종류, 판정, 보류 사유, 사유 글이 같은 것끼리 한 줄로 묶어 건수를 보인다.** 설계의 "종류별 건수와 파일"이다. 비밀 값은 묶음마다 한 줄이고 첫 파일과 줄, 파일 수, 위치 수를 보인다. 외부화한 항목(2절)은 소스에 쓰인 이름(`${ext.…}`, `${EXT_…}`)마다 한 줄이고 바뀐 자리(파일과 적용 뒤의 줄, 또는 "파일 제거")를 모두 적는다.
9. **[권고안으로 확정, 구현 중 100줄로 줄임] 절마다 100줄까지만 싣고 "외 N줄"을 적는다. 외부화한 항목 하나의 자리는 50곳까지다.** 건수는 언제나 전체를 센다. 로그 파일이 수천 개인 프로젝트에서도 화면이 5초 안에 그려지고 PDF가 제한 시간 안에 만들어지게 하려는 것이다. 판정 이력은 한 번의 판정 요청이 여러 묶음에 남긴 기록(같은 시각, 같은 사람, 같은 내용)을 한 줄로 합쳐 새것부터 100줄이다. 처음 권고는 1,000줄이었으나 Task 5에서 모든 한도까지 채운 리포트를 변환기가 인쇄하지 못해 100줄로 줄였다(`docs/decisions/0042-what-a-scan-report-shows.md`). 원본 보기 기록은 싣지 않는다(판정 이력이 아니고, 감사 로그는 `admin`의 화면이다).
10. **[권고안으로 확정] 주소는 `GET /scans/{id}/report`와 `GET /scans/{id}/report.pdf`이고, `user`와 `admin` 모두 볼 수 있다.** 내용이 역할에 따라 다르지 않다. 성공하지 않은 스캔의 리포트 주소는 스캔 화면으로 돌려보낸다(303). 스캔 화면, 프로젝트 화면의 스캔 목록, 적용 결과 화면의 확인 스캔 옆에 리포트 링크를 둔다. PDF 내보내기는 상태를 바꾸지 않으므로 `GET`이고 감사 로그에 남기지 않는다.
11. **[권고안으로 확정] PDF 변환기는 Gotenberg이고(결정 기록 0006), HTML에 자원을 끼워 넣지 않고 한 요청의 multipart 파일로 함께 보낸다.** 보내는 파일은 `index.html`, `footer.html`, `app.css`(글꼴 주소를 같은 폴더의 파일 이름으로 바꾼 것), woff2 다섯 개다. 설계의 "글꼴, 스타일을 모두 포함한 상태로 보낸다"를 요청 단위로 지킨다. 다른 안: CSS와 글꼴을 `data:` 주소로 HTML에 넣는다. HTML이 6MB가 되고 `template.CSS`로 변환해야 한다. 설계 8절의 [제안] "한글 글꼴은 pdf 이미지에 넣는다"와 다르다. 이미지를 따로 빌드하지 않고 화면과 같은 글꼴 파일을 쓰려는 것이고, 결정 기록 0043에 적는다. 차트는 이 계획에 없다(요약은 숫자와 `meter` 막대다). 차트 라이브러리는 계획 7이 고른다.
12. **[권고안으로 확정] pdf 컨테이너는 받은 파일만 읽는다.** compose에 `internal: true`인 네트워크 `pdf`를 만들어 `pdf` 서비스는 거기에만 두고, web은 기본 네트워크와 `pdf` 둘 다에 붙인다. 호스트 포트는 열지 않는다. Gotenberg는 Chromium 경로만 켜고 JavaScript를 끄고, Chromium이 열 수 있는 주소를 자기 작업 폴더의 `file://`로 좁히고, webhook과 "주소에서 내려받기"를 끈다. 이미지는 digest로 고정한다. `docs/next-steps.md`의 "compose의 모든 서비스가 한 네트워크를 쓴다"는 pdf만 나누고 나머지는 그대로 남긴다.
13. **[권고안으로 확정] 쪽은 A4 세로이고, 바닥글에 `secret-externalizer | scan #N | 쪽 / 전체`를 넣는다.** 바닥글은 Chromium이 따로 그리는 문서라 보낸 글꼴을 쓰지 못하므로 ASCII만 쓴다(프로젝트 이름은 한글일 수 있어 넣지 않는다). 본문 첫머리에 프로젝트 이름, 스캔 번호, 커밋, 만든 시각이 있다.
14. **[권고안으로 확정] PDF 한 건의 제한은 변환 20초, 결과 64MiB다.** web의 `WriteTimeout`(30초) 안이다. 변환기가 없거나 늦으면 503 화면에 "PDF 변환 서비스에 연결할 수 없습니다. 잠시 뒤 다시 시도해 주세요."를 보이고, 변환기가 문서를 거부하면 500이다. PDF는 버퍼에 다 받은 뒤에만 응답을 쓴다. web은 pdf 컨테이너가 없어도 뜨고 다른 화면은 그대로 동작한다. `PDF_URL`은 필수 환경 변수다.
15. **[권고안으로 확정] 화면은 시안을 새로 만들지 않고 확인받은 토큰과 구성 요소로 만든다.** 계획 5와 같다. `docs/design/README.md`의 색, 글꼴, 간격과 `app.css`의 `card`, `facts`, `table`, `badge`, `notice`, `bars`를 쓰고 frontend-design 스킬로 구현한다. 계획 4는 리포트 시안을 이 계획에서 만든다고 했는데, 사용자의 "확인받지 말고 진행" 지시에 따라 만들지 않는다. 실제 확인(Task 6)에서 화면과 PDF의 그림을 남겨 나중에 볼 수 있게 한다.
16. **[권고안으로 확정] `docs/next-steps.md`의 항목은 아래 표대로 한다.**

    | 항목 | 처리 |
    |---|---|
    | `apply_run.project_id` 인덱스 | 한다 (Task 2, 마이그레이션 `0008`). `audit_event (project_id, id)` 인덱스도 함께 둔다 |
    | compose의 네트워크 나누기 | pdf만 나눈다 (Task 4). 나머지는 남긴다 |
    | 건수만 세는 저장소 메서드 | 넘긴다. 계획 7(대시보드)이 건수 조회를 만들 때 함께 한다 |
    | 그 밖의 "계획 3, 4, 5에서 남은 일" | 넘긴다 |

## 구현 전에 확인할 사실

실행해 보지 않았거나 공식 문서로 확인하지 못한 내용이다. 해당 Task의 첫 단계에서 확인하고, 다르면 계획을 고친 뒤 진행한다. Task 2, 3의 결과는 결정 기록 0042에, Task 4~6의 결과는 0043의 "배경"에 적는다.

| 내용 | Task |
|---|---|
| postgres에서 한 트랜잭션의 `now()`는 트랜잭션 시작 시각이라, `Decide` 한 번이 넣은 `audit_event`의 `at`이 모두 같다. `jsonb` 열로 `GROUP BY`할 수 있다 | 2 |
| 확인 스캔은 실행을 닫는 트랜잭션에서 요청되므로 `scan.requested_at`은 그 실행의 `started_at`보다 뒤다(같지 않다). `started_at`이 비어 있는 채 닫힌 실행(`ReleaseJob` 뒤 `FailApply`)은 `finished_at`이 확인 스캔의 `requested_at`과 같고 이후 스캔의 것보다 앞선다 | 2 |
| Gotenberg의 지금 안정 버전과 이미지 이름(계획 검토 때 본 것은 9.0.0, `gotenberg/gotenberg:9-chromium`). Chromium만 든 이미지를 쓴다. linux/amd64와 linux/arm64 digest. 9는 모르는 양식 필드를 400으로 거부하므로 필드 이름은 9의 문서에서 가져온다(`paperSize` 필드가 있으면 `paperWidth`·`paperHeight` 대신 쓴다) | 4 |
| `POST /forms/chromium/convert/html`: 파일 필드 이름은 `files`이고 `index.html`이 있어야 하며, 다른 파일은 이름만으로(`app.css`, `X.woff2`) 참조한다. `footer.html`이 바닥글이고 `pageNumber`, `totalPages` 클래스가 채워진다. 양식 필드 `paperWidth`, `paperHeight`, `marginTop`·`marginBottom`·`marginLeft`·`marginRight`(인치), `printBackground`, `preferCssPageSize`의 이름과 단위 | 4 |
| 같은 요청으로 보낸 woff2를 `@font-face`가 `file://`에서 읽어 한글이 그 글꼴로 그려진다. 5MB쯤의 요청이 기본 본문 한도 안이다. `app.css`의 `font-display: swap` 때문에 글꼴을 읽기 전에 인쇄되거나 읽기가 거부되면 이미지의 다른 글꼴로 조용히 그려지므로, PDF에 든 글꼴 이름(`pdffonts` 또는 PDF 바이트에서 `IBMPlexSansKR`, `D2Coding` 찾기)으로 확인한다 | 4, 6 |
| 이미지 안에 healthcheck가 쓸 클라이언트(`curl` 등)가 있다. Chromium을 미리 띄우는 옵션(`--chromium-auto-start`)을 켰을 때와 껐을 때 첫 변환에 걸리는 시간. 컨테이너의 사용자(uid 1001)가 `/tmp` tmpfs에 쓸 수 있다 | 4 |
| `header.html`도 Gotenberg가 따로 다루는 이름이다 | 4 |
| Gotenberg의 옵션 이름과 뜻: Chromium 말고 다른 경로 끄기, JavaScript 끄기, Chromium 허용·거부 주소 목록, webhook 끄기, "주소에서 내려받기" 끄기, 요청 제한 시간, 상태 확인 주소(`/health`) | 4 |
| Gotenberg 컨테이너가 `read_only: true`, `/tmp` tmpfs, `cap_drop: ALL`, `no-new-privileges`로 뜨고 변환한다. 안 되는 것이 있으면 그것만 풀고 이유를 적는다 | 4 |
| `internal: true` 네트워크에만 붙은 컨테이너는 바깥(인터넷, 호스트)으로 나가지 못하고, 같은 네트워크의 web은 이름 `pdf`로 닿는다 | 4, 6 |
| `http.ServeMux`가 `GET /scans/{id}/report`와 `GET /scans/{id}/report.pdf`를 함께 등록하고 구분한다 | 3, 5 |
| Chromium이 `meter` 요소와 배경색을 `printBackground`로 인쇄하고, 표의 `thead`를 쪽마다 되풀이한다 | 5 |

## Global Constraints

- `AGENTS.md`의 모든 규칙을 따른다. Task 하나를 끝낼 때마다 `make check`가 통과해야 한다. 코드, 주석, 커밋 메시지는 영문, `CHANGELOG.md`와 `docs/` 아래는 한글로 쓴다. 화면의 문구는 한글이다.
- 설계의 실패 조건 가운데 이 계획에 해당하는 것: **원본 값이 리포트, PDF, 로그 어디에든 평문으로 남는다.** 리포트의 자료는 postgres에서만 오고(postgres에는 값이 없다) web은 이 계획에서 OpenBao를 읽지 않는다. 그에 더해 미리보기, 지문, 주변 줄도 싣지 않는다(`TestTypes_HoldNoValueField`, `TestHandleReport_NoPreview`, `TestHandleReportPDF_Document`, Task 6의 8번).
- 리포트에 실리는 글자는 모두 `html/template`이 이스케이프한다. `template.HTML`, `template.CSS`, `template.JS`, `template.URL`로 바꾸지 않는다. CSP를 넓히지 않고 인라인 스크립트와 `style` 속성을 쓰지 않는다. PDF용 문서(`index.html`)는 스크립트가 없다.
- 리포트 화면과 PDF는 같은 템플릿 블록 `report`를 그린다. 한쪽에만 있는 내용은 화면의 머리 띠, "PDF로 내보내기" 버튼, PDF의 바닥글뿐이다.
- DB를 읽는 요청은 지금의 제한(5초) 안에 있다. `db.Report`는 `readOnly`(repeatable read, 읽기 전용) 트랜잭션 하나로 읽어, 요약의 숫자와 목록이 같은 스냅숏에서 나온다.
- 병합된 마이그레이션 파일(`0001`~`0007`)은 고치지 않는다. 새 파일은 `0008_report_indexes.sql`이다.
- 외부 서비스를 부르는 `http.Client`에는 제한 시간이 있다. pdf 클라이언트의 것은 25초이고 요청마다 context로 20초를 건다.
- 테스트 예제 값은 가짜임이 분명해야 한다(`dev-only-fake-...`, `010-0000-0000`, `db.invalid`).
- 결정 기록은 0042(스캔 리포트가 보이는 것), 0043(PDF 변환기의 구성과 제한), 0044(계획 6을 구현하면서 대신 정한 것)다.

## Review Focus

1. 큰 프로젝트: 고객정보 묶음이 수만 개, 로그 파일이 수천 개여도 건수는 전체를 세고 목록은 100줄에서 잘리며 "외 N줄"이 맞다. (Task 1 `TestBuild_Caps`, Task 2 `TestReport_CustomerDataGrouped`, `TestReport_Events`)
2. 변환기의 실패: pdf 컨테이너가 없거나, 답이 늦거나, PDF가 아닌 것을 돌려주거나, 한도보다 큰 것을 돌려줘도 사용자는 반쯤 내려받은 파일이 아니라 안내 화면을 보고, web은 멈추지 않는다. (Task 4 `TestConvert_Errors`, Task 5 `TestHandleReportPDF_ConverterDown`, `TestHandleReportPDF_Deadline`)
3. 이름과 사유에 든 글자: 파일 이름과 사유에 HTML, 스크립트, 아주 긴 줄, 줄바꿈 없는 긴 경로가 있어도 이스케이프되고 표가 쪽 밖으로 나가지 않는다. PDF 문서에서도 같다. (Task 3 `TestHandleReport_Escapes`, Task 5 `TestHandleReportPDF_Document`, Task 6의 6번)
4. 비어 있거나 오래된 스캔: 아무것도 찾지 못한 스캔, 앞의 성공한 스캔이 없는 첫 스캔, 적용이 한 번도 없던 프로젝트, 이후 스캔이 있는 지난 스캔, 성공하지 않은 스캔에서 각 절이 "없습니다"를 말하거나 돌려보낸다. (Task 1 `TestBuild_Empty`, `TestBuild_Delta`, Task 3 `TestHandleReport_Redirects`, `TestHandleReport_OlderScan`)
5. 사라진 대상: 묶음이나 항목이 지워진 뒤의 `apply_item`과 `audit_event`도 리포트에 남고, 다른 프로젝트의 자료가 섞이지 않는다. (Task 2 `TestReport_ItemOfMissingBundle`, `TestReport_OtherProject`)
6. 숫자의 일치: 요약의 숫자가 세 절의 묶음 수 합과 같고, 읽는 중에 판정이 바뀌어도 한 스냅숏의 값이다. (Task 2 `TestReport_TotalsMatchLines`)
7. 한글과 격리: PDF의 한글이 보낸 글꼴로 그려지고, pdf 컨테이너는 바깥으로 나가지 못한다. (Task 4 `TestConvert_Real`, Task 6의 7, 10번)

## File Structure

| 파일 | 책임 |
|---|---|
| `internal/report/report.go` | 리포트의 타입과 `Build`: 절 나누기, 묶기, 자르기, 증감 |
| `internal/db/migrations/0008_report_indexes.sql` | `apply_run (project_id, id)`, `audit_event (project_id, id)` 인덱스 |
| `internal/db/report.go` | `Store.Report`: 한 스냅숏에서 리포트의 자료를 읽는다 |
| `internal/web/handlers_report.go` | 리포트 화면과 PDF 내보내기 handler |
| `internal/web/reportview.go` | `report.Report`를 화면의 글자와 주소로 바꾼다 |
| `internal/web/templates/report.html` | `report` 블록(화면과 PDF가 함께 쓴다), `print`, `print-footer` |
| `internal/web/printassets.go` | PDF에 함께 보내는 스타일시트와 글꼴 |
| `internal/pdf/pdf.go` | Gotenberg 클라이언트 |
| 고치는 파일 | `internal/web/server.go`(`Stores.Reports`, `Converter`, `Limits`), `routes.go`, `render.go`, `scanview.go`, `handlers_project.go`, `applyrun.go`와 `templates/scan.html`·`project.html`·`apply_run.html`(리포트 링크), `assets/app.css`·`static/app.css`, `cmd/web/config.go`·`main.go`, `compose.yaml`, `Makefile`(`TEST_PDF_URL`), `AGENTS.md`, `CHANGELOG.md`, `docs/local-run.md`, `docs/next-steps.md` |

---

## PR 1: 리포트 화면

### Task 1: 리포트의 타입과 `Build`

**Files:**
- Create: `internal/report/report.go`, `internal/report/report_test.go`, `docs/decisions/0042-what-a-scan-report-shows.md`
- Modify: `AGENTS.md`(Security 절), `CHANGELOG.md`

**Interfaces:**
- Produces:

  ```go
  // Package report builds the report of one scan: what the scan found, what
  // the project's applies had changed before it, and the history of the
  // decisions. Nothing here holds a value, a fingerprint, a preview or a
  // line of source.
  package report

  const (
  	MaxRows   = 100  // the most lines a section lists
  	MaxPlaces = 50   // the most places an externalized entry lists
  )

  // Count is a number of bundles and of their locations.
  type Count struct{ Bundles, Locations int }

  // Totals are the numbers of one scan. Found is what the scan found, and
  // FalsePositive, Hold, Undecided and Pending split it by the verdict the
  // bundles have now; Pending is externalize or redact. Externalized and
  // Redacted count the applied items of the runs that started before the
  // scan was requested.
  type Totals struct {
  	Found, FalsePositive, Hold, Undecided, Pending Count
  	Externalized, Redacted                         Count
  }

  // Applied says how an apply changed places of a bundle the scan found.
  // The zero value means none did.
  type Applied string

  const (
  	AppliedBefore Applied = "before" // a run before the scan: found again
  	AppliedAfter  Applied = "after"  // a run after the scan
  )

  // Line is one line of what a scan found: a secret bundle (Bundles is 1),
  // or the customer data of one kind in one file with one verdict, hold
  // reason and note (Bundle is zero).
  type Line struct {
  	Bundle    triage.BundleID
  	Category  finding.Category
  	Kind      finding.Kind
  	Name      triage.Name
  	Verdict   triage.Verdict
  	Reason    triage.HoldReason
  	Note      string
  	DecidedAt time.Time // the newest decision of the line, zero when undecided
  	File      string    // the first file
  	FirstLine int
  	Files     int
  	Bundles   int
  	Locations int
  	Frontend  bool // a secret bundle with a member the scan found in a triage.Frontend file; false for customer data
  	Shell     bool // every member the scan found is in a triage.ShellFile
  	Applied   Applied
  }

  // Change is one applied replace or remove-file item.
  type Change struct {
  	Run  apply.ID
  	Item apply.Item
  }

  // Redaction is the redacted customer data of one kind in one file.
  type Redaction struct {
  	Kind      finding.Kind
  	File      string
  	Untracked bool // git did not track the file, so the change cannot be undone
  	Bundles   int
  	Locations int
  }

  // Event is one line of the history: the audit events one request left.
  // Category, Kind and File are those of the first finding the first bundle
  // holds now, empty when it holds none.
  type Event struct {
  	At       time.Time
  	Actor    audit.Actor
  	Action   audit.Action
  	Detail   map[string]string
  	Bundles  int
  	Category finding.Category
  	Kind     finding.Kind
  	File     string
  }

  // Data is what the database tells about one scan. Latest is the project's
  // newest succeeded scan and Previous the succeeded scan before Scan, zero
  // when there is none. Events holds the newest MaxRows of EventTotal lines.
  type Data struct {
  	Project        project.Project
  	Scan           scan.Scan
  	Latest         scan.ID
  	Previous       scan.ID
  	Totals         Totals
  	PreviousTotals Totals
  	Lines          []Line
  	Changes        []Change
  	Redactions     []Redaction
  	Events         []Event
  	EventTotal     int
  }

  // List is the lines a section shows and the number it stands for.
  type List[T any] struct {
  	Rows  []T
  	Total int
  }

  // More returns the number of lines the list leaves out.
  func (l List[T]) More() int

  // Place is one place an apply changed. Line is the line after the apply;
  // Removed means the whole file was removed and Line is zero.
  type Place struct {
  	File    string
  	Line    int
  	Removed bool
  	Run     apply.ID
  }

  // Entry is one externalized name as the source carries it.
  type Entry struct {
  	Bundle     triage.BundleID
  	Name       string // as written: ext.db.main.password or EXT_DB_MAIN_PASSWORD
  	Category   finding.Category
  	Kind       finding.Kind
  	StorePath  string
  	Places     []Place // the first MaxPlaces of PlaceTotal
  	PlaceTotal int
  	CodeChange bool // a place is in code that has to read the value itself
  	Reappeared bool // the scan still finds the bundle's value
  }

  // KindCount is the redacted customer data of one kind.
  type KindCount struct {
  	Kind      finding.Kind
  	Bundles   int
  	Locations int
  	Files     int
  }

  // Report is the report of one scan, ready to be shown.
  type Report struct {
  	Project        project.Project
  	Scan           scan.Scan
  	Latest         scan.ID
  	Previous       scan.ID
  	GeneratedAt    time.Time
  	Totals         Totals
  	Delta          Totals // Totals minus the previous scan's; zero without one
  	Externalized   List[Entry]
  	RedactedKinds  []KindCount
  	Redacted       List[Redaction]
  	Structure      List[Line]
  	Remaining      List[Line]
  	FalsePositives List[Line]
  	History        List[Event]
  }

  // Build shapes d into the report made at now. It keeps no slice or map of d.
  func Build(d Data, now time.Time) Report
  ```

- `Build`의 규칙:
  - 줄 나누기: `Verdict`가 오탐이면 `FalsePositives`. 아니고 `Frontend`이거나 (`Verdict`가 보류이고 `Reason`이 `triage.HoldStructure`)이면 `Structure`. 나머지는 `Remaining`. 각 목록은 파일, 첫 줄, 묶음 ID 순이다.
  - `Externalized`: `Changes`를 (`Item.BundleID`, `Item.Name`)으로 묶는다. 한 묶음이 `.yml`과 `.sh`에 걸치면 항목 둘이고 `StorePath`가 같다. 항목은 이름순, 자리는 파일과 줄 순이다. `Item.Action`이 `apply.ActionRemoveFile`이면 `Removed`다. `CodeChange`는 자리 가운데 하나라도 그러면 true다. `Reappeared`는 `Lines`에 그 묶음의 줄이 있고 `Applied`가 `AppliedBefore`일 때 true다.
  - `Redacted`는 종류(`phone`, `rrn`, `email`, `card`, `address` 순)와 파일 순이고, `RedactedKinds`는 같은 종류 순으로 종류마다 합친 것이다(`Redactions`가 없는 종류는 뺀다).
  - `History`는 `Events`를 그대로 복사하고 `Total`은 `EventTotal`이다.
  - 모든 `List`의 `Rows`는 `MaxRows`에서 자르고 `Total`은 자르기 전의 수다. `Delta`는 `Previous`가 0이면 영이다.

- [x] **Step 1: 실패하는 테스트를 쓴다** (`package report_test`, `cmp.Diff`로 비교).
  - `TestBuild_Partition`: 표 형태. 오탐(프론트엔드 파일 포함)은 `FalsePositives`, 미판정 프론트엔드와 "구조 변경 필요" 보류는 `Structure`, `.jsp`의 고객정보 줄(`Frontend` false, 삭제·마스킹 판정)은 `Remaining`, `HoldLater` 보류와 미판정과 외부화 판정(적용 전)은 `Remaining`에만 있다. 세 목록의 `Total` 합이 줄 수와 같다.
  - `TestBuild_Externalized`: 묶음 하나의 `ext.db.main.password`(`a.yml` 14줄, `b.xml` 31줄)와 `EXT_DB_MAIN_PASSWORD`(`deploy.sh` 3줄)는 항목 둘이고 자리가 파일과 줄 순이다. `remove-file` 항목은 `Removed`가 true이고 `Line`이 0이다. `.java` 자리 하나가 `CodeChange`면 항목도 그렇다. 그 묶음의 줄이 `AppliedBefore`면 `Reappeared`다.
  - `TestBuild_RedactedKinds`: `card` 2개 파일, `phone` 1개 파일을 주면 `RedactedKinds`가 `phone`, `card` 순이고 건수와 파일 수가 맞다.
  - `TestBuild_Caps`: 줄 `MaxRows+5`개는 `Rows` `MaxRows`개, `Total` `MaxRows`+5, `More()` 5다. 자리 `MaxPlaces+3`곳인 항목은 `Places` 50곳, `PlaceTotal` 53이다. `EventTotal`이 `MaxRows`+1,400이고 `Events`가 `MaxRows`개면 `History.More()`는 1,400이다.
  - `TestBuild_Delta`: `Previous`가 0이면 `Delta`는 영이다. 있으면 필드마다 차이이고 음수도 나온다.
  - `TestBuild_Empty`: 영 값 `Data`는 패닉 없이 모든 목록의 `Total`이 0이다.
  - `TestBuild_KeepsNoInput`: `Build` 뒤에 `Data`의 `Lines`, `Changes`, `Events`와 `Detail` 맵을 고쳐도 `Report`가 바뀌지 않는다.
  - `TestTypes_HoldNoValueField`: `reflect`로 `Data`와 `Report`에서 닿는 이 패키지의 모든 구조체 타입을 돌며 이름이 `Preview`, `Fingerprint`, `Context`, `Value`인 필드가 없음을 확인한다.
- [x] **Step 2:** `go test ./internal/report/` 가 컴파일 오류로 실패하는 것을 본다.
- [x] **Step 3:** `internal/report/report.go`에 위의 타입과 `Build`를 쓴다.
- [x] **Step 4:** `go test -race ./internal/report/` 가 통과한다.
- [x] **Step 5:** 결정 기록 0042를 쓴다(검토 항목 2~9의 내용과 다른 안, 다시 적용한 자리가 두 번 실리는 한계). `AGENTS.md` Security 절의 첫 항목 끝에 한 줄을 더한다: "A report and its PDF carry neither the fingerprint nor the preview." `CHANGELOG.md`의 `[Unreleased]`에 한 줄을 더한다.
- [x] **Step 6:** `make check`가 통과한 뒤 커밋한다(`feat(report): shape the report of a scan`).

### Task 2: 리포트 자료 조회

**Files:**
- Create: `internal/db/migrations/0008_report_indexes.sql`, `internal/db/report.go`, `internal/db/report_test.go`
- Modify: `internal/db/export_test.go`(seed 도우미: 스캔의 `requested_at`과 상태를 정해 넣기, `apply_run`의 `started_at`·`finished_at`을 NULL을 포함해 정해 넣기, `at`을 정한 `audit_event` 넣기, 항목의 `location`과 `finding`과 `bundle` 행 지우기. 시각은 모두 Go에서 정해 넣어 데이터베이스 시계와 섞지 않는다), `CHANGELOG.md`, `docs/next-steps.md`(`apply_run.project_id` 인덱스 항목 체크)

**Interfaces:**
- Consumes: Task 1의 `report.Data`, `report.Totals`, `report.Line`, `report.Change`, `report.Redaction`, `report.Event`, `report.MaxRows`.
- Produces:

  ```go
  // Report returns what the database tells about the scan with the given ID
  // for its report, read in one snapshot. It fails with an error matching
  // ErrNotFound when the scan is unknown. For a scan that has not succeeded
  // it returns only Project, Scan and Latest.
  func (s *Store) Report(ctx context.Context, id scan.ID) (report.Data, error)
  ```

- 마이그레이션 `0008`:

  ```sql
  -- The runs of a project, newest first, and the audit events of a project,
  -- newest first: what a report and the project page read.
  CREATE INDEX apply_run_project_id ON apply_run (project_id, id DESC);
  CREATE INDEX audit_event_project_id ON audit_event (project_id, id DESC);
  ```

- 읽는 규칙(검토 항목 3, 6~9):
  - `Latest`는 프로젝트의 가장 새 성공한 스캔, `Previous`는 `id`가 이 스캔보다 작은 성공한 스캔 가운데 가장 큰 것이다.
  - 찾은 묶음은 `triagelist.go`의 `scanBundles`와 같은 뜻이다(이 스캔의 `location`이 있는 `finding`의 묶음, 분류와 종류는 ID가 가장 작은 구성원의 것). 미리보기는 고르지 않는다.
  - `Totals`의 `Found`와 판정별 수는 그 묶음들의 수와 위치 수다. `Externalized`, `Redacted`는 그 프로젝트의 `apply_item` 가운데 `status = 'applied'`이고 실행의 `coalesce(started_at, finished_at)`(아래에서 "실행의 시각")이 이 스캔의 `requested_at`보다 앞서거나 실행이 이 스캔의 `apply_id`인 것(아래에서 "앞선 실행")의 `count(DISTINCT bundle_id)`와 `count(*)`다(`replace`와 `remove-file`은 외부화, `redact`는 삭제·마스킹). `PreviousTotals`는 같은 계산을 `Previous`에 한 것이다.
  - `Lines`: 비밀 값은 묶음마다 한 줄, 고객정보는 (파일, 종류, 판정, 보류 사유, 사유 글)마다 한 줄이다. `Applied`는 그 묶음의 `applied` 항목이 실행의 시각이 이 스캔의 요청보다 앞서지 않은 실행에 있으면 `after`, 아니고 앞선 실행에 있으면 `before`, 없으면 빈 값이다(고객정보 줄은 묶음 가운데 하나라도 해당하면 그 값이고 `after`가 우선한다).
  - `Changes`: 위의 외부화 항목 전부. 묶음이나 항목(`finding`)이 지워졌어도 `apply_item`의 열만으로 채운다. 이름, 파일, 줄 순이다.
  - `Redactions`: 위의 삭제·마스킹 항목을 (종류, 파일, `tracked`)로 묶은 것이다.
  - `Events`: 그 프로젝트의 `audit_event` 가운데 `action`이 `verdict`, `move`, `apply`인 것을 (`at`, `actor_subject`, `actor_name`, `action`, `detail`)로 묶어 새것부터 `report.MaxRows`줄, `EventTotal`은 묶은 줄의 전체 수다. `Bundles`는 묶인 기록 수이고, `Category`, `Kind`, `File`은 가장 작은 `bundle_id`의 묶음이 지금 가진 첫 항목의 것이다(없으면 빈 값). `move`는 `detail`의 `to`가 달라 묶이지 않는다.

- [x] **Step 1:** "구현 전에 확인할 사실"의 Task 2 항목 둘을 테스트 데이터베이스에서 확인한다. `now()`가 같지 않게 나오면 판정 이력을 묶는 열을 다시 정하고 계획을 고친다.
- [x] **Step 2: 실패하는 테스트를 쓴다.** `TEST_DATABASE_URL`이 없으면 건너뛴다. 지금의 `db` 테스트 도우미(`export_test.go`)로 프로젝트, 스캔, 항목, 판정, 적용 기록을 넣는다.
  - `TestReport_NotFound`: 없는 ID는 `ErrNotFound`.
  - `TestReport_NotSucceeded`: 대기, 실행 중, 실패한 스캔은 `Project`, `Scan`, `Latest`만 채우고 `Lines`가 없다.
  - `TestReport_Totals`: 스캔 1(묶음 6개: 외부화 판정 2, 삭제·마스킹 판정 1, 오탐 1, 보류 1, 미판정 1) → 적용(외부화 2개 묶음의 위치 3곳, 삭제 1곳이 `applied`, 1곳은 `skipped`) → 스캔 2(오탐, 보류, 미판정 묶음만 찾음). 스캔 2의 `Totals`는 `Found{3,3}`, `Externalized{2,3}`, `Redacted{1,1}`, `FalsePositive{1,1}`, `Hold{1,1}`, `Undecided{1,1}`, `Pending{0,0}`이고 `Previous`는 스캔 1, `PreviousTotals`는 `Found{6,…}`, `Externalized{0,0}`, `Pending{3,…}`다. 스캔 1의 `Totals`는 `Externalized{0,0}`이고 그 외부화 묶음의 줄은 `Applied`가 `after`다.
  - `TestReport_TotalsMatchLines`: `Lines`의 `Bundles`와 `Locations`를 판정별로 더한 값이 `Totals`의 것과 같다.
  - `TestReport_CustomerDataGrouped`: 한 파일의 전화번호 묶음 37개를 같은 사유로 삭제·마스킹 판정하면 한 줄(`Bundles` 37)이고, 그 가운데 하나의 사유 글이 다르면 두 줄이다. 줄에 `Bundle`이 0이다.
  - `TestReport_AppliedAsOf`: 이 스캔 전에 적용된 묶음을 이 스캔이 다시 찾으면 그 줄은 `before`이고 항목은 `Changes`에 있다. `skipped`, `failed`, `writing`, `planned` 항목은 어디에도 없다. `started_at`이 NULL이고 `finished_at`이 이 스캔의 요청보다 앞선 실행의 `applied` 항목도 `Changes`에 있다.
  - `TestReport_ItemOfMissingBundle`: 적용된 항목의 `finding`과 `bundle` 행을 지워도 `Changes`에 이름, 파일, 줄, 저장 경로가 그대로 있다.
  - `TestReport_Events`: 묶음 37개를 한 번의 `Decide`로 판정하면 줄 하나(`Bundles` 37, `Detail`의 판정)다. `reveal` 기록은 없다. 새것부터다. 묶인 줄이 `MaxRows`보다 많으면 `Events`는 `MaxRows`개이고 `EventTotal`은 전체다(기록을 직접 넣어 만든다).
  - `TestReport_OtherProject`: 다른 프로젝트의 묶음, 적용 항목, 기록이 하나도 섞이지 않는다.
  - `TestReport_FrontendAndShell`: `.jsp` 구성원이 있는 비밀 값 묶음은 `Frontend`, 구성원이 모두 `.sh`면 `Shell`이다. `.jsp`의 고객정보 줄은 `Frontend`가 false다.
- [x] **Step 3:** `TEST_DATABASE_URL=… go test ./internal/db/ -run TestReport` 가 실패하는 것을 본다.
- [x] **Step 4:** 마이그레이션 `0008`과 `internal/db/report.go`를 쓴다. `s.readOnly` 하나 안에서 읽는다. SQL은 이 파일에만 둔다.
- [x] **Step 5:** `make test-integration`(Step 3의 환경 변수와 함께)과 `make check`가 통과한다.
- [x] **Step 6:** `CHANGELOG.md`에 인덱스 줄을 더하고 `docs/next-steps.md`의 `apply_run.project_id` 인덱스 항목을 체크한 뒤 커밋한다(`feat(db): read the data of a scan report`).

### Task 3: 리포트 화면

**Files:**
- Create: `internal/web/handlers_report.go`, `internal/web/reportview.go`, `internal/web/templates/report.html`, `internal/web/handlers_report_test.go`
- Modify: `internal/web/server.go`, `routes.go`, `fakes_test.go`, `scanview.go`와 `templates/scan.html`, `handlers_project.go`와 `templates/project.html`, `applyrun.go`와 `templates/apply_run.html`, `assets/app.css`, `static/app.css`(`make css`), `cmd/web/main.go`, `CHANGELOG.md`

**Interfaces:**
- Consumes: `report.Build`, `db.Store.Report`.
- Produces:

  ```go
  // ReportStore reads what a scan's report shows. Report fails with an
  // error matching db.ErrNotFound when the scan is unknown.
  type ReportStore interface {
  	Report(ctx context.Context, id scan.ID) (report.Data, error)
  }
  ```

  `Stores`에 `Reports ReportStore`를 더한다. `cmd/web`은 `database`를 넣는다. 주소는 `GET /scans/{id}/report`(`usesStore`)이고 handler는 `handleReport(logger, rd, reports, now func() time.Time)`이다. `reportPath(id scan.ID) string`이 `/scans/<id>/report`를 돌려준다. 화면 자료는 `reportPage{User auth.User; View reportView; PDF string}`이고 `newReportView(r report.Report) reportView`가 만든다. `PDF`는 이 Task에서 빈 문자열이고(버튼 없음) Task 5가 채운다. 템플릿의 `{{define "report"}}`는 `reportView`만 받고 내용 전체를 `<article id="report">` 하나로 감싼다(Task 5의 PDF 문서가 같은 블록을 쓴다).

- 화면의 내용과 문구(고정):
  - 제목 `리포트 · 스캔 #N`, 위치 표시는 프로젝트 → 스캔 → 리포트.
  - 머리의 사실: 프로젝트 이름, 스캔 번호와 끝난 시각, 커밋, 규칙 버전, 도구 버전, 만든 시각. 그 아래 안내: "이 리포트에는 값, 미리보기, 주변 코드가 없습니다. 판정은 리포트를 만든 시각의 것이고, 외부화·삭제한 항목은 이 스캔이 요청되기 전에 적용된 것입니다."
  - 이후 스캔이 있으면(`Latest`가 이 스캔이 아님): "이후 스캔이 있습니다. 프로젝트의 마지막 성공한 스캔은 #M입니다."와 `/scans/M/report` 링크.
  - 검사하지 못한 것이 있으면(건너뛴 파일, 버린 결과, 스캐너 오류) 건수 한 줄과 스캔 화면의 `#unchecked` 링크.
  - `1. 요약`: 숫자 일곱 개(찾은 항목, 외부화 완료, 삭제·마스킹 완료, 오탐, 보류, 미판정, 적용 대기), 각각 "N개 · 위치 M곳"과 증감(`+3`, `-70`, `0`). 앞의 성공한 스캔이 없으면 증감 자리에 "이전 스캔 없음".
  - `2. 외부화한 항목`: 이름(`${ext.db.main.password}`, `${EXT_DB_MAIN_PASSWORD}` 모양), 분류, 종류, 저장 위치(`openbao:` + 저장 경로), 자리(`파일:줄`, 파일 제거는 `파일 · 파일 제거`, 넘치면 "외 N곳"), 표시 "코드 수정 필요", "다시 발견". 없으면 "외부화한 항목이 없습니다."
  - `3. 삭제·마스킹한 고객정보`: 종류별 합(묶음, 위치, 파일 수)과 그 아래 종류·파일별 표. 추적되지 않는 파일은 "복구 불가" 표시. 없으면 "삭제·마스킹한 고객정보가 없습니다."
  - `4. 구조 변경이 필요한 항목`: 분류, 종류, 첫 파일과 줄, 파일 수와 위치 수, 상태(미판정, 보류 사유, 판정), 사유 글. 없으면 "구조 변경이 필요한 항목이 없습니다."
  - `5. 남은 항목`: 같은 열. 상태는 `미판정`, `보류 · <사유>`, `외부화 판정 · 적용 전`, `삭제·마스킹 판정 · 적용 전`, `적용됨 · 다시 발견`, `이후 적용됨`. 없으면 "남은 항목이 없습니다."
  - `6. 오탐으로 판정한 항목`: 분류, 종류, 첫 파일과 줄, 수, 사유 글, 판정 시각. 없으면 "오탐으로 판정한 항목이 없습니다."
  - `7. 판정 이력`: 시각(UTC), 사람, 한 일(감사 화면의 표기를 따른다: `판정 · 외부화`, `판정 취소`, `묶음 옮기기`, `적용 #N`), 대상(종류와 파일, 묶인 기록이 둘 이상이면 "N건"), 이름·보류 사유·사유 글. 없으면 "판정 이력이 없습니다."
  - 잘린 목록의 끝: "처음 100줄만 싣습니다. 외 N줄."
  - 분류, 종류, 판정, 보류 사유의 표기는 지금의 `categoryLabel`, `kindLabel`, `verdictOf`, `reasonLabel`을 쓴다. 시각은 `formatTime`이다.
- 리포트 링크: 성공한 스캔의 스캔 화면 머리에 "리포트" 버튼(`btn btn-secondary`), 프로젝트 화면 스캔 목록의 성공한 줄에 "리포트" 링크, 적용 결과 화면의 확인 스캔이 성공했으면 그 옆에 "리포트" 링크.

- [x] **Step 1:** frontend-design 스킬을 불러 `docs/design/README.md`의 토큰과 `assets/app.css`의 지금 구성 요소로 리포트 화면의 배치를 정한다. 새 색과 글꼴은 만들지 않는다. 긴 경로와 사유는 표 안에서 줄을 바꾼다(`overflow-wrap: anywhere`).
- [x] **Step 2: 실패하는 테스트를 쓴다** (`fakes_test.go`에 `fakeReports`를 더한다. `NewServer`를 `run`과 같은 방식으로 부른다).
  - `TestHandleReport_OK`: 200. 본문에 일곱 절의 제목, `${ext.db.main.password}`, `openbao:secret/data/projects/3/ext.db.main.password`, `application-prod.yml:14`, "코드 수정 필요", "복구 불가", 요약의 숫자와 `-70`이 있다.
  - `TestHandleReport_Redirects`: 대기, 실행 중, 실패한 스캔은 303과 `Location: /scans/7`.
  - `TestHandleReport_NotFound`: 없는 스캔, 숫자가 아닌 ID, 0은 404.
  - `TestHandleReport_RequiresLogin`: 세션이 없으면 로그인으로 보낸다. `user` 역할만 있어도 200이다.
  - `TestHandleReport_OlderScan`: `Latest`가 9이고 스캔이 7이면 "이후 스캔이 있습니다"와 `href="/scans/9/report"`가 있고, 같으면 없다.
  - `TestHandleReport_NoPrevious`: `Previous`가 0이면 "이전 스캔 없음"이 있고 증감 숫자가 없다.
  - `TestHandleReport_Empty`: 줄이 하나도 없는 자료는 여섯 개의 "…없습니다" 문구를 모두 보인다.
  - `TestHandleReport_Capped`: `Remaining`이 `MaxRows`+5줄이면 "처음 100줄만 싣습니다. 외 5줄."이 있다.
  - `TestHandleReport_Escapes`: 파일 이름 `<img src=x onerror=alert(1)>.yml`과 사유 `</td><script>alert(1)</script>`가 이스케이프되어 나오고 `<script>alert`와 `<img src=x`가 본문에 없다.
  - `TestHandleReport_NoPreview`: 본문에 `****`가 없다.
  - `TestHandleReport_StoreError`: 저장소 오류는 500 화면이고 오류의 글이 본문에 없다.
  - `TestHandleScan_ReportLink`: 성공한 스캔의 화면에 `href="/scans/7/report"`가 있고 실패한 스캔에는 없다. 프로젝트 화면과 적용 결과 화면의 링크도 같은 방식으로 확인한다.
- [x] **Step 3:** `go test ./internal/web/ -run 'Report|ReportLink'` 가 실패하는 것을 본다.
- [x] **Step 4:** handler, `reportview.go`, `report.html`, 링크, CSS를 쓴다. `make css`로 `static/app.css`를 다시 만든다.
- [x] **Step 5:** `make check`가 통과한다.
- [x] **Step 6:** 로컬 스택(`docs/local-run.md`)을 `docker compose up -d --build web`으로 띄워 예제 프로젝트의 마지막 스캔 리포트를 브라우저(playwright)로 열고, 일곱 절이 보이는지와 1280px, 800px 폭에서 표가 넘치지 않는지 확인한다. 스택을 띄울 수 없으면 그 사실을 보고에 적고 Task 6으로 넘긴다.
- [x] **Step 7:** `CHANGELOG.md`에 줄을 더하고 커밋한다(`feat(web): show the report of a scan`).

PR 1을 만들고, 전체 검토를 거쳐 병합한다.

---

## PR 2: PDF 내보내기

### Task 4: pdf 컨테이너와 클라이언트

**Files:**
- Create: `internal/pdf/pdf.go`, `internal/pdf/pdf_test.go`, `internal/pdf/integration_test.go`, `docs/decisions/0043-pdf-converter-setup.md`
- Modify: `compose.yaml`, `Makefile`(`test-integration`의 `TEST_PDF_URL`), `docs/local-run.md`, `CHANGELOG.md`

**Interfaces:**
- Produces:

  ```go
  // Package pdf turns an HTML document into a PDF by sending it, with the
  // files it names, to a Gotenberg server.
  package pdf

  var (
  	// ErrUnavailable means the converter could not be reached or did not
  	// answer in time.
  	ErrUnavailable = errors.New("pdf converter is unavailable")
  	// ErrRejected means the converter answered with something other than
  	// a PDF of at most MaxBytes.
  	ErrRejected = errors.New("pdf converter rejected the document")
  )

  // MaxBytes is the largest PDF Convert returns.
  const MaxBytes = 64 << 20

  // File is a file the document names by Name, which has no folder part.
  type File struct {
  	Name string
  	Data []byte
  }

  // Document is what one conversion sends: the page, the footer of every
  // sheet, and the files they name.
  type Document struct {
  	Index  []byte
  	Footer []byte
  	Files  []File
  }

  // Client converts documents on one Gotenberg server.
  type Client struct{ /* unexported */ }

  // New returns a client for the server at baseURL, an absolute http or
  // https address without a path, query or user info. It contacts nothing.
  func New(baseURL string, client *http.Client) (*Client, error)

  // Convert returns the PDF of doc on A4 sheets. It fails with an error
  // matching ErrUnavailable or ErrRejected, and without sending anything
  // when a file name is empty, repeats, is index.html, header.html or footer.html, or
  // holds a slash, a backslash or a control character.
  func (c *Client) Convert(ctx context.Context, doc Document) ([]byte, error)
  ```

- 요청: `POST <baseURL>/forms/chromium/convert/html`, multipart. 양식 필드는 `paperSize` `A4`, 여백 `marginTop`·`marginLeft`·`marginRight` `0.6in`, `marginBottom` `0.8in`, `printBackground` true, `preferCssPageSize` false다(Step 1에서 확인한 9.9.1의 이름과 단위. 결정 기록 0043). 오류의 글에는 응답 본문을 넣지 않는다(상태 코드만).
- compose:
  - `pdf` 서비스: digest로 고정한 Gotenberg 이미지, `networks: [pdf]`, 호스트 포트 없음, `read_only: true`, `tmpfs: [/tmp]`, `cap_drop: [ALL]`, `no-new-privileges`, `restart: unless-stopped`, `/health` healthcheck. 옵션: Chromium 경로만, JavaScript 끔, Chromium 허용 주소는 자기 작업 폴더의 `file://`뿐, webhook과 주소에서 내려받기 끔, 요청 제한 15초(web의 변환 제한 20초보다 짧아 Gotenberg가 먼저 답한다), Chromium 미리 띄우기.
  - `networks: pdf: internal: true`. `web`은 `networks: [default, pdf]`, 환경 변수 `PDF_URL: http://pdf:3000`, `depends_on`에 `pdf: condition: service_started`.
  - `pdf-test` 서비스(`profiles: ["test"]`): 같은 이미지와 옵션, 기본 네트워크, `127.0.0.1:13000:3000`.

- [x] **Step 1:** "구현 전에 확인할 사실"의 Task 4 항목을 확인한다. Gotenberg의 공식 문서를 읽고, 이미지를 받아 임시로 띄워 `curl`로 한글 문서와 woff2를 보내 PDF를 받아 본다(받은 PDF는 scratchpad에 두고 Read로 연다). 옵션을 하나씩 켜 가며 `read_only` 등에서 뜨는지 본다. 결과와 고른 옵션, 풀어야 했던 제한을 결정 기록 0043에 쓴다. 계획과 다른 것이 있으면 계획을 고친다.
- [x] **Step 2: 실패하는 테스트를 쓴다** (`httptest.Server`가 가짜 Gotenberg다).
  - `TestConvert_SendsDocument`: 서버가 받은 요청이 `POST /forms/chromium/convert/html`이고, multipart에 `index.html`, `footer.html`, 준 파일들이 그 이름과 바이트로 있고, 양식 필드 값이 위와 같다. 서버가 `%PDF-1.7 fake`를 돌려주면 그 바이트를 그대로 받는다.
  - `TestConvert_Errors`: 표 형태. 닫힌 포트, 503, 504, context 제한 초과(서버가 답하지 않음)는 `ErrUnavailable`. 400, 500, 200인데 `%PDF-`로 시작하지 않는 본문, 200인데 `MaxBytes`를 넘는 본문(테스트는 한도를 줄일 수 있게 unexported 필드와 `export_test.go`를 쓴다)은 `ErrRejected`. 어느 오류의 글에도 서버가 보낸 본문이 없다.
  - `TestConvert_RejectsNames`: 빈 이름, `a/b.css`, `..`, `index.html`, `header.html`, `footer.html`, 같은 이름 둘, 줄바꿈이 든 이름은 서버에 요청이 가지 않고 오류다.
  - `TestNew_BadURL`: 빈 값, `pdf:3000`, `ftp://pdf`, 경로나 query나 사용자 정보가 있는 주소는 오류다.
  - `TestConvert_Real`(`integration_test.go`): `TEST_PDF_URL`이 없으면 건너뛴다. 한글 문단과 `@font-face`(저장소의 `IBMPlexSansKR-…-Regular.woff2`), `footer.html`을 보내면 `%PDF-`로 시작하는 PDF가 온다. `<script>`와 바깥 주소의 `<img>`가 든 문서도 제한 시간 안에 PDF가 온다(실행되거나 받아 오지 않는다는 것은 Task 6에서 확인한다).
- [x] **Step 3:** `go test ./internal/pdf/` 가 컴파일 오류로 실패하는 것을 본다.
- [x] **Step 4:** `internal/pdf/pdf.go`를 쓴다. 응답은 `io.LimitReader`로 `MaxBytes+1`까지만 읽는다.
- [x] **Step 5:** `compose.yaml`에 `pdf`, `pdf-test`, 네트워크를 더하고 `Makefile`의 `test-integration`이 `TEST_PDF_URL`을 요구하고 넘기게 한다. `docker compose --profile test up -d pdf-test` 뒤 `TEST_PDF_URL=http://127.0.0.1:13000 go test ./internal/pdf/ -run Real -count=1`이 통과한다.
- [x] **Step 6:** `docs/local-run.md`에 pdf 서비스와 `TEST_PDF_URL`을 적고, `CHANGELOG.md`에 줄을 더한다. `make check`가 통과한 뒤 커밋한다(`feat(pdf): convert a document through gotenberg`).

### Task 5: PDF 내보내기

**Files:**
- Create: `internal/web/printassets.go`, `internal/web/printassets_internal_test.go`
- Modify: `internal/web/handlers_report.go`, `handlers_report_test.go`, `templates/report.html`, `render.go`, `server.go`, `routes.go`, `fakes_test.go`, `assets/app.css`, `static/app.css`, `cmd/web/config.go`, `config_test.go`, `main.go`, `main_test.go`, `CHANGELOG.md`

**Interfaces:**
- Consumes: `pdf.Document`, `pdf.File`, `pdf.ErrUnavailable`, `pdf.ErrRejected`, `pdf.New`, Task 3의 `report` 블록과 `reportView`.
- Produces:

  ```go
  // Converter turns a document into a PDF. Convert fails with an error
  // matching pdf.ErrUnavailable when the converter cannot be reached or is
  // too slow, and pdf.ErrRejected when it does not return a PDF.
  type Converter interface {
  	Convert(ctx context.Context, doc pdf.Document) ([]byte, error)
  }

  // Limits bounds the calls one request makes: Store the database and the
  // secret store, PDF one conversion.
  type Limits struct {
  	Store, PDF time.Duration
  }

  func NewServer(logger *slog.Logger, authn Authenticator, sessions SessionStore, attempts AttemptStore,
  	stores Stores, suggester Suggester, originals Originals, converter Converter, limits Limits) http.Handler
  ```

  `cmd/web`: 환경 변수 `PDF_URL`(필수, `config.pdfURL`, `String`·`LogValue`에 `pdf_url`), `pdf.New(cfg.pdfURL, &http.Client{Timeout: 25 * time.Second})`, `web.Limits{Store: databaseLimit, PDF: 20 * time.Second}`.

  ```go
  // printFiles returns the files a PDF document names: app.css with every
  // font address turned into the bare file name, and the fonts it names. NewServer
  // calls it once, before it serves anything, and it panics when the
  // stylesheet names a font a does not hold.
  func printFiles(a *assets) []pdf.File

  // render returns the template tmpl of the named page drawn with data.
  func (rd *renderer) render(name, tmpl string, data any) ([]byte, error)
  ```

- 주소: `GET /scans/{id}/report.pdf`, `signedIn`만 건다(handler가 DB 읽기에 `limits.Store`, 변환에 `limits.PDF`를 따로 건다). 흐름: 자료 읽기 → 성공하지 않은 스캔이면 303 → `report.Build` → `print`와 `print-footer`를 버퍼에 그린다 → `Convert` → 성공하면 `Content-Type: application/pdf`, `Content-Disposition: attachment; filename="report-project-<프로젝트 ID>-scan-<스캔 ID>.pdf"`, `Content-Length`와 함께 쓴다. `pdf.ErrUnavailable`은 503과 "PDF 변환 서비스에 연결할 수 없습니다. 잠시 뒤 다시 시도해 주세요.", 그 밖의 오류는 500 화면이다. 오류는 handler에서 한 번 로그로 남긴다.
- 템플릿: `{{define "print"}}`는 `<!doctype html>`, `lang="ko"`, `<meta charset="utf-8">`, 제목, `<link rel="stylesheet" href="app.css">`, `<body class="print">` 안에 `{{template "report" .}}`만 있다. 머리 띠, 메뉴, 양식, 스크립트가 없다. `{{define "print-footer"}}`는 글자 크기를 정한 자기 `<style>`과 `secret-externalizer | scan #N | <span class="pageNumber"></span> / <span class="totalPages"></span>`만 있는 온전한 HTML 문서다(ASCII만).
- CSS: `body.print`와 `@media print`에서 바탕을 흰색으로, 본문 폭을 쪽에 맞추고, 표의 줄과 카드가 쪽 사이에서 잘리지 않게(`break-inside: avoid`) 하고, 절 제목이 쪽 끝에 홀로 남지 않게(`break-after: avoid`) 한다. 링크는 밑줄 없는 본문 색이다.
- 리포트 화면의 `reportPage.PDF`를 `/scans/<id>/report.pdf`로 채워 머리에 "PDF로 내보내기" 버튼(`btn btn-primary`, 일반 링크)을 보인다.

- [x] **Step 1: 실패하는 테스트를 쓴다** (`fakes_test.go`에 받은 `Document`를 기억하는 `fakeConverter`를 더한다).
  - `TestHandleReportPDF_OK`: 200, `Content-Type: application/pdf`, `Content-Disposition: attachment; filename="report-project-3-scan-7.pdf"`, 본문은 가짜 변환기가 준 바이트 그대로다.
  - `TestHandleReportPDF_Document`: 변환기가 받은 `Index`에 일곱 절의 제목과 `href="app.css"`가 있고, `/static/`, `<script`, `<form`, `<nav`, `****`가 없다. `Footer`에 `scan #7`, `pageNumber`, `totalPages`가 있다. `Files`는 `app.css`와 woff2 다섯 개이고 `app.css`에 `url(fonts/`가 없다. 파일 이름 `<img src=x onerror=alert(1)>.yml`이 `Index`에 이스케이프되어 있다.
  - `TestHandleReportPDF_SameAsPage`: 같은 자료로 그린 리포트 화면과 `Index`에서 `<article id="report">`부터 `</article>`까지가 같다.
  - `TestHandleReportPDF_ConverterDown`: 변환기가 `pdf.ErrUnavailable`이면 503, `text/html`, 안내 문구가 있고 `Content-Disposition`이 없다.
  - `TestHandleReportPDF_Rejected`: `pdf.ErrRejected`면 500 화면이다.
  - `TestHandleReportPDF_Deadline`: `Limits.PDF`가 50ms이고 변환기가 context가 끝날 때까지 기다리면(`ctx.Err()`를 `pdf.ErrUnavailable`로 감싸 돌려준다) 503이고, 변환기가 받은 context에 제한이 걸려 있다. `time.Sleep`을 쓰지 않는다.
  - `TestHandleReportPDF_Redirects`, `_NotFound`, `_RequiresLogin`: 화면과 같다. 변환기는 불리지 않는다.
  - `TestHandleReport_PDFButton`: 리포트 화면에 `href="/scans/7/report.pdf"`와 "PDF로 내보내기"가 있다.
  - `TestPrintFiles`(internal): 글꼴 다섯 개와 `app.css`를 돌려주고, 스타일시트가 없는 글꼴을 가리키면 패닉한다.
  - `cmd/web`: `PDF_URL`이 없으면 빠진 변수 목록에 나오고, `pdf:3000` 같은 값은 `checking PDF_URL` 오류다. `config`의 `String`에 `pdf_url`이 있다.
- [x] **Step 2:** `go test ./internal/web/ ./cmd/web/` 가 실패하는 것을 본다.
- [x] **Step 3:** `Limits`, `Converter`, `printFiles`, `render`, handler, 템플릿, CSS, `cmd/web`을 쓴다. `NewServer`를 부르는 모든 테스트를 새 서명에 맞춘다. `make css`.
- [x] **Step 4:** `make check`가 통과한다.
- [x] **Step 5:** `CHANGELOG.md`에 줄을 더하고 커밋한다(`feat(web): export the report of a scan as a PDF`).

### Task 6: 실제 확인과 문서

**Files:**
- Create: `docs/decisions/0044-decisions-made-while-building-plan-6.md`
- Modify: `docs/local-run.md`, `CHANGELOG.md`, `docs/next-steps.md`, 확인에서 나온 결함의 파일

- [ ] **Step 1:** `docs/local-run.md`대로 전체 스택을 다시 빌드해 띄운다(`docker compose up -d --build`). OpenBao 정책을 바꾸지 않았으므로 볼륨은 지우지 않는다. 예제 프로젝트(`legacy-shop`의 git 작업본)에 스캔, 판정, 적용, 확인 스캔이 있는 상태로 만든다(없으면 `docs/local-run.md`의 순서로 만든다).
- [ ] **Step 2:** 아래 표를 브라우저(playwright)와 명령으로 확인하고, 결과를 이 표에 적는다. 화면과 PDF의 그림은 scratchpad에 남기고 경로를 보고에 적는다.

  | 번호 | 확인할 것 | 결과 |
  |---|---|---|
  | 1 | 성공한 스캔의 화면, 프로젝트의 스캔 목록, 적용 결과 화면에 리포트 링크가 있고, 실패한 스캔의 리포트 주소는 스캔 화면으로 간다 | |
  | 2 | 확인 스캔의 리포트 요약의 오탐, 보류, 미판정 수가 판정 화면 탭의 수와 같고, 찾은 항목 = 오탐 + 보류 + 미판정 + 적용 대기다 | |
  | 3 | 2절에 적용한 이름마다 저장 위치와 `파일:줄`이 있고, 그 줄을 소스에서 열면 그 placeholder가 있다. `.java`의 자리에 "코드 수정 필요"가 있다. 제거한 키 파일은 "파일 제거"다 | |
  | 4 | 3절의 종류별 건수가 적용 결과 화면의 삭제·마스킹 건수와 맞고, 추적되지 않는 파일에 "복구 불가"가 있다 | |
  | 5 | 4절에 JSP의 항목이 있고, 5절과 6절에 같은 묶음이 겹쳐 나오지 않는다. 7절에 판정과 적용 확인이 새것부터 있고 고객정보 일괄 판정이 한 줄이다 | |
  | 6 | 적용 전 스캔의 리포트에 "이후 스캔이 있습니다"가 있고 외부화 판정 줄이 "이후 적용됨"이다. 긴 경로가 있는 줄이 표 밖으로 나가지 않는다 | |
  | 7 | "PDF로 내보내기"로 받은 파일이 A4이고, 한글이 IBM Plex Sans KR로, 경로와 이름이 D2Coding으로 그려지고, 바닥글에 스캔 번호와 쪽 번호가 있고, 표의 줄이 쪽 사이에서 잘리지 않는다(PDF를 Read로 연다) | |
  | 8 | 리포트 HTML과 PDF에서 뽑은 글(`pdftotext`가 있으면 그것으로)에 예제의 가짜 값, 미리보기(`****`), 지문이 없다. web과 pdf의 로그에도 없다 | |
  | 9 | `docker compose stop pdf` 뒤 PDF 내보내기는 503 안내 화면이고, 리포트 화면과 다른 화면은 그대로 동작한다. `start` 뒤 다시 된다 | |
  | 10 | pdf 컨테이너에 호스트 포트가 없고, `pdf` 네트워크에 붙인 임시 컨테이너에서 바깥 주소와 `postgres`, `openbao`에 닿지 않으며, web에서는 `pdf:3000/health`에 닿는다 | |
  | 11 | `dev-user`(admin 아님)로 리포트와 PDF를 볼 수 있다 | |
  | 12 | 예제 크기에서 리포트 화면과 PDF에 걸린 시간을 적는다 | |

- [ ] **Step 3:** 확인에서 나온 결함은 실패하는 테스트를 먼저 쓰고 고친다. 고치지 않고 넘기는 것은 `docs/next-steps.md`에 적는다.
- [ ] **Step 4:** 결정 기록 0044를 쓴다(0041의 형식: 정한 것, 이유, 틀렸을 때). 검토 항목 16개를 권고안으로 확정한 것과 구현하면서 바꾼 것을 모은다.
- [ ] **Step 5:** `docs/next-steps.md`를 고친다. 표의 계획 6을 완료로 바꾸고, "지금 상태" 줄을 계획 7로 옮기고, "계획 6에서 남은 일" 절에 넘긴 것을 적는다(적어도: 리포트는 저장되지 않아 지난 스캔의 판정을 그때의 것으로 볼 수 없다, 절마다 100줄 한도, 바닥글에 프로젝트 이름이 없다, PDF 변환의 동시 요청 한도가 없다, pdf 말고는 네트워크가 나뉘지 않았다, 계획 7이 추이를 그리려면 스캔별 건수의 스냅숏이 필요한지 정해야 한다). `CHANGELOG.md`를 마무리한다.
- [ ] **Step 6:** `make check`와 `make test-integration`이 통과한 뒤 커밋한다(`docs: close plan 6`).

PR 2를 만들고, 전체 검토를 거쳐 병합한다.

## 이 계획이 하지 않는 것

- 대시보드와 추이(계획 7). 요약의 숫자를 세는 규칙(`report.Totals`)은 계획 7이 그대로 쓸 수 있다.
- 리포트의 저장, 버전, 비교. PDF의 서명이나 암호.
- 리포트에서 판정이나 적용을 바꾸는 일. 리포트는 읽기 전용이다.
- 프로젝트 여러 개를 한 문서로 묶은 리포트.
