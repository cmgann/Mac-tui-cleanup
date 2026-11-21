# Phase 1 validation

The [CI workflow](../.github/workflows/ci.yml) runs actual native macOS programs and an explicitly opted-in live Docker suite. Job logs are the execution evidence; building a binary alone is not a runtime validation.

## Native Apple Silicon and Intel

`macos-15` is the native Apple Silicon job and `macos-15-intel` is the Intel job. CI prints the host architecture and Go version, then checks the expected Go architecture. The Apple Silicon job also asserts `uname -m=arm64` and `hw.optional.arm64=1`. `TestNativeMacRuntime` checks the architecture from within the executing Go test process and rechecks the hardware sysctl.

Both jobs run `go test -race -v ./...`, including actual deletion of temporary DerivedData/device-support fixtures, persistence, signed filesystem measurements, protected-device scenarios, and the interactive model's review/confirmation workflow. They build both distributable architectures, then launch the native binary in a real pseudo-terminal with `scripts/smoke_tui.py`. That smoke test uses a temporary HOME and isolated PATH, verifies the disk overview and asynchronous completion with optional-tool failures, requests scan cancellation to verify responsiveness, and exits using Q. User paths are confined to a temporary home; standard system simulator locations may be inspected read-only. No runner developer data is cleaned.

The current labels and architectures are documented in [GitHub's runner reference](https://docs.github.com/en/actions/reference/runners/github-hosted-runners). The hardware checks ensure a changed label cannot silently turn this into a cross-build-only check.

## Live Docker

Only the disposable `ubuntu-24.04` job sets both required opt-ins:

```sh
DEVCLEAN_LIVE_DOCKER=1 DEVCLEAN_DISPOSABLE_DOCKER=1 \
  go test -tags=integration -v -timeout=12m ./internal/providers \
  -run '^TestLiveDockerCleanup$'
```

Run this only against a disposable local Docker daemon. Ordinary `go test ./...` excludes it; adding the build tag without both opt-ins skips it. The interactive application remains macOS-only; the Linux job exercises the shared Docker provider and cleanup engine with actual host disk measurements.

The live test:

1. Verifies a local Unix-socket Docker context and logs real Docker/Buildx versions.
2. Creates two uniquely named `docker-container` builders without changing the selected builder/context.
3. Builds throwaway images from a local static Go fixture, producing real cache in each builder.
4. Creates a running container, a stopped container, an unused image, and a labeled database-named volume containing a sentinel.
5. Scans both builders and verifies real reclaimable bytes, protected running resources and protected persistent volumes.
6. Uses the application's planning/execution/history path to prune only its first builder. It asserts decreased provider usage, parsed provider-reported reclaimed bytes, a valid signed filesystem observation, and unchanged bytes in the second builder.
7. Removes only its selected stopped container and unused image, and verifies their absence.
8. Verifies that the running container, its referenced image and database volume remain, with the sentinel contents unchanged. It checks the three persisted history records.
9. Tears down only exact fixture resource names registered after their successful creation. No global system/image/container/volume prune is used, even on failure.

Docker Desktop's APFS disk-image shrink behavior is still host-dependent; these tests deliberately do not require a positive host free-space delta. The observed signed filesystem change and provider-reported cleanup remain separate metrics. This validation does not clean a personal Docker environment.

## Device-support policy

Device-source fixtures model real supported Xcode JSON shapes, with invented identifiers. Parser tests reject malformed, partial and unfamiliar output. On a physical Mac, supported flags can be inspected with:

```sh
xcrun xcdevice help
xcrun devicectl help list devices
```

The scanner requires successful `xcdevice` and CoreDevice JSON inventories to authorize support review. It conservatively protects versions belonging to any known physical device, even one reported unavailable. Missing/unknown versions, unknown platforms, command failures, timeouts, and unrecognized folder names keep support protected. Empty successful inventories are distinct from failed discovery. Review, planning, execution and the final deletion boundary are covered, including device attach/service-failure races, pins, recent folders, changed identities, and paths outside approved roots.

No physical iPhone/Watch is required for the automated suite, and fixtures are not a claim that every real device/toolchain combination was connected during CI. The application keeps uncertain cases read-only. Supported Xcode JSON evolves; see [Apple's Xcode release notes](https://developer.apple.com/documentation/xcode-release-notes/xcode-27-release-notes) for CoreDevice changes.
