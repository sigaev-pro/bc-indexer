# Владение служебным состоянием — E00-03.04, уточнение D23–D25

> D26–D31 (2026-10-16): [текущие требования доставки и восстановления](DELIVERY_RECOVERY.md). Restart с сохранённым диском автоматический; после потери диска история задаётся заново N–M. WAL копится без TTL/прикладной квоты до подтверждения NATS всех сообщений/частей блока. После ACK payload и временные статусы очищаются; остаётся минимальное рабочее состояние для restart. Сейчас один NATS; HA/backup и дальнейшая судьба подтверждённых данных вне scope. Backend принят D33: RocksDB. E00-03.06 завершён как проект: [pins](research/ROCKSDB_STACK.md), [протокол и REC-T01–16](ROCKSDB_DELIVERY_PROTOCOL.md).

2026-10-16. Вместо общего live/history coordinator используется независимое состояние получения и передачи каждого producer/run. Дедупликация пересечений и проверка содержимого блоков вне scope. Авторитетный контракт: [LIVE_HISTORY_DELIVERY.md](LIVE_HISTORY_DELIVERY.md).

| Состояние | Владелец | Ограничение |
|---|---|---|
| Native chain state, canonical head/finality | Native client сети | Gateway не определяет каноничность; native consensus validation сохраняется |
| Capture attempt и prepared messages | Adapter live или history | Разные pods/processes, независимые run_id и cursors |
| Наблюдаемый lifecycle/reorg/status | Native bridge данного producer | Не согласует активации с другими runs и не отбрасывает повторные включения |
| Event ID, immutable Envelope bytes/digest | Delivery writer данного producer/run | Retry сохраняет ID/bytes; повтор команды может создать новые события для тех же блоков |
| Outbound part layout | Adapter delivery writer | Фиксируется для retry; Gateway не перепаковывает pending событие молча |
| RECEIVED / WAL_SAVED / полученный IngestAck | Adapter progress journal | WAL_SAVED требует закрытого batch и согласованной durable записи; read cursor недостаточен |
| event_id → publication_epoch/chain_sequence / pending публикация | Gateway journal сети | Plan дополнительно хранит producer до публикации; только delivery identity, не dedup по block/tx hash или диапазонам |
| PubAck сообщений/частей | Gateway | NATS_ACKED только после всех ACK закрытого batch; потерянный ACK допускает повтор |
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

Это логическое распределение ответственности, не пройденные тесты. Полный block-content verifier, range coverage oracle и downstream dedup отсутствуют. E00-03.06 завершён как контракт; следующий шаг — .07, итоговая проверка.
