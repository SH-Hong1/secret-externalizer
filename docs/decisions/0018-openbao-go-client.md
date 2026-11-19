# OpenBao 클라이언트에 공식 Go 모듈을 쓴다

- 날짜: 2026-11-19
- 상태: 확정

## 배경

worker는 OpenBao transit으로 지문을 계산하고, KV v2에 판정 전의 원본을 저장, 목록, 삭제한다. 로그인은 AppRole이다. 표준 라이브러리에는 OpenBao 클라이언트가 없으므로 의존성 추가의 이유를 기록한다.

## 결정

- `github.com/openbao/openbao/api/v2`(v2.7.1)와 AppRole 인증 모듈 `github.com/openbao/openbao/api/auth/approle/v2`(v2.7.0)를 쓴다. 구현 시점의 각 모듈 최신판이다.
- 두 모듈은 `internal/bao`만 쓴다. 이 패키지의 공개 API에는 두 모듈의 타입이 나오지 않는다.
- 설정은 `api.NewConfig()`로 시작한다. `DefaultConfig()`와 달리 환경 변수(`BAO_ADDR`, `BAO_TOKEN` 등)를 읽지 않는다. 주소, HTTP 클라이언트, 재시도 횟수(2번)는 `bao.New`의 인자로만 정한다.
- HTTP 클라이언트는 호출하는 쪽이 넘기며 제한 시간이 없으면 거부한다. 넘긴 값은 복사해서 쓴다.
- 공식 클라이언트의 오류는 감싸지 않는다. 요청이 실패하면 `*api.ResponseError`에서 상태 코드만 꺼내 새 오류를 만든다. 응답이 없으면 고정 문장을 쓰고, `ctx`가 취소되었으면 `ctx.Err()`만 감싼다. `bao.New`에서 `api.NewConfig()`의 `Error`나 `api.NewClient`의 오류를 받으면 고정 문장으로 새 오류를 만들고, `api.NewClient`의 경우에는 서버 주소만 덧붙인다.
- 토큰을 갱신하는 goroutine은 두지 않는다. 처음 쓸 때 로그인하고, 403을 받으면 한 번 다시 로그인해 재시도한다.

## 이유

- 요청 형식, 토큰 헤더, `LIST` 요청, 404 처리, 재시도를 직접 만들지 않아도 된다. AppRole 로그인과 토큰 설정도 모듈이 한다.
- OpenBao 프로젝트가 관리하는 공식 모듈이라 서버 버전과 함께 갱신된다.
- 공식 클라이언트의 `ResponseError.Error()`는 요청 URL(KV 경로에는 지문이 있다)과 응답 본문을 그대로 담는다. 서버가 오류 본문에 요청 내용을 담아 돌려주면 값이 오류 메시지에 섞일 수 있으므로, 상태 코드만 남긴다. 테스트(`TestClient_ErrorsHideValues`)가 응답 본문에 값, secret-id, 토큰을 넣어 이를 확인한다.
- AppRole 로그인 정보는 모듈의 `AppRoleAuth` 안에 있다. fmt는 일부 verb에서 포인터 안의 필드까지 출력하므로, `bao.Client`는 `String`, `Format`, `LogValue`로 고정 문장만 출력한다.

## 검토한 다른 방법

- `net/http`로 직접 호출: 필요한 엔드포인트가 적어 가능하지만, 로그인, 재시도, `LIST`와 404 처리, 응답 형식을 직접 유지해야 한다.
- HashiCorp Vault의 Go 클라이언트(`github.com/hashicorp/vault/api`): API는 거의 같지만 Vault 서버를 기준으로 관리되므로 OpenBao 서버와의 호환이 보장되지 않는다.
- `api.DefaultConfig()`: 환경 변수를 읽어 설정을 바꾼다. 환경은 `run`에서 한 번만 읽는다는 규칙과 어긋난다.
