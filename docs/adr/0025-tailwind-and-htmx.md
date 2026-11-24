# 스타일은 tailwindcss standalone CLI, 부분 갱신은 htmx 2

- 날짜: 2026-11-19
- 상태: 확정

## 배경

계획 4에서 화면에 확인받은 시안(`docs/design/`)의 스타일을 입히고, 판정 화면에서 목록에서 고른 묶음의 상세만 바꿔 끼워야 한다. 이 저장소에는 Node.js 빌드 단계가 없고, web 이미지는 Go 소스만으로 만든다. CSP는 `default-src 'self'; base-uri 'none'; frame-ancestors 'none'`이고 CDN을 쓰지 않으며, 인라인 스크립트와 인라인 스타일을 쓰지 않는다. 글꼴은 결정 기록 0029에서 정했다.

## 결정

- **스타일**: tailwindcss 4의 standalone CLI(v4.3.5)로 `internal/web/assets/app.css`에서 `internal/web/static/app.css`를 만든다. 입력 파일은 `@import "tailwindcss" source(none)`과 `@source "../templates"`로 템플릿만 읽게 하고, `@theme`에 `docs/design/README.md`의 색, 글꼴, 글자 크기, 모서리 값을 옮긴다. 기본 색 팔레트는 지워 시안에 없는 색을 쓸 수 없게 한다. 화면 요소(머리 띠, 버튼, 표, 표시, 코드 상자, 양식, 목록 줄)는 `@layer components`의 클래스로 두고, 템플릿은 이 클래스와 배치용 유틸리티 몇 개를 쓴다.
- **CLI 고정**: `Makefile`에 버전과 플랫폼별(darwin, linux의 arm64, amd64) 체크섬을 적는다. 체크섬은 릴리스의 `sha256sums.txt`와 GitHub 자산 digest가 같은 것을 확인했다. `make tools`가 `bin/tailwindcss`를 받아 체크섬을 확인한다.
- **만든 CSS를 저장소에 넣는다**: `make css`가 만들고, `make check`의 `css-check`가 다시 만들어 저장소의 파일과 바이트 단위로 비교한다. `go build`와 web 이미지 빌드는 바뀌지 않는다.
- **htmx 2.0.11**을 `internal/web/static/htmx.min.js`로 넣는다. 체크섬은 GitHub 릴리스 자산의 digest이고 npm 패키지 `htmx.org@2.0.11`의 `dist/htmx.min.js`와 같다. `make htmx`가 받아 확인한다. 4.x가 나와 있지만 계획 4는 htmx 2를 기준으로 썼고, 4는 속성과 이벤트 이름이 바뀐 메이저 버전이라 쓰지 않는다. 2.0.11은 2026-11-01에 나온 2.x의 마지막 판이다.
- **htmx 설정**: `layout.html`의 `<meta name="htmx-config">`로 다음을 끈다.
  - `includeIndicatorStyles: false`: htmx가 `<head>`에 `<style>` 요소를 넣지 않는다(CSP의 `style-src`를 넓히지 않는다).
  - `allowEval: false`: `hx-on`, `hx-vars`, 이벤트 필터처럼 `eval`이나 `new Function`을 쓰는 기능이 동작하지 않고 오류 이벤트만 낸다.
  - `allowScriptTags: false`: 바꿔 끼운 HTML 안의 `<script>`를 실행하지 않는다.
  - `historyCacheSize: 0`, `refreshOnHistoryMiss: true`: htmx가 쪽의 HTML을 `localStorage`에 남기지 않는다. 화면에는 가린 미리보기와 주변 줄이 있어 브라우저 저장소에 남기지 않고, 뒤로 가기는 서버에서 다시 받는다.
  - 이 설정으로 CSP를 그대로 둔 채 `hx-get`으로 쪽 일부를 바꿔 끼우는 것을 Chromium에서 확인했다(CSP 위반 없음, `<style>` 요소와 `style` 속성 없음).
- **htmx를 쓰는 범위**: 판정 화면에서 목록에서 고른 묶음의 상세를 바꿔 끼우는 일과, 판정·원본 보기처럼 상세 한 곳만 바뀌는 요청이다. 모든 링크와 양식은 htmx 없이도 쪽 전체 요청으로 동작해야 하고, htmx는 그 위에 얹는다. 다른 화면은 쪽 전체를 그린다.
- **글꼴 파일**: 결정 기록 0029의 글꼴을 다음에서 받는다. `make fonts`가 받고, 확인하고, 변환한다.
  - IBM Plex Sans KR 1.1.0: npm 패키지 `@ibm/plex-sans-kr@1.1.0`(IBM/plex 저장소의 릴리스 `@ibm/plex-sans-kr@1.1.0`과 같은 판)의 `fonts/complete/woff2/hinted/`에서 Regular, SemiBold, Bold를 쓴다. 꾸러미 체크섬은 npm의 integrity(sha512)와 맞춰 보았다. 라이선스는 꾸러미의 `LICENSE.txt`(OFL 1.1)를 `IBMPlexSansKR-LICENSE.txt`로 둔다. 글자를 줄이지(subset) 않은 완전판이고 파일 하나가 370~440KB다.
  - D2Coding 1.4.0: naver/d2-coding-font 릴리스 `VER1.4.0`의 `D2Coding-Ver1.4.0-20261003.zip`에서 표준판(합자 없음) Regular, Bold TTF를 쓴다. 꾸러미 체크섬은 GitHub 자산 digest와 맞춰 보았다. 배포처가 TTF만 내므로 fontTools 4.68.0과 brotli 1.2.1로 WOFF2로 바꾼다(`fonttools ttLib.woff2 compress`). 같은 입력에서 같은 파일이 나오는 것을 확인했고, 결과 파일의 체크섬도 `Makefile`에 적어 `make fonts`가 확인한다. 라이선스는 꾸러미의 `OFL.txt`를 `D2Coding-LICENSE.txt`로 둔다. 파일 하나가 1.5MB 정도다.
  - 파일 이름에 글꼴 버전을 넣는다(`D2Coding-1.4.0-Regular.woff2`). CSS는 글꼴을 버전 쿼리 없는 상대 주소로 부르고 정적 파일은 1년 동안 캐시되므로, 글꼴을 바꾸면 주소도 바뀌어야 한다.
- **정적 파일 제공**: `//go:embed static`으로 바이너리에 넣고 `GET /static/{path...}`로 준다. 시작할 때 읽어 둔 파일을 정확한 경로로만 찾으므로 `..`, 폴더, 모르는 이름은 404다. 알려진 확장자(`.css`, `.js`, `.woff2`, `.txt`)만 넣고 `Content-Type`을 직접 정한다. 템플릿은 `asset` 함수로 `/static/app.css?v=<SHA-256 앞 8자리>` 주소를 얻고, 응답은 `Cache-Control: public, max-age=31536000, immutable`이다. 다른 응답의 `no-store`는 그대로다.

## 이유

- standalone CLI는 실행 파일 하나라 Node.js와 `package.json` 없이 버전과 체크섬으로 고정할 수 있다. 쓰지 않는 유틸리티는 빠지고, 시안의 값을 테마 변수 한 곳에 모을 수 있다.
- 만든 CSS를 저장소에 두면 web 이미지 빌드가 네트워크에서 CLI를 받지 않아도 되고, Go 도구만 있으면 빌드할 수 있다. 저장소의 CSS가 입력과 어긋나면 `make check`가 막는다.
- htmx는 서버가 그린 HTML을 그대로 바꿔 끼우므로 화면 상태를 서버 한 곳에 둘 수 있고, 위 설정이면 CSP를 넓히지 않아도 된다.
- 파일 주소에 내용의 지문을 붙이면 배포 뒤에도 옛 파일을 쓰지 않으면서 브라우저가 파일을 오래 보관할 수 있다.

## 검토한 다른 방법

- **이미지 빌드 단계에서 CLI로 CSS를 만든다**: 저장소에 만든 파일이 없어 깔끔하지만 web 이미지 빌드가 CLI를 받아야 하고, `go build`만으로는 화면이 깨진다.
- **Node.js와 npm의 tailwindcss**: 플러그인을 쓸 수 있지만 이 저장소에 Node.js 도구 사슬을 하나 더 들인다.
- **손으로 쓴 CSS만**: 도구가 없지만 시안의 값을 여러 곳에 되풀이하게 되고 쓰지 않는 규칙이 쌓인다.
- **htmx 4**: 최신판이지만 메이저 변경이고, 계획은 htmx 2를 기준으로 썼다.
- **htmx 없이 쪽 전체 새로 고침만**: 판정할 때마다 목록 스크롤 위치를 잃는다. 스크립트를 끈 브라우저에서는 이 방식으로 동작한다.
- **정적 파일을 `http.FileServerFS`로 준다**: 폴더 목록을 보여 주고 `index.html`로 돌려보내는 동작을 따로 막아야 한다. 고정된 파일 몇 개라 직접 찾는 편이 단순하다.
