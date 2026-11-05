# Владение служебным состоянием — E00-03.04, уточнение D23–D25

> Исторический проектный материал / source evidence. Сохранён для происхождения решений; прежние scope, next steps и схемы не являются действующим контрактом. Текущее поведение полностью задано в [ТЗ v3](../evm-transaction-extractor-tz-v3-codex.md), задачи — [DEVELOPMENT_PLAN](DEVELOPMENT_PLAN.md). Из этого файла нельзя добавлять требования в текущую реализацию.

> DOC-REPAIR-02: [нормативный автомат](DELIVERY_STATE_MACHINE.md) уточняет capture observation vs delivery batch, stable attempts, publisher/session fencing, ordered Release и bootstrap retired prefix. DestinationBinding сохраняется обоими owners до capture; native state не является Gateway journal. Drain сохраняет identity/lifecycle, удаляя только завершённое delivery-состояние.

> D40: у каждой сети отдельный RocksDB WAL адаптера и отдельный RocksDB журнал Gateway, со своими writers, каталогами/томами, identity и прогрессом. Namespace в общей writable DB не заменяет эту изоляцию. Общего multi-network WAL или writer нет; рестарт и изменение одной сети не требуют действий остальных. Общий NATS сохраняется как транспортная инфраструктура; pending каждой сети хранится независимо. История отложена D38.

> D26–D31 (2026-10-16): [текущие требования доставки и восстановления](DELIVERY_RECOVERY.md). Restart с сохранённым диском автоматический; после потери диска история задаётся заново N–M. WAL копится без TTL/прикладной квоты до подтверждения NATS всех сообщений/частей блока. После ACK payload и временные статусы очищаются; остаётся минимальное рабочее состояние для restart. Сейчас один NATS; HA/backup и дальнейшая судьба подтверждённых данных вне scope. Backend принят D33: RocksDB. E00-03.06 завершён как проект: [pins](research/ROCKSDB_STACK.md), [протокол и REC-T01–16](ROCKSDB_DELIVERY_PROTOCOL.md).

2026-10-16. Вместо общего live/history coordinator используется независимое состояние получения и передачи каждого producer/run. Дедупликация пересечений и проверка содержимого блоков вне scope. Авторитетный контракт: [LIVE_HISTORY_DELIVERY.md](LIVE_HISTORY_DELIVERY.md).

| Состояние | Владелец | Ограничение |
|---|---|---|
| Native chain state, canonical head/finality | Native client сети | Gateway не определяет каноничность; native consensus validation сохраняется |
| Capture attempt и prepared messages | Adapter live или history | Разные pods/processes, независимые run_id и cursors |
| Наблюдаемый lifecycle/reorg/status | Native bridge данного producer; durable lifecycle_state | References/counter/branch checkpoint сохраняются с READY, не удаляются ACK cleanup пока нужны Removed/Status. Нет общего registry между runs; подробные native finality правила E02/E05 |
| Event ID, immutable Envelope bytes/digest | Delivery writer данного producer/run | Retry сохраняет ID/bytes; повтор команды может создать новые события для тех же блоков |
| Outbound part layout | Gateway при Prepare; Adapter delivery writer сохраняет plan и frame bytes | D52: Gateway выбирает разбиение по своему лимиту; после durable plan layout/bytes неизменны для retry |
| RECEIVED / WAL_SAVED / полученный IngestAck | Adapter progress journal | WAL_SAVED требует закрытого batch и согласованной durable записи; read cursor недостаточен |
| event_id → publication_epoch/chain_sequence / pending публикация | Gateway journal сети | Plan дополнительно хранит producer до публикации; только delivery identity, не dedup по block/tx hash или диапазонам |
| Logical destination и PubAck сообщений/частей | Gateway; producer хранит destination вместе с plan | D54: NATS_ACKED только после всех ACK закрытого batch в одном назначении; потерянный ACK допускает повтор, смена назначения при pending запрещена |
| Параметры N–M / cursor задания | History producer/run | Простая конечная команда; нет общего job registry, устраняющего overlap |
| Stream messages и retention | NATS JetStream | ACK не подтверждает downstream-валидацию или бессрочное хранение |

Per-network служебное хранение означает namespaces и изолированные журналы, а не один embedded файл с несколькими writers из разных pods. RocksDB принят D33; pins/flush/cleanup закреплены в [протоколе .06](ROCKSDB_DELIVERY_PROTOCOL.md). Adapter WAL и Gateway publication journal сохраняют собственные факты; общей транзакции между native import, двумя журналами и NATS нет. Разрывы закрываются native replay, durable publication plan и at-least-once retry.

## Проверяемое поведение

- Live и history получили тот же блок: оба могут сохранить и отправить его; не требуется общий activation registry или взаимное ожидание.
- Повтор команды N–M: новый run_id; повторная публикация допустима.
- Потерян IngestAck: повтор исходного события не требует нового native чтения, если bytes доступны в WAL; повторная NATS-публикация допустима.
- Неполная отправка частей: блока в стадии NATS_ACKED ещё нет.
- Reorg: producer публикует native lifecycle observations со своими references; объединение нескольких наблюдателей относится к принимающей стороне.
- Ошибка получения/нормализации/WAL: ERROR не становится успешной доставкой.
- Снижение лимитов ниже уже сохранённой части: явная ошибка/остановка передачи; не усечение и не незаметная смена bytes/layout.
- Смена NATS account/stream/subjects при pending: явная ошибка до публикации; подтверждения разных назначений не суммируются. Замена адреса того же назначения проверяется отдельно по D54.

Это логическое распределение ответственности, не пройденные тесты. Полный block-content verifier, range coverage oracle и downstream dedup отсутствуют. E00-03.06 завершён как контракт, .07 — [историческая итоговая проверка](E00_03_ARCHITECTURE_REVIEW.md). CFG закрыт D39, история отложена D38; текущие технические пробелы — в [аудите](DESIGN_AUDIT_2026_11_04.md). Только документация.
