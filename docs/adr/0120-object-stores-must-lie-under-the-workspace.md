# 저장소의 객체 저장소도 작업 공간 안에 있어야 한다

- 날짜: 2026-11-26
- 상태: 확정

## 배경

`worktree.Inspect`와 `worktree.CommitHash`는 작업본의 루트, git 폴더, 공통 git 폴더가 작업 공간 안에 있을 때만 git을 믿는다(0033). 그러나 git은 이 셋 말고도 객체를 읽는 곳이 있다. 공통 git 폴더의 `objects` 폴더가 심볼릭 링크로 밖을 가리킬 수 있고, `objects/info/alternates`가 다른 객체 폴더를 가리킬 수도 있다(`git clone --shared`, `--reference`). 그러면 git은 작업 공간 밖의 객체를 읽어 커밋되지 않은 변경과 추적 여부를 정한다. 도구가 받는 것은 경로와 표시뿐이지만, git 폴더가 작업 공간 안에 있어야 한다는 규칙의 예외였다(#90).

## 결정

- 공통 git 폴더의 `objects`와 그 `info/alternates`가 가리키는 모든 객체 폴더가 심볼릭 링크를 푼 뒤 작업 공간 안에 있어야 한다. alternates를 따라간 객체 폴더의 alternates도 같은 규칙으로 본다. 하나라도 밖이면 `ErrNotRepository`이고, 적용 점검의 막힘 사유는 지금처럼 `not-repository`다.
- 다음 경우도 밖으로 본다.
  - 다섯 단계보다 깊은 alternates 사슬(git도 다섯 단계에서 멈춘다)
  - 64KiB보다 큰 alternates 파일
  - 큰따옴표로 시작하는 줄(git은 이 줄의 따옴표를 푼다)
  - 존재하지 않는 폴더를 가리키는 줄
  - 작업 공간 밖으로 가는 링크인 alternates 파일(작업 공간을 뿌리로 `os.OpenInRoot`로 연다)
- `#`으로 시작하는 줄과 빈 줄은 git처럼 건너뛴다. 상대 경로는 그 alternates 파일이 든 객체 폴더를 기준으로 푼다.
- 작업 공간 안의 저장소를 빌려 쓰는 clone은 그대로 점검한다.
- 막힘 안내에 객체 저장소를 더하고, 빌려 쓰는 clone을 푸는 방법(`git repack -a -d` 뒤 alternates 파일 지우기)을 적는다.

## 이유

- 작업 공간 밖의 내용은 도구가 읽지 않는다는 보안 규칙에 예외를 두지 않는다. git이 읽는 객체도 그 내용이다.
- 따옴표를 푸는 규칙을 다시 구현하면 git과 다르게 풀 수 있다. 따옴표 줄은 드물어서 거부해도 잃는 것이 적다.
- 존재하지 않는 폴더를 거부하는 이유: 나중에 작업 공간 밖에 그 폴더가 생기면 git이 읽게 된다.
- `GIT_ALTERNATE_OBJECT_DIRECTORIES`는 git 호출의 환경에 넘기지 않으므로 볼 필요가 없다.

## 검토한 다른 방법

- alternates가 있는 저장소를 모두 거부하기: 작업 공간 안에서 빌려 쓰는 clone도 막혀, 밖을 가리키는 경우만 막는 것보다 잃는 것이 많다.
- git 설정으로 alternates를 끄기: git에는 alternates를 끄는 설정이 없다.
