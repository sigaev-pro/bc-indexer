# Geth faults patch set (E01-04.early_faults, test-only)

`patches.json` adds a package variable `ExtractorFS` (`vfs.FS`, pebble v2)
to `ethdb/pebble`. When it is not nil, `pebble.New` opens the store on that
file system. The Geth fault probe sets it to a Pebble in-memory file system,
so it can drop the unsynced data of a power loss
([test/native_faults/eth](../../../../test/native_faults/eth/README.md)).

Production code never sets it. With the default nil value, `pebble.New` is
unchanged. The patch set applies after the E01-02.admission,
E01-02.decision_bridge and E01-02.lifecycle_seams Geth patch sets, on the
accepted tree `1df30b18`. Each original occurs exactly once.

| Patch | File | Change |
|---|---|---|
| `pebble_vfs_import` | `ethdb/pebble/pebble.go` | import `github.com/cockroachdb/pebble/v2/vfs` |
| `pebble_vfs_var` | `ethdb/pebble/pebble.go` | `var ExtractorFS vfs.FS` |
| `pebble_vfs_open` | `ethdb/pebble/pebble.go` | `opt.FS = ExtractorFS` before `pebble.Open` when it is set |

The legacy pebble v1 path (`NewV1`, for an existing v1 store) is not
patched. The probe always creates a new store, which uses pebble v2.
