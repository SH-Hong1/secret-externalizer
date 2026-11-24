# 자기가 닫은 작업을 찾은 임대 연장은 handler를 멈추지 않는다

- 날짜: 2026-11-25
- 상태: 확정

## 배경

CI에서 `cmd/worker`의 `TestRun_HandlesApplyJobs/ready`가 가끔 실패했다(#129). worker 로그에는 `apply-check` 작업을 가져간 지 약 0.14초 만에 `job lease lost`, 이어서 0.001초 뒤 `job stopped before it finished`가 남았고 `apply check finished`는 없었다.

원인은 handler가 작업을 닫는 커밋과 임대 연장의 경합이다.

1. handler(`SaveCheck`, `SaveResult` 등)는 한 트랜잭션에서 작업 행을 `done`이나 `failed`로 바꾸고 결과를 쓴 뒤 커밋한다.
2. 그사이 하트비트의 `ExtendJob` UPDATE가 같은 행의 잠금을 기다린다.
3. 커밋이 끝나면 PostgreSQL은 `ExtendJob`의 조건을 새 행으로 다시 평가한다. 작업이 더는 `running`이 아니므로 0행이고, `ExtendJob`은 `job.ErrLeaseLost`를 돌려준다.
4. `job.Run`은 임대를 잃었다고 보고 handler의 context를 취소한다. 그때 handler는 아직 COMMIT의 응답을 기다리는 중이라 pgx가 취소로 오류를 돌려준다. 커밋은 데이터베이스에 반영되었을 수도, 연결이 끊겨 되돌려졌을 수도 있다.

반영된 경우에는 결과는 맞지만 로그가 "끝내지 못함"으로 남고, 되돌려진 경우에는 작업이 `running`인 채 임대(2분)가 끝날 때까지 기다린다. 테스트는 하트비트를 20ms로 줄여 이 틈이 자주 열린다. 운영의 하트비트(30초)에서도 같은 틈이 있다.

## 결정

- `job.ErrClosed`를 둔다. `db.Store.ExtendJob`은 연장할 행이 없을 때 따로 한 문장으로 작업을 다시 읽어, 작업이 `done`이나 `failed`이고 `locked_by`가 자기이면 `ErrClosed`를, 아니면 전처럼 `ErrLeaseLost`를 돌려준다. 다시 읽는 문장을 따로 두는 것은, UPDATE와 같은 문장 안의 조회는 그 문장이 시작할 때의 스냅숏을 보아 방금 커밋된 닫기를 보지 못하기 때문이다.
- `job.Run`의 임대 연장은 `ErrClosed`를 받았을 때 worker 자신의 시계로 잰 임대가 아직 남아 있으면 handler를 취소하지 않고 연장만 멈춘다. 임대가 이미 끝났으면 `ErrLeaseLost`와 똑같이 handler를 취소한다.

## 이유

- 작업 행의 상태를 바꾸는 곳은 잡기, 포기(임대가 끝난 작업만), 돌려주기, 닫기 넷뿐이다. 자기 임대가 남아 있는 동안 다른 worker는 작업을 가져가거나 포기시킬 수 없고, 돌려주기는 handler가 돌아온 뒤에만 한다. 그러니 그동안 작업이 `locked_by`가 자기인 채로 닫혀 있다면 이 worker의 handler가 닫은 것이고, handler는 곧 돌아온다. 그 handler를 취소할 까닭이 없다.
- 임대가 끝난 뒤에는 다른 worker의 잡기가 시도 횟수를 다 쓴 작업을 `failed`로 포기시킬 수 있고, 그때도 `locked_by`는 이 worker로 남는다. 이 경우는 정말 임대를 잃은 것이라 0022대로 handler를 멈춘다.
- 원인을 고치면 handler 쪽은 바꿀 것이 없다. 닫는 커밋이 취소되지 않으므로 결과와 로그가 맞는다.

## 검토한 다른 방법

- 닫는 커밋을 취소되지 않는 context로 하기: 경합은 없애지만, 임대를 정말 잃은 뒤의 커밋도 멈추지 못한다. 그 커밋은 `locked_by` 조건으로 막히기는 하지만 0022의 "임대가 끝나면 스스로 멈춘다"와 어긋난다.
- handler가 닫기 직전에 연장을 멈추게 하기: 스캔, 점검, 적용의 닫는 곳마다 `job.Run`과의 약속을 넣어야 하고, 하나라도 빠지면 같은 경합이 남는다.
- 테스트의 하트비트를 늘리기: 틈이 드물어질 뿐이고 운영에서도 열린다.
