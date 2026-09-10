BINARY_NAME := mbp
GOARCH := amd64
VARS_PKG := mysqlbinlog-plus/internal/vars

ifeq ($(OS),Windows_NT)
HOST_GOOS := windows
HOST_BINARY_EXT := .exe
HOST_BINARY_PATH := $(BINARY_NAME)$(HOST_BINARY_EXT)
NULL_DEVICE := NUL
CLEAN_BINARY = @if exist "$(HOST_BINARY_PATH)" del /Q "$(HOST_BINARY_PATH)"
BUILD_TIME ?= $(shell powershell -NoProfile -Command "Get-Date -Format 'yyyy-MM-dd HH:mm:ss'" 2>$(NULL_DEVICE) || echo unknown)

define GO_BUILD
set "CGO_ENABLED=0"&& set "GOARCH=$(GOARCH)"&& set "GOOS=$(1)"&& go build -trimpath -ldflags="$(BUILD_FLAGS)" -o "$(2)" main.go
endef
else
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Linux)
HOST_GOOS := linux
else ifeq ($(UNAME_S),Darwin)
HOST_GOOS := darwin
else
$(error unsupported OS: $(UNAME_S))
endif
HOST_BINARY_EXT :=
HOST_BINARY_PATH := ./$(BINARY_NAME)
NULL_DEVICE := /dev/null
CLEAN_BINARY = @rm -f "$(BINARY_NAME)$(HOST_BINARY_EXT)"
BUILD_TIME ?= $(shell date +"%Y-%m-%d %H:%M:%S" 2>$(NULL_DEVICE) || echo unknown)

define GO_BUILD
CGO_ENABLED=0 GOARCH=$(GOARCH) GOOS=$(1) go build -trimpath -ldflags="$(BUILD_FLAGS)" -o "$(2)" main.go
endef
endif

APP_VERSION ?= $(shell git describe --tags --always --dirty 2>$(NULL_DEVICE) || echo unknown)
GO_VERSION ?= $(shell go version 2>$(NULL_DEVICE) || echo unknown)
GIT_COMMIT ?= $(shell git rev-parse HEAD 2>$(NULL_DEVICE) || echo unknown)
GIT_REMOTE ?= $(shell git config --get remote.origin.url 2>$(NULL_DEVICE) || echo unknown)

BUILD_FLAGS  = -X '${VARS_PKG}.AppName=${BINARY_NAME}'
BUILD_FLAGS += -X '${VARS_PKG}.AppVersion=${APP_VERSION}'
BUILD_FLAGS += -X '${VARS_PKG}.GoVersion=${GO_VERSION}'
BUILD_FLAGS += -X '${VARS_PKG}.BuildTime=${BUILD_TIME}'
BUILD_FLAGS += -X '${VARS_PKG}.GitCommit=${GIT_COMMIT}'
BUILD_FLAGS += -X '${VARS_PKG}.GitRemote=${GIT_REMOTE}'

.PHONY: all clean build run linux windows macos

all: clean build run

clean:
	@go clean
	$(CLEAN_BINARY)

build: TARGET_GOOS := $(HOST_GOOS)
build: TARGET_OUTPUT := $(BINARY_NAME)$(HOST_BINARY_EXT)
linux: TARGET_GOOS := linux
linux: TARGET_OUTPUT := $(BINARY_NAME)
windows: TARGET_GOOS := windows
windows: TARGET_OUTPUT := $(BINARY_NAME).exe
macos: TARGET_GOOS := darwin
macos: TARGET_OUTPUT := $(BINARY_NAME)

build linux windows macos:
	$(call GO_BUILD,$(TARGET_GOOS),$(TARGET_OUTPUT))

run:
	@$(HOST_BINARY_PATH) --version
