BINARY_NAME = mbp
BINARY_EXT =
CURRENT_OS =
GOOS =
CLEAN_BINARY =
VARS_PKG = mysqlbinlog-plus/internal/vars

ifeq ($(OS),Windows_NT)
	APP_VERSION ?= $(shell git describe --tags --always --dirty 2>NUL || echo unknown)
	GO_VERSION ?= $(shell go version 2>NUL || echo unknown)
	BUILD_TIME ?= $(shell powershell -NoProfile -Command "Get-Date -Format 'yyyy-MM-dd HH:mm:ss'" 2>NUL || echo unknown)
	GIT_COMMIT ?= $(shell git rev-parse HEAD 2>NUL || echo unknown)
	GIT_REMOTE ?= $(shell git config --get remote.origin.url 2>NUL || echo unknown)
else
	APP_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || printf unknown)
	GO_VERSION ?= $(shell go version 2>/dev/null || printf unknown)
	BUILD_TIME ?= $(shell date +"%Y-%m-%d %H:%M:%S" 2>/dev/null || printf unknown)
	GIT_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || printf unknown)
	GIT_REMOTE ?= $(shell git config --get remote.origin.url 2>/dev/null || printf unknown)
endif

BUILD_FLAGS  = -X '${VARS_PKG}.AppName=${BINARY_NAME}'
BUILD_FLAGS += -X '${VARS_PKG}.AppVersion=${APP_VERSION}'
BUILD_FLAGS += -X '${VARS_PKG}.GoVersion=${GO_VERSION}'
BUILD_FLAGS += -X '${VARS_PKG}.BuildTime=${BUILD_TIME}'
BUILD_FLAGS += -X '${VARS_PKG}.GitCommit=${GIT_COMMIT}'
BUILD_FLAGS += -X '${VARS_PKG}.GitRemote=${GIT_REMOTE}'

ifeq ($(OS),Windows_NT)
	CURRENT_OS = windows
	GOOS = windows
	BINARY_EXT = .exe
	CLEAN_BINARY = @cmd /C "if exist $(BINARY_NAME)$(BINARY_EXT) del /Q $(BINARY_NAME)$(BINARY_EXT)"
else
	UNAME_S := $(shell uname -s)
	ifeq ($(UNAME_S),Linux)
		CURRENT_OS = linux
		GOOS = linux
	else 
		ifeq ($(UNAME_S),Darwin)
			CURRENT_OS = macos
			GOOS = darwin
		else
			$(error unsupported OS: $(UNAME_S))
		endif
		CLEAN_BINARY = @rm -f $(BINARY_NAME)$(BINARY_EXT)
	endif
endif

all: clean build run

clean:
	@go clean
	$(CLEAN_BINARY)

build:
ifeq ($(OS),Windows_NT)
	set "CGO_ENABLED=0"&& set "GOARCH=amd64"&& set "GOOS=$(GOOS)"&& go build -trimpath -ldflags="$(BUILD_FLAGS)" -o $(BINARY_NAME)$(BINARY_EXT) main.go
else
	CGO_ENABLED=0 GOARCH=amd64 GOOS=$(GOOS) go build -trimpath -ldflags="$(BUILD_FLAGS)" -o $(BINARY_NAME)$(BINARY_EXT) main.go
endif

run:
ifeq ($(OS),Windows_NT)
	@${BINARY_NAME}${BINARY_EXT} --version
else
	@./${BINARY_NAME}${BINARY_EXT} --version
endif

# Cross-compilation targets for building binaries for other operating systems.
linux:
ifeq ($(OS),Windows_NT)
	set CGO_ENABLED=0&& set GOARCH=amd64&& set GOOS=linux&& go build -trimpath -ldflags="$(BUILD_FLAGS)" -o ${BINARY_NAME} main.go
else
	CGO_ENABLED=0 GOARCH=amd64 GOOS=linux go build -trimpath -ldflags="$(BUILD_FLAGS)" -o ${BINARY_NAME} main.go
endif

windows:
ifeq ($(OS),Windows_NT)
	set CGO_ENABLED=0&& set GOARCH=amd64&& set GOOS=windows&& go build -trimpath -ldflags="$(BUILD_FLAGS)" -o ${BINARY_NAME}.exe main.go
else
	CGO_ENABLED=0 GOARCH=amd64 GOOS=windows go build -trimpath -ldflags="$(BUILD_FLAGS)" -o ${BINARY_NAME}.exe main.go
endif

macos:
ifeq ($(OS),Windows_NT)
	set CGO_ENABLED=0&& set GOARCH=amd64&& set GOOS=darwin&& go build -trimpath -ldflags="$(BUILD_FLAGS)" -o ${BINARY_NAME} main.go
else
	CGO_ENABLED=0 GOARCH=amd64 GOOS=darwin go build -trimpath -ldflags="$(BUILD_FLAGS)" -o ${BINARY_NAME} main.go
endif
