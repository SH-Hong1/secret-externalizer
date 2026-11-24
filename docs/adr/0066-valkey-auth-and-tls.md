# web은 valkey에 자기 ACL 사용자와 TLS로 붙는다

- 날짜: 2026-11-24
- 상태: 확정

## 배경

valkey는 세션과 진행 중인 로그인 시도를 가진다(결정 0013). web은 인증 없이 평문으로 `valkey:6379`에 붙었고, valkey는 통합 테스트를 위해 호스트의 `127.0.0.1:16379`에도 열려 있었다. 키는 세션 ID의 SHA-256이라 valkey를 읽어도 쓸 수 있는 쿠키는 나오지 않지만, valkey에 닿는 누구든 세션을 지우거나(모두 로그아웃), 아무 사용자의 세션을 만들어 넣거나(키 이름과 값 형식만 알면 된다), 메모리 한도까지 채워 새 로그인을 막을 수 있었다. 남은 일에는 "공유하거나 배포하는 환경에 올리기 전에 web이 valkey에 인증하게 한다(비밀번호와 TLS)"가 있었다.

## 결정

web 쪽:

- 설정 `VALKEY_USERNAME`, `VALKEY_PASSWORD_FILE`을 반드시 받는다. 비밀번호는 파일에서 읽어 `session.Password`로 바꾸고, 이 타입은 로그와 `fmt`에서 언제나 `[redacted]`로 나온다.
- 설정 `VALKEY_CA_FILE`은 고를 수 있다. 있으면 TLS로 붙고 그 파일의 인증서만 믿으며, 인증서는 `VALKEY_ADDR`의 호스트 이름으로 확인한다(TLS 1.2 이상). 없으면 평문으로 붙고, 시작할 때 `valkey connection is not encrypted` 경고를 남긴다. 시작 로그의 설정에는 `valkey_tls`가 참인지 거짓인지가 나온다.

compose 쪽:

- `valkey-secrets` 서비스가 처음 뜰 때 `valkey-secrets` 볼륨에 사설 CA와 valkey의 서버 인증서(이름 `valkey`, `127.0.0.1`), ACL 파일, web의 비밀번호(무작위 64자리 16진수)를 만든다. 파일이 모두 있으면 아무것도 바꾸지 않는다. CA의 개인 키는 그 컨테이너의 `/tmp`에서만 살고 사라진다. 두 인증서는 10년짜리다.
- valkey는 TLS 포트 6379만 열고(평문 포트 0), `users.acl`의 사용자만 받는다.
  - `default`: 꺼져 있다. 로그인하지 않은 연결은 아무 명령도 못 한다.
  - `health`: 비밀번호 없이 `PING`만 할 수 있다. 상태 확인용이다.
  - `se-web`: web의 사용자다. `session:*`, `attempt:*` 키에만 닿고, `internal/session`이 쓰는 `GET`, `SET`, `GETDEL`, `PEXPIRE`, `DEL`과 valkey-go가 연결할 때 보내는 `HELLO`, `AUTH`, `PING`, `CLIENT SETINFO`, `CLIENT SETNAME`, `CLIENT TRACKING`만 할 수 있다. ACL 파일에는 비밀번호가 아니라 그 SHA-256이 있다.
- 볼륨은 서비스마다 자기 폴더만 마운트한다. valkey는 `valkey/`(인증서, 개인 키, ACL), web은 `web/`(CA 인증서, 비밀번호)이다.
- valkey는 호스트 포트를 열지 않고 내부 네트워크 `valkey`에 web과 둘만 붙는다. `edge`에서 빠졌다.
- 통합 테스트는 `test` 프로필의 `valkey-test`(`127.0.0.1:16379`)를 쓴다. 평문이고 고정된 가짜 비밀번호(`dev-only-valkey-test-password`)를 쓴다. 테스트는 `TEST_VALKEY_PASSWORD`로 그 비밀번호를 받는다.
- valkey와 valkey-test는 uid 999로, 읽기 전용 루트와 모든 capability를 뺀 채로 돈다.

## 이유

- 비밀번호만으로는 같은 네트워크에서 엿들은 사람이 그 비밀번호와 세션 쿠키 값을 얻는다(web이 세션 레코드를 읽고 쓰는 내용도 평문이다). 비밀번호와 TLS가 함께 있어야 남은 일이 말한 목적이 선다.
- 클라이언트 인증서(mTLS) 대신 비밀번호를 클라이언트의 증명으로 쓴다. valkey는 ACL 사용자 단위로 권한을 주고, 클라이언트 인증서를 ACL 사용자에 잇는 기능은 이 판에서 쓰지 않았다. 비밀번호 파일 하나가 OpenBao의 secret-id 파일(결정 0017)과 같은 모양이라 운영이 단순하다.
- `default` 사용자의 비밀번호(`requirepass`) 대신 따로 된 ACL 사용자를 둔다. web이 쓰는 키와 명령만 열어 두면 비밀번호가 새도 `FLUSHALL`, `CONFIG`, `KEYS` 같은 것은 할 수 없다.
- TLS를 기본으로 강제하지 않고 `VALKEY_CA_FILE`의 유무로 정한다. 통합 테스트의 valkey는 호스트에서 붙으므로 CA를 볼륨에서 꺼내야 하는데, 그 수고에 비해 테스트가 얻는 것이 없다. TLS 연결 자체는 단위 테스트(`TestValkeyOption_TLS`, `TestValkeyOption_TLSRefusesOtherCA`)가 가짜 TLS 서버로 확인하고, 평문으로 뜨면 경고 로그가 남는다.
- 인증서를 만드는 데 변환기 이미지(`gotenberg/gotenberg:9.9.1-chromium`, 이미 고정된 digest)를 쓴다. 이 compose의 이미지 가운데 `openssl`이 있는 것은 그것뿐이다. 새 이미지를 고정하는 것보다 낫고, 이 서비스는 네트워크 없이 볼륨에 파일만 쓰고 끝난다.
- 갱신을 두지 않는다. 로컬 개발용 스택이고, 갱신하려면 CA 개인 키를 볼륨에 남겨야 한다. 남기지 않으면 볼륨에 접근하는 사람도 다른 인증서를 만들 수 없다.

실제 스택에서 확인한 것:

- valkey는 평문 연결을 끊고(`Connection reset by peer`), TLS로 붙어도 로그인하지 않으면 `NOAUTH`, `health` 사용자의 `GET`은 `NOPERM`이었다.
- `valkey-secrets`를 다시 띄우면 "valkey secrets exist"만 남기고 파일을 바꾸지 않았다.
- 새 web 이미지는 시작 로그에 `"valkey_username":"se-web","valkey_password":"[redacted]","valkey_tls":true`를 남기고 떴다. 브라우저에서 `dev-admin`으로 로그인(로그인 시도 `SET`, 콜백의 `GETDEL`, 세션 `SET`), 화면 이동(`GET`, `PEXPIRE`), 로그아웃(`DEL`)이 모두 되었고, web 로그에 경고, 오류, `NOPERM`, 비밀번호가 없었다.
- `valkey-test`를 대상으로 `internal/session`과 `cmd/web`의 통합 테스트가 건너뛴 것 없이 통과했다.

## 알려진 한계

- 비밀번호나 인증서를 바꾸려면 `valkey-secrets` 볼륨을 지우고 valkey, web을 다시 띄운다. valkey는 디스크에 아무것도 남기지 않으므로 모든 사람이 다시 로그인하는 것 말고 잃는 것은 없다.
- valkey는 지금 인증서를 10년 뒤에 그대로 거부당한다. 그 전에 볼륨을 다시 만든다.
- `health` 사용자는 비밀번호 없이 `PING`을 할 수 있다. valkey가 떠 있다는 것 말고는 알 수 없다.
- 이 구성은 로컬 개발용 `compose.yaml`의 것이다. 공유하거나 배포하는 환경에서는 그 환경의 CA로 만든 인증서와 비밀 관리 방식을 쓰고, web에 `VALKEY_CA_FILE`을 반드시 준다.

## 검토한 다른 방법

- `requirepass`로 `default` 사용자에 비밀번호만 둔다: 가장 간단하지만 비밀번호 하나로 모든 명령이 열린다.
- TLS를 언제나 켜고 평문을 막는다: 통합 테스트도 TLS여야 해서 CA와 비밀번호를 볼륨에서 꺼내는 단계가 테스트 실행에 붙는다.
- 클라이언트 인증서(`--tls-auth-clients yes`): 비밀번호 대신 개인 키를 web에 주게 되고, ACL 사용자는 여전히 비밀번호로 정해야 한다.
- 인증서를 Go 코드나 OpenBao의 PKI로 만든다: 개발 스택만을 위한 바이너리나 설정이 늘어난다.
