# 스캐너는 worker 이미지에 넣고 os/exec로 실행한다

- 날짜: 2026-11-18
- 상태: 확정

## 배경

처음 구상은 Go의 docker client로 스캐너 컨테이너를 그때그때 띄우는 것이었다. 탐지 도구 후보는 betterleaks, trivy, opengrep이었다.

## 결정

- betterleaks와 opengrep 바이너리를 버전과 체크섬을 고정해 worker 이미지에 넣고 `os/exec`로 실행한다.
- 스캐너는 공통 인터페이스 뒤에 둔다.
- 1차에서 trivy는 쓰지 않는다.

## 이유

- docker client 방식은 앱 컨테이너에 docker.sock을 넘겨야 하고, 이는 호스트 root 권한과 같다. socket proxy로 좁혀도 컨테이너 생성 권한이 있으면 호스트 장악이 가능하다.
- 스캐너 컨테이너에 소스를 넘기려면 호스트 경로가 필요해, 로컬 소스 대상에서는 구성이 까다롭다.
- 두 도구 모두 단일 바이너리여서 이미지에 넣기 쉽다.
- betterleaks는 알려진 형식의 비밀 값, opengrep은 문맥으로 잡아야 하는 항목(ssh 인자, 설정 키 기반의 호스트와 계정)을 맡는다.
- trivy의 고유 강점인 취약점과 IaC 설정 오류 탐지는 이 프로젝트의 목표 밖이고, 비밀 값 탐지는 betterleaks와 겹친다. 도구가 셋이면 결과 병합이 복잡해지는 데 비해 얻는 탐지율이 작다.

## 검토한 다른 방법

- docker client + docker.sock 직접 마운트, docker client + socket proxy: 위 이유로 제외.
- trufflehog, kingfisher: 탐지한 키를 외부 API로 보내 유효성을 확인하는 기능이 차별점인데, 키를 외부로 보내는 동작이 이 프로젝트와 맞지 않는다.
