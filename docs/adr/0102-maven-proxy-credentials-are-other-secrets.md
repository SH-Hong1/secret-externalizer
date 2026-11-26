# settings.xml의 프록시 계정과 비밀번호는 기타 비밀 값으로 보고한다

- 날짜: 2026-11-26
- 상태: 확정

## 배경

Maven `settings.xml`의 `<server>` 안 `<username>`과 `<password>`는 규칙 `maven-settings-server-username`, `maven-settings-server-password`가 저장소 접속 정보(`storage-credential`)로 보고한다. 같은 파일의 `<proxies>/<proxy>` 안에도 `<username>`과 `<password>`를 쓸 수 있는데, 두 규칙이 `<server>` 안만 보아서 프록시 계정과 비밀번호는 빠졌다(0052의 "놓치는 것", #175).

## 결정

1. **새 규칙 `maven-settings-proxy-username`, `maven-settings-proxy-password`를 둔다.** `settings.xml`의 `<proxy>` 안 `<username>`은 계정, `<password>`는 비밀번호로 보고한다. 값이 `${…}`, `#{…}` placeholder이거나 비어 있으면 보고하지 않는다. `<server>` 규칙과 같은 모양이다.
2. **분류는 기타 비밀 값(`other-secret`)이다.** 프록시 계정은 빌드가 프록시를 거쳐 밖으로 나가게 해 줄 뿐, 저장소나 서버에 들어가게 해 주지 않는다.

## 이유

- 같은 파일에서 같은 모양으로 적힌 계정과 비밀번호가 감싼 요소에 따라 보고되거나 빠지면 안 된다.
- 분류는 새어 나간 값이 무엇에 접근하게 하는지로 나눈다(`finding.Category`). 프록시는 저장소 접속 정보도 SSH 접속 정보도 아니다.
- `<server>` 규칙에 `<proxy>`를 함께 넣지 않고 규칙을 따로 두어, 이미 보고하던 자리의 규칙 ID와 분류가 바뀌지 않는다.

## 검토한 다른 방법

- 저장소 접속 정보로 보고하기: 사내 프록시 계정이 사람의 사내 계정과 같은 경우가 있어 중요도는 비슷하지만, 분류가 접근 대상을 뜻하는 규칙과 맞지 않는다. 판정은 분류와 관계없이 사람이 하므로 빠뜨리지 않는 것이 먼저다.
- `<host>`, `<port>`도 보고하기: 프록시 주소는 비밀 값이 아니다.
