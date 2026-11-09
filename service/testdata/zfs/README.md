# ZFS fixtures

The outputs of `zpool` and `zfs` the parsers in `service/zfs.go` are tested on.

**These files were written from the OpenZFS documentation and man pages, and from the output a user pasted in
ReCasaOS/CasaOS#66 (the healthy mirror), not captured from a box.** Replace them with real captures when there are some
(`zpool list -H -p -o ...`, `zpool status -p -P <pool>`, `zfs list -H -p ...`, on OpenZFS 2.1, 2.2 and 2.3), and take this
note out then.
