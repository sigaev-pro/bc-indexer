# BSC faults patch set (E01-04.early_faults, test-only)

`patches.json` adds a package variable `ExtractorFS` (`vfs.FS`, pebble v1)
to `ethdb/pebble`. When it is not nil, `pebble.New` opens the store on that
file system. The BSC fault probe sets it to a Pebble in-memory file system,
so it can drop the unsynced data of a power loss
([test/native_faults/bsc](../../../../test/native_faults/bsc/README.md)).

Production code never sets it. With the default nil value, `pebble.New` is
unchanged. The patch set applies after the BSC patches 01 to 08
(E01-02.admission, E01-02.decision_bridge and E01-02.lifecycle_seams), on
the accepted tree `54afaff4`. Each original occurs exactly once.

| Patch | File | Change |
|---|---|---|
| `pebble_vfs_import` | `ethdb/pebble/pebble.go` | import `github.com/cockroachdb/pebble/vfs` |
| `pebble_vfs_var` | `ethdb/pebble/pebble.go` | `var ExtractorFS vfs.FS` |
| `pebble_vfs_open` | `ethdb/pebble/pebble.go` | `opt.FS = ExtractorFS` before `pebble.Open` when it is set |
