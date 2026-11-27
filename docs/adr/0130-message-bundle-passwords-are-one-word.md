# 메시지 묶음의 비밀번호는 한 낱말이다

- 날짜: 2026-11-27
- 상태: 확정

## 배경

`.properties` 파일에서 키의 끝이 `password`, `passwd`, `pwd`인 줄은 값을 비밀번호로 보고한다(0052). 한글이 든 값은 메시지로 보고 보고하지 않게 했지만(0105), 영어로 쓴 메시지 묶음의 `changePassword=Change password`, `error.password=Password is required.` 같은 줄은 여전히 비밀번호로 보고되었다(#116). 0105는 "값에 공백이 있으면 메시지로 본다"를 모든 `.properties`에 거는 방법을, 설정 파일의 공백 든 암호문을 놓친다는 이유로 기각했다.

## 결정

1. **메시지 묶음은 파일 이름으로 가린다.** 이름이 `messages`로 시작하거나(`messages.properties`, `messages_en_US.properties`) `Messages`를 담은(`ValidationMessages.properties`) `.properties` 파일이다. Spring의 `MessageSource`와 Bean Validation이 쓰는 이름이다.
2. **메시지 묶음에서는 새 규칙 `properties-message-password`가 비밀번호를 보고하고, 값은 공백 없는 한 낱말이다.** 공백이 든 값은 문장으로 보고 보고하지 않는다. 계정 규칙(`properties-username`, 0078)과 같은 기준이다. 키 모양, placeholder, 한글을 거르는 조건은 `properties-password`와 같다.
3. **`properties-password`는 메시지 묶음을 보지 않는다.** 다른 `.properties` 파일에서는 지금처럼 공백이 든 값도 보고한다.
4. 주석 줄의 규칙(`properties-comment-password`)은 이미 값이 한 낱말이므로 바꾸지 않는다.

이 결정은 사용자에게 세 가지 방법(메시지 묶음에서만 공백 값을 거르기, 모든 `.properties`에서 거르기, 이 오탐을 남겨 두기)을 보여 주고 사용자가 고른 것이다.

## 이유

- 메시지 묶음의 값은 화면에 보이는 문장이라 거의 언제나 공백이 있고, 비밀번호는 거의 언제나 한 낱말이다. 파일 이름으로 범위를 좁히면 설정 파일의 공백 든 암호문은 지금처럼 보고된다.
- 규칙을 둘로 나눈 것은 opengrep이 한 규칙 안에서 파일 이름에 따라 값의 모양을 바꿀 수 없기 때문이다. 두 규칙은 `paths`의 포함과 제외가 서로 맞물려 한 줄을 둘 다 보고하지 않는다.

## 검토한 다른 방법

- **모든 `.properties`에서 공백이 든 값을 거른다.** 오탐은 더 줄지만 0105에서 따진 것처럼 설정 파일의 공백 든 암호문을 놓친다.
- **메시지 묶음을 규칙에서 아예 뺀다.** 0052와 0105가 기각한 방법이다. 메시지 묶음에 잘못 넣은 실제 비밀번호를 놓친다.
- **문장 모양(마침표, `{0}` 같은 자리 표시, 대문자로 시작)으로 거른다.** `Change password`처럼 마침표도 자리 표시도 없는 메시지가 많아 거르지 못한다.

## 알려진 한계

- 메시지 묶음에 든, 공백이 있는 실제 비밀번호는 보고하지 않는다.
- 한 낱말 메시지(`label.password=Password`)는 지금처럼 비밀번호로 보고한다.
- `errors.properties`, `labels.properties`처럼 다른 이름의 메시지 묶음은 메시지 묶음으로 보지 않는다.
- 이미 스캔한 프로젝트를 다시 스캔하면 메시지 묶음의 항목이 줄고, 남은 한 낱말 항목은 규칙 이름이 `properties-message-password`로 바뀐다.
