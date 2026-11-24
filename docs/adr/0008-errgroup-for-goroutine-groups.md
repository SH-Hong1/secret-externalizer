# 실패할 수 있는 goroutine 묶음에 errgroup을 쓴다

- 날짜: 2026-11-18
- 상태: 확정

## 배경

`AGENTS.md`는 실패할 수 있는 goroutine 묶음에 `errgroup.Group`을 쓰라고 정한다. `errgroup`은 표준 라이브러리가 아니라 `golang.org/x/sync`에 있으므로 의존성 추가의 이유를 기록해야 한다.

## 결정

`golang.org/x/sync/errgroup`을 쓴다. `golang.org/x/sync`의 다른 패키지는 필요가 생길 때 따로 판단한다.

## 이유

- 표준 라이브러리의 `sync.WaitGroup`은 오류를 모으지 않는다. 오류 채널을 함께 쓰면 채널 크기, 닫는 시점, 첫 오류 뒤의 취소를 매번 직접 맞춰야 하고, 이 부분에서 goroutine 누수가 자주 생긴다.
- `errgroup.WithContext`는 첫 오류에서 context를 취소하므로 "모든 goroutine에는 주인이 있고 주인이 끝을 기다린다"는 규칙을 짧은 코드로 지킬 수 있다.
- `SetLimit`으로 동시 실행 수를 제한할 수 있어 스캐너를 여러 개 돌릴 때 따로 세마포어를 만들지 않아도 된다.
- Go 팀이 관리하는 모듈이고 다른 의존성을 끌어오지 않는다.

## 검토한 다른 방법

- `sync.WaitGroup`과 오류 채널: 위 이유로 제외.
- 직접 만든 작은 묶음 타입: `errgroup`과 같은 것을 다시 만드는 일이다.
