# `.bash`, `.ksh` 파일도 셸 스크립트로 본다

- 날짜: 2026-11-26
- 상태: 대체됨 (0124 참조)

## 배경

스캐너의 셸 규칙(`rules/opengrep/shell.yml`)은 `*.sh`, `*.bash`, `*.ksh`를 읽는다. 그러나 적용과 화면이 셸 스크립트로 보는 파일(`triage.ShellFile`)은 `.sh`뿐이었다. 그래서 `deploy.bash`에서 찾은 비밀번호는 `${ext.db.main.password}`로 바뀌었다. bash는 점이 든 이 표기를 펼치지 못해(`bad substitution`) 스크립트가 멈춘다(#96). 판정 화면의 변수 이름 안내, 리포트와 적용 목록의 셸 표시도 같은 기준이라 함께 어긋났다.

## 결정

- `triage.ShellExtensions`(`.sh`, `.bash`, `.ksh`)가 셸 스크립트의 확장자 목록이다. 셸 규칙이 읽는 확장자와 같다. `triage.ShellFile`, 적용의 placeholder 형식, 키를 지울 수 없는 소스 파일 판단(`apply`의 `sourceFileName`), 값 풀기의 문법(`literal`)이 이 목록을 따른다.
- 데이터베이스 쿼리의 셸 판단은 SQL 배열 상수 `shellPatterns`(`LIKE ANY`)로 한다. 이 상수가 `triage.ShellExtensions`와 같은지는 `TestShellPatterns`가 본다.
- 확장자가 없는 셸 스크립트와 작은따옴표 안의 placeholder는 이번에 다루지 않는다(#200).

## 이유

- 스캐너가 셸 규칙으로 읽는 파일은 셸 문법으로 값을 바꿔야 한다. 기준이 둘이면 한쪽만 바꾸는 일이 다시 생긴다.
- 프론트엔드 확장자(0085)는 쿼리 인자(`$7`)로 넘긴다. 셸 판단은 `scanBundles`에 들어 있고, `scanBundles`로 시작하는 쿼리는 서로 다른 인자를 받는다. 모든 쿼리에 인자를 더하는 대신 상수로 두고 테스트로 묶었다. 상수는 입력을 담지 않으므로 SQL을 입력으로 만드는 일이 아니다.
- `.zsh`는 셸 규칙이 읽지 않으므로 넣지 않았다. 규칙에 넣을 때 이 목록에도 넣는다.

## 검토한 다른 방법

- `scanBundles`를 인자를 받는 함수로 바꾸기: 쿼리 여섯 곳의 인자 번호가 모두 바뀌고, 얻는 것은 상수와 테스트로 얻는 것과 같다.
- SQL 정규식(`~ '\.(sh|bash|ksh)$'`): 0085에서 `LIKE`보다 느렸고 목록이 여전히 둘이다.
