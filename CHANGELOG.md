# 변경 내역

이 프로젝트에서 달라진 내용을 기록한다. 형식은 [Keep a Changelog](https://keepachangelog.com/ko/1.1.0/)를 따른다. 결정의 이유는 여기에 쓰지 않고 `docs/decisions/`에 쓴다.

## [Unreleased]

### 추가

- Go 모듈과 `cmd/web` 진입점 골격
- 버전을 고정한 gofumpt, golangci-lint 설치와 `make fmt`, `make lint`, `make vet`, `make test`, `make check`
- golangci-lint 설정
- Go 규칙 문서 `AGENTS.md`와 이를 불러오는 `CLAUDE.md`
- 결정 기록 `docs/decisions/` 0001~0006
