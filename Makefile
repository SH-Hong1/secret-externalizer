GOFUMPT_VERSION := v0.12.0
GOLANGCI_LINT_VERSION := v2.15.0
BIN := $(CURDIR)/bin

# The tailwindcss standalone CLI that builds internal/web/static/app.css. The
# checksums are the ones the release lists in sha256sums.txt.
TAILWIND_VERSION := v4.3.5
TAILWIND_ASSET_darwin_arm64 := tailwindcss-macos-arm64
TAILWIND_ASSET_darwin_amd64 := tailwindcss-macos-x64
TAILWIND_ASSET_linux_arm64 := tailwindcss-linux-arm64
TAILWIND_ASSET_linux_amd64 := tailwindcss-linux-x64
TAILWIND_SHA256_darwin_arm64 := f3f4f5d1727e2b693b8f1c9a1492b5dd87008c86ea286bee1589cb458e6235fc
TAILWIND_SHA256_darwin_amd64 := a63702686a3ed048b3135d84a73be49ea9d47f880c9b20e54b3ef60934ad8cdf
TAILWIND_SHA256_linux_arm64 := ffcd0d21dbd7c605dc12743ff0c0b47debc963c86b399674084c1c8f2c96ecad
TAILWIND_SHA256_linux_amd64 := e47695cee3f0d90623a91f3939b08bdee610eb5ad99340adff9d4145b284d6cc
GOOS := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)

CSS_INPUT := internal/web/assets/app.css
CSS_OUTPUT := internal/web/static/app.css

# htmx, served as internal/web/static/htmx.min.js. The checksum is the asset
# digest of the GitHub release, which matches dist/htmx.min.js in the npm
# package of the same version.
HTMX_VERSION := 2.0.11
HTMX_SHA256 := d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717

# The fonts under internal/web/static/fonts. make fonts downloads the two
# archives, checks them, takes the files out, and converts D2Coding from TTF
# to WOFF2 with fontTools in a temporary virtual environment. The archive
# checksums match what the publishers list (the npm integrity hash, the
# GitHub release digest); FONT_SHA256 holds the checksums of the files that
# make fonts writes.
FONT_DIR := internal/web/static/fonts
PLEX_SANS_KR_VERSION := 1.1.0
PLEX_SANS_KR_URL := https://registry.npmjs.org/@ibm/plex-sans-kr/-/plex-sans-kr-$(PLEX_SANS_KR_VERSION).tgz
PLEX_SANS_KR_SHA256 := d351afb104446fe1b877144a36f10b3c05ea675de187b573c35a73023c5f9ae5
D2CODING_VERSION := 1.4.0
D2CODING_RELEASE := Ver$(D2CODING_VERSION)-20261003
D2CODING_URL := https://github.com/naver/d2-coding-font/releases/download/VER$(D2CODING_VERSION)/D2Coding-$(D2CODING_RELEASE).zip
D2CODING_SHA256 := 17e2da5e2879006eb725b87943f7988736ef8b6c9f8b84bc8e3bdbcb8dd46744
FONTTOOLS_VERSION := 4.68.0
BROTLI_VERSION := 1.2.1
FONT_SHA256 := \
	IBMPlexSansKR-$(PLEX_SANS_KR_VERSION)-Regular.woff2=055a35664c3c3965161c92292504c5633ba9604ec904edc1ed799bf2a436276d \
	IBMPlexSansKR-$(PLEX_SANS_KR_VERSION)-SemiBold.woff2=5ad7db28ba74d59fe14c260205c62ddb701320f4f098d8a45ef2757bebc29666 \
	IBMPlexSansKR-$(PLEX_SANS_KR_VERSION)-Bold.woff2=cf874a368dc2c2c0e4d1933c35611b849e48942023bfdd1fa11128475dbc4860 \
	D2Coding-$(D2CODING_VERSION)-Regular.woff2=76def7a188d179bb51e626731f6c2583e31b4ca37175175ed6f319aa713d951c \
	D2Coding-$(D2CODING_VERSION)-Bold.woff2=95d47a0c90566553f72c4c60526321409370f41254803441ab1785780c4aea1e

# sha256 prints the SHA-256 checksum of the file $(1).
sha256 = $$( (command -v sha256sum >/dev/null 2>&1 && sha256sum "$(1)" || shasum -a 256 "$(1)") | cut -d ' ' -f 1)

# fetch downloads the URL $(1) to the file $(2) and fails unless the file's
# SHA-256 checksum is $(3).
fetch = curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 --retry 3 --output "$(2)" "$(1)" && \
	got=$(call sha256,$(2)) && \
	if [ "$$got" != "$(3)" ]; then echo "checksum mismatch for $(1): got $$got, want $(3)" >&2; exit 1; fi

# The scanners the rule tests in internal/ruleset run. The tests skip when
# the executables are missing; make scanners puts them in bin/.
BETTERLEAKS_BIN ?= $(BIN)/betterleaks
OPENGREP_BIN ?= $(BIN)/opengrep

.PHONY: tools tailwind css css-check htmx fonts scanners fmt lint vet test test-integration hooks scanners-test openbao-unseal-key deploy-scripts-test check

tools: tailwind
	GOBIN=$(BIN) go install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)
	GOBIN=$(BIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(BIN)/gofumpt $(BIN)/golangci-lint:
	$(MAKE) tools

$(BIN)/tailwindcss:
	$(MAKE) tailwind

# tailwind installs the tailwindcss CLI for this machine as bin/tailwindcss.
tailwind:
	@test -n "$(TAILWIND_ASSET_$(GOOS)_$(GOARCH))" || { echo "no tailwindcss build is pinned for $(GOOS)/$(GOARCH)" >&2; exit 1; }
	mkdir -p $(BIN)
	$(call fetch,https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/$(TAILWIND_ASSET_$(GOOS)_$(GOARCH)),$(BIN)/tailwindcss.download,$(TAILWIND_SHA256_$(GOOS)_$(GOARCH)))
	chmod +x $(BIN)/tailwindcss.download
	mv $(BIN)/tailwindcss.download $(BIN)/tailwindcss

# css builds the stylesheet from $(CSS_INPUT) and the templates it names
# with @source.
css: $(BIN)/tailwindcss
	$(BIN)/tailwindcss --silent --minify --input $(CSS_INPUT) --output $(CSS_OUTPUT)

# css-check builds the stylesheet again and fails when the result differs
# from the committed $(CSS_OUTPUT).
css-check: $(BIN)/tailwindcss
	@out=$$(mktemp) && \
	$(BIN)/tailwindcss --silent --minify --input $(CSS_INPUT) --output "$$out" && \
	if cmp -s "$$out" $(CSS_OUTPUT); then rm -f "$$out"; else rm -f "$$out"; echo "$(CSS_OUTPUT) is out of date; run make css" >&2; exit 1; fi

# htmx downloads htmx into internal/web/static.
htmx:
	$(call fetch,https://github.com/bigskysoftware/htmx/releases/download/v$(HTMX_VERSION)/htmx.min.js,internal/web/static/htmx.min.js.download,$(HTMX_SHA256))
	mv internal/web/static/htmx.min.js.download internal/web/static/htmx.min.js

# fonts downloads the fonts and their licences into $(FONT_DIR). It needs
# curl, tar, unzip and python3, and works in a temporary directory that it
# removes at the end.
fonts:
	@work=$$(mktemp -d) && trap 'rm -r -f -- "$$work"' EXIT && \
	$(call fetch,$(PLEX_SANS_KR_URL),$$work/plex.tgz,$(PLEX_SANS_KR_SHA256)) && \
	$(call fetch,$(D2CODING_URL),$$work/d2coding.zip,$(D2CODING_SHA256)) && \
	mkdir "$$work/plex" "$$work/d2coding" "$$work/out" && \
	tar -xzf "$$work/plex.tgz" -C "$$work/plex" && \
	unzip -q "$$work/d2coding.zip" -d "$$work/d2coding" && \
	python3 -m venv "$$work/venv" && \
	"$$work/venv/bin/pip" install --quiet --disable-pip-version-check fonttools==$(FONTTOOLS_VERSION) brotli==$(BROTLI_VERSION) && \
	for w in Regular SemiBold Bold; do \
		cp "$$work/plex/package/fonts/complete/woff2/hinted/IBMPlexSansKR-$$w.woff2" "$$work/out/IBMPlexSansKR-$(PLEX_SANS_KR_VERSION)-$$w.woff2" || exit 1; \
	done && \
	cp "$$work/plex/package/LICENSE.txt" "$$work/out/IBMPlexSansKR-LICENSE.txt" && \
	"$$work/venv/bin/fonttools" ttLib.woff2 compress -q -o "$$work/out/D2Coding-$(D2CODING_VERSION)-Regular.woff2" "$$work/d2coding/D2Coding/D2Coding-$(D2CODING_RELEASE).ttf" && \
	"$$work/venv/bin/fonttools" ttLib.woff2 compress -q -o "$$work/out/D2Coding-$(D2CODING_VERSION)-Bold.woff2" "$$work/d2coding/D2Coding/D2CodingBold-$(D2CODING_RELEASE).ttf" && \
	cp "$$work/d2coding/OFL.txt" "$$work/out/D2Coding-LICENSE.txt" && \
	for pair in $(FONT_SHA256); do \
		f=$${pair%%=*}; want=$${pair#*=}; got=$(call sha256,$$work/out/$$f); \
		if [ "$$got" != "$$want" ]; then echo "checksum mismatch for $$f: got $$got, want $$want" >&2; exit 1; fi; \
	done && \
	chmod 644 "$$work"/out/* && mkdir -p $(FONT_DIR) && cp "$$work"/out/* $(FONT_DIR)/

# The scanner versions and checksums are pinned in deploy/worker/scanners.lock.
scanners:
	sh deploy/worker/fetch-scanners.sh "$$(go env GOOS)" "$$(go env GOARCH)" $(BIN)

fmt: $(BIN)/gofumpt $(BIN)/golangci-lint
	$(BIN)/golangci-lint fmt ./...

lint: $(BIN)/gofumpt $(BIN)/golangci-lint
	$(BIN)/golangci-lint run ./...

vet:
	go vet ./...

test:
	go test -race ./...

test-integration:
	@test -n "$(TEST_VALKEY_ADDR)" || { echo "set TEST_VALKEY_ADDR to a running valkey, for example TEST_VALKEY_ADDR=127.0.0.1:16379 make test-integration (the compose valkey-test service, started with --profile test)" >&2; exit 1; }
	@test -n "$(TEST_VALKEY_PASSWORD)" || { echo "set TEST_VALKEY_PASSWORD to the password of the valkey in TEST_VALKEY_ADDR, for example TEST_VALKEY_PASSWORD=dev-only-valkey-test-password make test-integration (the compose valkey-test service)" >&2; exit 1; }
	@test -n "$(TEST_DATABASE_URL)" || { echo "set TEST_DATABASE_URL to a running postgres, for example TEST_DATABASE_URL=postgres://se_app:dev-only-se-app-password@127.0.0.1:15432/secret_externalizer_test make test-integration (the compose postgres)" >&2; exit 1; }
	@test -n "$(TEST_BAO_ADDR)" || { echo "set TEST_BAO_ADDR to a running OpenBao dev server, for example TEST_BAO_ADDR=http://127.0.0.1:18200 make test-integration (the compose openbao-test service, started with --profile test)" >&2; exit 1; }
	@test -n "$(TEST_PDF_URL)" || { echo "set TEST_PDF_URL to a running Gotenberg, for example TEST_PDF_URL=http://127.0.0.1:13000 make test-integration (the compose pdf-test service, started with --profile test)" >&2; exit 1; }
	TEST_VALKEY_ADDR="$(TEST_VALKEY_ADDR)" TEST_VALKEY_PASSWORD="$(TEST_VALKEY_PASSWORD)" TEST_DATABASE_URL="$(TEST_DATABASE_URL)" TEST_BAO_ADDR="$(TEST_BAO_ADDR)" TEST_PDF_URL="$(TEST_PDF_URL)" \
	BETTERLEAKS_BIN="$(BETTERLEAKS_BIN)" OPENGREP_BIN="$(OPENGREP_BIN)" go test -race -count=1 ./...

hooks: $(BIN)/gofumpt $(BIN)/golangci-lint
	bash .claude/hooks/hooks_test.sh

scanners-test:
	sh deploy/worker/fetch-scanners_test.sh

# Makes the local OpenBao's unseal key on the host, or moves it out of the
# openbao-secrets volume; see deploy/openbao/unseal-key.sh.
openbao-unseal-key:
	sh deploy/openbao/unseal-key.sh

deploy-scripts-test:
	sh deploy/openbao/unseal-key_test.sh

check: lint vet test hooks scanners-test deploy-scripts-test css-check
