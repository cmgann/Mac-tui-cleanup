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
| Xcode | Individual DerivedData projects; iOS/watchOS/tvOS/visionOS/macOS device-support folders; archives; user/system CoreSimulator; unavailable devices and installed runtimes through `simctl` | Individual DerivedData projects, or all eligible projects through selection and confirmation |
| Homebrew | Executable-discovered prefix/cache sizes; `cleanup --dry-run` output and savings when reported | Supported `brew cleanup`; scope is rechecked against reviewed dry-run output |
| Other developer tools | Tool-discovered Go build/module caches, npm cache, pnpm store, Yarn cache; standard SwiftPM cache | Read-only |

BuildKit defaults to a **30-day** policy; Settings offers 7 days or all reclaimable cache. Each selected builder uses `buildx prune --builder NAME --all --force`, adding `--filter until=...` when age restricted. Installed flag support is checked before pruning. Docker handles live cache eligibility. An age-restricted operation may recover less than the displayed all-age private-cache estimate.

Docker images are checked for container references immediately before removal. Stopped containers are rechecked immediately before removal; no force or volume-removal flag is used. Container writable-layer data is permanently lost; attached volumes remain. Select the `<none>:<none>` image entries to remove dangling images. Selecting other unused images is a higher-risk operation: they must be downloaded or rebuilt later.

## Protection and current limits

- All volumes are inspection-only, including anonymous volumes. There is no volume deletion command. Database data is never intentionally targeted. Volume sizes/reference information is shown only where the Docker overview reports it; individual volume inspection is limited to names.
- Remote Docker contexts, remote drivers, and multi-node builders cannot be cleaned in this release. Unset `DOCKER_HOST`, `DOCKER_CONTEXT`, and `BUILDX_BUILDER` overrides to use Docker scanning. The context, endpoint, builder driver, and node identity are revalidated before cleanup. Aliases of the same supported cache store are not counted twice.
- **Device support is protected in this release.** Versions/builds appear as directory names; a device identifier may be unavailable. Reliable connected-device matching and a reviewed device-support deletion policy are future work. No newest-directory heuristic is used. Pins can add protection; they cannot override built-in protection. Historical support files may be needed to symbolize old crash reports.
- Archives, release dSYMs, signing assets, provisioning profiles, Xcode UserData, simulator data and runtime bundles remain read-only. Simulator deletion through `simctl` is not implemented yet. No runtime bundles are removed directly.
- Filesystem cleanup is limited to direct children of the current user's standard DerivedData root. Roots, parent paths, symlinked cleanup roots, repositories, special files, changed directories, and mount crossings are rejected. Open directory handles and Go `os.Root` constrain traversal during deletion. Do not concurrently move or modify directories being cleaned.
- Filesystem cleanup uses **permanent deletion**, with the separate typed confirmation. Trash support is not implemented. There is no claim that moving to Trash recovers space.
- Additional caches, source repositories, `node_modules`, and module contents are not deletion targets.
- No test or development step invokes cleanup against a real Docker environment or real developer files.

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

Tests use temporary trees and fake command responses. Coverage includes JSON parsing, shared-cache accounting, protected selection, cleanup planning, path traversal, symlinks, changed/disappeared targets, active resources, context changes, Homebrew dry-run changes, canceled/partial cleanup, signed disk changes, JSON persistence, locks, terminal sanitization, and resizing. macOS CI runs tests, vet, and both architecture builds. Hardware-specific execution and Docker daemon integration still require validation on the target Mac; cross-compilation is not an Apple Silicon runtime test.

Architecture: `cmd/devclean` assembles the app; `internal/tui` owns asynchronous messages/rendering; `internal/storage` handles candidates, plans, constrained filesystem operations, measurements and history; `internal/providers` implements tool behavior; `internal/runner` executes fixed programs with argument arrays, contexts, process-group cancellation and bounded output; `internal/config` persists preferences. No shell command strings are executed.

Dependencies are pinned: [Bubble Tea](https://github.com/charmbracelet/bubbletea/releases/tag/v2.1.1), [Bubbles](https://github.com/charmbracelet/bubbles/releases/tag/v2.2.1), [Lip Gloss](https://github.com/charmbracelet/lipgloss/releases/tag/v2.0.6). Tool behavior follows the supported [Buildx list](https://docs.docker.com/reference/cli/docker/buildx/ls/), [disk usage](https://docs.docker.com/reference/cli/docker/buildx/du/), and [prune](https://docs.docker.com/reference/cli/docker/buildx/prune/) interfaces. Unsupported installed flags or formats surface as provider errors rather than triggering unsafe fallbacks.
