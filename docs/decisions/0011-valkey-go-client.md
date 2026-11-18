# valkey 클라이언트에 valkey-go를 쓴다

- 날짜: 2026-11-18
- 상태: 확정

## 배경

세션과 로그인 시도를 valkey에 저장한다. 표준 라이브러리에는 valkey(Redis) 프로토콜 클라이언트가 없으므로 의존성 추가의 이유를 기록한다.

## 결정

`github.com/valkey-io/valkey-go`를 쓴다. 테스트에서 시각을 확인하는 키 수명(`PTTL`)도 같은 클라이언트로 읽는다.

## 이유

- valkey 프로젝트(`valkey-io` 조직)가 관리하는 클라이언트다.
- web 바이너리에 더하는 외부 패키지가 `golang.org/x/sys/cpu` 하나뿐이다(`go list -deps ./internal/session`으로 확인).
- 명령 빌더(`client.B()`)가 명령마다 타입을 갖고 있어, 인자 순서와 옵션을 컴파일할 때 확인할 수 있다.
- 없는 키는 `valkey.IsValkeyNil`로, 연결 실패는 그 밖의 오류로 구분되므로 `ErrNotFound`와 인프라 오류를 섞지 않는다.
- 모든 호출이 `context.Context`를 첫 인자로 받아 `AGENTS.md`의 규칙과 맞는다.

## 검토한 다른 방법

- `redis/go-redis`, `gomodule/redigo`: Redis 프로토콜을 쓰므로 valkey에도 붙는다. 이 둘을 비교 검증하지는 않았고, valkey 프로젝트가 직접 관리하는 클라이언트가 있으므로 그것을 골랐다.
- 직접 RESP 프로토콜 구현: 연결 풀, 재시도, 핸드셰이크를 직접 맡게 되고 보안 도구에서 얻을 것이 없다.
