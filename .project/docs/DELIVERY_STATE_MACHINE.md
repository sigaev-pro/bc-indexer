# Нормативный автомат доставки — DOC-REPAIR-02

Точные identities, binding JSON/digests, manifest/reservation bytes, wrapper layout, RPC/errors и исполнимый cutover заданы в [CONTROL_WIRE_CONTRACT](CONTROL_WIRE_CONTRACT.md). Ниже — автомат состояний этого же протокола, не отдельная альтернативная схема.

2026-11-05. D26–D33, D52, D54, D57–D59, D73. Этот документ уточняет и заменяет неоднозначные переходы [ROCKSDB_DELIVERY_PROTOCOL](ROCKSDB_DELIVERY_PROTOCOL.md). Только технический проект; реализация/crash fixtures не выполнены. Native admission и source retention — отдельный контракт; сюда поступают durable capture obligations, затем immutable RecordFragment bytes логической Envelope.

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
| Gateway NATS_ACKED | следующий ordered Release | Barrier publisher/callback; sync RELEASING с cleanup_cursor, bounded cleanup | RELEASING; больше не публиковать |
| Gateway RELEASING | очередной cleanup chunk / restart | Удалить chunk, sync cursor; последним atomic commit продвинуть retired_prefix и удалить RELEASING | ReleaseAck только после финального commit |
| Producer cleanup завершён | ReleaseAck | Sync retired_prefix и удаление outbox | RETIRED; payload/plan отсутствуют |

IngestAck проверяет batch/manifest/plan/destination; ACK части либо успешный gRPC write не заменяет его. Перед удалением любых recovery fragments должна существовать durable замена либо достаточные replay prerequisites. Старый attempt не смешивается с новым manifest. После READY неизменны event identity и зафиксированное логическое кодирование RecordFragment по D73; после SEALED также неизменны reservation, layout, headers и TransportFrame bytes. Новый неопубликованный transport plan разрешён только в явно заданном pre-SEALED recovery после потери Gateway; READY fragments/IDs при этом не меняются.

До DELIVERY_STAGING уже durable semantic descriptors с activation/revision/source identity; retry attempt меняет только public event reservations. Один native decision с публичными событиями имеет ровно один batch с полным manifest, в том числе multi-block reorg/finality-only. Decision→batch_id и batch counter сохраняются атомарно, повтор native marker не выделяет новый batch. Decision без public events сохраняет checkpoint без batch. Технические chunks/WriteBatch не меняют эту семантическую границу.

Полный IngestAck, включая issuer_store_epoch и layout_manifest_digest, и release_outbox сохраняются до проверенного ReleaseAck и атомарного продвижения producer retired_prefix. Payload cleanup их не удаляет; они нужны RecoveryRelease после Gateway loss. Удаление outbox/ACK и продвижение retired_prefix — один sync commit после проверки ReleaseAck.

## Prepare и повторные попытки

Prepare передаёт v2 event descriptors и упорядоченные descriptors всех immutable RecordFragment: event identity/kind/ordinal, record_length/digest, fragment_count/manifest digest, затем fragment_index/encoded_length/SHA256. Весь manifest и список фрагментов одного event не помещаются в один RPC/WriteBatch; передаются bounded chunks. Closing manifest digest фиксирует порядок и состав. READY требует полного schema/tree/blob/seal validation и durable fragment bytes/manifest. Повтор с тем же batch_id/manifest_digest возвращает сохранённую reservation. Другой digest после READY — ERROR.

Reservation seed: `store_epoch,reservation_id,manifest_digest,event_count,sequence_base,destination_binding_digest,frame_limit,layout_version,header_version`. Событие получает chain_sequence=sequence_base+ordinal; это порядок регистрации, не blockchain chronology. Counter overflow проверяется до commit, значения не переиспользуются.

Layout определяется Gateway по фиксированной версии алгоритма и фактическим длинам каждого RecordFragment/headers. Producer сериализует frames и перед SEALED проверяет actual byte size. Изменение текущего лимита не меняет существующую reservation. Пока producer plan не SEALED, ни одна его часть не могла публиковаться. При потере Gateway такой незакрытый plan строится в новой plan generation после durable отказа от старой неопубликованной попытки; READY fragment bytes/IDs/digests остаются прежними. SEALED всегда повторяется с прежними epoch/sequence/layout/headers/bytes и не проходит новое распределение sequence.

Partial uploads возобновляются по сохранённым offsets. Orphan PREPARING/PREPARED удаляется только через fenced owner handshake, доказывающий, что SEALED/публикации не было; неизвестность не является разрешением очистки. Потерянный producer означает ручную диагностику/abandon по прежнему контракту, не таймерное удаление CLOSED payload.

## Release и потеря Gateway store

Публикация/ACK разных batch может завершаться вне порядка. **Release идёт строго по batch_seq, один in-flight Release на run.** Поздний ACK не перепрыгивает ранний незавершённый batch; поздние outbox/receipts хранятся на диске до своей очереди. Producer навсегда в рамках run сохраняет только compact retired_prefix, не архив payload/digests.

Handshake перед replay передаёт authenticated producer/run identity, destination binding и durable retired_prefix. Новый Gateway принимает этот prefix как baseline только от текущего fenced owner; затем восстанавливает pending >prefix. При потерянном ReleaseAck Gateway может быть на один Release впереди producer: это штатный случай, producer завершает свой ACKED outbox по повторному ReleaseAck, а не получает ошибку за меньший prefix. Gateway не откатывает свой prefix. При восстановлении от producer prefix, который выше локального Gateway, это owner recovery assertion о завершённых Release; старые операции сначала fenced, затем baseline устанавливается sync. Конфликт с локальным неподтверждённым producer batch, другой identity/generation или недоказанный произвольный скачок — ERROR, не удаление pending. Lost ReleaseAck разрешает повтор Release; уже удалённые batch 1…N повторно не требуются.

Prepare/Publish с seq<=retired_prefix возвращает ALREADY_RETIRED, не создавая keys. Producer не признаёт неподтверждённый batch доставленным по этому ответу: он применим только к локальному ACKED/outbox либо уже retired. Конфликт с неподтверждённым state — ERROR. Архив старых digests для сравнения намеренно не создаётся.

Если Gateway потерял store после IngestAck, producer уже мог удалить payload, сохранив ACKED/outbox следующего N при prefix=N−1. Для отсутствующего batch разрешён RecoveryRelease ровно N от текущего authenticated/fenced producer: его durable ACKED/outbox assertion содержит batch/plan/binding identity и полученный IngestAck. Gateway sync принимает assertion, затем продвигает prefix и отвечает ReleaseAck без требования повторить удалённый payload. Произвольный скачок, unACKed producer и конфликт существующей локальной записи запрещены. Это продолжение уже завершённой доставки, не новое подтверждение от NATS. RELEASING marker сохраняется до конца всей bounded уборки; отсутствие части keys после crash не повод публиковать снова.

## Fencing

Gateway boot_nonce меняется при каждом процессе; producer хранит монотонную connection_generation. Challenge-response handshake аутентифицирован mTLS/UDS owner и связан с network/role/producer/run/binding; старый challenge не принимается в новом boot. Session switch и DB mutation проходят общий serialized ordering. Проверка token выполняется вместе с admission/commit, а не только при чтении RPC.

Internal publisher worker имеет publisher_generation и работает только с существующим CLOSED batch. PubAck callback связан с batch_id/plan_digest/publisher_generation, не создаёт отсутствующие записи. Release и SealGeneration ставят barrier: старый callback не может воскресить state. Уже ушедший NATS запрос может дать допустимый дубль; нельзя обещать отозвать отправленные сетевые bytes.

## DestinationBinding до первого capture

Окружение создаёт общий immutable descriptor `DestinationBinding={destination_id,generation,network,expected_account,stream,subject_prefix,descriptor_digest}`. Оба процесса получают ссылку на него в YAML и сохраняют его до любых новых обязательств. Адаптер не получает NATS credentials. Первый offline capture уже имеет заданное назначение; это не онлайн-зависимость от Gateway/NATS. Потеря Gateway store не меняет binding READY/SEALED producer.

Gateway при подключении/reconnect и допуске CLOSED batch к публикации проверяет account, StreamInfo, subjects/limits и две metadata identity D77: extractor.network_digest / extractor.destination_id по [CONTROL_WIRE_CONTRACT](CONTROL_WIRE_CONTRACT.md). URL не является identity. Ответы UserInfo/AccountInfo/StreamInfo и необходимые request/reply permissions описывает deployment-контракт. Недоступная проверка — publication blocked; missing/malformed/mismatch — CONFIG_BLOCKED, без переназначения pending. Замена URL/credentials допустима только после проверки того же binding/account/stream/route. Метки не содержат binding generation/digest и не заменяют отдельные проверки route/plan. Потеря NATS store после ACK по-прежнему вне гарантии D30–D31; bootstrap не маскирует её автоматическим разрушительным recreate/reset.

## Drain и cutover D54/D57/D59

1. Остановить LIVE штатным способом и подтвердить отсутствие старого writer. Запустить ту же совместимую native сборку с `--extractor-mode=drain`; YAML содержит тот же store/binding. Перед recovery sync установить DRAIN, если он ещё не сохранён. DRAIN не включает новый H+1 capture и не выполняет sync-range выгрузку.
2. Восстановить сохранённые capture/lifecycle intents; новые непредусмотренные live callbacks не создают обязательств. Доставить READY/SEALED; закончить ACKED_CLEANUP и ordered Release. Native источник нужен для незакрытого capture; его недоступность не даёт DRAINED.
3. Только после исчерпания native obligations, ещё способных создать batch, producer фиксирует drain_id/highwater и отправляет SealGeneration(binding_generation,admission_generation,drain_id,highwater). Gateway barrier проверяет отсутствие незавершённых операций <=highwater и совпадение retired_prefix. Sync SEALED_GENERATION запрещает Prepare этой admission generation; повтор возвращает тот же SealAck. Retained pins, ожидающие будущего finalized anchor, не требуют нового capture и не блокируют DRAINED.
4. Producer sync сохраняет DRAINED с SealAck и завершается. Crash до этого продолжает DRAIN; новый LIVE не включается автоматически. Оба owners сохраняют identity/lifecycle, terminal prefix и seal evidence, очищают временные delivery payload/receipts. «Оба журнала очищены» не означает удалить DB/ключи.
5. Для смены destination обе роли получают desired descriptor следующей generation через binding_file; old binding/seal/drain_id берутся из store. Gateway desired mismatch входит в AWAIT_SWITCH, сохраняя прежнее active назначение. Native explicit LIVE sync создаёт SwitchIntent; PrepareSwitch → producer binding commit → CommitSwitch завершают переход. Затем Resume до снятия DRAINED/фиксации H. Для прежнего binding Switch пропускается, Resume обязателен. Необъявленные YAML keys old/new/drain_id не вводятся.
6. До первого SwitchPrepared и первого durable CommitSwitch новый target проверяется по desired binding, включая обе stream metadata D77. Неудача сохраняет старую либо незавершённую transition state; producer commit не откатывается автоматически. Retry уже committed transition возвращает durable результат независимо от текущей доступности NATS; новые публикации отдельно блокируются до успешной identity-проверки.
7. Старые SEALED generations не принимают новых данных. Упавший процесс завершает сохранённый switch, не угадывает нужную сторону по более свежему YAML. Если последний owner store потерян, операция требует восстановления seal/binding evidence от surviving owner; при недостатке evidence — ERROR, не bypass.

D59: будущие Removed/Status сохраняют activation references, даже если исходный ACKed Applied был в прежнем назначении. Уже подтверждённая история не копируется. Новому stream не обещается самостоятельная полнота старых ссылок; downstream/copy вне scope.

Resume использует отдельную монотонную admission_generation, не подменяет binding generation или connection_generation. Producer sync сохраняет ResumeIntent(old_seal,new_admission_generation,binding_digest), Gateway под barrier sync принимает ровно следующую generation, producer sync сохраняет LIVE после ответа. Повтор intent возвращает тот же результат. Capture заблокирован до финального producer commit. Все mutating RPC/session tokens привязаны также к admission_generation; поздние RPC старого seal отвергаются. При прежнем назначении binding не меняется. Crash между ответом Gateway и producer commit продолжает тот же ResumeIntent, а не увеличивает generation повторно. Store loss восстанавливает generation/intent от surviving owner до admission.

Дополнительные FRONTIER/DRAIN fixtures: IngestAck → producer cleanup → потеря Gateway → RecoveryRelease без payload; crash после каждого Gateway cleanup chunk; drain → LIVE в прежний binding → новый batch → поздний RPC прежней admission generation.

## Размеры, headers, память

Единица control/chunk RPC ограничена 256 KiB включая wrapper; payload fragment <=64 KiB. Эти технические defaults ограничивают одно сообщение/рабочий буфер, не итоговый event/block. Большие manifests/plans/records состоят из произвольного числа chunks. Bounded WriteBatch <=1 MiB включает metadata/marker; большой список references хранится вне closing batch. Любая конфигурация должна учитывать фактические wrapper bytes; невозможный frame limit отклоняется до создания нового plan.

Immutable publication descriptor содержит exact subject и header template: `Nats-Msg-Id`, `Nats-Expected-Stream`. Другие прикладные headers запрещены без новой header_version. Message ID — версия кодирования + network digest + length-delimited producer/run/event_sequence + complete-or-part-index; конкретный бинарный tuple кодируется base64url без неоднозначной конкатенации. Reply inbox, connection ID и timeout — транспортные свойства, не часть immutable logical request.

Plan сохраняет header bytes/версию и layout; actual TransportFrame+headers <=transport.max_frame_bytes (default 2 MiB). PubAck.Stream проверяется против plan. record_digest не подменяется fragment_digest либо digest публикационного запроса. NATS_ACKED требует PubAck всех transport частей всех фрагментов всех событий manifest; seal ACK отдельно ничего не завершает. NATS documented expectation header: [официальный контракт](https://docs.nats.io/nats-concepts/jetstream/headers).

Producer кодирует логическую Envelope bounded RecordFragment через disk spool/несколько проходов; полное Envelope protobuf-message не создаётся даже на диске. Отдельное большое поле дробится до сериализации, максимум data в BlobChunk=16384 bytes, encoded RecordFragment<=65536 bytes. Gateway проверяет schema/tree/blob/seal потоково; parent/cycle/unique-address проверки используют bounded disk-backed temporary indexes. Index keys очищаются после валидации/recovery; marker полного validation обязателен перед CLOSED. Capture snapshot/VM-safe yield и ограничение дополнительной памяти коллектора — native контракт D58; native память самой VM не считается ограниченной размером нашей transport queue.

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
