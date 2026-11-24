# OpenBao의 봉인 해제 키는 호스트 파일에 두고, 리스너는 TLS만 받는다

- 날짜: 2026-11-24
- 상태: 확정

## 배경

결정 0017은 static 봉인의 키를 `openbao-secrets` 볼륨의 `unseal/key`에 두었다. 데이터(`openbao-data`)와 키가 같은 Docker 볼륨 영역에 있으므로, 볼륨 영역을 복사한 사람(VM 디스크, 볼륨 백업)은 저장 시 암호화를 풀 수 있었다. 또 리스너가 TLS 없이 열려 있어, `bao` 네트워크에서 web과 worker의 요청(AppRole secret-id, 토큰, 원본 값)이 평문으로 오갔다. 남은 일에는 "공유하거나 배포하는 환경에 올리기 전에 OpenBao의 봉인 방식을 바꾼다"가 있었다.

## 결정

봉인:

- `config.hcl`에서 `seal` 블록을 떼어 `deploy/openbao/seal-static.hcl`에 둔다. OpenBao는 두 파일을 함께 읽는다(`-config`를 두 번). 공유하거나 배포하는 환경은 `config.hcl`을 그대로 쓰고 봉인 파일만 키 관리 서비스에 기댄 자동 봉인 해제(transit, 클라우드 KMS, HSM 등)로 바꾼다. self-init은 자동 봉인 해제가 있어야 하므로 shamir 봉인으로 바꾸는 길은 두지 않는다.
- 로컬 compose의 static 봉인 키는 Docker 볼륨 밖의 호스트 파일이다. 기본 경로는 `~/.config/secret-externalizer/openbao-unseal.key`이고 `OPENBAO_UNSEAL_KEY_FILE`로 바꾼다. compose의 `secrets`로 openbao에만 `/run/secrets/openbao-unseal-key`로 넘긴다. 키 ID(`local-1`)와 키 바이트는 그대로이므로 이미 있는 데이터를 다시 봉인할 필요가 없다.
- 호스트의 `deploy/openbao/unseal-key.sh`(`make openbao-unseal-key`)가 키 파일을 만든다.
  - 파일이 있으면 크기(32바이트)와 폴더 권한만 확인한다.
  - 볼륨에 예전 키가 있으면 파일로 복사한다. 그 뒤 볼륨의 키는 파일과 바이트가 같을 때만 지운다. 다르면 아무것도 지우지 않고 멈춘다.
  - 어디에도 키가 없는데 `openbao-data` 볼륨이 있으면 새 키를 만들지 않고 멈춘다.
  - 그 밖에는 난수 32바이트로 새 키를 만든다.
  - 폴더는 `0700`으로 만들고, 그룹이나 다른 사용자가 들어갈 수 있는 폴더는 거부한다. 파일은 `0644`다.
- `openbao-secrets`는 봉인 해제 키를 더는 만들지 않는다. 볼륨에 `unseal/`이 남아 있으면 `make openbao-unseal-key`를 실행하라는 메시지로 멈춰, 옮기지 않은 볼륨으로 뜨지 않게 한다.

TLS:

- `openbao-secrets`가 처음 뜰 때 사설 CA와 OpenBao의 서버 인증서(이름 `openbao`, `127.0.0.1`, 10년)를 만든다. 서버 인증서와 개인 키는 `tls/`(openbao 소유, 개인 키 `0400`)에, CA 인증서는 `tls/`, `worker/`, `web/`에 둔다. CA의 개인 키는 그 컨테이너의 `/tmp`에서만 살고 사라진다. `openssl`이 있어야 하므로 `openbao-secrets`의 이미지를 valkey와 같은 변환기 이미지로 바꾼다.
- 리스너는 TLS 1.2 이상만 받는다. 상태 확인도 `https://127.0.0.1:8200`과 CA로 한다.
- web과 worker는 새 설정 `BAO_CA_FILE`을 고를 수 있다. 있으면 `BAO_ADDR`가 `https://`여야 하고 그 CA만 믿는다. 없으면 `https://` 주소는 시스템의 루트로 확인하고, `http://` 주소는 평문으로 붙되 시작할 때 `openbao connection is not encrypted` 경고를 남긴다. 시작 로그의 설정에는 `bao_tls`가 나온다. `BAO_ADDR`는 이제 `http://`나 `https://`로 시작하고 호스트가 있으며 사용자 정보, 경로, 쿼리, 조각이 없어야 한다.
- CA 파일 읽기와 "그 CA만 믿는 TLS 설정"은 `internal/cafile`에 하나로 모았다. web의 valkey 연결도 같은 코드를 쓴다.
- 통합 테스트의 `openbao-test`(`-dev`, 평문)는 그대로 둔다.

## 이유

- 키와 데이터를 서로 다른 곳에 두면 한쪽만 새어도 값이 나오지 않는다. 호스트 파일은 Docker VM의 디스크와 볼륨 백업에 들어가지 않는다. 로컬 개발에서 이것보다 강하게 하려면 키를 사람이 매번 넣거나(shamir) 다른 키 서비스를 띄워야 하는데, shamir는 self-init과 함께 쓸 수 없고 로컬의 키 서비스는 같은 문제를 한 단계 옮길 뿐이다.
- 봉인 블록을 따로 두면, 배포하는 쪽이 정책과 self-init을 담은 `config.hcl`을 고치지 않고 봉인만 바꿀 수 있다. 어떤 키 서비스를 쓸지는 배포 환경이 정하므로 이 저장소는 고르지 않는다.
- 파일을 `0644`로 두는 것은 Linux 호스트 때문이다. bind mount는 호스트의 소유자(사람의 uid)와 권한을 그대로 보이므로, `0600`이면 컨테이너의 uid 100이 읽지 못한다. swarm이 아닌 compose는 secret 파일을 bind mount로 넘기며, 이 기계에서도 컨테이너 안의 파일이 호스트의 권한 그대로 보였다. 대신 폴더를 `0700`으로 두어 호스트의 다른 사용자를 막는다. 이 기계(colima)에서는 `0600`도 읽혔다.
- 볼륨의 키를 지우기 전에 바이트를 비교하는 것은, 복사가 잘못되었을 때 데이터를 여는 유일한 키를 잃지 않기 위해서다.
- TLS는 valkey(결정 0066)와 같은 방식이다. web과 worker가 같은 CA 파일 하나로 서버를 확인하고, 클라이언트 증명은 AppRole이 맡는다.
- `BAO_CA_FILE`을 필수로 하지 않는 것은 배포 환경이 공인 CA나 시스템에 설치된 CA를 쓸 수 있고, 통합 테스트는 평문 `-dev` 서버를 쓰기 때문이다. CA 파일을 주고 `http://`를 쓰는 것은 실수이므로 거부한다.

실제 스택에서 확인한 것(2026-11-24, colima):

- 키 파일이 없으면 `docker compose up -d openbao`가 경로를 밝힌 `bind source path does not exist`로 멈추고, 떠 있던 openbao는 그대로였다.
- `make openbao-unseal-key`가 기존 볼륨의 키를 복사하고 볼륨에서 지웠다. 다시 실행하면 "exists"만 남겼다. 폴더는 `0700`, 파일은 `0644`였다.
- 새 설정의 openbao가 옮긴 키로 기존 데이터의 봉인을 풀었다(`unsealed with stored key`, 리스너 `tls: "enabled"`). 평문 요청은 `Client sent an HTTP request to an HTTPS server`, CA 없는 요청은 `certificate signed by unknown authority`로 거부되었고, CA로 붙은 worker의 AppRole은 이전 스캔의 원본 목록을 읽었다.
- 새 web과 worker는 `"bao_tls":true`로 경고 없이 떴다. 스캔이 지문과 원본 보관을 거쳐 끝났고, 원본 보기가 값을 보였다. web과 worker 로그에 값이 없었다.
- openbao 로그에 `post-unseal upgrade seal keys failed: error="no recovery key found"` 경고가 한 번 나온다. 복구 키를 만들지 않는 self-init 구성에서 나오는 것으로 보이며 봉인 해제와 요청은 정상이었다. 옮기기 전에도 나왔는지는 확인하지 않았다.

## 알려진 한계

- 키 파일과 `openbao-data` 볼륨을 함께 가진 사람은 모든 값을 읽는다. 같은 기계의 Docker 권한이 있는 사람은 둘 다 얻을 수 있다. 이 구성은 여전히 개발자 PC 한 대를 위한 것이다.
- 키 파일을 잃으면 데이터도 잃는다. 백업은 사람이 한다.
- 인증서는 10년 뒤 만료되고 갱신하지 않는다. 새 인증서가 필요하면 `tls/`와 `worker/ca.crt`, `web/ca.crt`를 지우고 `openbao-secrets`를 다시 실행한 뒤 web과 worker를 다시 띄운다.
- 배포용 봉인 파일의 예시는 두지 않았다. 확인하지 않은 설정 파일은 쉽게 낡는다.

## 검토한 다른 방법

- 키를 다른 Docker 볼륨에 둔다: 같은 VM 디스크와 같은 볼륨 백업에 들어가므로 문제가 그대로다.
- 키를 환경 변수로 넘긴다: `docker inspect`와 프로세스 환경에 보이고, 셸 기록이나 `.env` 파일에 남기 쉽다.
- shamir 봉인과 손으로 하는 봉인 해제: self-init을 쓸 수 없고, 다시 띄울 때마다 사람이 키를 넣어야 한다.
- 두 번째 OpenBao를 transit 봉인용으로 띄운다: 그 서버의 봉인 키를 어디에 둘지가 같은 문제로 남는다.
- 클라이언트 인증서(mTLS): AppRole이 이미 클라이언트를 증명하고, 개인 키를 web과 worker에 하나씩 더 나눠 주게 된다.
