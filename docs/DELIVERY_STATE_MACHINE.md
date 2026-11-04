# Нормативный автомат доставки — DOC-REPAIR-02

2026-11-05. D26–D33, D52, D54, D57–D59. Этот документ уточняет и заменяет неоднозначные переходы [ROCKSDB_DELIVERY_PROTOCOL](ROCKSDB_DELIVERY_PROTOCOL.md). Только технический проект; реализация/crash fixtures не выполнены. Native admission и source retention — отдельный контракт; сюда поступают durable capture obligations, затем immutable Envelope bytes.

## Identity, режимы и atomicity

`batch_id=(producer_id,run_id,batch_seq)`. batch_seq выделяется атомарно с durable **delivery intent** для уже определённого native canonical/lifecycle результата; отсутствие delivery intent означает отсутствие выделенного batch. Ранний native admission имеет отдельный observation_sequence: speculative/отклонённый native input не расходует batch_seq. Повтор сборки delivery STAGING сохраняет batch_id, увеличивает attempt_generation и резервирует свежие event_sequence. Номера event могут иметь дыры; номера существующих batch не бросаются без завершения. CAPTURE_STAGING относится к native observation, DELIVERY_STAGING — к batch; ни одно не публикуется.

Режим producer: `LIVE | DRAIN | DRAINED | BLOCKED`. `LIVE` разрешает новые capture после native barrier H. `DRAIN` восстанавливает только уже сохранённые обязательства, доставляет их и завершает release. `DRAINED` сохраняется durable; обычный restart без явного разрешённого возврата в LIVE не начинает новый capture. BLOCKED сохраняет причину, если запись возможна; отсутствие места не должно мешать самому прекращению небезопасной работы. Identity/counters/lifecycle references не удаляются при drain.

Один serialized writer на store. Все изменения recoverability/прав на удаление и ответы, разрешающие следующий шаг, имеют sync commit; отдельно fsync memtable на каждой отправке не требуется. Данные/chunks могут записываться несколькими bounded WriteBatch, но закрывающий marker атомарно связывает manifest/identity/checkpoint. Незакрытый marker не разрешает публикацию.

Producer prefixes: `meta/{identity,counters,destination_binding,retired_prefix,mode}`, `batch/<seq>/intent`, `batch/<seq>/attempt/<generation>/...`, `ready_manifest`, `plan/<seq>/{header,chunks,SEALED}`, `cleanup/<seq>`, `release_outbox/<seq>`, `cutover/<generation>`.

Gateway prefixes: `meta/{store_epoch,destination_binding}`, `session/<producer,run>`, `retired_prefix/<producer,run>`, `batch/<id>/{prepare_chunks,reservation,frame_chunks,CLOSED,ackset,NATS_ACKED}`, `sealed_generation/<producer,run>`, `cutover/<generation>`. Prefixes — logical contract, физическое кодирование/field numbers фиксируются до E03.

## Таблица переходов

| Owner/state | Вход | Durable действие | Разрешённое продолжение |
|---|---|---|---|
| Producer INTENT/STAGING | повтор capture | Новый attempt generation/event IDs того же batch_id; сохранить прежние recovery inputs до замены | STAGING; никаких Publish |
| Producer STAGING | полный корректный capture | Manifest + READY + capture/lifecycle checkpoint одним closing commit | READY |
| Gateway PREPARING | descriptor chunk | Проверить offset/digest, сохранить bounded chunk | Повтор того же chunk безопасен |
| Gateway PREPARING | FinalizePrepare | Проверить закрытый manifest; sync immutable reservation seed и sequence range | Prepared с теми же reservation bytes при повторе |
| Producer READY | Prepared | Записать plan/header/frame chunks; проверить actual size/digests; sync SEALED | Разрешён Publish |
| Gateway PREPARED/RECEIVING | Publish chunk / replay SEALED plan | Проверить binding/plan/offset/digest; сохранить frames; sync CLOSED после проверки полноты manifest | Публиковать только CLOSED |
| Gateway CLOSED | PubAck части | Проверить ожидаемый stream и plan identity; учесть ACK | Повтор при потере bitmap допустим |
| Gateway CLOSED | все PubAck batch | Sync NATS_ACKED с manifest/plan identity | IngestAck |
| Producer SEALED | проверенный IngestAck | Sync ACKED_CLEANUP + release_outbox, затем bounded payload cleanup | Release только после локальной уборки |
| Gateway NATS_ACKED | следующий ordered Release | Barrier publisher/callback, bounded cleanup; sync retired_prefix после уборки | ReleaseAck |
| Producer cleanup завершён | ReleaseAck | Sync retired_prefix и удаление outbox | RETIRED; payload/plan отсутствуют |

IngestAck проверяет batch/manifest/plan/destination; ACK части либо успешный gRPC write не заменяет его. Перед удалением любых recovery fragments должна существовать durable замена либо достаточные replay prerequisites. Старый attempt не смешивается с новым manifest. Любые bytes под существующим immutable event_id неизменны, даже до первой публикации.

## Prepare и повторные попытки

Prepare передаёт описания immutable Envelope: event_id, digest, byte length, event family/kind и ordinal. Весь manifest не помещается в один RPC/WriteBatch. Каждый chunk имеет стабильные offset/digest; closing manifest digest фиксирует порядок и состав. Повтор с тем же batch_id/manifest_digest возвращает сохранённую reservation. Другой digest после READY — ERROR.

Reservation seed: `store_epoch,reservation_id,manifest_digest,event_count,sequence_base,destination_binding_digest,frame_limit,layout_version,header_version`. Событие получает chain_sequence=sequence_base+ordinal; это порядок регистрации, не blockchain chronology. Counter overflow проверяется до commit, значения не переиспользуются.

Layout определяется Gateway по фиксированной версии алгоритма и фактическим длинам Envelope/headers. Producer сериализует frames и перед SEALED проверяет actual byte size. Изменение текущего лимита не меняет существующую reservation. Пока producer plan не SEALED, ни одна его часть не могла публиковаться. При потере Gateway такой незакрытый plan строится в новой plan generation после durable отказа от старой неопубликованной попытки; recovery inputs остаются. SEALED всегда повторяется с прежними epoch/sequence/layout/headers/bytes и не проходит новое распределение sequence.

Partial uploads возобновляются по сохранённым offsets. Orphan PREPARING/PREPARED удаляется только через fenced owner handshake, доказывающий, что SEALED/публикации не было; неизвестность не является разрешением очистки. Потерянный producer означает ручную диагностику/abandon по прежнему контракту, не таймерное удаление CLOSED payload.

## Release и потеря Gateway store

Публикация/ACK разных batch может завершаться вне порядка. **Release идёт строго по batch_seq, один in-flight Release на run.** Поздний ACK не перепрыгивает ранний незавершённый batch; поздние outbox/receipts хранятся на диске до своей очереди. Producer навсегда в рамках run сохраняет только compact retired_prefix, не архив payload/digests.

Handshake перед replay передаёт authenticated producer/run identity, destination binding и durable retired_prefix. Новый Gateway принимает этот prefix как baseline только от текущего fenced owner; затем восстанавливает pending >prefix. При потерянном ReleaseAck Gateway может быть на один Release впереди producer: это штатный случай, producer завершает свой ACKED outbox по повторному ReleaseAck, а не получает ошибку за меньший prefix. Gateway не откатывает свой prefix. При восстановлении от producer prefix, который выше локального Gateway, это owner recovery assertion о завершённых Release; старые операции сначала fenced, затем baseline устанавливается sync. Конфликт с локальным неподтверждённым producer batch, другой identity/generation или недоказанный произвольный скачок — ERROR, не удаление pending. Lost ReleaseAck разрешает повтор Release; уже удалённые batch 1…N повторно не требуются.

Prepare/Publish с seq<=retired_prefix возвращает ALREADY_RETIRED, не создавая keys. Producer не признаёт неподтверждённый batch доставленным по этому ответу: он применим только к локальному ACKED/outbox либо уже retired. Конфликт с неподтверждённым state — ERROR. Архив старых digests для сравнения намеренно не создаётся.

## Fencing

Gateway boot_nonce меняется при каждом процессе; producer хранит монотонную connection_generation. Challenge-response handshake аутентифицирован mTLS/UDS owner и связан с network/role/producer/run/binding; старый challenge не принимается в новом boot. Session switch и DB mutation проходят общий serialized ordering. Проверка token выполняется вместе с admission/commit, а не только при чтении RPC.

Internal publisher worker имеет publisher_generation и работает только с существующим CLOSED batch. PubAck callback связан с batch_id/plan_digest/publisher_generation, не создаёт отсутствующие записи. Release и SealGeneration ставят barrier: старый callback не может воскресить state. Уже ушедший NATS запрос может дать допустимый дубль; нельзя обещать отозвать отправленные сетевые bytes.

## DestinationBinding до первого capture

Окружение создаёт общий immutable descriptor `DestinationBinding={destination_id,generation,network,expected_account,stream,subject_prefix,descriptor_digest}`. Оба процесса получают ссылку на него в YAML и сохраняют его до любых новых обязательств. Адаптер не получает NATS credentials. Первый offline capture уже имеет заданное назначение; это не онлайн-зависимость от Gateway/NATS. Потеря Gateway store не меняет binding READY/SEALED producer.

Gateway при подключении проверяет account, StreamInfo, subjects/limits и binding metadata, заданные environment bootstrap; URL не является identity. Ответы UserInfo/AccountInfo/StreamInfo и необходимые request/reply permissions описывает deployment-контракт. Недоступная проверка — publication blocked; заведомое несовпадение — CONFIG_BLOCKED, без переназначения pending. Замена URL/credentials допустима только после проверки того же binding/account/stream/route. Потеря NATS store после ACK по-прежнему вне гарантии D30–D31; bootstrap не маскирует её автоматическим разрушительным recreate/reset.

## Drain и cutover D54/D57/D59

1. Остановить LIVE штатным способом и подтвердить отсутствие старого writer. Запустить ту же совместимую native сборку с `--extractor-mode=drain`; YAML содержит тот же store/binding. Перед recovery sync установить DRAIN, если он ещё не сохранён. DRAIN не включает новый H+1 capture и не выполняет sync-range выгрузку.
2. Восстановить сохранённые capture/lifecycle intents; новые непредусмотренные live callbacks не создают обязательств. Доставить READY/SEALED; закончить ACKED_CLEANUP и ordered Release. Native источник нужен для незакрытого capture; его недоступность не даёт DRAINED.
3. Producer фиксирует drain_id и batch_highwater, затем отправляет SealGeneration(binding_generation,drain_id,highwater). Gateway fencing barrier проверяет отсутствие незавершённых операций <=highwater и совпадение retired_prefix. Sync SEALED_GENERATION запрещает новые Prepare этой generation; повтор возвращает тот же SealAck.
4. Producer sync сохраняет DRAINED с SealAck и завершается. Crash до этого продолжает DRAIN; новый LIVE не включается автоматически. Оба owners сохраняют identity/lifecycle, terminal prefix и seal evidence, очищают временные delivery payload/receipts. «Оба журнала очищены» не означает удалить DB/ключи.
5. Для смены destination новый descriptor имеет новую generation; обе конфигурации указывают old/new binding и drain_id. Fenced handshake пишет Gateway SWITCH_PREPARED, producer sync новую binding, Gateway подтверждает SWITCH_COMMITTED. Повтор каждого шага идемпотентен; несовпадение generation блокирует capture. Команда LIVE выполняет этот handshake до снятия DRAINED, затем фиксирует новую H. Для возврата в LIVE без смены назначения также требуется явный restart с mode=live и завершённый seal handshake.
6. Старые SEALED generations не принимают новых данных. Упавший процесс завершает сохранённый switch, не угадывает нужную сторону по более свежему YAML. Если последний owner store потерян, операция требует восстановления seal/binding evidence от surviving owner; при недостатке evidence — ERROR, не bypass.

D59: будущие Removed/Status сохраняют activation references, даже если исходный ACKed Applied был в прежнем назначении. Уже подтверждённая история не копируется. Новому stream не обещается самостоятельная полнота старых ссылок; downstream/copy вне scope.

## Размеры, headers, память

Единица control/chunk RPC ограничена 256 KiB включая wrapper; payload fragment <=64 KiB. Эти технические defaults ограничивают одно сообщение/рабочий буфер, не итоговый event/block. Большие manifests/plans/records состоят из произвольного числа chunks. Bounded WriteBatch <=1 MiB включает metadata/marker; большой список references хранится вне closing batch. Любая конфигурация должна учитывать фактические wrapper bytes; невозможный frame limit отклоняется до создания нового plan.

Immutable publication descriptor содержит exact subject и header template: `Nats-Msg-Id`, `Nats-Expected-Stream`. Другие прикладные headers запрещены без новой header_version. Message ID — версия кодирования + network digest + length-delimited producer/run/event_sequence + complete-or-part-index; конкретный бинарный tuple кодируется base64url без неоднозначной конкатенации. Reply inbox, connection ID и timeout — транспортные свойства, не часть immutable logical request.

Plan сохраняет header bytes/версию и layout; actual TransportFrame+headers <=transport.max_frame_bytes (default 2 MiB). PubAck.Stream проверяется против plan. Полный Envelope digest не подменяется digest публикационного запроса. NATS documented expectation header: [официальный контракт](https://docs.nats.io/nats-concepts/jetstream/headers).

Producer строит Envelope через disk spool/two-pass lengths и потоковый digest; не требует полного protobuf object graph в RAM. Gateway проверяет field chunks потоково; parent/cycle/unique-address проверки используют bounded disk-backed temporary indexes. Index keys очищаются после валидации/recovery; marker полного validation обязателен перед CLOSED. Capture snapshot/VM-safe yield и ограничение дополнительной памяти коллектора — native контракт D58; native память самой VM не считается ограниченной размером нашей transport queue.

## Errors, ENOSPC, совместимость

| Класс | Действие |
|---|---|
| TRANSIENT: disconnect/timeout/NATS offline | capped exponential backoff+jitter, без лимита попыток; bounded in-flight, pending на диске |
| CONFIG_BLOCKED: неверный route/TLS/permissions/limit | прекратить публикацию, сохранить данные и понятную причину; после исправления проверить снова |
| SOURCE_UNSUPPORTED / локальная normalization corruption | D57: завершить native процесс этой сети; не silent skip/false READY |
| LOCAL_FATAL adapter WAL | остановить native процесс; не требовать успешной записи ERROR на уже сломанном диске |
| Gateway journal fatal | остановить Gateway; адаптер продолжает safe local WAL по D28, пока его состояние исправно. Потерянная связь не считается доказательством remote corruption |
| Immutable READY/SEALED conflict | BLOCKED, bytes под тем же ID не менять; исправление сборки/диагностика, не автоматический discard/re-ID |

Повтор с новым event_id допустим только для непубликовавшейся STAGING attempt с сохранённым обязательством. Полностью закрытые events не «исправляются» задним числом. Poison batch удерживает соответствующий frontier/drain, ошибка видна; это честная остановка, не бесконечный быстрый retry.

ENOSPC: прекратить capture; добавить место/расширить volume либо безопасно освободить посторонние данные вне RocksDB. Ручное удаление SST/WAL/pending и repair с drop запрещены. Recovery прежней совместимой сборкой отдаёт приоритет ACK/cleanup; drain не обещает завершиться без места для служебных записей. Отдельная TTL/квота pending не вводится.

Preflight versions: `store_format,plan_format,wire_schema,validator_profile,native_profile`; read/replay/new-write capabilities проверяются до writable mutation. Build ID сам по себе не означает несовместимость. Совместимый reader публикует прежние bytes; новая запись использует объявленную новую schema только после preflight. Incompatible wire/plan upgrade требует DRAINED старой сборкой. **Изменение несовместимого persistent store format не разрешается одним drain:** identity/lifecycle остаются, автоматических миграций нет. Такой upgrade/rollback отклоняется до записи даже при пустом payload. Поддержка нового формата требует отдельного будущего проекта, не скрытого reset.

## Приёмочные fixtures будущей реализации

ATTEMPT-01 — повторные STAGING, pruning source, сохранение последней копии. PREPARE-01 — lost chunks/replies, conflict digest, новый limit. FRONTIER-01 — 100 retired, потеря Gateway, batch101 и поздний Publish50. FENCE-01 — barriers token/commit/Release/PubAck. DEST-01 — READY до Prepare, иной target после loss. DRAIN-01 — crash каждого seal/switch шага, оба owners, постоянный live вход до остановки. MEM-01 — большой record/manifest без роста working set. UPGRADE-01 — reader capabilities, pending старой версии, отказ persistent format change даже после drain. POISON-01 и ENOSPC-01 — отсутствие false ACK/drop и корректный recovery. Эти fixtures не запускались; DOC-REPAIR закрывает проектные переходы, не E03 runtime gate.
