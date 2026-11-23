# 스캔 규칙의 알려진 누락과 오탐 가운데 고친 것

- 날짜: 2026-11-23
- 상태: 확정

## 배경

계획 3의 검토에서 규칙이 놓치는 모양 아홉 가지와 잘못 보고하는 모양 다섯 가지가 남은 일로 적혀 있었다. 비밀 값을 놓치면 그 값은 판정 화면에 오르지 않고 소스에 남으므로, 이 도구에서 가장 큰 실패다. 오탐은 사람이 판정으로 걸러 내지만, 묶음 수와 대시보드의 "남은 항목"을 부풀린다.

규칙 테스트의 예제 `legacy-shop`은 스캔 테스트와 적용 테스트(통합 테스트 포함)가 함께 쓴다. 여기에 사례를 더하면 그 테스트들의 건수와 적용 결과가 함께 달라진다.

## 결정

1. **사례는 새 예제 `internal/ruleset/testdata/rule-cases`에 둔다.** 기대 결과는 예제와 같은 이름의 `rule-cases.json`이고, `legacy-shop`의 것은 `expected.json`에서 `legacy-shop.json`으로 이름을 바꿨다. `TestRules_ExpectedFindings`가 두 예제를 모두 스캔해 목록과 정확히 같은지 본다. 찾지 말아야 하는 사례는 "목록에 없는 줄이 나오면 차이"로 확인한다.
2. **누락 아홉 가지를 규칙에 넣는다.**
   - `.properties`와 `application*.yml`의 비밀번호 키는 마지막 부분이 `password`, `passwd`, `pwd`로 끝나거나(`dbPassword`, `db_passwd`), `pass`이거나 `_pass`, `-pass`로 끝나면 된다(`jdbc.pass`, `ftp-pass`). `compass`, `bypass`는 아니다.
   - `.properties`는 키와 값을 공백만으로 나눈 줄도 읽는다(비밀번호, 계정, 호스트, JDBC URL). 값이 `=`나 `:`로 시작하는 줄은 빈 값으로 보고 건너뛴다.
   - YAML 블록 스칼라(`password: >`, `password: |`, `>-` 등)는 새 규칙 `yaml-block-password`가 키 아래 한 줄을 값으로 잡는다. 블록이 두 줄 이상이면 보고하지 않는다. 기존 규칙이 `>-`나 `|`를 값으로 보고하던 것도 막았다.
   - `SSHPASS=` 뒤의 값은 새 규칙 `shell-sshpass-env-password`가 잡는다(`export SSHPASS='…'`, `SSHPASS=… sshpass -e …`). 변수(`"$VAR"`)는 보고하지 않는다.
   - `"Cookie", "이름=값"` 모양으로 쿠키를 넣는 호출은 betterleaks의 `partner-session-cookie`가 첫 쿠키의 값을 잡는다. `Cookie: 이름=값` 머리 줄과 같은 규칙이다.
   - `settings.xml`의 `<server>` 안 `<username>`은 새 규칙 `maven-settings-server-username`이 계정으로 잡는다.
   - Spring XML의 비밀번호는 이름이 `password`나 `passwd`로 끝나는 `<property>`와 `<constructor-arg>`다(`managerPassword`, `<constructor-arg name="password">`). `passwordEncoder`는 아니다.
   - 주민등록번호 규칙은 뒷자리 첫 숫자 5~8(외국인등록번호)도 잡는다. 종류는 그대로 `rrn`이다.
   - 전화번호 규칙은 하이픈 없이 붙여 쓴 휴대전화 번호(`01x` 뒤 7~8자리)도 잡는다. 앞뒤가 숫자나 글자면 잡지 않는다.
3. **오탐 다섯 가지를 고친다.**
   - Java 변수 이름이 `header`, `name`, `field`, `param`, `parameter`, `attr`, `attribute`, `label`, `prefix`, `suffix`, `type`, `url`, `uri`, `path`, `pattern`, `format`으로 끝나거나(`TOKEN_HEADER`, `passwordParam`), `MSG_`, `MESSAGE_`, `ERR_`, `ERROR_`, `LABEL_`, `TITLE_`, `TEXT_`나 그 낙타 표기(`msgPwd`)로 시작하면 `java-secret-variable`이 보고하지 않는다.
   - 값이 `#{`로 시작하면 `${`와 같이 자리 표시자로 보고 건너뛴다(XML, properties, YAML, Maven, Ant의 모든 규칙).
   - 데이터 소스 bean을 알아보는 이름에서 `pool`을 빼고 `jdbc`, `bonecp`, `proxool`을 더했다. `ThreadPoolTaskExecutor` 같은 bean의 `url`, `user`는 보고하지 않는다.
   - ssh의 `-i`는 `user@host` 앞에 있고, 앞에서 열린 따옴표 안이 아닐 때만 키 파일로 본다. `ssh host 'sed -i …'`는 보고하지 않는다.

## 이유

- 사례를 따로 두면 규칙을 고칠 때 스캔과 적용 테스트가 함께 흔들리지 않는다. `legacy-shop`은 "그럴듯한 프로젝트 하나"로, `rule-cases`는 "모양 하나에 줄 하나"로 역할이 갈린다.
- 비밀번호 키의 끝을 넓히는 쪽을 골랐다. 설정 파일에서 `…password`로 끝나는 키의 값은 거의 언제나 비밀번호이고, 놓치는 것이 잘못 보고하는 것보다 비싸다. `pass`만은 `compass`, `bypass` 때문에 구분자 뒤나 단독일 때로 좁혔다.
- 블록 스칼라는 한 줄일 때만 잡는다. 적용은 잡은 범위를 placeholder로 바꾸는데, 여러 줄 가운데 첫 줄만 바꾸면 값의 일부가 파일에 남는다. 보고하지 않는 것이 반쯤 바꾸는 것보다 낫다.
- 외국인등록번호를 `rrn`으로 두면 종류, 미리보기(`******-*******`), 마이그레이션, 화면을 건드리지 않는다. 두 번호는 모양과 다루는 방법이 같다.
- 하이픈 없는 번호는 휴대전화만 잡는다. `01x`로 시작하는 10~11자리는 다른 뜻일 가능성이 낮지만, `02`, `031`로 시작하는 9~11자리 숫자는 주문 번호나 금액과 구분할 수 없다.
- Java의 이름 규칙은 값이 아니라 이름을 본다. 값에 공백이 있으면 메시지로 보는 방법이 더 많이 걸러 내지만, 공백이 든 암호문을 놓친다. 이름 끝의 `Header`, `Param`은 그 문자열이 비밀 값이 아니라 무엇의 이름임을 쓴 사람이 직접 밝힌 것이다.
- 알려진 DB 풀의 class 이름에는 `DataSource`, `jdbc`, `Hikari`, `c3p0`, `dbcp`, `druid`, `BoneCP`, `proxool` 가운데 하나가 있다. `pool`은 그 밖에 스레드 풀, 객체 풀, HTTP 연결 풀만 더했다.
- `#{…}`는 Spring이 실행할 때 계산하는 식이다. `${…}`와 같이 값이 파일에 없다.

## 검토한 다른 방법

- **`legacy-shop`에 사례를 더한다.** 예제가 하나로 남지만, 통합 테스트가 추천 판정과 적용까지 도는 파일이 늘어 규칙 하나를 고칠 때마다 적용 쪽 기대도 고쳐야 한다.
- **외국인등록번호에 새 종류를 둔다.** 리포트에서 둘을 나눠 셀 수 있지만 종류 하나에 미리보기, 화면의 이름, 삭제·마스킹 형식이 따라붙는다. 나눠 세야 할 일이 생기면 그때 더한다.
- **쿠키 호출을 opengrep의 Java 규칙으로 잡는다.** 문법으로 호출을 알아보지만, 문자열 안에서 `이름=` 뒤만 고를 수 없어 `이름=값` 전체가 값이 된다. 머리 줄 규칙과 값의 범위가 달라진다.
- **ssh의 인자를 제대로 읽는다(옵션과 그 인자를 차례로).** 따옴표 없는 원격 명령의 `-i`까지 가릴 수 있지만 정규식 하나로는 옵션마다 인자가 있는지를 알아야 한다. 따옴표와 `user@host`를 경계로 삼는 것으로 흔한 모양을 가린다.
- **메시지 묶음 파일(`messages*.properties`)을 규칙에서 뺀다.** 오탐이 줄지만 이름만으로 파일을 빼면 그 이름의 설정 파일에 든 값을 놓친다. 남은 일로 둔다.

## 알려진 한계

- 놓치는 것: 두 줄 이상인 블록 스칼라 비밀번호, 목록 항목(`- password: >`)의 블록 스칼라, `p:password`가 아닌 `p:` 속성(`p:dbPassword`), `settings.xml`의 `<proxy>` 안 계정과 비밀번호, 하이픈 없는 지역 전화번호, 이름에 `pool`만 있고 class가 위 목록에 없는 bean의 `url`과 `user`, 이름이 `…Header`, `…Name`, `…Url`처럼 끝나는 변수에 든 실제 비밀 값, 글자 그대로 `#{…}` 모양인 비밀 값.
- 잘못 보고하는 것: 메시지 묶음의 `label.password=비밀번호`, `changePassword=…` 같은 줄(키 끝이 넓어져 전보다 늘 수 있다), 따옴표와 `user@` 없이 쓴 원격 명령의 `-i`(`ssh host sed -i …`), 로그에서 `01`로 시작하는 다른 뜻의 10~11자리 숫자.
- 규칙 파일이 바뀌어 규칙 버전(`ruleset.Version`)이 바뀐다. 이미 스캔한 프로젝트는 다시 스캔해야 새 규칙의 결과가 나오고, 그 스캔에서 묶음 수가 늘거나 줄 수 있다. worker 이미지는 규칙을 `/rules`로 복사하므로 다시 빌드해야 한다.
