# devclean — macOS Developer Storage Cleaner

A local Go terminal application for discovering developer disk usage, reviewing cleanup scope, and measuring the resulting change in available disk space. No cloud services, telemetry, scheduled tasks, sudo, or automatic cleanup.

This is the first functional release. It performs real cleanup; review the supported scope and limitations below before using it.

## Build and run

Requires macOS and Go 1.26 or later. Both Intel and Apple Silicon are supported.

```sh
git clone git@github.com:cmgann/Mac-tui-cleanup.git
cd Mac-tui-cleanup
go mod download
make build
./dist/devclean
```

Optional installation:

```sh
mkdir -p "$HOME/.local/bin"
cp dist/devclean "$HOME/.local/bin/devclean"
# Add $HOME/.local/bin to PATH if needed.
```

`make release` builds `dist/devclean-darwin-arm64` and `dist/devclean-darwin-amd64`. Binaries are standalone; Docker, Xcode utilities, Homebrew, and other tools are optional integrations, discovered on PATH. Builds are not Developer ID signed or notarized.

```sh
devclean --scan                 # Read-only scan, JSON on stdout
devclean --export-history       # History JSON on stdout
devclean --version
```

The terminal starts with a read-only scan. Missing tools and permission failures appear as scan warnings; other providers continue. Scans have per-provider timeouts. Measurements are real, with no sample values. Ordinary terminal emulators work; use at least 32 columns and 14 rows. An 80×28 terminal gives the dashboard more room.

## Keyboard workflow

| Key | Action |
| --- | --- |
| Up/Down, j/k | Navigate; scroll details, review, history, warnings |
| Enter | Open category/details; advance from review |
| Space | Select candidate, or toggle eligible candidates in a category |
| A | Select eligible candidates in current view |
| C | Prepare and review the exact cleanup plan |
| R | Rescan |
| W | Open full scan warnings |
| H / E | History / export history from history screen |
| S / Enter | Settings / cycle BuildKit age policy |
| V | Individually review a device-support folder; type REVIEW to acknowledge |
| P | Pin or unpin a candidate; rescan after unpinning |
| O / T | Sort candidate list by size / modification time |
| PgUp/PgDn | Scroll long views |
| Esc | Back; during work, request cancellation |
| Q | Quit when idle |

Select candidates, press **C**, read the paths, Docker context/builder and command arguments, then press Enter. A second screen requires typing **DELETE** and pressing Enter. Pressing C alone never deletes anything. Close Xcode and `xcodebuild` before DerivedData cleanup.

During cleanup the spinner shows the current operation and elapsed time, without an invented percentage. Esc cancels the running command or filesystem traversal and prevents subsequent operations. Cancellation cannot undo completed work. Results include failures and operations that did not start. The app rescans after cleanup, including partial cleanup. Process termination requests cancel active work and wait for local history persistence before exiting when possible; force-killing the process cannot provide that guarantee.

## Supported scope

| Provider | Inspection | Cleanup |
| --- | --- | --- |
| Docker | Context, `system df`, each builder, cache, images, container writable layers, volume names | Individual local single-node builders; individual unused images; individual stopped containers |
| Xcode | Individual DerivedData projects; iOS/watchOS/tvOS/visionOS/macOS device-support folders; archives; user/system CoreSimulator; unavailable devices and installed runtimes through `simctl` | DerivedData projects; individually reviewed device-support folders with fresh device checks |
| Homebrew | Executable-discovered prefix/cache sizes; `cleanup --dry-run` output and savings when reported | Supported `brew cleanup`; scope is rechecked against reviewed dry-run output |
| Other developer tools | Tool-discovered Go build/module caches, npm cache, pnpm store, Yarn cache; standard SwiftPM cache | Read-only |

BuildKit defaults to a **30-day** policy; Settings offers 7 days or all reclaimable cache. Each selected builder uses `buildx prune --builder NAME --all --force`, adding `--filter until=...` when age restricted. Installed flag support is checked before pruning. Docker handles live cache eligibility. An age-restricted operation may recover less than the displayed all-age private-cache estimate.

Docker images are checked for container references immediately before removal. Stopped containers are rechecked immediately before removal; no force or volume-removal flag is used. Container writable-layer data is permanently lost; attached volumes remain. Select the `<none>:<none>` image entries to remove dangling images. Selecting other unused images is a higher-risk operation: they must be downloaded or rebuilt later.

## Protection and current limits

- All volumes are inspection-only, including anonymous volumes. There is no volume deletion command. Database data is never intentionally targeted. Volume sizes/reference information is shown only where the Docker overview reports it; individual volume inspection is limited to names.
- Remote Docker contexts, remote drivers, and multi-node builders cannot be cleaned in this release. Unset `DOCKER_HOST`, `DOCKER_CONTEXT`, and `BUILDX_BUILDER` overrides to use Docker scanning. The context, endpoint, builder driver, and node identity are revalidated before cleanup. Aliases of the same supported cache store are not counted twice.
- Device support uses both supported Xcode inventories: `xcdevice list` JSON (including older devices) and `devicectl list devices --json-output` (CoreDevice). Known physical devices, including locked or unavailable devices, protect matching platform/version or build. Simulators do not authorize removing physical-device support. Unknown device versions/platforms, malformed output, missing tools and service failures block support cleanup. The newest-folder heuristic is never used.
- Device-support folders show parsed platform, OS version, build, model identifier when encoded in the folder name, size, modification time, and inventory sources. Each folder, including recent entries, requires **V → type REVIEW → Enter**, then individual Space selection. Category toggles and A never select device support, even after review. Review acknowledgement expires on rescan. Historical support files may be needed to symbolize older crash reports; pin folders you need to retain.
- Cleanup planning and execution query fresh inventories, with another check after tree measurement immediately before deletion. A new device, service failure, changed folder, pin, running Xcode or running `xcodebuild` blocks deletion. Both device utilities must work for device-support cleanup (current Xcode toolchains); older/unsupported toolchains remain read-only for device support. Unfamiliar future CoreDevice schemas fail closed. Archive, UserData and signing directories are not eligible roots.
- Archives, release dSYMs, signing assets, provisioning profiles, Xcode UserData, simulator data and runtime bundles remain read-only. Simulator deletion through `simctl` is not implemented yet. No runtime bundles are removed directly.
- Filesystem cleanup is limited to direct children of the current user's standard DerivedData and explicitly enumerated platform DeviceSupport roots. Roots, parent paths, symlinked cleanup roots, repositories, special files, changed directories, and mount crossings are rejected. Open directory handles and Go `os.Root` constrain traversal during deletion. Do not concurrently move or modify directories being cleaned.
- Filesystem cleanup uses **permanent deletion**, with the separate typed confirmation. Trash support is not implemented. There is no claim that moving to Trash recovers space.
- Additional caches, source repositories, `node_modules`, and module contents are not deletion targets.
- Default tests use temporary filesystem trees and fake commands. The separately opted-in integration suite cleans only its own resources on an isolated, disposable real Docker daemon. No real developer files or existing Docker resources are cleanup targets.

## Understanding the numbers

These metrics are intentionally separate:

1. **Estimated reclaimable:** eligible filesystem candidates and supported BuildKit private reclaimable records. Shared cache records and protected resources are excluded. Before/after provider usage is also recorded for BuildKit and successful DerivedData removals. Duplicate builder aliases are protected. Images and containers show nominal sizes in details but contribute zero to aggregate estimates because unique recoverable layers are unknown. A zero estimate does not mean no usage.
2. **Provider reported:** parsed from actual tool output when available. `null` in JSON means unreported, not zero. Filesystem deletion does not invent a provider-reported byte count.
3. **Observed disk change:** signed available bytes after minus available bytes before each operation, measured using macOS `statfs` on the user's home filesystem. A failed or canceled operation can still have an observed change. Measurement errors are recorded separately.

Filesystem sizes use allocated blocks and avoid double-counting hard links within each tree. Read-only directory sizes can overlap (for example a cache inside an installation); they are not added to reclaimable totals. Mount crossings and unreadable subtrees produce warnings and partial measurements.

APFS snapshots/clones and Docker disk images can delay physical reclamation. Concurrent activity can make the observed change negative or unrelated to cleanup. The lifetime value is the signed **cumulative observed space change**, including failed operations with valid measurements; it is not current available space and is not proof of causal savings. Session totals reset when the app restarts. History provides per-provider sums, dates, command output, scope, separate metrics, and errors.

## Local data

Uses `os.UserConfigDir()` and the `devclean` namespace, normally:

```text
~/Library/Application Support/devclean/settings.json
~/Library/Application Support/devclean/history.json
~/Library/Application Support/devclean/history-export.json
```

Writes use temporary files, sync, atomic rename, and mode 0600. Locks prevent concurrent cleaners and conflicting history appends. Corrupt history is preserved and blocks cleanup rather than being overwritten; inspect/back up and repair it manually. Settings errors prevent launch. History contains local paths/resource names and command output; review exports before sharing.

## Development and verification

```sh
make test       # Go tests with the race detector
make vet
make release
```

Tests use temporary trees and fake command responses. Coverage includes physical-device inventories, per-folder review, connected-device changes, unavailable device services at the deletion boundary, JSON parsing, shared-cache accounting, protected selection, cleanup planning, path traversal, symlinks, changed/disappeared targets, active resources, context changes, Homebrew dry-run changes, canceled/partial cleanup, signed disk changes, JSON persistence, locks, terminal sanitization, and resizing. CI explicitly verifies native Apple Silicon hardware (`hw.optional.arm64=1`) and Go runtime architecture, runs the tests on both Apple Silicon and Intel macOS, and executes the actual binary in an isolated terminal. A separate disposable Docker job runs real cleanup and verifies preserved data. See [validation procedures](docs/validation.md) and [CI runs](https://github.com/cmgann/Mac-tui-cleanup/actions).

Architecture: `cmd/devclean` assembles the app; `internal/tui` owns asynchronous messages/rendering; `internal/storage` handles candidates, plans, constrained filesystem operations, measurements and history; `internal/providers` implements tool behavior; `internal/runner` executes fixed programs with argument arrays, contexts, process-group cancellation and bounded output; `internal/config` persists preferences. No shell command strings are executed.

Dependencies are pinned: [Bubble Tea](https://github.com/charmbracelet/bubbletea/releases/tag/v2.1.1), [Bubbles](https://github.com/charmbracelet/bubbles/releases/tag/v2.2.1), [Lip Gloss](https://github.com/charmbracelet/lipgloss/releases/tag/v2.0.6). Tool behavior follows the supported [Buildx list](https://docs.docker.com/reference/cli/docker/buildx/ls/), [disk usage](https://docs.docker.com/reference/cli/docker/buildx/du/), and [prune](https://docs.docker.com/reference/cli/docker/buildx/prune/) interfaces. Unsupported installed flags or formats surface as provider errors rather than triggering unsafe fallbacks.
