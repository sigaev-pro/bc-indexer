# Общий контракт сообщений до NATS.IO

E00-14, 2026-10-15. Проект логического контракта; определения ниже заменяют старые proto/Any-заготовки §13–14 v3. Принятые границы D16–D20 сохраняются. Формат реализации — Protobuf с закрытыми `oneof` и versioned validators; компиляция, bindings и fixtures относятся к E02. Эта работа не вводит последующие хранилища или обработчики.

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

Отсутствующий scalar и корректный ноль различаются. Proto scalar с обязательным присутствием использует explicit presence и validator. Для `oneof` требуется ровно один вариант; неизвестные enum values, profile/type/schema versions не публикуются как нормализованные данные. Empty list допустим только при подтверждённой полноте соответствующего capture domain. `UNAVAILABLE` у обязательных transaction details блокирует complete; неприменимость доказывается профилем.

Произвольные JSON, `Any`, map<string,bytes> не являются обходом типов. Bytes разрешены для hashes/addresses, calldata, code, logs data, source encoding и сериализованного транспортного payload. Каждое сетевое расширение имеет конкретную схему и version gate.

Основание: [Protobuf field presence](https://protobuf.dev/programming-guides/field_presence/). Protobuf по умолчанию не проверяет эти доменные ограничения; нужен validator E02.

## 2. Envelope и идентичность

`Envelope={schema_version, network, profile_ref, event_id, source, capture_mode, payload}`.

`source={node_instance_id, writer_epoch, source_sequence}`. `capture_mode=LIVE|SYNC|BACKFILL`. Эти поля — provenance, не идентичность транзакции. `payload` — закрытый union из таблицы событий ниже. Версия wire schema не обязана совпадать с номером редакции Markdown v3.

`BlockRef={number:u64,hash:Hash32,parent_hash:Hash32,timestamp:Timestamp}`. `InclusionRef={network,block_hash,native_origin,native_position,transaction_hash}`. Для native body transaction native_position — индекс в body, native_origin=`BLOCK_BODY`. Для `ARBITRUM_CLASSIC_AVM` позиция включает native AVM log/result index, а source transaction_hash имеет kind=NATIVE_REQUEST_ID; optional Ethereum projection index хранится отдельно. Правила и ограничения — [Classic profile](research/ARBITRUM_CLASSIC_CAPTURE.md). Нельзя идентифицировать включение одним tx hash либо считать Classic projection единственным inventory.

`ActivationRef={inclusion_ref,activation_id}`. Для блока аналогично используется `BlockActivationRef={network,block_hash,activation_id}`. `event_id` обозначает неизменяемое событие; повтор передачи сохраняет ID и исходные байты. Разные события lifecycle имеют разные event_id. Формат ID, сохранение mapping и восстановление без локального состояния окончательно закрепляются в REC; вероятностная генерация нового ID на каждый retry запрещена.

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
| capture_coverage | Применимость/полнота receipt,calls,actions,state_changes,network details; отсутствие обязательного domain запрещает complete |

`ExecutionProjection` — отдельный union `EvmMessage | NativeProtocolExecution`; последнее задаёт typed operation и адреса только если они имеют смысл. Execution sender нельзя заполнять нулём потому, что так отвечает универсальный accessor системной транзакции.

`Receipt`: native outcome=`Status(success:bool) | PreStatusStateRoot(Hash32)`; execution result=`SUCCESS | REVERT | FAILURE`; cumulative_gas_used и gas_used по применимости; effective_gas_price, blob_gas_used, blob_gas_price, created_contract с явным присутствием; logs, logs_bloom, source_receipt_encoding с encoding kind. Pre-Byzantium source root не превращается в RPC status: результат берётся из native execution. Network details — конкретные варианты, включая BaseDepositReceipt; точный набор Nitro/Bor extensions закрывается fixtures E02.

`Log={address,topics:Hash32[],data,transaction_log_index,block_log_index}`. Receipt содержит только committed logs. Если сохраняются reverted log attempts, это отдельные execution actions с effects_committed=false, не receipt logs.

`CallFrame={frame_id,parent_id?,execution_order,depth,engine,kind,context_address?,code_address?,msg_sender?,target?,value?,input,output,gas?,gas_used?,outcome,effects_committed,error_code?,revert_data?,created_address?}`. kind — закрытый набор CALL/CALLCODE/DELEGATECALL/STATICCALL/CREATE/CREATE2/STYLUS_HOST_CALL/NATIVE_PROTOCOL; runtime-specific дополнительные поля требуют registered variant. `execution_order` отражает источник; сортировать только по depth нельзя. Успешный child внутри reverted ancestor имеет effects_committed=false.

`BalanceAction={action_index,cause,engine,kind,from?,to?,amount,asset=NATIVE,effects_committed,rollback_scope?}`. kind: TRANSFER/MINT/BURN/FEE/REFUND/SELFDESTRUCT_TRANSFER; cause=`Frame(frame_id) | TransactionProtocol(registered_operation)`. Точный перечень network protocol operations — E02-05, до этого неизвестный cause не превращается в OTHER. Fee attribution сохраняет плательщика/получателя или burn, не предполагает обычную EVM-пару адресов для всех сетей. Gas refund units и native monetary refund различаются. +10 и −10 остаются двумя actions при нулевом diff.

`AccountChange={address,existed_before,exists_after,balance_before,balance_after,nonce_before,nonce_after,code_before,code_after,storage_reset,storage_changes[]}`. `StorageChange={key:Hash32,before:Hash32,after:Hash32}`. Данные относятся к transaction boundary, не к последнему child frame. Code и storage reset семантика проверяются по fork, включая SELFDESTRUCT. Balance actions и итоговый diff — разные представления одного исполнения; их нельзя складывать друг с другом как независимые изменения.

## 4. События и полнота блока

| Payload | Обязательные данные и смысл |
|---|---|
| TransactionApplied | activation_ref, block_activation, record; исходное включение и все обязательные детали |
| TransactionRemoved | activation_ref, revision, reorg_id, reason=`REORG`; отменяет конкретную активацию |
| TransactionStatusChanged | activation_ref, revision, typed finality evidence; не создаёт новую активацию |
| BlockApplied | block_activation, BlockRef, native transaction inventory, profile_ref, scope; metadata канонического блока, включая пустой |
| BlockComplete | block_activation, revision, source inventory digest, transaction manifest, capture scope; все обязательные записи опубликованы |
| BlockRemoved | block_activation, revision, reorg_id; отменяет утверждение о каноничности и полноте этой активации |
| BlockStatusChanged | block_activation, revision, typed finality evidence |
| CoverageUpdated | coverage_id, revision, диапазон [from,to], branch anchors, status, scope, reference на block manifests |
| SystemBalanceApplied/Removed/StatusChanged | Optional отдельный typed record/activation/lifecycle вне транзакций; без выдуманного tx hash |
| BorLegacyReceiptApplied/Removed/StatusChanged | Только при включении optional legacy расширения: typed auxiliary source receipt, отдельная identity и lifecycle; не TransactionRecord |

BlockApplied не является событием начала обработки: это опубликованная metadata уже нативно исполненного канонического блока. Для каждого body item inventory хранит позицию, source hash и native type. BlockComplete manifest связывает inventory item с transaction activation, applied event_id и digest исходных bytes. Отдельные legacy records при их поддержке имеют отдельный inventory; не увеличивают body transaction count.

Для Classic обязательный source inventory строится по persisted AVM TxResult/BlockInfo range с явной классификацией результатов. Ethereum body — дополнительная projection; её count/index могут отличаться. Неизвестный или не преобразуемый result нельзя отбрасывать по поведению FilterEthTxResults. Native control records проверяются отдельно и не становятся искусственными транзакциями. Classic receipt/native outcome использует зарегистрированный ClassicResultCode вместе с общей execution classification; projection status не заменяет этот enum. Exact mapping и all-epoch validators — E02, engine=`CLASSIC_AVM`.

Условие complete: inventory нативного блока и захваченные inclusion совпадают точно; все обязательные capture domains полны; все части соответствующих Applied получили PubAck. Одна сверка количества недостаточна. Затем публикуется сам BlockComplete; только его PubAck завершает публикацию блока. Пустой блок имеет пустой manifest и корректный BlockComplete. Большой inventory/manifest разбивается транспортом по тем же правилам, что transaction record.

`CoverageStatus=COMPLETE|GAP|REVOKED`. Scope явно содержит транзакционные domains и `outside_transaction_balances=COLLECTED|NOT_COLLECTED`; COMPLETE относится только к объявленному scope. GAP имеет диапазон, branch anchor, registered reason и доступные детали, но не заменяет потерянные транзакции. COMPLETE требует непрерывной parent-linked последовательности BlockComplete без дыр. После reorg старое покрытие отзывается, новое утверждается только для новой ветки; stale revision не восстанавливает старую ветку. Локальный progress cursor не равен публикации coverage.

Optional SystemBalanceRecord использует operation_index, typed cause, balance actions/diffs и отдельную активацию. Его детальная схема не является gate текущего обязательного transaction scope. Если расширение выключено, нельзя заявлять полную историю всех нативных балансов.

E00-15 определяет `BorLegacyReceiptRecord` в [POLYGON_LEGACY_CAPTURE.md](research/POLYGON_LEGACY_CAPTURE.md): origin `BOR_LEGACY_SYNTHETIC_RECEIPT`, derived source receipt hash, synthetic receipt index, typed receipt/logs и `BOR_RECEIPT_STORAGE_RLP`. Scope — SOURCE_RECEIPT_ONLY. При включении такого optional record auxiliary inventory/coverage отделяется от body transactions и вне-транзакционных balance actions. Native success status не доказывает успех всех вызовов, а missing receipt не означает отсутствие действий. Wire fields и subjects расширения фиксируются только при его включении; наличие этой схемы не является решением пользователя включить его в первую поставку.

## 5. Lifecycle, finality и поздняя metadata

| Текущее состояние | Событие | Результат |
|---|---|---|
| ABSENT | Applied(A) | ACTIVE(A) |
| ACTIVE(A) | retry Applied(A), те же bytes | Тот же факт, не новое включение |
| ACTIVE(A) | StatusChanged(A, revision выше) | ACTIVE(A), новое evidence |
| ACTIVE(A) | Removed(A) | REMOVED(A), терминальное для A |
| REMOVED(A) | поздний StatusChanged/Applied(A) | Не реактивирует A |
| REMOVED(A) | возвращение того же блока | Новый Applied(B), B≠A |

Пересечение live/history одного канонического включения использует существующую активацию. При доставке Removed раньше Applied ссылка сохраняет смысл отмены; поздний Applied не оживляет удалённую активацию. Revision монотонна внутри объекта lifecycle, не между сетями. Порядок history относительно live не определяет состояние цепочки.

Finality — typed evidence, а не свободная строка и не универсальный confirmations counter. Варианты: Ethereum consensus safe/finalized; BSC native finality evidence; Base L2 safe/finalized с L1 block reference; Arbitrum L1 inclusion/settlement evidence; Polygon Heimdall/checkpoint evidence. Эти стадии не объявляются эквивалентными. Конкретные структуры evidence и availability guards требуют E02 fixtures для pinned clients.

Поздняя finality передаётся StatusChanged. Отсутствующие обязательные execution details не разрешается выдавать как полную транзакцию с обещанием дослать. Если потребуется изменяемая non-finality metadata, до реализации вводится отдельный typed event/version с fixture; свободного patch JSON нет. Fork-specific L1/batch сведения, недоступные на данном этапе, имеют явно определённый pending статус, не нулевое выдуманное значение.

## 6. Транспорт и порядок

`TransportFrame={transport_version,network,event_id,payload_digest,chain_sequence,payload}`.

payload — ровно `CompleteRecord{envelope_bytes}` либо `RecordPart{part_index,part_count,total_bytes,data}`. envelope_bytes — bytes конкретной versioned Envelope, прошедшей validator; это транспортное кодирование, не разрешение opaque native payload. Маленькая запись передаётся одним CompleteRecord. Большая — частями с индексами 0..count−1. Каждая часть содержит одинаковые event_id/digest/count/total_bytes. Смешивать complete и parts в одном retry события нельзя.

SHA-256 вычисляется по исходным сериализованным Envelope bytes. Они фиксируются один раз до передачи и повторяются побайтно. Part layout также фиксируется для retry. Digest проверяет транспорт; он не является универсальным tx hash или semantic event ID. [Protobuf serialization is not canonical](https://protobuf.dev/programming-guides/serialization-not-canonical/): повторная сериализация другим runtime/version не обязана дать те же bytes.

Лимит учитывает полную сериализованную TransportFrame и NATS headers, а не только data. Он не превышает настроенные server/stream limits. Конкретные значения, retention и spool limits относятся к эксплуатационному контракту/REC; они пока не утверждены. При невозможности разместить запись — backpressure/явная ошибка и незавершённый блок; обрезать данные запрещено.

Gateway назначает chain_sequence при регистрации публикации события; все части имеют один номер. Это порядок назначения событий одной сети, не block chronology, не NATS stream sequence и не гарантия порядка завершения частей. Части history и live могут чередоваться; лимиты очередей/приоритет live проектируются в transport stage. Межсетевого sequence нет.

Execution chronology задают block parent links, native transaction position, operation_index и action/frame order. Delivery chronology задают chain_sequence и transport metadata. `part_index` задаёт только сборку bytes. Нельзя сортировкой chain_sequence восстанавливать blockchain chronology.

NATS message ID должен стабильно различать `(network,event_id,complete-or-part-index)`. После всех PubAck Gateway может подтвердить adapter конкретные event_id/digest/sequence. NATS deduplication window не обеспечивает вечный exactly-once: контракт допускает повторы с тем же ID/bytes. ACK не подтверждает работу downstream consumers. Durable mapping/WAL, повторный запуск и потеря состояния — отдельный REC-пул, не закрытый этой схемой.

## 7. Маршрутизация

Для каждой сети отдельные streams/subjects/limits. Сохраняется рабочий namespace ТЗ: `evm.v3.<chain_key>.<event_family>.<event_kind>`, где event_family=`tx|block|coverage|balance.system`. Event kind задаётся закрытым реестром: tx applied/removed/status; block applied/complete/removed/status; coverage updated; optional balance.system applied/removed/status. Transport version/schema version находятся внутри frame; изменение Markdown v3 само по себе не меняет subject.

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
| Пустой блок / одна пропущенная tx | Первый получает Complete; второй — никогда |
| Parts reorder/duplicates/corruption | Сборка по индексу, повторы идемпотентны, конфликт bytes/digest отклоняется |
| Live/history overlap и reorg во время публикации | Одна активация на включение; отмена адресная, старый status не оживляет удалённое |
| Неизвестный тип/версия/fork | Явная ошибка coverage; нет молчаливого пропуска |
| Optional balances выключены | Transactions COMPLETE допустим; all-balance completeness не заявляется |

Это acceptance matrix будущих E01/E02/E03, не отчёт о пройденных тестах. E00-15 определил Classic/legacy source profiles и ограничения; их runtime-покрытие и архивы ещё не доказаны. Открыты точные network execution/finality extensions, Classic all-epoch decoders и validators (E02), ресурсные параметры/NATS delivery (E03/REC), история/YAML control (JOB-01, отложен пользователем).
