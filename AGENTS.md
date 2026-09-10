# Repository Guidelines

## Project Structure & Module Organization

| Path | Purpose |
|---|---|
| `main.go`, `cmd/` | Executable entry point and Cobra CLI definition |
| `internal/app/` | Coordinates parsing, SQL generation, and output |
| `internal/binlog/` | Parses MySQL binlog events |
| `internal/mysql/` | Handles MySQL connections and schema metadata |
| `internal/event/` | Generates original and rollback SQL |
| `internal/config/`, `internal/filter/` | Validates options and matches tables |
| `internal/spool/` | Stores rollback data in SQLite |
| `*_test.go` | Colocated package tests |
| `README.md`, `README_zh.md` | Usage documentation; root `mbp`/`mbp.exe` files are generated and ignored |

## Build, Test, and Development Commands

Use Go 1.26.4 from the repository root:

```bash
go test ./...       # Run all tests
go vet ./...        # Run static checks
go run . --help     # Run the CLI during development
make build          # Build the current OS binary
make linux          # Cross-build Linux amd64
make windows        # Cross-build Windows amd64
make macos          # Cross-build macOS amd64
```

## Release Build

Use the [GitHub Releases page](https://github.com/realcp1018/mysqlbinlog-plus/releases) as the canonical release history. For each version, preserve the established asset naming: `mbp-vX.Y.Z-{darwin,linux,windows}-amd64.zip` and `SHA256SUMS.txt`.

1. From the intended clean commit, run `go test ./...` and `go vet ./...`; create and push an annotated tag: `git tag -a vX.Y.Z -m "Release vX.Y.Z" && git push origin vX.Y.Z`.
2. Use the existing local ignored `dist/` staging directory, then run `make linux`, `make windows`, and `make macos` from the tagged commit. `APP_VERSION` is populated automatically by `git describe`; override it only when intentionally building before the tag exists. This build variable is independent of the tag argument passed to `gh release create`; use a separate shell variable such as `VERSION` only to avoid repeating the tag. Copy each `mbp`/`mbp.exe` immediately because targets reuse root-level filenames.
3. Zip the binaries with the names above, record SHA-256 values, and verify archives plus `mbp --version`.
4. Generate `release-notes.md` from the user-visible changes between tags. Set `VERSION=vX.Y.Z`, derive `PREVIOUS_TAG` with `PREVIOUS_TAG=$(git describe --tags --abbrev=0 "${VERSION}^")`, then review `git log --first-parent --format="%s" "$PREVIOUS_TAG..$VERSION"` and the corresponding diff. Write notes in the established format:

   ```markdown
   - Adds or supports ...
   - Fixes or improves ...
   - Updates compatibility or behavior ...
   ```

   Use concise, user-facing bullets with no more than three description items; combine related changes by theme instead of copying every commit. Keep this file local unless it is intentionally added to the repository.
5. Publish with `gh release create vX.Y.Z dist/mbp-vX.Y.Z-darwin-amd64.zip dist/mbp-vX.Y.Z-linux-amd64.zip dist/mbp-vX.Y.Z-windows-amd64.zip dist/SHA256SUMS.txt --title "vX.Y.Z" --notes-file release-notes.md`. Confirm the published assets before reporting success; GitHub supplies source archives automatically. Only after successful confirmation, delete the local temporary `release-notes.md` and verify it is absent from the working tree.
