# CasaOS-LocalStorage

> **Not affiliated with IceWhale.** An independent, community-maintained distribution of CasaOS, not produced or endorsed by Shanghai IceWhale Technology Limited. CASAOS is their trademark, used here only to say what this is a release of. The original project is [IceWhaleTech/CasaOS](https://github.com/IceWhaleTech/CasaOS); report problems with this distribution at [ReCasaOS/CasaOS/issues](https://github.com/ReCasaOS/CasaOS/issues).

The storage service of CasaOS. It enumerates the disks and partitions attached to the host, mounts and unmounts them, formats and renames volumes, handles USB automount, and builds and restores the merged `/DATA` tree that the rest of CasaOS reads and writes.

This repository is part of **ReCasaOS**, a maintained release of the project after upstream [IceWhaleTech/CasaOS-LocalStorage](https://github.com/IceWhaleTech/CasaOS-LocalStorage) stopped shipping in 2025. Almost everything this fork adds came through [alvins82's fork](https://github.com/alvins82/CasaOS-LocalStorage), which kept merged storage working on mergerfs 2.40 and Ubuntu 26.

## What it does

The service listens on a loopback port chosen at start-up and registers these paths with CasaOS-Gateway, which is what the dashboard actually talks to:

| Path | What it serves |
|---|---|
| `/v1/disks` | disk and partition list, sizes, USB disks, unmount |
| `/v1/storage` | list, add, format, rename and remove volumes |
| `/v1/usb` | the USB automount setting |
| `/v2/local_storage` | merges (`/merge`, `/merge/init`) and mounts (`/mount`) |
| `/doc/v2/local_storage` | the OpenAPI document for the v2 API, and its viewer |

Every request needs a CasaOS JWT, unless it comes from loopback with `Authorization: Internal <secret>`, the per-boot secret the gateway writes to `<runtime path>/internal.secret`. Cloud drives (`/v1/cloud`, `/v1/driver`, `/v1/recover`) are served by the CasaOS core service, not by this one.

Disk hotplug arrives from the kernel over a netlink uevent socket. The service publishes `local-storage:storage_status` and `local-storage:merge_status`, plus per-device disk and USB events, on CasaOS-MessageBus.

### Merged storage

`/DATA` is where CasaOS keeps app data and user files. To back it with more than one disk, the service mounts [mergerfs](https://github.com/trapexit/mergerfs) — a FUSE union filesystem — over `/DATA` with each selected volume as a branch, so several disks read as one tree without RAID or LVM. Branches are read and changed live through the extended attributes of the `.mergerfs` control file, not by remounting.

The pool definition — mount point and source volumes — lives in the SQLite database; nothing is written to `/etc/fstab`. The mount is re-created from that record at boot and re-checked every 30 seconds. `/DATA/AppData` is kept on the system disk and bind-mounted from `/var/lib/casaos/files/AppData`, so a `/DATA` merged from external disks does not put container data on removable media.

`casaos-local-storage -init` runs from `casaos-local-storage-first.service`, ordered `Before=docker.service`: it restores every persisted merge and waits for it before Docker starts containers that have a restart policy.

### Files on the host

| Path | Contents |
|---|---|
| `/etc/casaos/local-storage.conf` | log path, DB path, `USBAutoMount`, `EnableMergerFS` |
| `/var/lib/casaos/db/local-storage.db` | SQLite: volumes (`o_disk`, legacy name) and merges (`o_merge`) |
| `/var/lib/casaos/files` | the system data tree moved aside when `/DATA` becomes a merge |
| `/var/log/casaos/local-storage.log` | log |
| `/usr/share/casaos/shell/local-storage-helper.sh` | the shell helpers it calls for mount and format work |

## Install

Components are not installed individually. One command installs or upgrades the whole distribution:

```sh
curl -fsSL https://github.com/ReCasaOS/CasaOS-Install/releases/latest/download/install.sh | sudo bash
```

What a release contains, and how it is built, is described in [CasaOS-Install](https://github.com/ReCasaOS/CasaOS-Install#readme).

## What this fork changed

Most of it is alvins82's work, oldest first. Per-release notes are in [CHANGELOG.md](CHANGELOG.md).

- **Ubuntu 26 setup fallback** — the setup script chose its systemd unit directory through a chain of `pushd` fallbacks that never reached the `ID_LIKE` branch, so a distribution without a directory of its own aborted the install with `Unsupported OS`. It now builds an explicit candidate list — `ID/VERSION_CODENAME`, then `ID`, then each `ID_LIKE` — and takes the first that exists.
- **mergerfs 2.40** — the branch list is read and written through `user.mergerfs.branches`, falling back to the legacy `user.mergerfs.srcmounts` key, so merges stop failing on current mergerfs ([#2](https://github.com/alvins82/CasaOS-LocalStorage/pull/2)).
- **External-only `/DATA`** — the system disk is no longer added as a branch of the merged pool. The first merge is created by the API from the volumes the user selects, and `/DATA/AppData` stays on the system disk. An install on a flash drive can therefore pool external disks for media.
- **Accurate usage and ownership** — nested `lsblk` trees are traversed, so filesystems under partitions and LVM logical volumes are listed; used and available space is read from the mounted filesystem rather than the physical disk; each entry keeps its parent disk's model and path ([#5](https://github.com/alvins82/CasaOS-LocalStorage/pull/5)).
- **Volume rename** — `PUT /v1/storage/rename` relabels ext2/ext3/ext4 volumes with `e2label`, refuses `/`, `/boot` and `/boot/*`, and reads the new label back with `blkid` when `lsblk` has not picked up the udev change yet ([#6](https://github.com/alvins82/CasaOS-LocalStorage/pull/6)).
- **Default directories** — `Documents`, `Downloads`, `Gallery` and `Media` are created in a new external merge when they are missing ([#7](https://github.com/alvins82/CasaOS-LocalStorage/pull/7)).
- **Restore before create** — persisted merges are restored before the default `/DATA` directories are created, so an upgrade or a service restart no longer leaves the configured pool unmounted ([#9](https://github.com/alvins82/CasaOS-LocalStorage/pull/9)).
- **Restore until the disks appear** — the restore pass ran once at boot, so a merge whose source disks were not mounted yet stayed unmounted for the rest of the session. It now runs every 30 seconds, serialized against the API's own create, update and remove calls. A merge point that is not empty fails loudly with the offending entries, and `/merge/init` reports the last restore failure instead of a stale "initialized" ([#10](https://github.com/alvins82/CasaOS-LocalStorage/pull/10)).

Ours is small:

- `TestAreAllMergesMounted` opened `file::memory:mergeall?cache=shared`. That is mattn/go-sqlite3 syntax; this repository uses the pure-Go glebarez driver, which read it as an on-disk file named `:memory:mergeall` and wrote it into the package directory on every run. The test passed anyway, which hid an untracked database in the source tree, no isolation between runs, and a hard failure on any filesystem that rejects `:` in a filename. The DSN is now `file:mergeall?mode=memory&cache=shared`.
- **No geo-IP at install time.** `build/scripts/migration/script.d` ran `__get_download_domain` at top level, curling `ipconfig.io/country` and then `ifconfig.io/country_code` to pick a download mirror by country. `install.sh` runs every script in that directory on every install and every upgrade, so both services were contacted each time, before the script had even decided whether a migration was due — which, on anything but a pre-0.4 box, it never is. The migration tools now come from GitHub unconditionally, and the domain is a constant: it is not an environment knob either, because what it points at is downloaded and run as root without verification.
- Releases are published from this fork: `.goreleaser.yaml` targets `ReCasaOS`, and the two workflows that could only ever run inside IceWhale — an npm publish to the `@icewhale` scope, and a push to their ZeroTier test server — are removed.
- The Go module is `github.com/ReCasaOS/CasaOS-LocalStorage` and depends on `github.com/ReCasaOS/CasaOS-Common`, so import paths, stack traces and `go version -m` on the shipped binary name this distribution rather than IceWhale.

## Development

The service is Linux-only: it uses netlink, FUSE and extended attributes, and does not build on macOS or Windows. Cross-compiling to Linux works anywhere.

```sh
go build ./...
go test ./...
```

Both pass on Linux with Go 1.23. `TestLiveMountSourceControl` needs a real mergerfs mount and skips unless `MERGERFS_TEST_MOUNT` points at one; every other test runs without a disk and without root.

From a non-Linux host, build with:

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...
```

`codegen/` is generated from `api/local_storage/openapi.yaml`; run `go generate` after changing the spec.

## Licence and credit

Apache License 2.0 — see [LICENSE](LICENSE). The upstream copyright notices are kept in the source, as the licence requires.

CasaOS-LocalStorage was written by IceWhale and its contributors. The merged-storage, disk-reporting and rename work in this repository is [alvins82](https://github.com/alvins82/CasaOS-LocalStorage)'s. CasaOS is a mark of IceWhale; this distribution uses the name to say what it is a release of, and nothing more.
