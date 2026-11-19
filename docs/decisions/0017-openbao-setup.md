# 로컬 OpenBao는 static 봉인과 self-init으로 띄운다

- 날짜: 2026-11-19
- 상태: 확정

## 배경

원본 값은 OpenBao에만 둔다(0003). 개발자 PC에서 `compose up` 한 번으로 바로 쓸 수 있고, 다시 띄워도 봉인이 풀린 채로 뜨며, 루트 토큰이 남지 않고, 저장소에 키가 없는 OpenBao가 필요하다. OpenBao 2.7에는 `file` 저장소가 없다.

## 결정

- 이미지는 구현 시점의 최신 안정판인 `openbao/openbao:2.7.2`를 태그와 digest로 고정한다.
- 저장소는 `storage "pebbledb"`이고, 이름 있는 볼륨 `openbao-data`를 이미지에서 openbao 사용자의 폴더인 `/openbao/file`에 마운트한다.
- 봉인은 `seal "static"`이다. 키는 `openbao-secrets` 볼륨의 `unseal/key` 파일(난수 32바이트)이다.
- 한 번만 실행되는 `openbao-secrets` 컨테이너(`deploy/openbao/make-secrets.sh`)가 볼륨에 파일이 없을 때만 봉인 해제 키와 두 AppRole의 secret-id(난수 32바이트의 16진수)를 만든다. 이미 있는 파일은 바꾸지 않는다.
- 초기 설정은 `deploy/openbao/config.hcl`의 `initialize` 블록(self-init)으로 한다. 첫 기동 때 한 번만 실행되고, 쓴 루트 토큰은 끝나면 폐기된다.
  - transit 마운트와 키 `fingerprint`, KV v2 마운트 `secret`, AppRole 인증, 정책 `worker`·`web`, 역할 `worker`·`web`을 만든다.
  - role-id는 고정값(`se-worker`, `se-web`)이다. compose가 `BAO_ROLE_ID`로 넘긴다.
  - secret-id는 `custom-secret-id` 요청의 `secret_id`를 `eval_source = "file"`로 볼륨의 파일에서 읽어 지정한다. secret-id는 만료와 사용 횟수 제한이 없고, 토큰은 TTL 1시간이다.
- audit device는 self-init이 아니라 설정 파일의 `audit "file" "stdout"` 블록으로 선언한다.
- 지문용 transit 키는 하나(`fingerprint`)이고, 모든 프로젝트가 같이 쓰며 회전하지 않는다. 요청마다 `key_version: 1`을 지정한다. worker 정책에는 `rotate`와 키 읽기 권한이 없다.
- `web` 역할과 `projects/` 경로도 지금 만든다. self-init은 한 번만 실행되므로 나중에 더하려면 볼륨을 지워야 하기 때문이다. 이 계획에서 web은 OpenBao에 접속하지 않는다.
- 실제 OpenBao는 호스트에 포트를 열지 않는다. 통합 테스트는 compose `test` 프로필의 `-dev` 모드 `openbao-test`(`127.0.0.1:18200`, 메모리 저장소)를 쓰고, 테스트가 개발용 루트 토큰으로 같은 마운트와 정책을 만든다.
- 컨테이너 보안 설정:
  - `openbao`, `openbao-test`: 이미지의 openbao 사용자(`100:1000`), 읽기 전용 루트, `cap_drop: ALL`, `no-new-privileges`. 이미지의 진입점 스크립트는 root로 폴더 소유자를 바꾼 뒤 사용자를 바꾸므로 쓰지 않고 `bao server`를 바로 실행한다. `IPC_LOCK` 권한이 없으므로 `disable_mlock = true`로 둔다.
  - `openbao-secrets`: 파일 소유자를 정하려면 root여야 한다. 권한은 `CHOWN`, `DAC_OVERRIDE`, `FOWNER`만 남긴다. `DAC_OVERRIDE`는 두 번째 실행 때 openbao 사용자 소유의 `unseal/` 폴더 안을 확인하는 데 필요하다. 읽기 전용 루트와 `no-new-privileges`는 적용한다.
- worker는 `openbao-secrets` 볼륨의 `worker` 하위 폴더만 `/run/secrets/openbao`에 읽기 전용으로 마운트한다(compose의 `volume.subpath`). 그래서 봉인 해제 키와 web의 secret-id를 볼 수 없다. 파일은 소유자 `65532`, 그룹 `1000`(openbao), 모드 `0440`이라 worker와 OpenBao만 읽는다.
- 리스너는 TLS 없이 compose 네트워크 안에서만 쓴다.

## 이유

- static 봉인과 self-init을 함께 쓰면 사람이 봉인 해제 키나 루트 토큰을 다루지 않아도 된다. 루트 토큰은 self-init이 끝나면 폐기되고 복구 키도 만들어지지 않는다.
- 키와 secret-id를 기동 시 난수로 만들면 저장소에는 비밀이 하나도 없다.
- `file` 저장소가 없어졌으므로, 단일 노드에 맞는 내장 저장소인 pebbledb를 쓴다.
- 확인 결과 OpenBao 2.7은 API(`sys/audit/...`)로 audit device를 만드는 요청을 `invalid request`로 거부한다. 그래서 self-init의 요청으로 만들 수 없고, 설정 파일의 선언형 `audit` 블록으로 옮겼다. 이 블록은 따로 실행되는 컨테이너 없이 같은 설정 파일 안에 있다.
- 지문은 스캔과 프로젝트를 넘어 같은 값을 같은 지문으로 묶는 데 쓰므로, 키 버전이 바뀌면 안 된다.

## 알려진 한계

봉인 해제 키가 데이터와 같은 Docker 볼륨 영역에 있다. 그래서 저장 시 암호화는 Docker 볼륨에 접근할 수 있는 사람으로부터 데이터를 지키지 못한다. 이 구성은 개발자 PC 한 대에서만 쓴다. 공유하거나 배포하는 환경에 올리기 전에 봉인 방식을 바꾼다(`docs/next-steps.md`).

self-init은 첫 기동 때만 실행된다. `config.hcl`의 `initialize` 블록을 고친 뒤에는 `openbao-data`와 `openbao-secrets` 볼륨을 지우고 다시 띄워야 반영된다. 그러면 보관한 원본도 모두 사라진다.

## 검토한 다른 방법

- shamir 봉인과 수동 init/unseal: 다시 띄울 때마다 사람이 봉인을 풀어야 하고, 루트 토큰과 키 조각을 어딘가에 보관해야 한다.
- `-dev` 모드: 메모리 저장소라 다시 띄우면 원본이 사라지고, 루트 토큰이 알려진 값으로 남는다. 통합 테스트에만 쓴다.
- 초기 설정을 하는 별도 컨테이너(`bao` CLI로 init 후 설정): 루트 토큰을 셸 스크립트가 다뤄야 한다. self-init으로 되지 않는 것이 audit device뿐이었고, 그것은 설정 파일로 해결되어 필요 없었다.
- 봉인 해제 키를 저장소에 커밋: 저장소에 키를 두지 않는다는 원칙에 어긋난다.
