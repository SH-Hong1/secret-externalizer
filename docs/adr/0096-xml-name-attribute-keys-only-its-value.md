# XML 태그의 name 속성은 그 태그의 값에만 키가 된다

- 날짜: 2026-11-26
- 상태: 일부 대체됨 (bean이 아닌 요소의 `name`을 속성 값의 키 앞에 붙이는 것은 0100 참조)

## 배경

0053은 XML 값의 키를 "값이 있는 요소에 `name="…"`이 있으면 그 값"으로 읽었다. 이 규칙은 `<property name="password" value="…"/>`처럼 `name`이 값의 이름일 때 맞다. 그런데 `name`이 요소 자신의 이름일 때도 같이 적용되었다. `<bean id="ds" name="alias" p:password="…"/>`의 키는 `ds.alias`였고, 한 줄에 쓴 Tomcat의 `<Resource name="jdbc/live" password="…"/>`는 `name`의 `/`가 키 글자가 아니어서 키가 비었다. 같은 `Resource`를 속성마다 줄을 나눠 쓰면 키가 `password`여서, 같은 설정이 쓰는 모양에 따라 다른 키를 받았다(이슈 #60, #61의 일부).

## 결정

1. **값이 `value` 속성이나 요소의 글(`<property name="password">…</property>`)이면 그 태그의 `name` 속성이 키의 끝 조각이다.**
2. **값이 그 밖의 속성에 있으면 그 속성 이름이 끝 조각이다.** 이름공간 접두사는 지금처럼 뺀다(`p:password` → `password`). 같은 태그에 `name`이 있어도 쓰지 않는다.
3. bean의 `id`를 앞에 붙이는 규칙과 `name`이 없는 요소의 글을 요소 이름으로 읽는 규칙은 0053 그대로다.

## 이유

- `value` 속성과 요소의 글은 스스로 이름이 없으므로 `name`이 그 값의 이름이다. `password="…"` 같은 속성은 속성 이름이 이미 값의 이름이고, 같은 태그의 `name`은 bean의 별칭이나 JNDI 이름처럼 요소를 가리킨다.
- 한 줄로 쓴 `Resource`와 줄을 나눈 `Resource`가 같은 키 `password`를 받는다.
- 키가 틀려도 값이 드러나지는 않지만, `ds.alias`처럼 엉뚱한 키는 추천 이름과 묶음 나누기를 잘못 이끈다.

## 검토한 다른 방법

- **`name`을 앞 조각으로 붙인다(`jdbc/live.password`, `alias.password`).** `Resource` 두 개를 나누는 데는 도움이 되지만, `name`에는 `/` 같은 키 밖의 글자가 흔하고, bean에서는 `id`와 `name`이 같은 것을 두 번 가리킨다. 두 `Resource`를 나누는 일은 이슈 #61에 남긴다.
- **`value`뿐 아니라 `key`, `ref` 같은 속성에도 `name`을 쓴다.** 비밀 값이 그런 속성에 있는 예를 보지 못했다.

## 알려진 한계

- 키가 바뀐 자리(값이 `name`이 있는 태그의 `value`가 아닌 속성에 있던 자리)는 다음 스캔에서 키가 다른 새 항목이 되어 전의 판정을 이어받지 않는다. 키 이름이 바뀔 때 판정을 잇는 일은 이슈 #56이다.
- 같은 파일의 두 `Resource`는 여전히 같은 키 `password`를 받아 나뉘지 않는다(이슈 #61).
- `value` 속성이 `name`보다 앞에 오는 태그(`<property value="…" name="password"/>`)는 `name`을 보지 못해 키가 `value`다.
