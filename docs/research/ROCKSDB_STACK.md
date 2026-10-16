# RocksDB — pinned baseline Go/Rust

2026-10-16. D33 выбирает engine; этот файл закрепляет технический baseline для E00-03.06-B. Проверены upstream manifests/build script/API, но не выполнены сборка, ABI-проверка, загрузка нод, benchmark или crash tests. Это совместимость по исходным декларациям, не пройденная интеграция.

| Компонент | Pin | Основание |
|---|---|---|
| Native engine | RocksDB 10.4.2 | [version.h](https://github.com/facebook/rocksdb/blob/v10.4.2/include/rocksdb/version.h), совпадающая версия в Go build script и Rust sys manifest |
| Go binding | github.com/linxGnu/grocksdb v1.10.2; commit 390e6f92f83ca06746e14fe9dcc098c2ec9b39fd | [tag](https://github.com/linxGnu/grocksdb/tree/v1.10.2), [build.sh](https://github.com/linxGnu/grocksdb/blob/v1.10.2/build.sh) явно задаёт 10.4.2 |
| Rust binding | rocksdb =0.24.0 | [Cargo.toml](https://github.com/rust-rocksdb/rust-rocksdb/blob/v0.24.0/Cargo.toml), MSRV 1.85.0 |
| Rust native binding | resolved librocksdb-sys 0.17.3+10.4.2 | [sys Cargo.toml](https://github.com/rust-rocksdb/rust-rocksdb/blob/v0.24.0/librocksdb-sys/Cargo.toml) |

Выбор намеренно не означает «самые новые релизы»: v1.10.3 grocksdb build script использует 10.5.1, v1.10.4 — 10.6.2. Для текущего единого baseline они не подставляются автоматически. Источники: [v1.10.3](https://github.com/linxGnu/grocksdb/blob/v1.10.3/build.sh), [v1.10.4](https://github.com/linxGnu/grocksdb/blob/v1.10.4/build.sh).

## Сборка и API

- Go module объявляет Go 1.17, что не требует повышать ранее выбранные native Go toolchains. Это не доказательство отсутствия конфликтов всего dependency graph. Go-компонент связывается через CGO с отдельно pinned librocksdb 10.4.2. Поддерживаемые API: WriteOptions.SetSync(true), DisableWAL(false), DB.Write(options,WriteBatch). Все native ресурсы явно освобождаются; Go GC не заменяет Destroy/Free/Close. Источники: [go.mod](https://github.com/linxGnu/grocksdb/blob/v1.10.2/go.mod), [write options](https://github.com/linxGnu/grocksdb/blob/v1.10.2/options_write.go), [DB.Write](https://github.com/linxGnu/grocksdb/blob/v1.10.2/db.go).
- Rust: WriteOptions::set_sync(true), disable_wal(false), DB::write_opt(batch,options). Pin rocksdb =0.24.0 недостаточен для фиксации всего transitive graph: upstream dependency librocksdb-sys задан диапазоном. Реализация обязана сохранить Cargo.lock с resolved 0.17.3+10.4.2 и проверять native version; не подхватывать произвольную системную библиотеку. Base Rust 1.96.0 выше заявленного MSRV, но совместный workspace build ещё требуется. Источники: [WriteOptions 0.24.0](https://docs.rs/rocksdb/0.24.0/rocksdb/struct.WriteOptions.html), [db.rs](https://github.com/rust-rocksdb/rust-rocksdb/blob/v0.24.0/src/db.rs).
- Native build требует C/C++ toolchain; Rust bindgen — Clang/libclang. Go build102 включает Snappy/LZ4/Zlib/Zstd, Bzip2 выключен. Общий baseline хранения: compression LZ4, bottommost compression LZ4; оба language build profiles должны включать LZ4. Rust features: default-features=false, lz4 и bindgen-runtime; static sys из binding manifest. BlobDB, TTL/FIFO eviction, io_uring и отдельный allocator не включаются как обязательные оптимизации.
- Build artifact обязан фиксировать checksum native source, resolved dependency locks и toolchain/container digest при реализации. Нельзя делать непроверенный upgrade поверх pending store. Новая schema/version каталога проверяется до открытия записи; несовместимость даёт ошибку, не удаление очереди.

## Durability и настройки

WAL enabled; sync writes; один serialized writer и один DB catalog на owner. Обычный default CF с key prefixes. Все Write/Sync/iterator errors проверяются. Automatic repair с отбрасыванием записей не считается восстановлением. Pending payload без TTL/квоты, bounded RAM. Внутренние WAL-файлы RocksDB не удерживаются до NATS ACK: очередь живёт в KV/SST; приложение не обслуживает internal WAL вручную.

Точные memory/cache/compaction размеры — deployment tuning будущего benchmark, не новая гарантия доставки. Выбранная гарантия требует успешного storage sync; сбой/ложное подтверждение со стороны диска не устраняется названием engine. Производительность и бесконечная ёмкость не обещаются.

## Проверки этапа реализации

- E01: собрать Go/Rust adapters и Gateway с pins, проверить actual native version, кодеки и memory ownership.
- E03: WriteBatch/sync/reopen, сохранение bytes/IDs, partial batch, cleanup, release; сценарии [REC-T01–16](../ROCKSDB_DELIVERY_PROTOCOL.md).
- E05/E06: branch-aware replay, независимые live/history stores, восстановление задания N–M.
- E07: benchmark накопления и очистки на тех же durable настройках; отдельно optional BlobDB, без переноса vendor throughput на наш workload.
