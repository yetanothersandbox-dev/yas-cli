# The yas CLI.
#
# Ported from the monorepo this was split out of, keeping the one property that
# mattered there: `make yas` and `make dist` stamp IDENTICAL binaries. The
# GitHub client id in particular is not decoration — a build without it silently
# loses the sign-in flow and falls back to pasting a key, which is the kind of
# thing discovered on somebody else's laptop.
GO      ?= go
BUILD   ?= build
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# The PUBLIC id of the GitHub App `yas login` fronts for. Public by
# construction: it is in the URL the browser is sent to. Overridable so a fork
# or a staging deployment can point at its own app.
YAS_GITHUB_CLIENT_ID ?= Iv23lig4x8prtIm0ClpT
YAS_GITHUB_APP_SLUG  ?= yetanothersandbox-app

LDFLAGS := -s -w -X main.version=$(VERSION) \
	-X main.githubClientID=$(YAS_GITHUB_CLIENT_ID) \
	-X main.githubAppSlug=$(YAS_GITHUB_APP_SLUG)

.PHONY: yas
yas:
	@mkdir -p $(BUILD)
	GOTOOLCHAIN=auto CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' \
		-o $(BUILD)/yas ./cmd/yas
	@printf '  %-14s %s\n' "yas" "$$(ls -lh $(BUILD)/yas | awk '{print $$5}')"

.PHONY: test
test:
	$(GO) vet ./...
	$(GO) test ./...

# What a release ships. Darwin first because that is what the people using this
# actually have.
DIST_PLATFORMS ?= darwin/arm64 darwin/amd64 linux/amd64 linux/arm64

# The checksums are the point of doing this in one place: the Homebrew formula
# verifies a SHA-256, and a digest produced by a different command from the
# archive it describes is a digest nobody has actually checked.
.PHONY: dist
dist:
	@rm -rf $(BUILD)/dist && mkdir -p $(BUILD)/dist
	@for p in $(DIST_PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		out=$(BUILD)/dist/yas_$(VERSION)_$${os}_$${arch}; \
		mkdir -p $$out; \
		GOTOOLCHAIN=auto GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 $(GO) build \
			-trimpath -ldflags '$(LDFLAGS)' -o $$out/yas ./cmd/yas || exit 1; \
		tar -czf $$out.tar.gz -C $$out yas || exit 1; \
		rm -rf $$out; \
		printf '  %-34s %s\n' "$$(basename $$out.tar.gz)" "$$(ls -lh $$out.tar.gz | awk '{print $$5}')"; \
	done
	@cd $(BUILD)/dist && shasum -a 256 *.tar.gz > checksums.txt
	@printf '  %-34s %s\n' "checksums.txt" "$$(wc -l < $(BUILD)/dist/checksums.txt | tr -d ' ') entries"
