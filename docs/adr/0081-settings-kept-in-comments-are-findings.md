# 주석으로 막아 둔 설정의 접속 정보를 항목으로 보고한다

- 날짜: 2026-11-25
- 상태: 확정

## 배경

공개 예제 `egovframe-web-sample`의 `context-datasource.xml`에는 주석으로 막아 둔 dataSource bean 셋(hsql, MySQL, Oracle)이 있고, 그 안에 JDBC URL, `username`, `password` 속성이 있다. 스캔은 이 가운데 아무것도 보고하지 않았다(#127). Spring XML 규칙은 opengrep이 XML로 읽은 문서를 보는데, 그 문서에서 주석은 요소를 담지 않는다. `.properties`와 `application*.yml` 규칙도 키가 줄 처음에 오는 줄만 보아 `#`으로 시작하는 줄을 건너뛴다.

레거시 소스에서는 예전 운영 접속 정보를 주석으로 남겨 두는 일이 흔하다. 프로그램이 읽지 않을 뿐 값은 저장소에 그대로 남아 있다. 이슈는 두 가지를 정하라고 했다: 주석 안의 설정(XML 주석, properties의 `#` 줄, Java 주석)을 탐지할지, 탐지한다면 적용이 주석 안의 값을 바꿀지.

사용자의 상시 지시에 따라 권장안으로 정했다.

## 결정

1. **XML 주석, properties와 YAML의 주석 줄을 탐지한다.** 새 규칙 파일 `rules/opengrep/comment.yml`의 규칙은 모두 정규식이고 규칙 ID에 `comment`가 든다.
   - `spring-xml-comment-datasource-url`, `spring-xml-comment-datasource-username`: `<!-- … -->` 안에 있고, id나 class에 datasource, jdbc, hikari 등이 든 bean 안에 있는 `<property name="url|URL|jdbcUrl|driverUrl">`, `<property name="username|user">`의 값, 또는 그런 bean의 `p:url`, `p:jdbcUrl`, `p:username`, `p:user` 속성. 범위를 정하는 조건은 `spring.yml`의 같은 규칙과 같다.
   - `spring-xml-comment-password`: 주석 안의 `<property>`나 `<constructor-arg>` 가운데 이름이 password나 passwd로 끝나는 것의 `value` 속성 또는 `<value>` 자식, 그리고 bean의 `p:password`. 어느 bean인지 따지지 않는다.
   - `properties-comment-*`, `yaml-comment-*`(비밀번호, 계정, JDBC URL, 호스트): `#`(properties는 `!`도)으로 시작하는 줄에 키와 값이 같은 모양으로 적힌 것. 키와 값은 `=`이나 `:`으로 나뉘어야 하고, 값은 줄을 끝내는 한 낱말이다. 그래서 "# password must be rotated", "# password: see the wiki" 같은 문장은 항목이 아니다.
   - 값이 `${…}`, `#{…}` placeholder이거나 비어 있으면 보고하지 않는다.
2. **Java 주석은 이번에 다루지 않는다.** Java 규칙은 opengrep이 코드로 읽은 대입과 메서드 호출을 보고, 이름 규칙(이름이 무엇을 뜻하는지에 따라 보고하지 않는 이름)이 정규식으로 옮기기 어렵다. 주석으로 막은 Java 코드는 #144로 남긴다.
3. **적용은 주석 안의 값도 다른 항목과 똑같이 다룬다.** 판정은 사람이 하고, `externalize`면 주석 안의 값이 placeholder로 바뀌고 값은 OpenBao에 저장되며, `redact`면 표시로 가려지고 저장되지 않는다. 값 해석(`internal/literal`)은 이미 XML 주석, properties와 YAML의 주석 줄 안의 자리를 글자 그대로 읽는다. 규칙 ID가 판정 화면의 탐지 표시에 보이므로, 판정하는 사람은 주석 안의 값인지 알 수 있다.
4. **공개 예제 검증의 기대 목록에 주석 속 bean의 항목이 더해진다.** 예제의 값이 `user`, `password`, `sa` 같은 자리 표시 낱말이라, 스캔이 파일 안의 같은 글자를 모두 가리면서 같은 파일의 `username`, `password` 키 이름도 가려 그 항목들의 키가 빈다(seed한 bean 포함). 이것은 짧은 낱말 값의 기존 동작이라 그대로 기대 목록에 적고, 고치는 일은 #145로 남긴다. 또 `TestIntegration_PublicSample`의 "표에 원본이 없다" 확인은 규칙 파일에 나오는 낱말인 원본(`password`)을 건너뛴다. 그 낱말은 종류 이름과 규칙 이름으로 표에 원래 있으므로, 거기서 찾았다고 값이 남았다는 뜻이 아니다.

## 이유

- 주석 안의 값도 소스 저장소에 남은 비밀이다. 도구의 목적은 소스에 남은 비밀을 찾아 꺼내는 것이고, 프로그램이 그 값을 읽는지는 상관이 없다.
- 규칙을 정규식으로 쓰면 opengrep의 `patterns`가 여러 정규식의 범위가 겹치는 곳만 남기므로 "주석 안이면서 dataSource bean 안"을 한 규칙으로 쓸 수 있다. 주석을 벗긴 사본을 만들어 XML 규칙에 넘기는 방법은 스캔 대상 내용의 사본을 하나 더 만들게 되어 보안 규칙(사본은 EUC-KR 변환본 하나)에 어긋난다.
- 주석 줄은 설명 문장도 담는다. 값을 한 낱말로, 구분자를 `=`이나 `:`로 좁히면 주석으로 막은 설정 줄은 거의 다 잡고 문장은 거의 다 거른다. 공백이 든 비밀번호를 주석에 적은 경우는 놓친다.
- 적용이 주석 안의 값을 따로 다루면 사람이 고른 판정을 도구가 다시 뒤집게 된다. 주석으로 막은 설정에 placeholder가 들어가도 프로그램 동작은 바뀌지 않고, 값을 아예 남기고 싶지 않으면 `redact`를 고르면 된다.

## 검토한 다른 방법

- 탐지와 리포트만 하고 적용에서 빼기: 값이 소스에 남는다. 사람이 손으로 지우게 하면 도구가 남긴 기록(manifest, 감사 기록)과 실제 소스가 어긋난다.
- 주석 안의 항목은 추천 판정을 `redact`로 두기: 지금 추천 판정은 종류와 묶음으로 정하고 탐지 규칙을 보지 않는다. 필요해지면 따로 정한다.
- Java 주석까지 한 번에: 위 2의 이유로 미뤘다.
- 공개 예제 검증에서 원본 확인을 그대로 두고 예제 사본의 주석 속 값을 가짜 값으로 바꾸기: 받은 예제 그대로의 주석을 검증하려는 목적에 어긋난다.
