# manifest를 읽을 수 없게 만드는 manifest 안의 자리는 건너뛴다

- 날짜: 2026-11-25
- 상태: 확정

## 배경

`secrets-manifest.yml`도 스캔 대상이라, 그 안의 값이 비밀 값 항목이 되고 `externalize`로 판정되면 적용의 대상 파일이 된다. ADR 0032의 9번에 따라 적용은 치환 단계 뒤에 manifest를 다시 읽고 합쳐 쓴다. 그런데 그 값이 따옴표 없는 문자열이면 치환한 placeholder(`${…}`)는 `manifest.Parse`가 받는 형식이 아니다. 그래서 manifest 쓰기가 `the manifest could not be written; see the worker log`로 실패했다. 소스는 이미 바뀌었고 OpenBao에 값이 저장되었는데, manifest는 사람이 고쳐야 다음 적용이 이어 쓸 수 있었다(ADR 0032의 "알려진 한계", #99).

## 결정

적용과 점검은 값을 저장하기 전, 실행을 준비하는 단계에서 manifest(프로젝트 폴더 바로 아래의 `secrets-manifest.yml`) 안에 남은 대상의 치환을 미리 해 본다. 그 결과를 `manifest.Parse`가 읽지 못하면 그 파일의 대상을 모두 새 사유 `breaks-manifest`로 건너뛴다. 이 경우 값은 OpenBao에 저장하지 않고, manifest도 바꾸지 않는다. 화면에는 "바꾸면 manifest를 읽을 수 없게 됨"으로 보이고, 그 값을 큰따옴표로 감싸거나 직접 옮기라고 안내한다. 치환한 결과를 읽을 수 있으면(값이 큰따옴표 안에 있으면) 지금처럼 적용한다.

`apply_item.reason`의 허용 목록에 `breaks-manifest`를 더하는 마이그레이션은 `0013_apply_breaks_manifest.sql`이다.

## 이유

- 실패를 실행 끝이 아니라 계획을 확정할 때 알면, 소스와 OpenBao를 바꾸기 전에 멈출 수 있다. 점검 화면에도 같은 사유가 보여 사용자가 확인 전에 안다.
- 건너뛴 값은 manifest에 그대로 남는다. 하지만 manifest는 도구가 관리하는 파일이고, 거기 든 값은 사람이 손으로 쓴 것이다. 그 값을 고치는 일은 사람에게 맡기고, 다른 파일의 적용과 manifest 기록은 막지 않는다.
- 미리 해 보는 치환은 실제 치환 단계와 같은 `rewrite.Replace`를 쓰므로 두 결과가 어긋나지 않는다.

## 검토한 다른 방법

- manifest 안의 자리를 늘 건너뛰기: 단순하지만, 큰따옴표 안의 값은 지금도 문제 없이 외부화된다(`TestApply_ManifestIsATarget`). 그런 값까지 소스에 남기게 된다.
- placeholder를 manifest에서만 큰따옴표로 감싸 쓰기: 치환한 글자가 다른 파일과 달라지고, 계획의 `Text`와 실제로 쓴 글자가 어긋난다.
- 스캔에서 manifest를 빼기: 사람이 manifest에 적은 비밀 값을 아무도 보지 못하게 된다.
