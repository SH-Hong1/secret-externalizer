# PR의 제목, 본문, 라벨 변경은 그것이 바꾸는 체크만 다시 돌린다

- 날짜: 2026-11-25
- 상태: 확정

## 배경

`ci.yml`은 `opened`, `edited`, `synchronize`, `reopened`, `labeled`, `unlabeled`에 모두 돌았다(#136). PR 제목이나 본문을 고치거나 라벨 하나를 붙여도 `check`, `integration`, `sample`, AI 검토가 모두 다시 돌아 검토 비용이 들었고, 같은 ref의 concurrency 그룹이 진행 중인 `integration`을 취소했다.

제목과 본문이 바꾸는 것은 `pull-request` 체크뿐이다. 라벨 가운데 결과를 바꾸는 것은 `review: override` 하나이고, 그것도 `review` 작업의 판정만 바꾼다.

## 결정

- `ci.yml`은 `opened`, `synchronize`, `reopened`(그리고 `main` push)에만 돈다.
- `pull-request` 작업을 `ci.yml`에서 `.github/workflows/pr-text.yml`로 옮긴다. 이 워크플로는 `opened`, `edited`, `synchronize`, `reopened`에 돌고 작업 이름은 그대로 `pull-request`라 필수 체크 이름이 바뀌지 않는다. concurrency 그룹이 따로라 `ci.yml` 실행을 취소하지 않는다.
- `.github/workflows/review-label.yml`은 `labeled`, `unlabeled`에 돌고, 바뀐 라벨이 `review: override`일 때만 작업 `rerun-review`가 head 커밋의 마지막 `ci.yml` 실행이 끝나기를 기다렸다가 그 실행의 `review` 작업만 다시 돌린다. 다른 라벨은 아무것도 돌리지 않는다.
- `review` 작업의 판정 단계는 `review: override` 여부를 이벤트 내용이 아니라 그 순간 PR의 라벨에서 읽는다.

## 이유

- 작업을 다시 돌리지 않으려고 `ci.yml` 안에서 작업마다 `if`로 건너뛰면, 건너뛴 작업은 성공으로 집계되어 필수 체크를 통과시킨다. 라벨 하나를 붙였을 때 건너뛴 `review`가 앞서 실패한 `review`를 가린다. 그래서 다시 돌리지 않을 체크는 아예 실행을 만들지 않는 워크플로로 나눴다. `review-label.yml`의 작업 이름을 `review`가 아닌 `rerun-review`로 둔 것도 같은 이유다.
- 작업 하나를 다시 돌리면 GitHub은 처음 실행의 이벤트 내용을 그대로 쓴다. 이벤트의 라벨 목록을 읽으면 다시 돌린 검토가 새로 붙인 라벨을 보지 못하므로, 판정 단계가 PR의 라벨을 직접 읽는다.
- 라벨이 바꾸는 것은 판정뿐이지만, 검토자의 출력은 저장해 두지 않으므로 검토를 다시 돌린다. `review: override`는 드물게 붙이는 라벨이라 비용이 작다.
- 작업은 그 실행 전체가 끝난 뒤에만 다시 돌릴 수 있어 `rerun-review`가 기다린다. 기다리는 동안 원래 `review` 작업이 판정에 이르면 그때의 라벨을 읽으므로, 기다린 뒤 한 번 더 돌리는 것은 같은 결과를 확인할 뿐이다.

## 검토한 다른 방법

- `ci.yml` 안에서 작업별 `if`로 건너뛰기: 건너뛴 필수 체크가 성공으로 보여 실패한 검토를 가린다.
- 라벨 이벤트 전용 워크플로에 `review`라는 이름의 작업을 두고 `if`로 거르기: 다른 라벨이 붙으면 건너뛴 `review`가 생겨 같은 문제가 난다.
- 검토자 출력을 아티팩트로 남기고 라벨 변경 때 판정만 다시 내기: 검토를 다시 돌리지 않아도 되지만, 아티팩트를 찾고 믿는 단계가 늘어난다. 드문 라벨 하나에 비해 복잡하다.
- 재사용 워크플로(`workflow_call`)로 `pull-request` 작업을 나누기: 체크 이름이 `호출자 / 작업`이 되어 필수 체크 이름이 바뀐다.

## 남은 한계

- PR의 base 브랜치를 바꾸는 `edited`는 `pull-request`만 다시 돌린다. 다른 체크를 새 base에 맞춰 돌리려면 커밋을 push하거나 PR을 닫았다 다시 연다.
