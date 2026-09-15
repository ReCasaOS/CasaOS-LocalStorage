# Changelog

All notable changes to CasaOS LocalStorage are documented here.

## [0.4.40] - 2026-09-15

### Fixed

- **Built without UPX, this time for real.** 0.4.38 said the binaries were no longer packed with UPX. They were: the change never reached `.goreleaser.yaml`, so every amd64 and armv7 build since kept going through UPX 3.96. A packed binary exits with status 127 when its stub cannot unpack itself, which is how app-management failed its first start on the install checks of v0.4.83 and v0.4.88, with nothing logged. The hooks are gone now.

## [0.4.39] - 2026-09-15

### Fixed

- **A RAID array is one storage.** lsblk lists an MD array again under every disk it is built on, and the storage list walked every disk for its mounted filesystems: a RAID10 of four disks was four storages holding the same filesystem, in Storage Manager, in the file manager's sidebar and wherever a storage is picked. The list now comes from a view of the same tree in which an array is a storage of its own, named after its level, or System when it holds /; each mounted filesystem is listed once; and a disk that only lends itself to an array does not appear. Where space is added up across disks, which happens when / cannot be found, each filesystem counts once. From IceWhaleTech/CasaOS-LocalStorage#72, ported onto this distribution's storage accounting.
- **A disk is offered for formatting only when nothing on it is in use.** Only a disk's direct children were checked for a mount point, so a disk whose partitions were RAID members, or held a volume group with a mounted logical volume, was listed as available. A mount or swap anywhere below it, or a RAID, LVM or ZFS member anywhere in it, keeps it out of the list.

## [0.4.38] - 2026-09-13

### Fixed

- **A token stays valid while user-service restarts** (Common v0.4.25). The signing key was asked of user-service every ten seconds and every token failed the moment it did not answer, restarting or held still for a backup of the box. The key last seen serves until it answers again.
- **Built without UPX.** On the amd64 leg of the v0.4.83 install check a first start exited 127, the UPX stub's own code, once in a run of five services starting at once; the same binary ran by hand. The binaries are static and stripped already.

## [0.4.37] - 2026-09-13

### Fixed

- **Loopback is not an identity.** Any request from 127.0.0.1 skipped the token. Loopback is not this box's services alone: a container on the host network, or any local account, reaches the same addresses. A request skips the token only when it is one of this box's services, which come from loopback with the secret the gateway writes for this boot (`/var/run/casaos/internal.secret`, readable by root only); the dashboard always had a token. This service formats, mounts and merges disks as root. Its own calls to the core (the storage status every five seconds, the shares of a removed disk) carry the secret through the shared clients.

### Changed

- echo 4.15 with echo-jwt, x/crypto 0.57, x/net 0.59, Go 1.26.

## [0.4.34] - 2026-09-10

### Changed

- `api/local_storage/openapi.yaml` is embedded verbatim and served at `/doc`, so the IceWhale banner it opened with was plaintext in the shipped binary and fetched from `IceWhaleTech/logo` by the reader's browser. It is gone, and the link for reporting problems points at this distribution. `PKGBUILD` and the local codegen package follow the same move; the `@icewhale` npm scope named a publisher this project is not.
- Nothing that belongs to IceWhale moved: the catalogue, the icon CDN, the cloud OAuth host and the migration entries are untouched.

## [0.4.33] - 2026-09-10

### Changed

- The Go module is now `github.com/ReCasaOS/CasaOS-LocalStorage`, built against `github.com/ReCasaOS/CasaOS-Common v0.4.23`, following the move to the ReCasaOS organisation. A module path is not a URL and does not follow a redirect, so the rename has to be made in the source and released to take effect.
- Nothing else changed.

## [0.4.32] - 2026-09-07

### Changed

- The Go module path is now `github.com/inkly/CasaOS-LocalStorage`, and the shared library dependency is `github.com/inkly/CasaOS-Common` v0.4.22 (same code as IceWhale's v0.4.21 apart from its own module path). The regenerated `codegen/` output is byte-identical. The pin moves a long way — from v0.4.9-alpha6 — but the twelve packages this component imports from it are unchanged on the paths it calls, with one exception below.
- The shared library brings in `orca-zhang/ecache`, whose package `init()` starts a goroutine that sleeps in a loop for the lifetime of the process. Nothing here calls the cache it backs; the goroutine exists on import alone.

## [0.4.31] - 2026-09-07

### Changed

- The message-bus client is generated from this distribution's own tag (`inkly/CasaOS-MessageBus` at `v0.4.19`) instead of IceWhale's live `main` branch; the regenerated output is byte-identical.
- The coverage job no longer runs `IceWhaleTech/github/.github/workflows/go_codecov.yml@main`, an unpinned reusable workflow from a repository IceWhale still pushes to, which meant they could run arbitrary steps in this repository's CI. Its five steps are inlined, as the other five components already had them.
- The install-time migration script no longer geo-locates the host. `__get_download_domain` curled `ipconfig.io/country`, falling back to `ifconfig.io/country_code`, at the top level of `build/scripts/migration/script.d/04-migrate-local-storage.sh` — and `install.sh` runs every script in that directory on every install and every upgrade, so both third-party services were contacted each time regardless of whether a migration applied. Migration tools are fetched from `https://github.com/`, and the domain is a constant rather than a setting: what it points at is downloaded and run as root without verification. The migration lists are unchanged.

## [0.4.30] - 2026-09-06

### Fixed

- Disk health no longer reads a missing `smart_status` (virtual disks such as QEMU/Proxmox, standby or unopenable devices) as a failure, which showed a red "Damage" tag on the home Storage widget while Storage Manager reported the same disk healthy. `sys_disk` and each item of `GET /v1/disks` now also carry `smart_status` (`passed`, `failed` or `unavailable`); the existing `health` fields keep their type and mean "not failed".

## [0.4.29] - 2026-09-05

### Changed

- First release of the inkly distribution: the release pipeline publishes under `inkly` with GoReleaser; the npm publish and test-server workflows that could only run at IceWhale are removed.

## [0.4.28] - 2026-08-20

### Changed

- No code changes since v0.4.27. Republished from `main` after the boot fixes were merged ([CasaOS-LocalStorage #10](https://github.com/alvins82/CasaOS-LocalStorage/pull/10)) so the release commit is the one actually merged into `main`.
- Backfilled the CHANGELOG entries for v0.4.26 and v0.4.27, which previously existed only as release notes.

### Verification

- `git diff v0.4.27 v0.4.28` shows only the CHANGELOG.md change; the binary sources are identical.

## [0.4.27] - 2026-08-19

### Fixed

- The before-docker init step (`casaos-local-storage-first`, `casaos-local-storage -init`) now waits until every persisted merged mount is up before reporting done, so `/DATA` is complete before Docker restores containers. This closes the boot window that left user apps exited (127) when branch disks appeared after dockerd.

### Verification

- Linux-targeted build and tests pass.
- Reboot-verified on real hardware (two reboots with 6+ branch disks): all user apps come back automatically; the storage-first unit exits 0 with all merges mounted before Docker starts.

## [0.4.26] - 2026-08-19

### Fixed

- Merged storage restore keeps retrying until the source disks appear instead of giving up on the first pass, so a slow branch-disk enumeration during boot no longer leaves `/DATA` unmounted. The last restore failure is surfaced in the merge status endpoint.

### Verification

- Linux-targeted build and tests pass.
- Reboot-verified on real hardware.

## [0.4.25] - 2026-08-14

### Fixed

- Restore persisted mergerfs mounts before creating default `/DATA` directories, preventing upgrades and service restarts from leaving the configured merged storage unmounted ([CasaOS-LocalStorage #9](https://github.com/alvins82/CasaOS-LocalStorage/pull/9)).

### Verification

- Added a regression test documenting the startup ordering contract.
- Linux-targeted build and focused tests pass.

## [0.4.24] - 2026-08-13

### Added

- Create the standard `Documents`, `Downloads`, `Gallery`, and `Media` directories in `/DATA` when an external merged storage is created and they are missing ([CasaOS-LocalStorage #7](https://github.com/alvins82/CasaOS-LocalStorage/pull/7)).

### Changed

- Reuse the default-directory helper during startup and merged-storage recovery while preserving the special system `AppData` compatibility mount.

### Verification

- Focused default-directory tests pass.
- Linux cross-compilation succeeds for the service and root package.

## [0.4.23] - 2026-08-13

### Added

- Add a protected `PUT /v1/storage/rename` endpoint for ext2/ext3/ext4 volumes and keep system storage protected from renaming ([CasaOS-LocalStorage #6](https://github.com/alvins82/CasaOS-LocalStorage/pull/6)).

### Fixed

- Read the filesystem label directly with `blkid` when `lsblk` has not refreshed udev data yet, so a successful rename is reflected immediately in Storage Manager ([CasaOS-LocalStorage #6](https://github.com/alvins82/CasaOS-LocalStorage/pull/6)).

### Verification

- `go generate ./...`
- `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...`
- Invalid-device API validation probe completed successfully.

## [0.4.22] - 2026-08-13

### Changed

- Traverse nested `lsblk` trees so filesystems under partitions and LVM logical volumes are represented accurately.
- Preserve the physical parent disk model and path for each storage entry ([CasaOS-LocalStorage #5](https://github.com/alvins82/CasaOS-LocalStorage/pull/5)).

### Fixed

- Report used and available space from the mounted filesystem rather than the allocated physical disk, with coverage for nested mounts and logical volumes ([CasaOS-LocalStorage #5](https://github.com/alvins82/CasaOS-LocalStorage/pull/5)).

### Verification

- `GOOS=linux GOARCH=amd64 go build ./...`
- `GOOS=linux GOARCH=amd64 go test -c ./service`
