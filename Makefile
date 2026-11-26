# The gates. CI runs these same commands: fmt, vet and test split across the
# jobs in .github/workflows/ci.yml, and `make cover` itself in its coverage job,
# so a green local run means a green CI run.
.PHONY: all fmt vet test cover run

all: fmt vet test

fmt:
	gofmt -l . | tee /dev/stderr | (! read)

vet:
	go vet ./...

test:
	go test ./...

# Coverage ACROSS packages, not per package.
#
# Plain `go test -cover` attributes nothing to a package exercised only from
# another one's tests, and reported auth and estate at 0% while the HTTP suite
# was driving both. A number that says untested about tested code is worse than
# no number: somebody writes tests that already exist.
COVER_OUT ?= cover.out

cover:
	go test ./internal/... -coverpkg=./internal/... -coverprofile=$(COVER_OUT)
	go tool cover -func=$(COVER_OUT) | tail -1

run:
	go run ./cmd/costcrew -data ./local
