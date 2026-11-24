# 스캔과 적용의 AppRole을 나누고, 역할과 정책을 기동마다 맞춘다

- 날짜: 2026-11-24
- 상태: 확정

## 배경

결정 0068로 스캔(worker)과 적용(applier)을 다른 컨테이너로 나눴지만 두 서비스는 같은 AppRole(`se-worker`)과 secret-id를 썼다. 스캐너는 worker 컨테이너에서 secret-id 파일을 읽을 수 있으므로, 잘못 만들어졌거나 바뀐 스캐너가 외부화한 값(`secret/data/projects/*`)을 읽고 덮어쓸 수 있었다.

역할을 나누려면 정책을 바꿔야 한다. 그런데 정책과 역할은 self-init(`config.hcl`의 `initialize`)이 첫 기동 때 한 번만 만들고 루트 토큰은 폐기된다. 봉인이 static이라 복구 키도 없으므로(`Total Recovery Shares 0`, `generate-root`는 403) 이미 초기화된 저장소의 정책을 바꿀 방법이 없었다. 볼륨을 지우면 적용을 한 환경에서는 추적되지 않는 파일에서 빼낸 값의 유일한 사본이 사라진다.

## 결정

### 역할 셋과 정책

정책은 `deploy/openbao/policies/<역할>.hcl` 파일로 둔다. 통합 테스트도 같은 파일을 읽어 그 권한으로 돈다.

| 역할 | role-id | 허용 |
|---|---|---|
| `worker`(스캔) | `se-worker` | 지문 계산, 원본 보관(`pending` 쓰기), 원본 목록과 삭제. `projects`에는 닿지 못한다 |
| `applier`(적용) | `se-applier` | 지문 계산, 외부화한 값 쓰기와 다시 읽기, 정리된 원본 삭제(`metadata/pending` 삭제만) |
| `web` | `se-web` | 원본과 외부화한 값 읽기. 바뀌지 않았다 |

- 적용이 원본을 지우는 것은 적용 뒤 오탐으로 확정된 값의 원본을 정리하기 때문이다(`apply` 실행의 마지막 단계). 목록, 읽기, 쓰기는 주지 않는다.
- compose의 applier는 `openbao-secrets` 볼륨의 `applier` 폴더만 마운트한다. `make-secrets.sh`가 그 폴더의 secret-id와 CA를 만든다.

### 관리 역할과 `openbao-roles`

- self-init은 마운트, transit 키, AppRole 인증, KV 설정과 관리 역할 `admin`(role-id `se-admin`)만 만든다.
  - `admin` 정책은 모든 ACL 정책과 모든 AppRole 역할을 쓰고 지울 수 있다. 자기 정책과 자기 역할(`auth/approle/role/admin`, 그 아래)은 거부한다. 토큰은 5분짜리다.
  - secret-id는 호스트 파일(`~/.config/secret-externalizer/openbao-admin.secret-id`, `OPENBAO_ADMIN_SECRET_ID_FILE`)이다. `make openbao-host-files`가 봉인 해제 키와 함께 만들고, compose가 openbao와 `openbao-roles`에만 넘긴다. self-init이 이 파일로 `custom-secret-id`를 등록한다.
- 새 서비스 `openbao-roles`(openbao 이미지, uid 100, 내부 네트워크 `bao`)가 `docker compose up` 때마다 OpenBao가 열린 뒤 `deploy/openbao/roles.sh`를 실행한다.
  - 관리 역할로 로그인해 역할마다 정책, 역할 설정, 고정 role-id를 쓰고, 볼륨의 secret-id가 등록되어 있지 않으면 등록한다. 그 역할의 다른 secret-id는 모두 지운다.
  - 선언하지 않은 역할과 정책은 지운다. `admin`, `default`, `root` 정책과 `admin` 역할은 남긴다.
  - 끝나면 토큰을 폐기한다. secret-id는 표준 입력으로만 `bao`에 넘기고 인자나 출력에 나오지 않는다.
  - 파일이 하나라도 없으면 아무 요청도 보내기 전에 멈춘다.
- worker와 applier는 `openbao-roles`가 성공한 뒤에 뜬다. web은 기다리지 않는다. 원래 OpenBao를 기다리지 않고, 원본 보기를 할 때 로그인한다.

### 이전 저장소

관리 역할이 없는 저장소는 바꿀 방법이 없으므로, 한 번은 두 볼륨을 지워야 한다. `docs/local-run.md`의 "역할을 나누기 전에 만든 저장소"에 볼륨을 묶어 두는 법과, 지우기 전에 이전 `worker` 역할로 외부화한 값을 읽어 두는 법을 적었다. `make openbao-host-files`는 `openbao-data` 볼륨이 이미 있는데 secret-id 파일이 없으면 새로 만들지 않고 멈춘다. 새 secret-id로는 그 저장소의 관리 역할로 로그인할 수 없기 때문이다.

## 이유

- 스캐너가 읽을 수 있는 secret-id에 외부화한 값의 권한이 없으면, 스캐너가 무엇을 하든 외부화한 값을 읽거나 바꾸지 못한다. 적용 쪽은 스캐너를 실행하지 않는다.
- 정책을 파일로 두고 기동마다 맞추면 정책을 바꾸는 데 볼륨을 지우지 않아도 된다. 실제로 정책 파일을 넓혔다가 되돌리며 `up`만 다시 해 권한이 바뀌는 것을 확인했다.
- 관리 역할의 토큰은 어떤 권한이든 줄 수 있으므로 루트와 같다. 그래서 secret-id를 볼륨이 아니라 호스트 파일로 두었다. `openbao-secrets` 볼륨에 접근하는 사람은 지금도 역할 secret-id로 값을 읽을 수 있지만, 정책을 바꾸거나 버전을 지우는 권한까지는 얻지 못한다. 호스트 파일을 읽을 수 있는 사람은 이미 봉인 해제 키도 읽을 수 있으므로(결정 0067) 새로 생기는 위험은 없다.
- 셸 스크립트 하나와 OpenBao 이미지의 `bao` CLI로 끝나 새 의존성이 없다.

## 알려진 한계

- worker는 여전히 원본(`pending`)을 덮어쓰거나 지울 수 있다. 스캔이 원본을 보관하고 정리하는 데 필요한 권한이다. 원본은 판정 전 확인용 사본이므로 지워져도 소스의 값은 남는다.
- 관리 역할의 secret-id 파일을 잃으면 정책과 역할을 바꿀 수 없다. 이미 있는 역할은 계속 쓴다. 다시 바꾸려면 볼륨을 지워야 한다.
- 역할을 더하거나 빼려면 `roles.sh`의 목록, 정책 파일, compose의 마운트, `make-secrets.sh`의 목록을 함께 바꿔야 한다.
- `WORKER_JOBS=all`로 worker 하나를 띄우면 두 정책의 권한이 모두 필요하다. compose에는 그런 서비스가 없다.
- 이 기계의 로컬 스택은 아직 이전 저장소다. 볼륨 삭제는 사용자가 정한다. 새 저장소에서의 동작은 따로 띄운 compose 프로젝트(새 볼륨)로 확인했다.

## 검토한 다른 방법

- **이미 초기화된 저장소에서 루트 토큰을 다시 만든다(`generate-root`).** static 봉인과 self-init에서는 복구 키가 0개이고 엔드포인트도 403이라 쓸 수 없었다.
- **관리 역할의 secret-id를 `openbao-secrets` 볼륨에 둔다.** 사람이 할 일이 하나 줄지만, 볼륨에 접근한 사람이 정책을 바꾸고 버전을 지울 수 있게 된다.
- **정책만 바꾸고 역할은 self-init에 남긴다.** 새 역할(`applier`)을 더하는 것부터 볼륨 삭제가 필요해 지금의 문제가 그대로 남는다.
- **Go 코드에 정책 맞추기를 넣는다(web이나 worker가 기동 때).** 서비스 중 하나가 루트와 같은 권한을 늘 가지게 된다. 한 번 실행되고 끝나는 컨테이너가 더 좁다.
