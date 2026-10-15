# Владение служебным состоянием — E00-03.04

2026-10-15. Логическая модель, не выбранная СУБД и не доказанная crash safety. В каждой сети собственные coordinator, WAL и Gateway; никаких глобальных locks/прогресса между сетями. Стек — [COMPONENT_STACK.md](COMPONENT_STACK.md), wire semantics — [MESSAGE_CONTRACT.md](MESSAGE_CONTRACT.md).

## Один ответственный за изменение каждого факта

| Состояние | Авторитетный владелец | Кто использует / ограничение |
|---|---|---|
| Native chain state, receipts, canonical head/finality evidence | Native client соответствующей сети | Adapter наблюдает через native integration. Ни Gateway, ни NATS не решают, какая ветка каноническая |
| Capture attempt: frames/actions/state observations до решения о каноничности | Capture adapter данного native execution | Пока это execution observation, а не опубликованная canonical activation. Live/history producers не создают независимые lifecycle IDs |
| Inclusion → activation, block activation, lifecycle revisions и reorg ordering | Один Canonical Coordinator сети | Получает authoritative native chain notifications; сверяет исторические наблюдения с актуальной веткой; хранит один lifecycle для пересечения live/history |
| Нормализованное событие: event_id, source writer epoch/sequence, immutable Envelope bytes/digest | Coordinator + его durable outbox/WAL | Event ID назначается один раз логическим writer. Producer provenance не подменяет identity включения; history worker не генерирует новую активацию из-за другого job ID |
| Разбиение записи на outbound parts | Adapter-side delivery writer в контуре coordinator | После согласования доступного размера фиксирует layout для события в WAL. Gateway проверяет и передаёт этот layout; не меняет part_count/index на retry |
| event_id → chain_sequence и состояние регистрации публикации | Gateway сети, publication journal | Повтор того же ID/bytes возвращается к существующей записи. Одинаковый ID с другим digest/layout — конфликт, не overwrite |
| PubAck отдельных частей и итог готовности IngestAck | Gateway сети | ACK события только после PubAck CompleteRecord либо всех RecordPart. Adapter не считает успешную gRPC-запись подтверждением NATS |
| Полученный IngestAck и право отметить запись доставленной | Adapter-side delivery writer / WAL | Сверяет event_id/digest/sequence; правила физической очистки и retention зависят от REC |
| Native read/replay cursor и локальный прогресс обработки | Конкретный live/history worker | Не означает опубликованное покрытие; допустим speculative progress, но после restart он требует согласованного checkpoint |
| Job range и статус задачи истории | Coordinator сети / job registry | Wire/control интерфейс ещё JOB-01. Worker сообщает результат, но сам не изменяет global inclusion lifecycle |
| Опубликованное block/range coverage | Coordinator сети, на основании manifests и полученных delivery ACK | Coverage COMPLETE создаётся после проверки source inventory и ACK обязательных записей. Окончательная публикация coverage также требует PubAck |
| Stream messages/replication/retention | NATS JetStream в Compose/Kubernetes | NATS отвечает за применённую конфигурацию хранения; не определяет blockchain completeness или business lifecycle |

«Один владелец» — одна активная роль изменения состояния, а не обещание одного файла или универсальной БД. WAL физически может объединять несколько таблиц/ключей одного контура. Атомарные границы, durable transactions, fencing после restart и backend определяются в .06/REC.

## Размещение и native integration

Coordinator — модуль сетевого адаптера, не отдельный общесетевой сервис. Начальный baseline — размещать его с live integration сети. Нативные notifications поступают от этого клиента; history workers передают наблюдения в тот же coordinator через внутренний типизированный interface. При выборе отдельного history процесса способ IPC уточняет .05; это не разрешение shared writable datadir или второго независимого publisher.

Для Base модуль остаётся на Rust, для Go-клиентов — на Go. Общими являются протокол/инварианты/fixtures, а не обязательная попытка загрузить Go library в Rust runtime. Для Classic отдельный Go/C++ backend относится к Arbitrum One и согласует activation/coverage с тем же сетевым контуром.

## Контрольные сценарии

1. **Live и N–M нашли одно включение.** Coordinator сверяет network/block hash/native position/hash и текущую активацию. Другой producer/job не создаёт второй Applied. Если полнота или bytes расходятся, это проверяемый конфликт/ошибка нормализации, не overwrite.
2. **Публикация закончилась, IngestAck потерялся.** WAL повторяет тот же event_id/bytes/layout. Gateway сверяет journal; повторный PubAck допустим. Восстановление потерянного journal пока зависит от REC — эта модель не обещает exactly-once.
3. **Reorg во время публикации частей.** Source chain notifications обрабатывает coordinator; создаёт адресный Removed для активации. Старые оставшиеся части/status не могут реактивировать её. Block/range coverage старой ветки отзывается; правила parts/lifecycle проверяются fixture.
4. **Новый NATS/gRPC limit меньше сохранённой части.** Нельзя молча изменить layout или обрезать запись. Контур возвращает явную delivery error/backpressure; совместимость лимитов проверяется до выпуска записи. Процедура изменения лимитов при pending WAL — .06, не незаметная миграция.
5. **Исторический worker отстаёт.** Его read cursor не двигает live cursor, finality или публикацию чужой сети. Published coverage остаётся отдельным фактом, а ресурсная изоляция требует .05 и измерений.

## Граница готовности

E00-03.04 завершает логическое распределение ответственности. Не завершены: physical state store, crash-atomic commit native→WAL, storage replication/backup, lease/fencing, очистка journals, replay при потере всех локальных данных. Они явно зависят от отдельного REC.

При реализации E02/E03/E05 проверяются overlap/conflict, потерянный IngestAck, repeated PubAck, reorg при незавершённой multipart publication, limit downgrade и отсутствие продвижения coverage по одному read cursor. Выполнение этих тестов не заявляется.
