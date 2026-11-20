# git 작업본 점검

- 날짜: 2026-11-20
- 상태: 확정

## 배경

적용(계획 5)은 커밋되지 않은 변경이 있는 작업본을 건드리면 안 된다. 그래서 적용 전에 프로젝트 폴더가 git 작업본인지, 추적되는 파일에 변경이 없는지를 알아야 한다. 이 일은 worker가 하고, worker는 스캔 대상 저장소를 신뢰할 수 없다. 저장소의 `.git/config`에는 git이 실행할 프로그램(fsmonitor, hook, pager, filter, promisor remote의 전송 명령)을 적을 수 있기 때문이다.

2026-11-20에 git 2.54.0(macOS)에서 임시 저장소를 만들어 확인했다. 표시 파일을 만드는 스크립트를 각 설정에 걸고, 아래 "결정"의 인자와 환경으로 `rev-parse`, `config`, `status`, `ls-files`를 실행했다. 작업 파일의 수정 시각을 바꿔 인덱스의 stat 정보와 어긋나게 해 git이 내용을 비교하고 인덱스를 다시 쓰고 싶어 하는 상태에서 확인했다. 비교를 위해 옵션 없는 `git status`도 같은 저장소에서 실행했다.

| 저장소 설정 | 옵션 없는 `git status` | 이 도구의 인자와 환경 |
|---|---|---|
| `core.fsmonitor` | 실행됨, 인덱스를 씀 | 실행 안 됨, 인덱스 그대로 |
| `core.hooksPath`의 `post-index-change` | 실행됨, 인덱스를 씀 | 실행 안 됨, 인덱스 그대로 |
| 설정으로 정의한 hook(`hook.<이름>.command`, `hook.<이름>.event=post-index-change`) | 실행됨 | 실행 안 됨. 다만 `-c core.hooksPath=/dev/null`은 이 hook을 끄지 못하고, 인덱스를 쓰지 않는 `--no-optional-locks`만이 막는다(검토에서 확인). 그래서 `hook.*` 키가 있으면 거부한다 |
| `core.pager`와 `pager.status`(터미널에서) | 실행됨 | 실행 안 됨(`--no-pager`) |
| `alias.status` | 실행 안 됨(git은 내장 명령의 별칭을 무시한다) | 실행 안 됨 |
| `filter.x.clean`과 `.gitattributes` | 실행됨 | **실행됨** |
| `filter.x.clean`과 `.git/info/attributes` | 실행됨 | **실행됨** |
| `filter.x.process`와 `.gitattributes` | 실행됨 | **실행됨** |
| `include.path`로 불러온 파일의 `filter.x.clean` | 실행됨 | **실행됨** |
| partial clone(`extensions.partialClone`, promisor remote의 `ext::` 주소와 `protocol.ext.allow=always`, 또는 `ssh://` 주소와 `core.sshCommand`)에서 HEAD의 tree나 commit 객체를 지움 | 실행됨(지연 가져오기) | 실행 안 됨, `status`가 128로 실패 |

그 밖에 확인한 것:

- filter는 명령행에서 끌 방법이 이름마다 `-c filter.<이름>.clean=`을 주는 것뿐이다. `--attr-source=<빈 tree>`(그리고 `attr.tree`)는 작업본의 `.gitattributes`는 막지만 `.git/info/attributes`는 막지 못했다. 그래서 계획대로 설정을 먼저 읽고 filter가 있으면 거부한다.
- `config --name-only --list -z`는 `include.path`로 불러온 파일의 키와, `extensions.worktreeConfig`가 켜진 저장소의 `config.worktree`에 있는 키(`core.worktree`)도 보여 준다. 섹션과 키 이름은 소문자로 나오고 하위 섹션 이름은 대소문자가 그대로다.
- **`-c protocol.allow=never`만으로는 지연 가져오기를 막지 못했다.** 저장소 설정의 `protocol.ext.allow=always`(또는 `protocol.ssh.allow=always`)가 명령행의 `protocol.allow`보다 우선하기 때문이다. `GIT_NO_LAZY_FETCH=1`은 막았다. 환경 변수 `GIT_ALLOW_PROTOCOL`은 저장소의 `protocol.<이름>.allow`보다 우선하고, 값이 `none`(없는 프로토콜 이름)이면 모든 전송을 막았다. 그래서 계획의 환경 목록에 `GIT_ALLOW_PROTOCOL=none`을 더했다.
- `rev-parse --is-inside-work-tree --show-prefix --absolute-git-dir --git-common-dir --show-toplevel`은 인자 순서대로 한 줄씩 출력한다. 접두어는 하위 폴더에서 `sub/`처럼 끝에 `/`가 붙고 루트에서는 빈 줄이다. `--git-common-dir`는 보통 저장소에서 `-C`로 준 폴더 기준 상대 경로(`.git`, `../.git`)이고, linked worktree에서는 절대 경로다. linked worktree에서 git 폴더는 `<공용 git 폴더>/worktrees/<이름>`, 작업본 루트는 worktree 폴더다. `.git` 파일(`gitdir:`)이 가리키는 폴더가 git 폴더와 공용 git 폴더로 나온다.
- 커밋이 없는 저장소에서 `rev-parse ... HEAD`는 저장소가 아닌 폴더와 같은 종료 코드 128로 실패한다. 그래서 위치를 읽는 호출과 `rev-parse --verify --quiet HEAD`(커밋이 없으면 1)를 나눴다.
- `ls-files -z -v -t`의 표시: 보통 `H`, assume-unchanged는 소문자(`h`), skip-worktree는 `S`다. 지운 파일도 `H`로 나오므로 지움은 `status`가 잡는다.
- `status --porcelain=v1 -z`의 경로는 하위 폴더에서 실행해도 저장소 루트 기준이고, `ls-files`의 경로는 `-C`로 준 폴더 기준이다.
- `-c safe.directory=*`가 있으면 다른 사용자 소유로 보이는 저장소(`GIT_TEST_ASSUME_DIFFERENT_OWNER=1`로 흉내 냄)에서 `rev-parse`, `status`, `ls-files`가 성공한다. 없으면 128로 실패한다. `config --list`는 소유자를 따지지 않는다.
- `core.autocrlf=true`이고 작업 파일이 CRLF인 저장소는 stat이 어긋난 상태에서도 `status`가 변경 없음이다.
- `GIT_CEILING_DIRECTORIES`를 `root`의 부모로 두면 `root` 아래 폴더에서 `root`의 부모에 있는 저장소를 찾지 않는다.
- 컨테이너 마운트에서 inode, ctime, uid가 다른 인덱스를 읽는 경우(`core.checkStat=minimal`, `core.trustctime=false`)는 호스트에서 확인할 수 없어 계획 5의 Task 15에서 확인한다.

## 결정

`internal/worktree`가 `git` 실행 파일을 `exec.CommandContext`와 인자 목록으로 부른다. 쓰는 명령은 읽기만 하는 넷이다.

1. `rev-parse --is-inside-work-tree --show-prefix --absolute-git-dir --git-common-dir --show-toplevel`: 작업본인지, 프로젝트 폴더의 접두어, 세 경로. 세 경로를 `filepath.EvalSymlinks`로 풀어 모두 작업 공간 루트 안이어야 한다. 아니면 `ErrNotRepository`다. 출력 줄 수가 다르면(경로에 줄바꿈이 있으면) 잘못 읽지 않고 거부한다.
2. `rev-parse --verify --quiet HEAD`: 커밋이 없으면 `ErrNoCommit`.
3. `config --name-only --list -z`: `filter.<이름>.clean`, `filter.<이름>.process`, `hook.`으로 시작하는 키, `core.worktree`가 있으면 `ErrUnsafeConfig`. 내용을 비교하기 전에 한다. `include.path`, `includeIf`, `config.worktree`에서 온 키도 목록에 나온다.
4. `status --porcelain=v1 -z --untracked-files=no --ignore-submodules=all --no-renames -- .`와 `ls-files -z -v -t -- .`: staged, unstaged 변경과 `H`가 아닌 표시(assume-unchanged, skip-worktree, 병합 중)가 `Dirty`다. 추적되지 않는 파일은 `Dirty`가 아니고 `Tracked`가 false다.
5. 두 명령이 보고한 경로가 프로젝트 폴더 밖을 가리키거나(`..` 마디, 절대 경로) 대소문자와 상관없이 `.git` 마디를 가지면 `ErrUnsafePath`로 거부한다. git은 그런 경로를 인덱스에 쓰지 않지만, 손으로 만든 인덱스(`../evil`, `.git/config`, `x/../../y`, `sub/../.git/hooks/z`)는 `ls-files`가 `H`로, `status`가 `AD`, `AM`으로 그대로 보고했다(검토에서 확인). 적용이 이 경로를 그대로 쓰면 폴더 밖이나 git 폴더의 파일을 가리키게 된다.

모든 호출에 붙이는 옵션과 환경, 그리고 각각이 막는 것:

| 옵션이나 환경 | 막는 것 |
|---|---|
| `--no-pager` | 저장소의 `core.pager`, `pager.<명령>` |
| `--no-optional-locks`, `GIT_OPTIONAL_LOCKS=0` | `status`가 인덱스를 새로 고쳐 쓰는 것(저장소에 쓰기) |
| `-c safe.directory=*` | 읽기 전용 마운트처럼 다른 사용자 소유인 저장소에서 git이 거부하는 것 |
| `-c core.fsmonitor=false` | 저장소의 fsmonitor 프로그램 |
| `-c core.hooksPath=/dev/null` | 저장소의 hook 폴더(`.git/hooks`나 `core.hooksPath`가 가리키는 폴더)의 hook. 설정으로 정의한 hook(`hook.<이름>.command`)은 끄지 못하므로 설정 검사로 거부한다 |
| `-c core.untrackedCache=false` | 인덱스의 untracked cache 갱신 |
| `-c core.checkStat=minimal`, `-c core.trustctime=false` | 컨테이너 마운트에서 inode, ctime, uid가 달라 모든 파일이 변경된 것으로 보이는 것 |
| `-c core.attributesFile=/dev/null`, `GIT_ATTR_NOSYSTEM=1` | 저장소 밖의 속성 파일이 filter를 켜는 것 |
| `-c core.quotePath=false` | 경로를 따옴표로 감싸 바꾸는 것(`-z`와 함께 원래 이름 그대로 받는다) |
| `-c submodule.recurse=false`, `--ignore-submodules=all` | submodule 안으로 들어가 그 저장소의 설정을 따르는 것 |
| `-c protocol.allow=never`, `GIT_ALLOW_PROTOCOL=none`, `GIT_NO_LAZY_FETCH=1` | partial clone의 지연 가져오기와 모든 전송(원격의 `ext::` 주소, `core.sshCommand`) |
| `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=/dev/null`, `HOME=/nonexistent` | worker 이미지나 사용자의 git 설정 |
| `GIT_CEILING_DIRECTORIES=<root의 부모>` | 작업 공간 위의 저장소를 찾는 것 |
| `GIT_TERMINAL_PROMPT=0` | 자격 증명을 묻는 것 |
| `GIT_LITERAL_PATHSPECS=1` | 경로 인자의 와일드카드 해석 |
| `PATH=/usr/bin:/bin`, `LC_ALL=C` | worker 환경의 `PATH`, 지역화된 출력 |

worker의 환경은 물려주지 않는다. 호출마다 제한 시간 60초, `WaitDelay` 1초, 표준 출력은 64MiB를 넘으면 실패이고 표준 오류는 버린다. 오류에는 실패한 명령과 종료 코드만 담는다. git의 메시지는 파일 내용을 인용할 수 있기 때문이다.

점검 범위는 저장소 전체가 아니라 프로젝트 폴더다(`-C <프로젝트 폴더>`와 경로 `.`). 하위 폴더 안의 `.git`(다른 저장소, submodule)은 `Nested`로 따로 알아낸다.

스캔이 커밋 해시를 읽는 호출(`internal/scan/commit.go`)도 `worktree.CommitHash`를 불러 같은 인자와 환경, 같은 위치 확인을 쓴다.

## 이유

- 검토 항목 6: 스캔 대상 저장소의 설정은 신뢰할 수 없는 입력이다. 위 표의 옵션이 실행 경로를 하나씩 막고, 막을 수 없는 filter와, 한 겹(`--no-optional-locks`)으로만 막히는 설정 hook은 설정에서 찾아 거부한다. 인덱스도 신뢰할 수 없는 입력이므로 보고된 경로가 폴더 안인지 확인한다. `core.worktree`는 작업본을 다른 곳으로 옮길 수 있으므로 거부한다.
- 검토 항목 5: 작업본 루트, git 폴더, 공용 git 폴더 가운데 하나라도 작업 공간 밖이면 git은 작업 공간 밖의 파일을 읽는다. 세 경로를 모두 풀어 확인한다.
- 검토 항목 18: 저장소가 프로젝트 폴더보다 크면 폴더 밖의 변경은 도구가 건드리지 않는 곳이므로 적용을 막을 이유가 없다. 폴더 안의 다른 저장소에 든 파일은 바깥 저장소에서 "추적되지 않음"으로 보이지만 안쪽 저장소의 커밋되지 않은 변경일 수 있으므로 `Nested`로 가려낸다.
- 검토 항목 29: 도구는 커밋하지 않고, 저장소를 바꾸는 git 명령을 실행하지 않는다. 인덱스도 쓰지 않는다(`--no-optional-locks`). 점검 앞뒤로 `.git` 아래 모든 파일의 이름, 크기, 내용 해시가 같은지 테스트한다.
- assume-unchanged, skip-worktree로 표시한 파일은 `status`가 변경을 보고하지 않는다. 그런 파일의 변경을 덮어쓰지 않도록 표시 자체를 변경으로 센다.

## 검토한 다른 방법

- Go git 라이브러리(go-git): 의존성이 크고, `autocrlf`, `.gitattributes`, 무시 규칙의 해석이 git과 달라 "변경 없음"의 뜻이 개발자의 `git status`와 어긋난다.
- 인덱스의 blob 해시를 직접 계산해 비교: 외부 명령이 실행될 여지는 없지만 `core.autocrlf=true`인 저장소의 CRLF 파일이 모두 "변경됨"이 되어 적용할 수 없다.
- filter를 거부하지 않고 끄기: 이름마다 `-c filter.<이름>.clean=`, `-c filter.<이름>.process=`를 주려면 먼저 설정을 읽어야 하므로 설정 검사와 같은 일을 하고, 빠뜨린 이름 하나가 곧 실행이다. `--attr-source`는 `.git/info/attributes`를 막지 못했다.
- `-c protocol.allow=never`만 쓰기: 저장소의 `protocol.<이름>.allow`가 우선해 지연 가져오기가 실행되었다.
