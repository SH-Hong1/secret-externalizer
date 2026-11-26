# bean이 아닌 요소의 name 속성은 속성 값의 키 앞에 붙는다

- 날짜: 2026-11-26
- 상태: 확정

## 배경

0096에 따라 `<Resource name="jdbc/live" password="…"/>`의 키는 `password`다. 값이 `value`가 아닌 속성에 있으면 같은 태그의 `name`은 값이 아니라 요소를 가리키기 때문이다. 그래서 Tomcat `server.xml`이나 `context.xml`의 `Resource` 두 개(`jdbc/dev`, `jdbc/live`)가 같은 키 `password`를 받아, 비밀번호가 같으면 한 묶음이 되고 다르면 추천 이름이 겹쳤다(이슈 #61). 0096은 `name`을 앞 조각으로 붙이는 방법을 미뤘다. `name`에는 `/` 같은 키 밖의 글자가 흔하고, bean에서는 `id`와 `name`이 같은 것을 두 번 가리키기 때문이다.

## 결정

1. **값이 `value`가 아닌 속성에 있고 그 태그에 `name` 속성이 있으면, `name`의 값을 `/`에서 나눈 조각들을 끝 조각 앞에 붙인다.** `<Resource name="jdbc/live" password="…"/>`의 키는 `jdbc.live.password`다. 줄을 나눠 쓴 태그도 같다.
2. **bean(`<bean>`, `<beans:bean>`)에는 붙이지 않는다.** bean은 지금처럼 `id`로 이름을 받는다(`<bean id="ds" name="alias" p:password="…"/>` → `ds.password`).
3. **`name`의 한 조각이라도 비었거나 키 글자(영문, 숫자, `.`, `_`, `-`)가 아닌 글자를 담으면 `name`을 붙이지 않고 끝 조각만 쓴다.** `jdbc//live`, `jdbc/`, `java:comp/env/x`, 빈 `name`이 그렇다.
4. 값을 담은 요소가 열린 bean 안에 있으면 bean의 `id`가 맨 앞이다(`ds.jdbc.live.password`).
5. `name`은 값보다 앞에 있을 때만 읽는다. `value` 속성의 `name`과 같은 한계다.

## 이유

- 키의 조각은 파일의 바이트 구간이고 조각을 `.`로 이어 키를 만든다. `/`에서 나누면 `/`를 바꿔 쓰지 않고도 구간만으로 키를 만들 수 있고, JNDI 이름의 계층이 YAML 키처럼 `.`로 이어진다.
- `jdbc/dev`와 `jdbc/live`처럼 마지막 조각만 다른 경우가 흔해, 마지막 조각만 쓰면 `mail/live`와 `jdbc/live`가 겹친다. 조각을 모두 쓰면 `name`이 다른 두 요소는 다른 키를 받는다.
- `name`을 값 뒤에서까지 찾으면 값 안의 글자를 `name="…"`으로 잘못 읽을 수 있다. 그 경우 키 조각이 값과 겹쳐 버려지기는 하지만(`spotKey`), 값보다 앞만 보면 그런 일이 아예 생기지 않는다.

## 검토한 다른 방법

- **`name`의 마지막 조각만 붙인다(`live.password`).** 짧지만 `jdbc/live`와 `mail/live`가 같은 키를 받는다.
- **`Resource` 요소에만 적용한다.** Tomcat 말고도 `name`과 비밀 속성을 함께 쓰는 요소(Ant 작업, 다른 서버의 설정)가 있고, 요소 이름에 기대는 규칙은 목록을 계속 늘려야 한다.
- **`:`에서도 나눈다(`java:comp/env/jdbc/x` → `java.comp.env.jdbc.x`).** 그런 이름을 쓰는 설정을 아직 보지 못했다. 보이면 다시 정한다.

## 알려진 한계

- 키가 바뀐 자리(bean이 아닌 요소의 `name` 옆 속성 값)는 다음 스캔에서 키가 다른 새 항목이 되어 전의 판정을 이어받지 않는다(이슈 #56).
- `name`이 값보다 뒤에 있는 태그(`<Resource password="…" name="jdbc/live"/>`)는 키가 `password`다.
