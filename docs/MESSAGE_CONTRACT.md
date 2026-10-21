# Общий контракт сообщений до NATS.IO

> D42 (2026-10-21): текущий этап — Ethereum Mainnet, BNB Smart Chain (BSC) Mainnet и Polygon PoS Mainnet. Base/Arbitrum и их адаптеры/fixtures отложены; исследования и типизированные расширения ниже сохранены справочно, не блокируют текущую версию. Исторические сценарии отложены D38. Действующие live-типы транзакций ETH/BSC/Polygon, включая native system types, остаются обязательными. У каждой сети собственные WAL и журнал Gateway.

E00-14, обновлено 2026-10-16 по D23–D25. Проект логического контракта; определения ниже заменяют старые proto/Any-заготовки §13–14 v3. Принятые границы D16–D20 сохраняются. Формат реализации — Protobuf с закрытыми `oneof` и versioned validators; компиляция, bindings и fixtures относятся к E02. Эта работа не вводит последующие хранилища или обработчики.

D23–D25 отменяют source inventory verifier, BlockComplete/CoverageUpdated и согласование live/history overlap. Действующая граница: [получение → WAL → NATS ACK](LIVE_HISTORY_DELIVERY.md). Typed metadata и детали сохраняются; доставка не является доказательством полноты блока.

## 1. Типы и присутствие

| Тип | Инвариант |
|---|---|
| Hash32 / Address20 | Ровно 32 / 20 bytes; не произвольные строки |
| UInt256 | Ровно 32 bytes, unsigned big endian; арифметика без float |
| Delta256 | знак плюс UInt256 magnitude; отрицательный ноль запрещён |
| Timestamp | seconds:u64 + nanos:u32 < 1e9 + precision=`SECONDS | MILLISECONDS | NANOSECONDS`; сохраняется доступная source precision |
| NetworkIdentity | family=`EVM`, chain_id:UInt256, genesis_hash:Hash32; chain_key — только alias для маршрутизации |
| ProfileRef | network profile, client commit, adapter version, fork rules version, normalization version; зарегистрированный профиль |
| FieldAvailability | `VALUE(T) | NOT_APPLICABLE | UNAVAILABLE(reason_code)`; применяется только к полям с определённой семантикой отсутствия |

Отсутствующий scalar и корректный ноль различаются. Proto scalar с обязательным присутствием использует explicit presence и validator. Для `oneof` требуется ровно один вариант; неизвестные enum values, profile/type/schema versions не публикуются как нормализованные данные. Empty list означает, что источник вернул пустой применимый набор; известная ошибка получения не маскируется пустым списком. Независимый verifier полноты не требуется. `UNAVAILABLE` у обязательных transaction details означает ошибку обработки; неприменимость доказывается профилем.

Произвольные JSON, `Any`, map<string,bytes> не являются обходом типов. Bytes разрешены для hashes/addresses, calldata, code, logs data, source encoding и сериализованного транспортного payload. Каждое сетевое расширение имеет конкретную схему и version gate.

Основание: [Protobuf field presence](https://protobuf.dev/programming-guides/field_presence/). Protobuf по умолчанию не проверяет эти доменные ограничения; нужен validator E02.

## 2. Envelope и идентичность

`Envelope={schema_version, network, profile_ref, event_id, producer_id, run_id, source, capture_mode, payload}`.

`source={node_instance_id, writer_epoch, source_sequence}`. `capture_mode=LIVE|SYNC|BACKFILL`. Эти поля — provenance, не идентичность транзакции. `payload` — закрытый union из таблицы событий ниже. Версия wire schema не обязана совпадать с номером редакции Markdown v3.

`BlockRef={number:u64,hash:Hash32,parent_hash:Hash32,timestamp:Timestamp}`. `InclusionRef={network,block_hash,native_origin,native_position,transaction_hash}`. Для native body transaction native_position — индекс в body, native_origin=`BLOCK_BODY`. Для `ARBITRUM_CLASSIC_AVM` позиция включает native AVM log/result index, а source transaction_hash имеет kind=NATIVE_REQUEST_ID; optional Ethereum projection index хранится отдельно. Правила и ограничения — [Classic profile](research/ARBITRUM_CLASSIC_CAPTURE.md). Нельзя идентифицировать включение одним tx hash либо считать Classic projection единственным inventory.

`ActivationRef={inclusion_ref,producer_id,run_id,activation_id}`. Для блока аналогично используется `BlockActivationRef={network,block_hash,producer_id,run_id,activation_id}`. `event_id` обозначает неизменяемое событие; повтор передачи сохраняет ID и исходные байты. Разные события lifecycle имеют разные event_id. event_id=(producer_id,run_id,event_sequence); persistent counters и publication plan определены в [протоколе .06](ROCKSDB_DELIVERY_PROTOCOL.md); вероятностная генерация нового ID на каждый retry запрещена.

## 3. TransactionRecord

| Поле | Состав и правило |
|---|---|
| inclusion, block_activation | Связь с конкретной веткой и активацией блока |
| native_payload | Ровно один вариант из [реестра типов](TRANSACTION_TYPE_REGISTRY.md); native type проверяется с сетью/fork |
| source_hash | Значение native клиента; hash_kind=`NATIVE_TRANSACTION | IMPORTED_OVERRIDE | NATIVE_REQUEST_ID`; Bor synthetic receipt имеет отдельный auxiliary origin |
| raw_encodings | Непустой список `{kind,bytes}`: `EVM_LEGACY_RLP | EIP2718_ENVELOPE | ARB_NATIVE_ENVELOPE | ARB_TRIE_VALUE | BOR_STATE_SYNC_ENVELOPE | CLASSIC_REQUEST_BYTES | CLASSIC_AVM_VALUE`; обязательный набор определяется вариантом |
| execution_projection | Typed effective sender/target/value/input и provenance=`ENCODED | NATIVE_DERIVED | NOT_APPLICABLE`; исходные native поля не перезаписываются |
| receipt | Typed outcome, fees/gas, committed logs и применимые network receipt details |
| calls | Упорядоченные execution frames, включая failed/reverted calls |
| actions | Причинные native balance actions, включая fee/refund/mint/burn и компенсирующие действия |
| state_changes | Итоговый transaction-boundary diff затронутых accounts/storage после rollback, включая реально сохранённые fees/nonce |
| execution_position | operation_index:u64 и execution BlockRef; при отличии от inclusion block связь задаёт профиль |
| capture_status | Применимость receipt/calls/actions/state_changes/network details; известная ошибка сбора даёт ERROR, независимой сверки состава блока нет |

`ExecutionProjection` — отдельный union `EvmMessage | NativeProtocolExecution`; последнее задаёт typed operation и адреса только если они имеют смысл. Execution sender нельзя заполнять нулём потому, что так отвечает универсальный accessor системной транзакции.

`Receipt`: native outcome=`Status(success:bool) | PreStatusStateRoot(Hash32)`; execution result=`SUCCESS | REVERT | FAILURE`; cumulative_gas_used и gas_used по применимости; effective_gas_price, blob_gas_used, blob_gas_price, created_contract с явным присутствием; logs, logs_bloom, source_receipt_encoding с encoding kind. Pre-Byzantium source root не превращается в RPC status: результат берётся из native execution. Network details — конкретные варианты, включая BaseDepositReceipt; точный набор Nitro/Bor extensions закрывается fixtures E02.

`Log={address,topics:Hash32[],data,transaction_log_index,block_log_index}`. Receipt содержит только committed logs. Если сохраняются reverted log attempts, это отдельные execution actions с effects_committed=false, не receipt logs.

`CallFrame={frame_id,parent_id?,execution_order,depth,engine,kind,context_address?,code_address?,msg_sender?,target?,value?,input,output,gas?,gas_used?,outcome,effects_committed,error_code?,revert_data?,created_address?}`. kind — закрытый набор CALL/CALLCODE/DELEGATECALL/STATICCALL/CREATE/CREATE2/STYLUS_HOST_CALL/NATIVE_PROTOCOL; runtime-specific дополнительные поля требуют registered variant. `execution_order` отражает источник; сортировать только по depth нельзя. Успешный child внутри reverted ancestor имеет effects_committed=false.

`BalanceAction={action_index,cause,engine,kind,from?,to?,amount,asset=NATIVE,effects_committed,rollback_scope?}`. kind: TRANSFER/MINT/BURN/FEE/REFUND/SELFDESTRUCT_TRANSFER; cause=`Frame(frame_id) | TransactionProtocol(registered_operation)`. Точный перечень network protocol operations — E02-05, до этого неизвестный cause не превращается в OTHER. Fee attribution сохраняет плательщика/получателя или burn, не предполагает обычную EVM-пару адресов для всех сетей. Gas refund units и native monetary refund различаются. +10 и −10 остаются двумя actions при нулевом diff.

`AccountChange={address,existed_before,exists_after,balance_before,balance_after,nonce_before,nonce_after,code_before,code_after,storage_reset,storage_changes[]}`. `StorageChange={key:Hash32,before:Hash32,after:Hash32}`. Данные относятся к transaction boundary, не к последнему child frame. Code и storage reset семантика проверяются по fork, включая SELFDESTRUCT. Balance actions и итоговый diff — разные представления одного исполнения; их нельзя складывать друг с другом как независимые изменения.

## 4. События и передача блока

| Payload | Смысл |
|---|---|
| TransactionApplied | Нормализованное native включение, producer/run и его наблюдаемая активация |
| TransactionRemoved / TransactionStatusChanged | Native reorg/finality observations; ссылки на активацию данного producer |
| BlockApplied | Native BlockRef/header metadata, producer/run, source transaction count при наличии; пустой блок также публикуется |
| BlockRemoved / BlockStatusChanged | Native lifecycle evidence данного producer/run |
| SystemBalanceApplied/Removed/StatusChanged | Optional вне-транзакционный scope D17 |
| BorLegacyReceiptApplied/Removed/StatusChanged | Optional typed auxiliary source receipt; не body transaction |

BlockComplete и CoverageUpdated исключены D25. Source metadata не является результатом собственного пересчёта/сверки хеша или полного inventory. Для Classic сохраняются native AVM result identity и отдельная Ethereum projection; корректный native decoder обязателен, но production completeness verifier не добавляется.

Внутренний закрытый delivery batch содержит references фактически подготовленных сообщений блока. После durable сохранения batch/messages — WAL_SAVED; после PubAck всех этих сообщений/частей — NATS_ACKED. Это локальный учёт передачи, не утверждение, что источником выданы все данные блокчейна. Отдельное публичное событие прогресса/coverage и UI не требуются.

Optional BorLegacyReceiptRecord описан в [POLYGON_LEGACY_CAPTURE.md](research/POLYGON_LEGACY_CAPTURE.md): source receipt/hash/index/encoding, без фиктивной transaction envelope. Его source-only ограничения сохраняются, auxiliary completeness verifier не вводится.

## 5. Lifecycle, finality и поздняя metadata

| Текущее состояние | Событие | Результат |
|---|---|---|
| ABSENT | Applied(A) | ACTIVE(A) |
| ACTIVE(A) | retry Applied(A), те же bytes | Тот же факт, не новое включение |
| ACTIVE(A) | StatusChanged(A, revision выше) | ACTIVE(A), новое evidence |
| ACTIVE(A) | Removed(A) | REMOVED(A), терминальное для A |
| REMOVED(A) | поздний StatusChanged/Applied(A) | Не реактивирует A |
| REMOVED(A) | возвращение того же блока | Новый Applied(B), B≠A |

Пересечение live/history может создать разные наблюдения и активации одного включения в разных producer/run. Общий registry и подавление повторов не требуются. При доставке Removed раньше Applied ссылка сохраняет смысл отмены; поздний Applied не оживляет удалённую активацию. Revision монотонна внутри объекта lifecycle данного producer/run, не между независимыми загрузками или сетями. Порядок history относительно live не определяет состояние цепочки.

Finality — typed evidence, а не свободная строка и не универсальный confirmations counter. Варианты: Ethereum consensus safe/finalized; BSC native finality evidence; Base L2 safe/finalized с L1 block reference; Arbitrum L1 inclusion/settlement evidence; Polygon Heimdall/checkpoint evidence. Эти стадии не объявляются эквивалентными. Конкретные структуры evidence и availability guards требуют E02 fixtures для pinned clients.

Поздняя finality передаётся StatusChanged. Отсутствующие обязательные execution details не разрешается выдавать как полную транзакцию с обещанием дослать. Если потребуется изменяемая non-finality metadata, до реализации вводится отдельный typed event/version с fixture; свободного patch JSON нет. Fork-specific L1/batch сведения, недоступные на данном этапе, имеют явно определённый pending статус, не нулевое выдуманное значение.

## 6. Транспорт и порядок

`TransportFrame={transport_version,network,event_id,payload_digest,publication_epoch,chain_sequence,payload}`.

payload — ровно `CompleteRecord{envelope_bytes}` либо `RecordPart{part_index,part_count,total_bytes,data}`. envelope_bytes — bytes конкретной versioned Envelope, прошедшей validator; это транспортное кодирование, не разрешение opaque native payload. Маленькая запись передаётся одним CompleteRecord. Большая — частями с индексами 0..count−1. Каждая часть содержит одинаковые event_id/digest/count/total_bytes. Смешивать complete и parts в одном retry события нельзя.

SHA-256 вычисляется по исходным сериализованным Envelope bytes. Они фиксируются один раз до передачи и повторяются побайтно. Part layout также фиксируется для retry. Digest проверяет транспорт; он не является универсальным tx hash или semantic event ID. [Protobuf serialization is not canonical](https://protobuf.dev/programming-guides/serialization-not-canonical/): повторная сериализация другим runtime/version не обязана дать те же bytes.

Лимит учитывает полную сериализованную TransportFrame и NATS headers, а не только data. Он не превышает настроенные server/stream limits. Конкретный max frame size задаётся transport/deployment конфигом и валидируется до отправки; pending spool без TTL/прикладной квоты по D28. Post-ACK retention вне scope. При невозможности разместить запись — backpressure/явная ошибка и незавершённый блок; обрезать данные запрещено.

Gateway назначает chain_sequence в пределах publication_epoch своего store; все части имеют одну пару epoch/sequence. Epoch сохраняется при restart с диском и меняется при создании нового store. Publication plan и точные frame bytes сохраняются producer до первой публикации; после потери Gateway store replay использует прежнюю пару и bytes. Это порядок регистрации в одном epoch, не block chronology, не NATS stream sequence и не глобальный порядок через потерю диска. Части history/live могут чередоваться. Протокол Prepare/Publish/IngestAck/Release: [ROCKSDB_DELIVERY_PROTOCOL](ROCKSDB_DELIVERY_PROTOCOL.md).

Execution chronology задают block parent links, native transaction position, operation_index и action/frame order. Delivery chronology задают chain_sequence и transport metadata. `part_index` задаёт только сборку bytes. Нельзя сортировкой chain_sequence восстанавливать blockchain chronology.

NATS message ID должен стабильно различать `(network,event_id,complete-or-part-index)`. После PubAck всех сообщений/частей закрытого block batch Gateway sync фиксирует NATS_ACKED и подтверждает adapter batch_id/digest/plan identity. ACK одного event не разрешает очистить весь блок. NATS deduplication window не обеспечивает вечный exactly-once: контракт допускает повторы с тем же ID/bytes. ACK не подтверждает работу downstream consumers. Durable plan/WAL, повторный запуск, потеря состояния и block-batch IngestAck/Release определены в ROCKSDB_DELIVERY_PROTOCOL.

## 7. Маршрутизация

Для каждой сети отдельные streams/subjects/limits. Сохраняется рабочий namespace ТЗ: `evm.v3.<chain_key>.<event_family>.<event_kind>`, где event_family=`tx|block|balance.system`. Event kind задаётся закрытым реестром: tx applied/removed/status; block applied/removed/status; optional balance.system applied/removed/status. Transport version/schema version находятся внутри frame; изменение Markdown v3 само по себе не меняет subject.

Точные stream configuration и правила миграции namespace закрепляются в E03. Полная запись и её части направляются в один subject своего logical event; identity сети из frame проверяется против route. Настройка stream topology/retention не входит в E00-14.

## 8. Проверки для последующих этапов

| Fixture | Обязательный результат |
|---|---|
| Failed tx + успешный child в reverted parent | Gas/nonce сохранены по native правилам; child actions не committed |
| Компенсирующие transfers | Оба actions есть, net diff может быть 0 |
| Base deposit | Нет искусственной подписи/nonce; mint и deposit receipt fields сохранены |
| BSC Parlia system | Фактический wire type/signature не потеряны; native Finalize capture проверен |
| Nitro retryable / imported legacy | Native payload отделён от projection; HashOverride и разные encodings проверены |
| Nitro EVM ↔ Stylus | Frame engines и native actions без потери меж-runtime причинности |
| Classic native result ↔ Ethereum projection | Все source results учтены; CL-01…08 проверяют archive, декодирование, observer и неизменность native исполнения |
| Bor StateSync / legacy synthetic | Body 0x7f и optional legacy source receipt не смешаны; fixtures POLY-L01…07 из исследования E00-15 |
| Пустой блок / ошибка нормализации | Metadata пустого блока передаётся; известная ошибка даёт ERROR, а не NATS_ACKED |
| Parts reorder/duplicates/corruption | Сборка по индексу, повторы идемпотентны, конфликт bytes/digest отклоняется |
| Live/history overlap и reorg во время публикации | Дубли разных runs допустимы; lifecycle references не смешиваются, ACK учитывает все части |
| Неизвестный тип/версия/fork | Явная ошибка нормализации, без фиктивного поддержанного payload |
| Optional balances выключены | Передаётся заявленный transaction scope; полнота всех балансов не заявляется |

Это acceptance matrix будущих E01/E02/E03, не отчёт о пройденных тестах. E00-15 определил Classic/legacy source profiles и ограничения; их runtime-покрытие и архивы ещё не доказаны. Открыты точные network execution/finality extensions, Classic all-epoch decoders и validators (E02), измерение ресурсных параметров/реализация NATS delivery (E03), история/YAML control (JOB-01, отложен пользователем).
