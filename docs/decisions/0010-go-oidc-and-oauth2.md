# OIDC 로그인에 go-oidc와 oauth2를 쓴다

- 날짜: 2026-11-18
- 상태: 확정

## 배경

0005에서 로그인을 Keycloak OIDC(Authorization Code + PKCE)로 정했다. web은 인가 코드를 토큰으로 바꾸고, ID 토큰의 서명과 클레임을 검증해 사용자와 역할을 얻어야 한다. 표준 라이브러리에는 OAuth 2.0 클라이언트도, JWT 서명 검증도 없으므로 의존성 추가의 이유를 기록한다.

## 결정

- 인가 요청과 토큰 교환에 `golang.org/x/oauth2`를 쓴다. PKCE는 `S256ChallengeOption`과 `VerifierOption`으로 처리한다.
- discovery 문서 읽기와 ID 토큰 검증(서명, 발급자, 대상, 만료)에 `github.com/coreos/go-oidc/v3`를 쓴다. nonce 비교만 앱이 직접 한다.
- 테스트의 가짜 발급자가 ID 토큰에 서명할 때 `github.com/go-jose/go-jose/v4`를 쓴다. go-oidc가 이미 의존하는 모듈이라 새로 끌어오는 코드는 없고, 테스트에서 직접 import하므로 `go.mod`에 직접 의존성으로 적힌다. 운영 코드는 이 모듈을 직접 쓰지 않는다. 다만 직접 의존성으로 적은 go-jose 버전이 모듈 그래프에서 선택되므로, go-oidc가 요구하는 것보다 높은 버전을 적으면 운영 빌드에서 go-oidc가 쓰는 go-jose 버전도 함께 올라간다.

## 이유

- ID 토큰 검증은 서명 알고리즘 제한, 키 회전에 따른 JWKS 다시 읽기, 발급자와 대상과 만료 확인을 모두 맞게 해야 하는 일이다. 하나라도 빠지면 위조 토큰으로 로그인할 수 있으므로 보안 도구가 직접 구현하지 않는다.
- go-oidc는 OIDC 클라이언트로 널리 쓰이고, oauth2와 같은 context 키로 HTTP 클라이언트를 넘겨받아 두 라이브러리가 같은 클라이언트(시간 제한 포함)를 쓴다.
- go-oidc의 `InsecureIssuerURLContext`로 "컨테이너 안에서는 `keycloak:8080`으로 닿고, 토큰의 발급자는 브라우저 주소 `localhost:8081`"인 구성을 발급자 검증을 끄지 않고 처리할 수 있다.
- `golang.org/x/oauth2`는 Go 팀이 관리하고 이미 `go.mod`에 있다.

## 검토한 다른 방법

- JWT 라이브러리(`golang-jwt/jwt` 등)와 직접 만든 discovery, JWKS 처리: 검증 단계를 직접 조립해야 하고, 빠뜨리기 쉬운 부분이 바로 그 조립이다.
- 토큰 검증을 Keycloak의 userinfo나 introspection 엔드포인트에 맡기기: 로그인마다 요청이 하나 더 들고, ID 토큰의 nonce 확인은 여전히 앱이 해야 한다.
- 테스트에서 서명 코드를 직접 작성하기(`crypto/rsa`와 base64): 가능하지만 JWS 형식을 손으로 맞추는 테스트 코드가 생기고, 이미 받은 go-jose로 같은 일을 할 수 있다.
