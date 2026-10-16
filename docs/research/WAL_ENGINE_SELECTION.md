# E00-03.06-B — выбор локального движка

Дата: 2026-10-16. SQLite исключён пользователем. Это исследование и рекомендация; движок, bindings и версии ещё не утверждены. POC/benchmarks не выполнялись.

## Вывод

Рекомендуемый базовый кандидат — RocksDB с включённым внутренним WAL, синхронными WriteBatch и раздельным writable каталогом каждого producer/Gateway. Для крупных payload отдельно проверить integrated BlobDB. Обычный RocksDB и BlobDB должны сравниваться на одном наборе сообщений; преимущество конкретной конфигурации пока не доказано.

Причина выбора: уже имеющиеся атомарные операции для payload/metadata/checkpoint, поддержка через Go/Rust bindings, отсутствие отдельного серверного компонента. Это оценка соответствия требованиям, а не утверждение об абсолютном лидерстве по скорости.

Альтернативы и ограничения языков: [WAL_ALTERNATIVES.md](WAL_ALTERNATIVES.md). Главный резервный вариант для Go — Pebble; для другой модели хранения — сегментный append-only log, но его протокол восстановления потребует отдельной работы.

## Что подтверждено первичными источниками

| Свойство | Значение для extractor | Источник |
|---|---|---|
| WriteBatch атомарно применяет несколько изменений; sync=true синхронизирует запись; disableWAL отключает внутренний WAL | Сообщения/closed marker и checkpoint/удаление можно фиксировать атомарными пакетами. WAL не отключать, durable статус не выдавать до успеха sync | [Basic Operations](https://github.com/facebook/rocksdb/wiki/Basic-Operations) |
| Integrated BlobDB выносит крупные values в blob files, в LSM оставляет ссылки | Меньше переписывания крупных payload при compaction, но требуется GC неиспользуемых blobs | [BlobDB](https://github.com/facebook/rocksdb/wiki/BlobDB) |
| Legacy BlobDB отличается от integrated; документация предупреждает о его ограничениях | Рассматриваем только integrated BlobDB через обычный DB API, не legacy StackableDB/TTL вариант | [BlobDB](https://github.com/facebook/rocksdb/wiki/BlobDB) |
| DeleteRange пишет range tombstone; физическое освобождение места происходит позднее | ACK даёт право удалить логически. Нельзя обещать немедленное сокращение диска | [DeleteRange](https://rocksdb.org/blog/2018/11/21/delete-range.html) |
| RocksDB замедляет/останавливает запись при отставании flush/compaction | Отсутствие прикладной квоты WAL не гарантирует бесконечную скорость накопления; нужны измерения backlog и write stalls | [Write Stalls](https://github.com/facebook/rocksdb/wiki/Write-Stalls) |
| grocksdb использует C API/librocksdb и CGO; Rust binding использует native RocksDB и требует toolchain | Один engine возможен для Go/Rust, но нужны совместимые pins и build-проверки; bindings сторонние, не обещание единой версии из коробки | [grocksdb](https://github.com/linxGnu/grocksdb), [rust-rocksdb](https://github.com/rust-rocksdb/rust-rocksdb) |

## Предлагаемая схема использования

Это кандидат технического протокола, не дополнительное принятое пользовательское решение.

- Namespaces: immutable сообщения/части; block batch manifests; активные задания и cursors. Identity содержит network/producer/run/block hash; ключ доставки использует локальную последовательность, а не только высоту блока.
- Один владелец записи на каталог. Payload и необходимые metadata в одном DB, чтобы не строить транзакцию между независимыми хранилищами. Native callback не должен выполнять блокирующий fsync на consensus-critical path; границу передачи и bounded RAM нужно определить отдельно.
- Подготовку большого блока можно сохранять частями. Closed marker фиксировать лишь после durable записи всех перечисленных references. После crash незакрытый batch не считается готовым к публикации.
- После PubAck всех сообщений/частей блока одним sync WriteBatch обновить рабочее состояние и удалить payload/временные metadata. Ошибка операции не продвигает checkpoint. При повторной доставке дубли допустимы.
- Удалять только подтверждённые batch. ACK более позднего блока не разрешает удалить более ранний pending блок; cursor продвижения учитывает незавершённые записи.
- Если ключи одного подтверждённого batch образуют диапазон, можно проверить DeleteRange; иначе удалить точные ключи. Нельзя удалять диапазон по максимальному ACK без учёта дыр и отдельных run.
- Внутренний WAL RocksDB обслуживает восстановление БД. Pending очередь живёт в key/value записях и может перейти в SST/blob files; срок жизни внутренних WAL-файлов не управляется NATS ACK. Не закреплять бесконечное хранение именно внутренних WAL-файлов.
- Не включать TTL или FIFO eviction для pending payload. Обычный ACK cleanup остаётся единственной прикладной причиной удаления.
- Для BlobDB threshold/GC/compression выбрать по измерениям; не задавать произвольные «оптимальные» цифры. Метаданные маленькие, отделение values имеет смысл проверять для payload.

## Что проверить до окончательного выбора конфигурации

1. Одни и те же реальные protobuf fixtures пяти сетей: размеры сообщений, большие multipart transactions, пустые блоки. Отдельно live и быстрый history replay.
2. Обычный RocksDB против integrated BlobDB при одинаковой sync durability и размере batch. Запись/чтение/ACK cleanup одновременно; NATS заменяется тестовым приёмником с управляемой задержкой, это не production downstream.
3. Длительное накопление без ACK, затем догоняющая отправка и очистка. Замерить полезные MB/s, p95/p99 durable commit latency, CPU/RAM, реально записанные байты, максимум занятого диска, write stalls и время освобождения места.
4. Crash перед/после sync и перед/после ACK cleanup, потеря ответа, незакрытый batch, ACK с дырой, физическая ошибка записи. Проверять отсутствие потери pending данных, а не полноту исходного blockchain блока.
5. Сборки выбранных Go/Rust bindings с pinned native RocksDB; проверить API WriteBatch/sync/BlobDB/DeleteRange и корректное освобождение native buffers. Не обещать throughput до этого.

Численный throughput/SLO пока не задан; исследование не подменяет его выдуманным целевым значением. Реализация и запуск POC требуют отдельного разрешения согласно AGENTS.md.

## Оставшиеся вопросы реализации

Выбор версии engine/bindings, физическое размещение journal, Gateway restart/cleanup handshake, writer ownership и crash fixtures остаются .06-B/C/D. Исследование не закрывает весь E00-03.06 и не создаёт новый сервис БД.
