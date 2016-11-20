GO_VERSION := 1.7.3
SOURCEDIR=.
SOURCES := $(shell find $(SOURCEDIR) -name '*.go')
NON_VENDOR_PKGS = $(subst $(shell go list .),.,$(shell go list ./... | grep -v vendor))

BINARY=sqsexec

VERSION:=$(shell git describe --long --tags --dirty --always)
LDFLAGS=-ldflags "-X main.Version=${VERSION}"

TOOLS=honnef.co/go/staticcheck/cmd/staticcheck honnef.co/go/simple/cmd/gosimple honnef.co/go/unused/cmd/unused

.DEFAULT_GOAL: $(BINARY)

$(BINARY): $(SOURCES)
	go build ${LDFLAGS} -o ${BINARY} .

.PHONY: test
test:
	@# vet or staticcheck errors are unforgivable. gosimple produces warnings
	@go vet $(NON_VENDOR_PKGS)
	@staticcheck $(NON_VENDOR_PKGS)
	@unused -exported $(NON_VENDOR_PKGS)
	@-gosimple $(NON_VENDOR_PKGS)
	@go test $(testargs) $(NON_VENDOR_PKGS)

.PHONY: install
install:
	go install ${LDFLAGS} ./...

.PHONY: clean
clean:
	@if [ -f ${BINARY} ] ; then rm ${BINARY} ; fi

.PHONY: release
release:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ${LDFLAGS} -o ${BINARY}.linux .
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ${LDFLAGS} -o ${BINARY}.darwin .

.PHONY: docker-release
docker-release:
	docker run --rm -v ${PWD}:/go/src/github.com/Wattpad/sqsexec \
                -w /go/src/github.com/Wattpad/sqsexec \
		-e GOOS=linux -e GOARCH=amd64 \
		golang:${GO_VERSION}-alpine go build ${LDFLAGS} -o ${BINARY}.linux .
	docker run --rm -v ${PWD}:/go/src/github.com/Wattpad/sqsexec \
                -w /go/src/github.com/Wattpad/sqsexec \
		-e GOOS=darwin -e GOARCH=amd64 \
		golang:${GO_VERSION}-alpine go build ${LDFLAGS} -o ${BINARY}.darwin .

.PHONY: bootstrap
bootstrap:
	$(foreach tool,$(TOOLS),$(call goget, $(tool)))

define goget
	go get -u $(1)
	
endef

