# pdf 컨테이너는 스왑을 쓰지 않는다

- 날짜: 2026-11-25
- 상태: 확정

## 배경

`docs/adr/0043-pdf-converter-setup.md`는 pdf 컨테이너(Gotenberg)에 `mem_limit: 1g`를 두어, 너무 큰 문서에서 Chromium이 이 한도 안에서 죽고 그 변환만 실패하게 했다. 그런데 `memswap_limit`은 두지 않았다. Docker는 `memswap_limit`이 없으면 메모리와 스왑의 합을 `mem_limit`의 두 배로 둔다. 지금 띄운 pdf 컨테이너의 `docker inspect`도 `HostConfig.Memory`가 1073741824, `HostConfig.MemorySwap`이 2147483648이다.

이 개발 VM에는 스왑이 없어(`SwapTotal: 0 kB`) 차이가 드러나지 않았지만, 스왑이 있는 호스트에서는 Chromium이 1GiB를 넘은 뒤에도 스왑을 1GiB까지 더 쓴다. 그 동안 변환은 죽지 않고 느려지며, 호스트의 스왑을 다른 서비스와 나눠 쓴다.

## 결정

`compose.yaml`의 pdf 서비스에 `memswap_limit: 1g`를 둔다. `mem_limit`과 같은 값이라 컨테이너는 스왑을 쓰지 않는다. `pdf-test`는 같은 블록을 이어받는다.

## 이유

- 0043이 메모리 한도를 둔 까닭은 "큰 문서는 한도 안에서 빨리 실패한다"였다. 스왑이 열려 있으면 실패하기 전에 느려져, Gotenberg의 15초 제한이나 web의 20초 제한에 걸려 같은 실패를 더 늦게, 호스트를 더 무겁게 한 뒤에 낸다.
- 리포트가 가질 수 있는 크기는 0042가 정하고, 그 크기의 peak는 480~582MiB로 1GiB 안에 든다. 스왑이 없어도 정상 리포트는 영향을 받지 않는다.

## 검토한 다른 방법

- `mem_swappiness: 0`: 커널이 스왑을 덜 쓰게 할 뿐 막지 않고, cgroup v2에서는 Docker가 이 값을 무시한다.
- 다른 서비스에도 같은 한도를 두기: 다른 서비스에는 아직 `mem_limit`이 없다. 메모리 한도를 어떻게 둘지는 서비스마다 따로 재야 하므로 이 결정에 넣지 않았다.
