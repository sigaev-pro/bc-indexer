# Идентификаторы, binding и служебный протокол v2

DOC-FINAL-01 · 2026-11-05. Точный логический контракт первой версии для ETH/BSC/POLY. Дополняет [автомат доставки](DELIVERY_STATE_MACHINE.md) и [YAML](LIVE_CONFIG_SPEC.md). Это документация, не созданные proto/bindings или выполненные тесты.

## Базовые кодирования и идентичности

`Id16` — 16 bytes; новые независимые identity генерируются криптографическим RNG и сохраняются до использования. Текстовая форма — 32 lowercase hex characters без `0x`. `Hash32`/`UInt256` — 32 bytes, текст 64 lowercase hex без `0x`; `UInt256` big endian. Счётчики — checked unsigned u64: начало 1, ноль только для пустого frontier; ordinal/index начинается 0. Overflow — явная ошибка до записи/публикации, не wrap/reuse.

| Поле | Жизненный цикл |
|---|---|
| producer_id, run_id:Id16 | Создаются при первом создании adapter store; сохраняются при restart/drain/Resume. Новая H не создаёт новый run. Новый store после потери диска имеет новые IDs; чужой pending run не присваивается |
| node_instance_id:Id16 | Persistent identity native datadir, связанная с chain/genesis; восстановление adapter проверяет её |
| writer_epoch:Id16 | Новый provenance ID каждого native процесса; исходный epoch в уже созданных descriptors не переписывается. Не заменяет server-side fencing |
| store_epoch:Id16 | Persistent Gateway store identity; новый store имеет новый epoch. publication_epoch plan — issuer store_epoch, включая replay старого epoch |
| reservation_id, drain_id, switch_id, resume_id, request_id:Id16 | ID соответствующего объекта/операции, сохраняется при повторе той же durable операции |
| boot_nonce, challenge:Id16 | Случайные значения процесса/handshake; старый challenge не действует в новом boot |
| session_token:bytes32 | Непредсказуемый token текущей authenticated session; не логируется. Только поверх mTLS или разрешённого authenticated UDS |
| binding/admission/connection generation:u64 | Разные счётчики, initial=1; binding меняет назначение, admission — новый capture после seal, connection — новую RPC session |

`event_id = producer_id16 || run_id16 || event_sequence_u64be` (40 bytes). `batch_id` и `observation_id` используют ту же форму с **отдельными** batch/observation counters. `source_sequence` — отдельный durable счётчик semantic descriptors, не event_sequence и не номер блока. `activation_id = native_decision_sequence_u64be || entity_ordinal_u64be`, всегда квалифицирован producer/run в ActivationRef; block entity получает ordinal 0, tx entities — body index+1; optional entities следуют после native body count в native order. Новое применение ветки имеет новый decision sequence. `execution_action_id`, frame/action indices и revision — u64 в пределах указанной tx/activation; revision Applied=0, updates>0.

Семантический manifest фиксирует event kind, source provenance/sequence, activation refs и revisions **до DELIVERY_STAGING**. Native decision→batch_id и batch counter фиксируются одним sync commit. Повтор decision marker находит прежнюю связь; после cleanup от повтора защищает applied-decision frontier. Decision без public events не выделяет batch. Attempt allocation сохраняет `{attempt_generation,event_sequence_base,event_count}` атомарно; event ordinal задаёт ID. Retry до READY может сменить только public event IDs/Envelope attempt, не semantic identity/revision. После READY IDs/bytes неизменны независимо от Prepare.

D74: execution_order — отдельный единый tx-local счётчик от 1 для calls/execution_actions/balance actions. action_index, execution_action_id и frame_id сохраняют собственные пространства identity; scope_id=0 — транзакционная граница, следующие scope IDs выделяются по порядку создания native областей. Сравнение/повтор counters задаёт CURRENT_SEMANTICS; эти значения не входят в allocation event/source/batch/chain sequence и не меняются от D73 fragmentation/retry. `execution_position.operation_index` — body index блока, `TransactionProtocol.operation_index` — отдельный tx-local protocol index. Ни один из них не подменяет execution_order.

## DestinationBinding file

`destination.binding_file` — путь к **желаемому** descriptor. JSON UTF-8, без duplicate/unknown keys, comments/BOM. Никаких env substitutions. Порядок ключей/whitespace не имеют значения. Старый binding, seal, drain_id и generation берутся только из durable store; не из новых YAML полей.

| JSON field | Формат |
|---|---|
| schema_version | JSON integer 1 |
| destination_id | Id16 hex, постоянная identity линии конфигурации сети |
| generation | Canonical decimal string положительного u64, без leading zeros/знака |
| network.family | Строка `EVM` |
| network.chain_id | UInt256 hex |
| network.genesis_hash | Hash32 hex |
| expected_account | Непустая UTF-8 строка без управляющих символов; точное поле account UserInfo, не display name |
| stream | Непустое буквальное имя stream, проходит правила pinned NATS server; wildcard запрещён |
| subject_prefix | Непустые разделённые точками tokens; без whitespace, wildcard и пустых tokens |
| descriptor_digest | Hash32 hex по формуле ниже |

В бинарных формулах `LP(s)=u32be(length(UTF8(s))) || UTF8(s)`; Unicode normalization отсутствует. Length overflow/невалидный UTF-8 отвергаются. Полный JSON descriptor ограничен 16 KiB; config check проверяет также размер полного PrepareSwitch с wrapper/SealAck/identity ≤256 KiB. Descriptor, который нельзя передать допустимым RPC, отвергается до запуска. В network digest family EVM кодируется byte `0x01`:

`network_digest = SHA256(UTF8("extractor.network.v1") || 0x00 || 0x01 || chain_id32 || genesis32)`.

`descriptor_digest = SHA256(UTF8("extractor.binding.v1") || 0x00 || destination_id16 || generation_u64be || 0x01 || chain_id32 || genesis32 || LP(expected_account) || LP(stream) || LP(subject_prefix))`.

Digest не включает собственное поле, URL, TLS/credentials или frame limit. Changing account/stream/prefix требует следующей binding generation той же destination_id и DRAINED. Изменять network identity в существующем store нельзя. Same generation+different content — конфликт; неожиданный скачок generation запрещён. Gateway YAML stream/subject_prefix должны точно совпасть с descriptor: это проверяемые дубли, не альтернативный route. Каждый owner сохраняет свой desired binding локально до создания обязательств; offline adapter делает это до capture. Между owners descriptor сверяется при первом authenticated handshake, до Prepare/Publish; отсутствие Gateway не запрещает безопасный offline capture.

## Stream metadata identity D77

Поля находятся в `StreamInfo.config.metadata` выбранного NATS stream: строковые key/value, не message headers, description или отдельный YAML descriptor. Поддержка map metadata проверена в [pinned NATS StreamConfig](https://github.com/nats-io/nats-server/blob/b2fd3efc7456b11b06a54877ad6f6ccfb911edb4/server/stream.go). Этот проект задаёт ровно два обязательных ключа; регистрация дополнительного generation/digest metadata не подразумевается.

| Точный key | Формат value | Ожидаемое значение |
|---|---|---|
| extractor.network_digest | 64 ASCII lowercase hex characters, без 0x, пробелов или padding | Hex SHA-256 network_digest по формуле extractor.network.v1 выше, вычисленный из network выбранного DestinationBinding |
| extractor.destination_id | 32 ASCII lowercase hex characters, без 0x/пробелов | Hex destination_id выбранного DestinationBinding, исходные Id16 bytes |

Ключи чувствительны к регистру; значения сравниваются точно после проверки формата, без case folding/trim/Unicode normalization. Отсутствующий metadata map/ключ, пустая строка, неверный тип/длина/регистр или несовпадающее значение дают CONFIG_BLOCKED. Ошибка указывает stream/key и вид несоответствия, без credentials. Недоступный INFO/нет ответа — publication blocked/retry по действующей классификации ошибок, не доказательство missing либо совпадения. Неизвестные дополнительные metadata не участвуют в сравнении binding и сохраняются bootstrap; они не заменяют обязательный ключ.

| Фаза | С чем сверять / результат |
|---|---|
| Подключение/reconnect Gateway и допуск CLOSED batch к публикации | С active durable binding и binding сохранённого plan; они обязаны совпасть. Старое успешное наблюдение не переносится на новое соединение или другой target. При возобновлении публикации после ошибки выполняется новая проверка |
| AWAIT_SWITCH / первый PrepareSwitch | Новый target сверяется с desired descriptor из точного SwitchIntent; active binding пока старый, публикация в desired route запрещена. Неудача не даёт SwitchPrepared |
| Первый CommitSwitch | Повторно проверить новый target перед durable активацией. Если producer уже сохранил новый binding, а проверка не прошла, сохранить intent и блокировку; не откатывать producer и не публиковать в старое назначение |
| Retry уже durable CommitSwitch / Resume / Release | Вернуть сохранённый результат по прежним identity/idempotency правилам. Недоступность NATS либо metadata mismatch не отменяет уже committed control receipt. Разрешение новой публикации всё равно требует проверки текущего target |
| Same-binding offline capture/recovery | Online metadata check — обязанность Gateway перед публикацией. Она не добавляет зависимости native capture от доступности NATS и не препятствует cleanup уже ACKed обязательств |

Смена account/stream/prefix по-прежнему требует drain и согласованного control transition. В следующей generation той же destination_id метки остаются прежними, если network тот же. Новый stream получает те же ожидаемые две identity-метки до активации назначения. При смене только prefix generation/digest не записываются в stream metadata; subjects/account/stream name/limits и descriptor_digest проверяются отдельно. Совпадение двух меток не заменяет ни одной из этих проверок и не разрешает объединять ACK разных plans/destinations.

Metadata проверяются в указанных точках; это конфигурационные утверждения окружения, не NATS atomic publish precondition и не доказательство сохранности истории. При обнаруженном mismatch новые публикации останавливаются, pending/bytes/plan сохраняются; уже отправленные frames нельзя отозвать. `Nats-Expected-Stream` и PubAck.Stream продолжают проверяться отдельно. Смена metadata не переписывает durable binding. Потеря/recreate NATS store после ACK не покрывается этими метками и не разрешает bootstrap скрыто восстанавливать историю.

## READY manifest и reservation

Descriptor события v2: `{ordinal:u64,event_id:40 bytes,event_family:u16,event_kind:u16,record_length:u64,record_digest:Hash32,fragment_count:u64,fragment_manifest_digest:Hash32}`. Следом идут fragment_count descriptors `{fragment_index:u64,encoded_length:u64,fragment_sha256:Hash32}`. Fragment indices непрерывны; encoded_length=1…65536; сумма длин равна record_length. Family registry v1: tx=1, block=2, balance.system=3, bor.legacy_receipt=4; последние два optional. Kind: applied=1, removed=2, status=3. Unknown value — ошибка, OTHER отсутствует.

`fragment_manifest_digest=SHA256(UTF8("extractor.fragments.v1") || 0x00 || event_id40 || fragment_count_u64be || fragment_descriptors_in_index_order)`. Manifest bytes — `UTF8("extractor.manifest.v2") || 0x00 || event_count_u64be || concat(event_descriptor || its_fragment_descriptors)` в ordinal order; каждый descriptor — перечисленные поля в фиксированном порядке и big endian. Manifest digest — SHA256 этих bytes. Ordinals непрерывны 0…N−1, event IDs уникальны; empty public batch не создаётся. Entries/chunks хранятся на диске; closing marker содержит count/digest, не весь список. Никакой event со всеми descriptors не обязан помещаться в один RPC/Protobuf/WriteBatch; поток descriptors допускает chunk boundary внутри фиксированной записи.

Reservation seed binary v2: domain `extractor.reservation.v2` + zero byte, затем `reservation_id16,issuer_store_epoch16,batch_id40,manifest_digest32,event_count_u64be,sequence_base_u64be,binding_digest32,frame_limit_u64be,transport_version_u32be,layout_version_u32be,header_version_u32be,record_format_version_u32be,manifest_version_u32be`. Transport/layout/header/manifest=2, record_format=1. `plan_digest=SHA256(seed_bytes)`; seed неизменен и возвращается побайтно при Prepare retry. Record chain_sequence=base+ordinal с overflow check: один sequence на логическое событие, общий для всех его фрагментов. Gateway может импортировать прежний seed из SEALED producer после store loss; issuer epoch не переписывается на новый.

## TransportFrame layout v2

Этот небольшой transport wrapper имеет закреплённые field numbers. Он переносит один ограниченный RecordFragment из [MESSAGE_CONTRACT](MESSAGE_CONTRACT.md) целиком или частями; общего Envelope protobuf-message нет.

| Message | Fields: tag → type |
|---|---|
| NetworkIdentity | 1 family(enum EVM=1), 2 chain_id(bytes32), 3 genesis_hash(bytes32) |
| TransportFrame | 1 transport_version(u32=2), 2 network(message), 3 event_id(bytes40), 4 record_digest(bytes32), 5 publication_epoch(bytes16), 6 chain_sequence(u64), oneof: 7 complete(message) / 8 part(message), 9 fragment_index(u64), 10 fragment_count(u64), 11 fragment_digest(bytes32), 12 record_format_version(u32=1) |
| CompleteFragment | 1 fragment_bytes(bytes, 1…65536 bytes) |
| FragmentPart | 1 part_index(u64), 2 part_count(u64), 3 fragment_total_bytes(u64, 1…65536), 4 data(bytes) |

Каждый указанный scalar присутствует, включая fragment_index/part_index=0. Canonical wrapper encoding v2: ascending tag order, minimal unsigned varints, обычные protobuf length-delimited fields, no unknown fields/duplicates. Внутри — исходные immutable fragment bytes, их независимая reserialization запрещена. Это локально определённое wrapper encoding, не утверждение о каноничности произвольного Protobuf. `EncodedFrameSize` — точная сумма key/varint/length/data sizes этой таблицы, без приблизительного overhead.

`Nats-Msg-Id = "ex2." + base64url_no_padding(network_digest32 || producer_id16 || run_id16 || event_sequence8 || fragment_index8 || form1 || part_index8)`. Целые — big endian; tuple 89 bytes, ID 123 ASCII bytes с prefix. COMPLETE: form=0,part_index=0; PART: form=1,part_index=i. Header bytes ровно `NATS/1.0\r\nNats-Msg-Id: <id>\r\nNats-Expected-Stream: <stream>\r\n\r\n` в этом порядке, без status line/дополнительных headers. Reply inbox не входит в эти bytes. `F=transport.max_frame_bytes` учитывает wrapper + эти header bytes.

Layout v2 применяется отдельно к каждой паре (event ordinal, fragment_index), в этом порядке:
1. Если точный COMPLETE с фактическими metadata/headers помещается в F, выбрать COMPLETE.
2. Иначе найти наибольшее целое 1<=C<=fragment_total_bytes, для которого PART с data длиной C, part_index=U64_MAX, part_count=U64_MAX и фактическими fragment metadata помещается в F. Это консервативная проверка overhead без циклической зависимости count/size. Размер headers фиксирован по длине tuple, stream из seed binding.
3. Count=ceil(fragment_total_bytes/C), part i содержит bytes `[i*C,min((i+1)*C,fragment_total_bytes))`. Checked arithmetic для всех длин/offsets; overflow отвергается без truncate. Сборка одного RecordFragment ограничена 65536 bytes; полная запись декодируется потоком фрагментов.
4. Before SEALED проверить фактический размер/digest каждой frame. При C<1 вернуть LIMIT_INCOMPATIBLE, сохранить READY; pending SEALED не перепланировать.

Layout manifest entries: `{ordinal,fragment_index,form,capacity,part_count,fragment_total_bytes}` в фиксированном порядке, u64 big endian кроме form byte; COMPLETE capacity=fragment_total_bytes,part_count=1. `layout_manifest_digest=SHA256(domain "extractor.layout.v2" + zero + plan_digest + entry_count_u64be + entries)`. Entry count — checked сумма fragment_count всех событий; entries идут ordinal/fragment_index order. При BeginPublish Gateway сверяет детерминированный layout; неизвестная version не принимается. Control/chunk RPC<=256 KiB с wrapper, data chunk<=64 KiB; RPC byte_offset относится к одной TransportFrame, не к логической записи. Максимум WriteBatch — 1 MiB. Это ограничения рабочих буферов, не предел размера транзакции.

## Служебные запросы и ответы

После authenticated handshake mutating RPC включает `{protocol_version=2,request_id,session_token,producer_id,run_id,connection_generation,admission_generation,binding_digest}`. Token check и commit сериализованы. mTLS проверяет deployment/network/role; challenge echo внутри authenticated transport подтверждает текущий boot, дополнительный неописанный signing key не вводится. Business idempotency — identity/manifest/offset/intent, а не только request_id.

Recovery CommitSwitch/Resume использует authenticated `MAINTENANCE` session: Gateway допускает old/new binding и admission generation **только для точного сохранённого transition intent** (switch_id/resume_id, drain_id, old/new digests/generations, issuer epoch). Сначала сопоставляется durable intent/receipt, затем возвращается прежний результат; потерянный ответ после commit не превращается в неразрешимый SESSION_STALE. Если transition ещё не был принят, его начало допустимо только из текущей sealed identity по обычным правилам. MAINTENANCE не принимает новые Prepare/Publish/Seal; конфликтующий либо retired intent отвергается. После завершения перехода producer открывает новую обычную session с действующей identity. Старый session_token не переносится через рестарт или соединение.

При потере Gateway store `BeginPublish` для producer SEALED сначала открывает `IMPORT_SEALED`: exact исходные seed/digests плюс повтор исходного READY manifest через `PrepareChunk`/`FinalizePrepare` с mode=IMPORT_SEALED. Chunk offset/checksum/idempotency те же, но **новые reservation/sequence не выделяются**; issuer epoch сохраняется из исходного seed. До проверки полного manifest digest, ordinal/event identities, routing kind, lengths и детерминированного layout state остаётся неготовым к CLOSED. Затем PublishChunk передаёт исходные frames; recovered manifest и bytes фиксируются по обычному atomic IngestAck правилу. Другой manifest/seed для того же batch — IMMUTABLE_CONFLICT. Эта ветка допускается только при доказанном fresh-store recovery по DELIVERY_STATE_MACHINE; не обходит fencing либо cutover.

| RPC | Дополнительные request fields | Успешный response |
|---|---|---|
| HelloChallenge | producer/run, role, network | boot_nonce, challenge |
| OpenSession | echoed challenge, mode, stored binding, desired binding digest, durable retired_prefix, admission/connection generation, имеющиеся seal/switch/resume evidence | session_token, Gateway binding/generation/prefix/state; recovery action из NONE/REPLAY/FINISH_RELEASE/FINISH_SWITCH/FINISH_RESUME |
| BeginPrepare | batch_id, manifest_digest, event_count, descriptor_bytes_total | resume_offset либо exact saved reservation |
| PrepareChunk | batch_id, manifest_digest, offset, chunk_sha256, bytes | durable next_offset |
| FinalizePrepare | batch_id, manifest_digest | exact seed bytes, plan_digest |
| BeginPublish | batch_id, seed bytes, plan_digest, layout_manifest_digest | saved state/resume cursors |
| PublishChunk | batch_id, plan_digest, event ordinal, fragment_index, frame index (0 для COMPLETE, part_index для PART), offset, chunk_sha256, bytes | durable next_offset для той же пары fragment/frame |
| FinalizePublish | batch_id, plan_digest, manifest_digest, layout_manifest_digest | CLOSED; не IngestAck |
| GetBatchState | batch_id, plan_digest, optional cursor | PREPARED/RECEIVING/CLOSED/NATS_ACKED/RELEASING/RETIRED либо NOT_FOUND; IngestAck только если durable; bounded page cursors (event ordinal, fragment_index, frame index, next_offset), continuation при превышении RPC limit |
| Release | batch_id, manifest_digest, plan_digest | ReleaseAck(batch_id,retired_prefix) |
| RecoveryRelease | batch_id, exact saved IngestAck, durable ACKED/outbox assertion | ReleaseAck после sync, только следующий seq и отсутствие conflicting pending |
| SealGeneration | binding/admission generation, drain_id, batch_highwater | SealAck(drain_id,binding_digest,admission_generation,highwater,issuer_store_epoch) |
| PrepareSwitch | old SealAck, old binding digest, desired descriptor, switch_id | SwitchPrepared(switch_id,old/new digest,drain_id,admission_generation,issuer_store_epoch) |
| CommitSwitch | switch_id, exact SwitchPrepared, producer durable-commit assertion | SwitchCommitted с той же identity |
| Resume | old SealAck, resume_id, active binding digest, next admission generation | ResumeAck(resume_id,binding_digest,old/new admission generation,drain_id,issuer_store_epoch) |

`IngestAck={batch_id,manifest_digest,plan_digest,layout_manifest_digest,binding_digest,issuer_store_epoch}`. Producer сохраняет **весь ACK** в ACKED/outbox до cleanup, чтобы RecoveryRelease не требовал утраченного payload. Seal/Switch/Resume ответы связывают весь intent; совпадения одного номера недостаточно. Handshake/control evidence имеет fixed versioned schema, не JSON opaque map. Конкретные generated RPC tags — E02; значения/validation/порядок commit заданы здесь.

Chunk retries: одинаковые offset/bytes/digest возвращают тот же next_offset; конфликт записанного диапазона — IMMUTABLE_CONFLICT; пропуск offset — OFFSET_MISMATCH с ожидаемым cursor. Закрытие manifest/batch возможно только после проверки всех contiguous chunks. GET/ChunkAck не является разрешением удалить producer payload.

## Ошибки протокола

Ответ error: `{code,request_id,owner_state,field_path?,retry_action,detail_redacted}`. Code registry v1: TRANSIENT_UNAVAILABLE, SESSION_STALE, OFFSET_MISMATCH, IDENTITY_MISMATCH, BINDING_MISMATCH, MODE_CONFLICT, DRAIN_REQUIRED, GENERATION_CONFLICT, IMMUTABLE_CONFLICT, INVALID_DESCRIPTOR, INVALID_PAYLOAD, LIMIT_INCOMPATIBLE, VERSION_UNSUPPORTED, ALREADY_RETIRED, STORE_FATAL. Retry action: RETRY_BACKOFF / RECONNECT / RESUME_AT_OFFSET / FIX_CONFIG / DRAIN_OLD_BUILD / STOP_LOCAL_OWNER / DIAGNOSE_IMMUTABLE.

ALREADY_RETIRED допустим только для producer ACKED/outbox/retired, не подтверждает unACKed batch. CONFIG_BLOCKED/invalid pending не меняет bytes и не удаляет batch. STORE_FATAL прекращает соответствующего owner по D57. Неизвестный code/version обрабатывается как protocol incompatibility с сохранением данных; никакого fallback success. gRPC status является transport categorization, не заменяет typed error и durable ACK.

## Управляемый restart, drain, cutover

1. Stop LIVE и подтвердить отсутствие старого writer. С прежним binding запустить ту же совместимую native сборку `--extractor-mode=drain`.
2. Закрыть сохранённые **native** obligations, способные создать delivery batch; доставить/очистить все batches и ordered Release. Только затем сохранить drain_id/highwater и выполнить SealGeneration.
3. Sync DRAINED+SealAck; native процесс завершается. Для обычного resume оставить desired binding прежним. Для cutover подготовить следующий descriptor, новый stream окружением и обновить `binding_file` обоих owners; Gateway stream/prefix согласовать.
4. Gateway с отличающимся desired binding открывается в AWAIT_SWITCH: старый binding остаётся active durable, разрешены только recovery/maintenance handshake, новые публикации в desired route запрещены. Наличие файла нового descriptor не активирует его.
5. Запустить native `--extractor-mode=live`. Из DRAINED store получить old binding/seal/drain_id, sync SwitchIntent, пройти PrepareSwitch → producer durable binding commit → CommitSwitch. Никаких old_binding/drain_id YAML keys нет.
6. При прежнем binding Switch пропускается. В обоих случаях durable ResumeIntent/ResumeAck открывают следующую admission generation; только после producer LIVE commit и нового H разрешён capture. Crash продолжает тот же intent, не увеличивает generation снова.

Если desired config меняется ещё раз во время switch, сначала требуется вернуть config к сохранённому intent и закончить его; новый switch не перепрыгивает незавершённый. Gateway store loss восстанавливает identity/prefix/seal/switch evidence от surviving producer; insufficient evidence блокирует переход. Producer loss не восстанавливает старый run из Gateway: новый run и явная диагностика оставшихся obligations по принятой политике потери диска. Forced discard/migration не добавляются.

## Проверки

Обязательные граничные случаи RPC/CUTOVER: потерянный CommitSwitch/Resume ответ после смены session identity; recovery через MAINTENANCE без приёма data RPC; fresh-store IMPORT_SEALED с исходным chunked manifest; descriptor 16 KiB плюс полный wrapper, превышение bound отвергается config check.

WIRE-01: binding digest не зависит от JSON formatting, меняется при любом identity поле; duplicate keys/unknown fields/overflow отвергаются. ID-01: restart/attempt/decision replay сохраняет semantic identity и не создаёт второй batch. LAYOUT-01: COMPLETE на границе F, PART на 1 byte больше, большие varints/counts, C<1, override limit и exact headers. RPC-01: chunks/retries/errors, lost replies и saved ACK. CUTOVER-01: native pending перед seal, AWAIT_SWITCH, crash каждого switch/resume commit, desired config drift, store loss. Все fixtures будущие; этот документ не объявляет PASS.
