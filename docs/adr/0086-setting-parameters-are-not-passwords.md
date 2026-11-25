# 비밀 낱말로 끝나는 JDBC 매개변수만 비밀번호로 본다

- 날짜: 2026-11-25
- 상태: 확정

## 배경

`connstr.Split`은 JDBC URL의 매개변수 가운데 이름에 `password`, `pwd`, `secret`, `token`이 든 것(대소문자 무시)의 값을 비밀번호 구성 요소로 나눴다(0026). 이름 어디에든 들어 있으면 되므로 `passwordCharacterEncoding=UTF-8`(MySQL), `sslpasswordcallback=클래스 이름`(PostgreSQL), `tokenProvider=클래스 이름` 같은 설정 값도 비밀번호 항목이 되어 판정할 항목이 늘고, 적용이 `UTF-8` 같은 값을 OpenBao로 옮길 수 있었다.

## 결정

- 매개변수 이름(소문자)의 끝에서 `accesskey`나 `key`를 하나 떼어 낸 뒤, 남은 이름이 `password`, `pwd`, `secret`, `token` 가운데 하나로 끝나면 그 값을 비밀번호로 본다.
  - 비밀번호로 본다: `password`, `sslpassword`, `trustCertificateKeyStorePassword`, `accessToken`, `keyStoreSecret`, `pwd`, `SecretKey`, `SecretAccessKey`.
  - 비밀번호로 보지 않는다: `passwordCharacterEncoding`, `sslpasswordcallback`, `tokenProvider`, `secretFile`, `AccessKey`, `sslkey`.
- 비밀번호로 보지 않는 매개변수는 다른 매개변수(`useSSL=false` 등)와 같이 구성 요소가 아니고 주변 줄에서도 가리지 않는다.

## 이유

- 비밀 낱말 뒤에 다른 낱말이 붙은 이름은 대개 그 비밀에 관한 설정(인코딩, 콜백 클래스, 공급자 클래스, 파일 경로)이고, 값 자체는 비밀이 아니다. 비밀 값을 담는 이름은 비밀 낱말로 끝난다.
- `key`와 `accesskey`를 떼는 이유: AWS 계열 드라이버(Athena, Redshift)의 `SecretKey`, `SecretAccessKey`는 비밀 값이다. 0026의 규칙에서 비밀번호였던 이 이름들을 이 변경으로 잃지 않게 한다. `key`만으로는 비밀로 보지 않는다. `sslkey`(PostgreSQL)는 키 파일의 경로이고 `AccessKey`는 공개되는 키 ID다.

## 검토한 다른 방법

- 설정 이름 목록(`passwordCharacterEncoding` 등)을 따로 두어 빼기: 드라이버마다 이름이 다르고 새 설정이 나올 때마다 목록을 고쳐야 한다. 끝 낱말 규칙은 알려진 설정 이름을 모두 거르고 목록이 필요 없다.
- `Callback`, `Provider`, `Encoding` 같은 뒤붙는 낱말 목록으로 빼기: 위와 같은 이유로 목록이 계속 늘어난다.
- 한계: 비밀 낱말 뒤에 다른 낱말이 붙은 이름에 실제 비밀 값을 두는 드라이버가 있다면(`passwordValue` 같은 이름) 그 값은 구성 요소가 되지 않는다. 이때 값은 주변 줄에 보이지만, 규칙이 그 값을 따로 보고하면(betterleaks의 `generic-password` 등) 여전히 항목이 된다.
