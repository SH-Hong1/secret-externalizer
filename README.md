# secret-externalizer

레거시 소스에 박혀 있는 민감 정보를 소스 밖으로 빼내는 작업 도구다. 개발자가 로컬 소스를 스캔하고, 찾은 항목을 하나씩 판정하고, 판정한 값을 placeholder로 바꿔 OpenBao에 옮기고, 무엇을 어디서 뺐는지 기록으로 남긴다. 진행 상황은 스캔별 리포트(화면과 PDF)와 대시보드로 본다.

도구가 맡는 일은 찾고, 사람이 판정하게 하고, 소스에서 빼내고, 기록하는 것까지다. 빼낸 값을 배포 환경에 넣는 일과 키를 폐기하고 바꾸는 일은 이 도구 밖의 일이다.

## 구성

| 서비스 | 하는 일 |
|---|---|
| web | 화면. 로그인(Keycloak), 프로젝트, 스캔, 판정, 적용 요청, 리포트, 대시보드 |
| worker | 스캔. betterleaks, opengrep, 키 파일 탐지를 돌리고 결과를 저장한다. 작업 공간을 읽기 전용으로 본다 |
| applier | 점검과 적용. 판정한 값을 OpenBao에 쓰고 소스의 그 자리를 placeholder로 바꾼다 |
| pdf | 리포트를 PDF로 바꾸는 변환기(Gotenberg) |
| OpenBao | 지문(HMAC), 판정 전 원본, 외부화한 값 |
| postgres | 프로젝트, 스캔 결과, 판정, 감사 기록, 작업 대기열 |
| valkey | 로그인 세션 |

자세한 설계는 [설계 문서](docs/design/secret-externalizer.md)에 있다.

## 빠른 시작

Docker Engine 26 이상과 Compose가 필요하다. 처음 한 번 OpenBao가 쓰는 호스트 파일을 만들고, 스캔할 프로젝트들이 있는 폴더를 `SE_WORKSPACE`로 준다.

```sh
make openbao-host-files
export SE_WORKSPACE=$HOME/work
docker compose up --build -d
```

`http://localhost:8080/`을 열고 개발용 계정으로 로그인한다. 계정, 포트, 스캔과 적용을 끝까지 해 보는 방법, 문제가 생겼을 때 할 일은 [로컬에서 띄우기](docs/local-run.md)에 있다.

## 개발 준비

Go(`go.mod`의 버전), Docker, `gh`가 필요하다. 새로 받은 저장소에서 한 번 실행한다.

```sh
make setup      # 도구(gofumpt, golangci-lint, tailwindcss)를 bin/에 설치하고 git hook을 켠다
make scanners   # 스캐너(betterleaks, opengrep)를 bin/에 받는다. 없으면 실제 스캐너 테스트가 건너뛰어진다
```

`bin/`은 git에 올라가지 않는다. 도구와 스캐너의 버전은 `Makefile`과 `deploy/worker/scanners.lock`에 고정되어 있다.

## 검사

| 명령 | 하는 일 |
|---|---|
| `make check` | 린트, `go vet`, 테스트(`-race`), hook과 스크립트 테스트, CSS 빌드 확인. 커밋할 때 `pre-commit` hook이 실행한다 |
| `make test-integration` | postgres, valkey, OpenBao, PDF 변환기를 쓰는 테스트까지 돌린다. 띄우는 방법은 [로컬에서 띄우기](docs/local-run.md)의 "통합 테스트" |
| `make sample-check` | 공개 예제(eGovFrame 웹 예제)를 받아 스캔, 적용, 확인 스캔까지 돌리고 찾은 목록을 기대 목록과 비교한다. `TEST_DATABASE_URL`, `TEST_BAO_ADDR`가 필요하다 |
| `make fmt` | gofumpt와 goimports로 포맷한다 |

PR마다 CI가 `check`, `integration`, `pull-request`(PR 제목과 `Closes #N`), `review`(AGENTS.md 규칙을 Claude가 검토)를 돌린다. 넷이 통과해야 `main`에 병합된다. 공개 예제를 돌리는 `sample`도 돌지만 필수 체크는 아니다.

## 일하는 방식

1. 할 일은 [GitHub Issue](https://github.com/SH-Hong1/secret-externalizer/issues)다. 묶음은 [Milestone](https://github.com/SH-Hong1/secret-externalizer/milestones), 진행 상황은 [Project](https://github.com/users/SH-Hong1/projects/2)에서 본다.
2. Issue 하나에 브랜치 하나를 만든다.
3. 커밋 메시지는 Angular 형식이고 이슈를 가리킨다. `commit-msg` hook이 확인한다.

   ```
   fix(scan): keep the context lines of a decoded finding

   Refs #12
   ```

4. PR 제목은 커밋 메시지 형식(영어), 본문은 한국어로 쓰고 `Closes #12`로 끝낸다.
5. `gh pr merge --auto --squash`로 자동 병합을 건다. 체크가 모두 통과하면 스쿼시 병합되고 Issue가 닫힌다.
6. AI 검토가 위반이 아닌 것을 지적하면 `review: override` 라벨을 붙인다. 라벨을 붙이면 `review` 작업만 다시 돈다. Security 절 위반은 라벨로 통과시킬 수 없다.

규칙 전체는 [AGENTS.md](AGENTS.md)에 있다.

## 문서

| 문서 | 내용 |
|---|---|
| [docs/design/secret-externalizer.md](docs/design/secret-externalizer.md) | 설계 문서 |
| [docs/adr/](docs/adr/) | 결정 기록(ADR) |
| [docs/design/screens/](docs/design/screens/) | 화면 디자인 시안 |
| [docs/local-run.md](docs/local-run.md) | 로컬 스택, 개발용 계정, 통합 테스트 |
| [CHANGELOG.md](CHANGELOG.md) | 변경 내역 |
| [AGENTS.md](AGENTS.md) | 코드, 커밋, 작업 규칙 |
