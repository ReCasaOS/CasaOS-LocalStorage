# ZFS fixtures

The outputs of `zpool` and `zfs` the parsers in `service/zfs.go` are tested on.

- `real_*`: **captured by a user on a real box** (se3, ReCasaOS/CasaOS discussion 66, 2026-11-09: OpenZFS 2.4.1, Ubuntu 26.04):
  `zpool list -H -p`, `zfs list -H -p` and the text of `zpool status -p -P` of a healthy mirror whose pool does not have all
  features enabled. In the pasted `zpool status` the indentation of the tree was lost by the forum, so `real_zpool_status_features.txt`
  has the words and numbers as pasted with the tab and two-space indentation `zpool` prints put back.
- The other files are **written from the OpenZFS documentation and man pages**, not captured: a degraded raidz, scrubs and
  resilvers in progress or canceled, logs/cache/spares, a volume, a locked dataset, no pool, no dataset, kstat counters.
  Replace them with real captures when there are some (OpenZFS 2.1, 2.2, 2.3), and take this note out then.
