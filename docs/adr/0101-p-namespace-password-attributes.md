# 이름이 password로 끝나는 모든 p: 속성을 비밀번호로 보고한다

- 날짜: 2026-11-26
- 상태: 확정

## 배경

Spring XML의 `spring-xml-password` 규칙은 bean 안의 `<property>`와 `<constructor-arg>`는 이름이 password나 passwd로 끝나면 모두 보고하지만, p 이름공간 속성은 `p:password` 하나만 보고했다. 그래서 같은 값을 `<property name="dbPassword" value="…"/>`로 쓰면 항목이 되고 `p:dbPassword="…"`로 쓰면 빠졌다(0052의 "놓치는 것", #173). 주석 안의 bean을 보는 `spring-xml-comment-password`도 같았다.

이 규칙은 opengrep이 XML로 읽은 문서에 패턴을 맞춘다. XML 패턴의 속성 이름은 글자 그대로 맞춰지고 메타변수로 쓸 수 없어서, `p:` 뒤의 이름을 정규식으로 거를 수 없다.

## 결정

1. **새 정규식 규칙 `spring-xml-p-password`를 둔다.** `*.xml`에서 `<bean` 태그 안의 `p:` 속성 가운데 이름이 password나 passwd로 끝나는 것(대소문자 무시)의 값을 보고한다. `p:password`는 지금처럼 `spring-xml-password`가 보고하므로 이 규칙에서 뺀다. 값이 `${…}`, `#{…}` placeholder이거나 비어 있으면 보고하지 않는다.
2. **태그 범위와 속성을 따로 맞춘다.** 한 패턴은 따옴표 안의 `>`를 건너뛰며 `<bean …>` 태그 전체를, 다른 패턴은 속성 하나를 맞추고, opengrep의 `patterns`가 둘이 겹치는 곳만 남긴다. 그래서 한 태그의 여러 비밀번호 속성(`p:managerPassword`, `p:adminPasswd`)이 모두 보고되고, 줄을 나눠 쓴 태그도 읽는다.
3. **주석 안은 이 규칙에서 빼고 `spring-xml-comment-password`가 맡는다.** 그 규칙의 `p:` 부분도 같은 이름 조건과 같은 두 패턴 구조로 넓힌다.
4. **bean이 아닌 요소의 `p:` 속성은 보고하지 않는다.** p 이름공간은 bean의 속성을 적는 문법이다.

## 이유

- 비밀번호를 어느 문법으로 적었는지에 따라 결과가 달라지면 안 된다. `<property>` 규칙과 같은 이름 조건을 쓰면 두 모양이 같은 범위를 갖는다.
- `p:password`까지 새 규칙으로 옮기면 그 자리를 보고하는 규칙 ID가 바뀌어 판정 화면의 탐지 표시가 달라진다. 이미 맞게 보고하던 자리는 그대로 두었다.
- 정규식 하나로 태그 시작부터 속성까지 맞추면 첫 비밀번호 속성에서 맞춤이 끝나, 같은 태그의 두 번째 속성은 다음 맞춤의 시작(`<bean`)을 찾지 못해 빠진다. 범위 두 개의 겹침으로 쓰면 속성마다 맞춘다.

## 검토한 다른 방법

- `p:dbPassword`, `p:adminPassword` 같은 흔한 이름을 XML 패턴으로 하나씩 더하기: 이름은 끝이 없고 `<property>` 규칙과 범위가 계속 어긋난다.
- XML 규칙을 모두 정규식으로 바꾸기: 지금 맞게 읽는 `<property>`와 `<value>` 자식까지 다시 써야 하고 규칙 ID가 바뀐다. 이번 범위를 넘는다.
