# Bor faults patch set (E01-04.early_faults, test-only)

`patches.json` adds two package variables, used only by the Bor fault probe
([test/native_faults/poly](../../../../test/native_faults/poly/README.md)):

- `ExtractorFS` (`vfs.FS`, pebble v1) in `ethdb/pebble`. When it is not nil,
  `pebble.New` opens the store on that file system. The probe sets it to a
  Pebble in-memory file system, so it can drop the unsynced data of a power
  loss.
- `ExtractorWrap` in `node`. When it is not nil, the node passes the pebble
  key-value store it opens through this function. The probe starts a full
  node stack (the Bor lifecycle wiring needs the eth backend), so this is
  where it wraps the store in its fault store.

Production code never sets them. With the default nil values the node opens
and uses its store exactly as before. The patch set applies after the Bor
patch sets of E01-02.admission, E01-02.decision_bridge and
E01-02.lifecycle_seams, on the accepted tree `3a018e38`. Each original
occurs exactly once.

| Patch | File | Change |
|---|---|---|
| `pebble_vfs_import` | `ethdb/pebble/pebble.go` | import `github.com/cockroachdb/pebble/vfs` |
| `pebble_vfs_var` | `ethdb/pebble/pebble.go` | `var ExtractorFS vfs.FS` |
| `pebble_vfs_open` | `ethdb/pebble/pebble.go` | `opt.FS = ExtractorFS` before `pebble.Open` when it is set |
| `node_store_wrap_var` | `node/database.go` | `var ExtractorWrap func(ethdb.KeyValueStore) ethdb.KeyValueStore` |
| `node_store_wrap_apply` | `node/database.go` | `newPebbleDBDatabase` returns `ExtractorWrap(db)` when it is set |

The eth backend opens one pebble store, `chaindata`, so the wrap reaches the
store that the native chain and every extractor package use.
