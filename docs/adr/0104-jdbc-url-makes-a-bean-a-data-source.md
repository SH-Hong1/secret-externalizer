# jdbc: URL을 가진 bean은 데이터 소스다

- 날짜: 2026-11-26
- 상태: 확정

## 배경

Spring XML의 데이터 소스 `url`과 `user`/`username`은 bean의 `id`나 `class`에 `datasource`, `jdbc`, `hikari`, `c3p0`, `dbcp`, `druid`, `bonecp`, `proxool` 가운데 하나가 있을 때만 보고한다(0052). `pool`은 스레드 풀, 객체 풀, HTTP 연결 풀의 `url`, `user`까지 잡아서 이 목록에서 뺐다. 그래서 회사가 직접 만든 `<bean id="orderPool" class="com.example.shop.OrderPool">`처럼 이름에 `pool`만 있는 DB 풀의 접속 URL과 계정이 빠졌다(0052의 "놓치는 것", #179).

## 결정

1. **`url`, `URL`, `jdbcUrl`, `driverUrl`의 값이 `jdbc:`로 시작하면, bean 이름과 상관없이 그 값을 데이터 소스 URL로 보고한다.** `<property>`와 `p:url`, `p:jdbcUrl` 속성 모두 같다.
2. **그런 `jdbc:` URL이 있는 bean의 `user`, `username`도 데이터 소스 계정으로 보고한다.** `<property>`끼리, 또는 `p:` 속성끼리 있을 때다. 두 줄의 순서는 상관없다.
3. 규칙 ID는 그대로 `spring-xml-datasource-url`, `spring-xml-datasource-username`이고, 이름으로 알아보는 기존 조건은 그대로 둔다.

## 이유

- `jdbc:` URL은 JDBC 드라이버가 받는 형식이라, 그 값을 가진 bean은 이름과 상관없이 DB에 접속한다. 스레드 풀이나 HTTP 풀의 `url`은 `jdbc:`로 시작하지 않으므로 0052가 막으려던 오탐은 다시 생기지 않는다.
- 계정은 값만 보고는 DB 계정인지 알 수 없으므로, 같은 bean에 `jdbc:` URL이 있다는 것을 근거로 삼는다.

## 검토한 다른 방법

- 이름 목록에 `pool`을 다시 넣기: 0052가 뺀 스레드 풀, 객체 풀의 오탐이 돌아온다.
- 값이 `jdbc:`인 `url`만 보고하고 계정은 두기: 같은 bean의 접속 정보 가운데 계정만 소스에 남는다.

## 이 결정 밖

- 주석 안의 bean(`spring-xml-comment-datasource-url`, `…-username`)은 지금처럼 이름으로만 알아본다.
- `<property name="url">`과 `p:user`처럼 한 bean에서 두 방식을 섞어 쓴 계정은 보고하지 않는다.
- bean 안에 다른 bean이 들어 있으면, 안쪽 bean의 `jdbc:` URL 때문에 바깥 bean의 계정도 보고될 수 있다.
