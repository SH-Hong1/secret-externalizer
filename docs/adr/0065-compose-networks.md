# compose 네트워크를 연결마다 나눈다

- 날짜: 2026-11-24
- 상태: 확정

## 배경

`compose.yaml`의 서비스는 `pdf`(결정 0043)를 빼고 모두 기본 네트워크 하나에 붙어 있었다. 그래서

- worker와 worker가 실행하는 스캐너(betterleaks, opengrep)가 바깥으로 연결할 수 있었다. 스캐너는 작업 공간의 원본을 읽으므로, 잘못 만들어졌거나 바뀐 스캐너가 값을 밖으로 보낼 수 있었다(결정 0032가 남긴 위험과 같은 줄기다).
- keycloak, postgres, valkey도 `openbao:8200`에 닿았다. 자격 증명이 없어 값을 읽지는 못하지만 닿을 이유가 없다.
- 모든 서비스가 바깥으로 연결할 수 있었다.

남은 일에는 "web이 `openbao:8200`에 닿을 수 있다"고 적혀 있었는데, 원본 보기(결정 0028) 뒤로 web은 OpenBao를 정당하게 쓴다. web은 계속 닿아야 한다.

## 결정

네트워크를 다음과 같이 나눈다.

| 네트워크 | 종류 | 붙는 서비스 | 나르는 연결 |
|---|---|---|---|
| `edge` | bridge, NAT 끔 | web, keycloak, postgres, valkey | 호스트 포트, web → postgres·valkey·keycloak, keycloak → postgres |
| `db` | internal | postgres, worker | worker → postgres |
| `bao` | internal | openbao, web, worker | web·worker → OpenBao |
| `pdf` | internal | pdf, web | web → 변환기 (그대로) |
| `test` | bridge, NAT 끔 | openbao-test, pdf-test (`test` 프로필) | 통합 테스트의 호스트 포트 |

`openbao-secrets`는 볼륨에 파일만 쓰므로 `network_mode: none`이다. 기본 네트워크는 더 이상 만들어지지 않는다.

NAT를 끈 bridge는 `driver_opts`의 `com.docker.network.bridge.enable_ip_masquerade: "false"`로 만든다.

## 이유

- 호스트 포트는 internal 네트워크로 열 수 없다. 이 기계(Docker 29.5.2, colima)에서 internal 네트워크에 붙은 컨테이너의 `127.0.0.1:19992`는 호스트에서 연결되지 않았다. NAT를 끈 bridge에 붙은 컨테이너는 호스트 포트가 열리고 `1.1.1.1:80`으로는 연결되지 않았다. 그래서 포트를 여는 서비스는 NAT를 끈 bridge에, 나머지는 internal 네트워크에 둔다.
- worker는 internal 네트워크(`db`, `bao`)에만 붙는다. worker와 스캐너는 postgres와 OpenBao 말고는 아무 데도 닿지 않고, 바깥 이름도 풀지 못한다. 스캐너는 이미지에 들어 있고 규칙도 `/rules`에서 읽으므로 바깥 연결이 필요 없다(결정 0020에서 네트워크 없이 실행해 확인했다).
- OpenBao에 닿는 것은 web과 worker뿐이다.
- web, keycloak, postgres, valkey를 `edge` 하나에 둔다. 서로 닿는 것은 web이 원래 쓰는 연결과 keycloak → postgres이고, 이 넷 가운데 원본을 가진 것은 없다. 연결마다 네트워크를 따로 두면(서비스마다 포트용 네트워크 하나씩) 이 넷이 서로 닿지 않게 할 수 있지만 네트워크가 넷 더 생기고 막는 것이 거의 없다.

실제 스택에서 각 서비스의 네트워크 이름공간으로 확인한 결과다(`OPEN`은 연결됨, `-`는 연결되지 않음).

| 출발 | postgres | valkey | keycloak | openbao | pdf | 1.1.1.1:80 | 바깥 이름 풀이 |
|---|---|---|---|---|---|---|---|
| web | OPEN | OPEN | OPEN | OPEN | OPEN | - | 됨 |
| worker | OPEN | - | - | OPEN | - | - | 안 됨 |
| keycloak | OPEN | OPEN | OPEN | - | - | - | 됨 |
| postgres | OPEN | OPEN | OPEN | - | - | - | 됨 |
| valkey | OPEN | OPEN | OPEN | - | - | - | 됨 |
| openbao | - | - | - | OPEN | - | - | 안 됨 |
| pdf | - | - | - | - | OPEN | - | 안 됨 |

호스트에서 8080, 8081, 15432, 16379가 열렸고, `test` 프로필의 13000, 18200도 열렸다(pdf-test에서 `1.1.1.1:80`과 `postgres:5432`는 연결되지 않았다). 그 위에서 `dev-admin`으로 Keycloak 로그인, 예제 프로젝트 등록과 스캔(83개 항목), 원본 보기(200), 리포트 PDF 내보내기(200, `application/pdf`)가 모두 되었다. 이 확인은 이미지를 다시 빌드하지 않고 마이그레이션 9까지 아는 기존 이미지로 했다. 바꾼 것은 compose뿐이다.

## 알려진 한계

- NAT를 끈 bridge에서도 Docker의 내장 DNS는 바깥 이름을 풀어 준다. `edge`의 서비스는 바깥에 연결하지 못하지만 DNS 질의로 정보를 내보낼 길은 남는다. worker, openbao, pdf는 internal 네트워크에만 있어 이름을 풀지 못한다.
- `edge`의 서비스는 네트워크 게이트웨이, 곧 Docker VM 자신의 열린 포트에 닿는다(colima에서 `172.22.0.1:22`의 sshd가 열렸다). 기본 네트워크도 같았다. VM 밖의 호스트 주소(`192.168.5.2`)에는 닿지 않았다.
- 바깥으로 나가는 길을 막는 것은 Docker의 bridge 설정에 기대는 것이다. 다른 런타임이나 네트워크 드라이버에서는 같은 확인을 다시 해야 한다.
- 이 구성은 로컬 개발용 `compose.yaml`의 것이다. 공유하거나 배포하는 환경의 네트워크는 그 환경에서 따로 정한다.

## 검토한 다른 방법

- 모든 네트워크를 internal로 두고 호스트 포트를 없앤다: 브라우저가 web과 Keycloak에 닿을 수 없고 통합 테스트가 postgres, valkey에 닿을 수 없다.
- 포트를 여는 서비스마다 NAT를 끈 네트워크를 따로 둔다: 위의 "이유" 마지막 항목.
- worker에 바깥 연결을 남기고 스캐너만 따로 막는다: 스캐너는 worker 컨테이너 안에서 같은 네트워크 이름공간으로 돌므로 compose로는 나눌 수 없다. 스캔을 다른 컨테이너로 나누는 일은 남은 일에 따로 있다.
