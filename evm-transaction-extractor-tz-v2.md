# EVM Transaction Extractor — техническое задание v2

Дата: 7 октября 2026 года.

## 1. Цель

Разработать многомодульный экстрактор, который получает транзакции непосредственно из нативных клиентов блокчейнов и передаёт в NATS JetStream завершённые записи транзакций со всеми внутренними вызовами, результатами и изменениями состояния.

Поддерживаемые семейства:

1. Ethereum / Geth.
2. BSC и совместимые форки Geth.
3. Reth и производные, включая Base и OP Stack на соответствующих клиентах.
4. Arbitrum Nitro.
5. ZKsync Era и совместимые ZK Stack сети с EraVM.
6. Avalanche C-Chain.

Единица полезных данных — TransactionRecord: одна включённая в цепочку транзакция, её receipt, logs, полное дерево внутренних вызовов и итоговый state diff.

В NATS передаются данные транзакций и изменения их канонического статуса. Этапы обработки блока остаются внутри адаптера.

## 2. Границы задачи

Обязательны:

- историческая выгрузка;
- получение транзакций при первоначальной синхронизации;
- непрерывная live-выгрузка;
- internal transactions;
- создание контрактов и связанные с транзакциями действия SELFDESTRUCT;
- receipts и сохранённые logs;
- итоговые изменения состояния в пределах каждой транзакции;
- chain-specific transaction data;
- корректная обработка reorg и finality;
- доставка без потерь с повторной отправкой и идемпотентной обработкой.

В выгрузку входят успешные и неуспешные транзакции, присутствующие в канонической цепочке. Не включаются mempool, симуляции, отклонённые кандидаты и транзакции блоков, которые ещё не получили необходимое каноническое подтверждение.

Операции протокола, действительно представленные транзакциями в родной сети, выгружаются независимо от того, кто их инициировал. Изменения вне транзакций — например, Ethereum withdrawals, награды блока и самостоятельные системные операции — не превращаются в искусственные транзакции.

Поэтому результат является полной выгрузкой транзакций, но не журналом всех изменений состояния цепочки.

## 3. Архитектура

Pipeline:

1. Native Node исполняет транзакцию.
2. Node Adapter собирает данные внутри нативного execution path.
3. Adapter сохраняет завершённый результат и связывает его с включением в цепочку.
4. Adapter передаёт запись в Ingest Gateway через gRPC.
5. Gateway проверяет, сохраняет и публикует её в NATS JetStream.
6. Consumers сохраняют и обрабатывают данные.

Основные компоненты:

| Компонент | Ответственность |
|---|---|
| Native Node | Нативное исполнение и определение состояния цепочки |
| Adapter | Транзакция, внутренние вызовы, receipt, logs, state diff |
| Canonical Coordinator | Каноничность включений, reorg, finality и checkpoints |
| Adapter WAL | Восстановление и повторная доставка |
| Ingest Gateway | Валидация, упорядочивание, дедупликация, публикация |
| NATS JetStream | Надёжный транспорт |
| Consumers | Хранение, мониторинг адресов, анализ переводов и другая бизнес-логика |

Canonical Coordinator может быть частью адаптера. Gateway обеспечивает единого издателя для потока конкретной сети.

Adapter не обращается к NATS и не пишет в ClickHouse.

## 4. Источник данных

Экстрактор получает данные через:

- нативные execution hooks;
- интерфейсы клиента для чтения транзакций и receipts;
- доступ к нативному состоянию и журналам его изменений;
- уведомления клиента об изменении канонической цепочки.

Local и remote JSON-RPC, WebSocket RPC и debug_trace* не используются экстрактором как рабочий источник транзакционных данных.

RPC разрешается для диагностики и независимой тестовой сверки. Использование RPC самим клиентом для L1 derivation или штатной синхронизации не является нарушением этого ограничения.

Для исторического исполнения используется родной executor соответствующей сети. Нельзя исполнять блоки BSC, Nitro, OP Stack или EraVM через обычный Ethereum executor.

## 5. Когда транзакция готова к публикации

TransactionApplied публикуется, когда:

1. Исполнение транзакции завершено.
2. Собраны все внутренние вызовы и их результаты.
3. Получен окончательный receipt и сохранённые logs.
4. Рассчитан итоговый state diff.
5. Известны окончательные идентификаторы транзакции и блока.
6. Клиент подтвердил каноничность включения.
7. Запись надёжно сохранена для доставки.

Само завершение транзакции не доказывает валидность или каноничность содержащего её блока.

Адаптер может получать данные заранее и временно хранить их по block hash и transaction index. При отклонении блока записи не публикуются.

Для сетей, где acceptance предшествует исполнению, нужно дождаться и acceptance, и завершения исполнения.

Полная финальность не является обязательным условием первоначальной публикации: она отражается отдельным транзакционным статусом.

## 6. Данные транзакции

Каждая запись содержит:

| Группа | Данные |
|---|---|
| Идентификация | Сеть, genesis hash, transaction hash, block hash, позиция в блоке |
| Контекст блока | Number, parent hash, timestamp, количество транзакций, применимые поля header |
| Исходная транзакция | Type, sender, receiver, nonce, value, calldata, gas и fee fields |
| Дополнительные поля | Access list, blob hashes, authorization list, signature, native extensions |
| Результат | Receipt, status или исторический post-state root, gas used, fees, contract address |
| Logs | Address, topics, data и индексы |
| Internal transactions | Полное дерево вызовов, включая неуспешные ветви |
| Связанные действия | SELFDESTRUCT с получателем и суммой |
| State diff | Изменения balance, nonce, code, storage и существования account |
| Сетевые данные | Typed extensions |
| Исходные данные | Native binary transaction и receipt, когда такой формат определён клиентом |

Отсутствующее поле отличается от нулевого значения. Например, отсутствие receiver у создания контракта не кодируется нулевым адресом.

Исторический receipt без status не получает искусственное success=false. Для него сохраняется native post-state root; результат исполнения отдельно определяется нативным executor.

Blob sidecars и содержимое blobs не входят в базовую запись транзакции: обязательны ссылки versioned hashes и применимые fee fields.

## 7. Internal transactions

Internal transactions представлены деревом внутренних вызовов, связанным с одной внешней транзакцией. Собственного transaction hash у внутреннего вызова нет.

Для EVM необходимо поддерживать:

- CALL;
- CALLCODE;
- DELEGATECALL;
- STATICCALL;
- CREATE;
- CREATE2.

Нулевой value не является основанием исключать вызов. Precompile calls также сохраняются.

Дерево передаётся плоским массивом CallFrame:

- id — уникален внутри транзакции;
- parent_id — ссылка на родителя;
- depth — глубина;
- order — порядковый номер входа;
- type — тип вызова;
- from и to — инициатор и адрес назначения вызова;
- context_address — адрес, в чьём контексте меняется состояние;
- code_address — адрес исполняемого кода;
- msg_sender — фактический sender внутри вызова;
- value — значение контекста вызова;
- input и output;
- supplied gas и gas used, если применимы;
- outcome — собственный результат вызова;
- effects_committed — сохранились ли его обратимые эффекты в итоговом результате транзакции;
- error и revert data;
- created_address — адрес создаваемого контракта, когда определён;
- runtime и native_type для сетевых разновидностей.

Корневой вызов имеет id=0, depth=0 и отсутствующий parent_id.

Для DELEGATECALL, CALLCODE и делегированного кода адрес назначения, адрес кода и адрес состояния могут различаться. Их нельзя смешивать. Значение value в таком frame не означает отдельный перевод средств.

Для EraVM и других нативных runtime сохраняются родная семантика и native_type. Нельзя придумывать EVM-тип, если эквивалентного вызова нет.

## 8. Ошибки и отменённые внутренние вызовы

В дереве сохраняются:

- успешные вызовы;
- revert;
- исключительные ошибки;
- неуспешное создание контракта;
- успешные дочерние вызовы, эффекты которых отменены родителем.

Пример:

| Frame | Собственный результат | Effects committed |
|---|---|---|
| A → B | Revert | false |
| B → C | Success | false |

Вызов C действительно состоялся, но его обратимые изменения отменены возвратом ошибки из B.

Если ошибка B перехвачена A, внешняя транзакция может завершиться успешно. Поэтому outcome внутреннего вызова, receipt внешней транзакции и effects_committed — отдельные характеристики.

State diff содержит только итоговые изменения, сохранившиеся после всех откатов. Неуспешная внешняя транзакция может сохранить изменения nonce, оплату комиссии и другие предусмотренные правилами сети эффекты.

## 9. SELFDESTRUCT и создание контрактов

SELFDESTRUCT хранится как отдельное действие внутри транзакции с привязкой к frame:

- order;
- frame_id;
- contract address;
- beneficiary;
- value;
- effects_committed;
- факт удаления account, если оно действительно произошло.

Для CREATE/CREATE2 сохраняются input создания, output, результат и созданный адрес; итоговый code включается в AccountChange.

Нельзя считать каждое действие SELFDESTRUCT удалением контракта: правила EIP-6780 сохраняют code и storage во многих случаях. Семантика определяется активными правилами конкретной сети и исторической высоты. Источник: [EIP-6780](https://eips.ethereum.org/EIPS/eip-6780).

## 10. Logs и state diff

Receipt.logs содержит только logs, сохранившиеся в окончательном receipt. Отменённые события не выдаются за события канонической транзакции.

Для каждого log обязательны:

- block-global log index;
- transaction-local log index;
- address;
- topics;
- data.

AccountChange содержит:

- address;
- account existence before/after;
- balance before/after;
- nonce before/after;
- code hash before/after;
- code before/after при изменении;
- storage changes;
- признак reset storage при полном удалении или сбросе.

Before/after относятся к границам одной транзакции с учётом нативного порядка применения её эффектов. При нескольких изменениях одного storage slot сохраняются первоначальное и окончательное значения.

Для полного удаления account не требуется перечислять каждый старый storage slot: account existence и storage_reset задают удаление storage целиком. Отменённые записи не включаются в итоговый diff.

Записи balance/code/nonce вне основного вызова, но относящиеся к транзакции, также учитываются. Например, authorization processing EIP-7702 может менять code и nonce до основного вызова. Источник: [EIP-7702](https://eips.ethereum.org/EIPS/eip-7702).

## 11. События протокола

| Событие | Смысл |
|---|---|
| TransactionApplied | Полная запись канонического включения транзакции |
| TransactionRemoved | Ранее опубликованное включение отменено |
| TransactionStatusChanged | Изменились статусы финальности конкретного включения |

Прикладных событий начала, окончания и промежуточных этапов обработки блока нет.

TransportChunk используется только для передачи больших записей. Он не описывает процесс исполнения.

Системные изменения вне транзакций не публикуются отдельными ExecutionRecord.

## 12. Идентификация, повторы и порядок

Идентификатор сети задаётся chain_id и genesis_hash; chain_key — удобное уникальное имя для subjects.

Идентификатор включения:

network + block_hash + transaction kind + transaction position + transaction_hash.

Для нестандартных транзакций используется native_position, если обычный EVM transaction index неприменим.

Transaction hash недостаточно: одна транзакция может после reorg оказаться в другом блоке.

Каждая публикация TransactionApplied получает activation_id. TransactionRemoved и TransactionStatusChanged ссылаются на эту активацию. Повторное возвращение того же блока в каноническую цепочку создаёт новую активацию.

event_id идентифицирует конкретное событие доставки. Повторная отправка из WAL сохраняет event_id и исходные байты. Новое изменение жизненного цикла получает новый event_id.

source_sequence монотонен внутри writer_epoch. После потери состояния writer новая эпоха получает новый идентификатор.

Gateway присваивает chain_sequence — общий монотонный номер публикации в потоке сети. Mapping event_id → chain_sequence сохраняется надёжно до публикации.

Порядок chain_sequence отражает порядок публикации, а не block number. Исторические записи могут публиковаться одновременно с live-данными.

## 13. Protobuf-протокол

Формат — Protocol Buffers v3. Ниже приведена общая схема протокола версии 2.

Для чисел uint256 используются ровно 32 байта unsigned big-endian; адреса EVM — 20 байт; hashes и storage keys/values — 32 байта. Native identifiers имеют длины, заданные сетевыми extension.

Поля optional не заполняются, если значение неприменимо или отсутствует в родном формате. Применимые обязательные данные нельзя пропускать.

~~~proto
syntax = "proto3";

package evm.extract.v2;

import "google/protobuf/any.proto";

enum CaptureMode {
  CAPTURE_MODE_UNSPECIFIED = 0;
  LIVE = 1;
  SYNC = 2;
  BACKFILL = 3;
}

enum TransactionKind {
  TRANSACTION_KIND_UNSPECIFIED = 0;
  EVM_TRANSACTION = 1;
  NATIVE_TRANSACTION = 2;
}

enum CallType {
  CALL_TYPE_UNSPECIFIED = 0;
  CALL = 1;
  CALLCODE = 2;
  DELEGATECALL = 3;
  STATICCALL = 4;
  CREATE = 5;
  CREATE2 = 6;
  NATIVE_CALL = 7;
}

enum CallOutcome {
  CALL_OUTCOME_UNSPECIFIED = 0;
  SUCCESS = 1;
  REVERT = 2;
  FAILURE = 3;
}

message ChainIdentity {
  bytes chain_id = 1;
  bytes genesis_hash = 2;
  string chain_key = 3;
}

message Source {
  string node_id = 1;
  string client = 2;
  string client_version = 3;
  string client_commit = 4;
  string adapter_version = 5;
  bytes writer_epoch = 6;
}

message InclusionRef {
  bytes transaction_hash = 1;
  bytes block_hash = 2;
  uint64 block_number = 3;
  TransactionKind kind = 4;
  optional uint64 transaction_index = 5;
  optional uint64 native_position = 6;
}

message BlockContext {
  bytes parent_hash = 1;
  uint64 timestamp = 2;
  uint64 transaction_count = 3;
  optional bytes base_fee_per_gas = 4;
  optional bytes raw_header = 5;
  repeated google.protobuf.Any extensions = 10;
}

message AccessTuple {
  bytes address = 1;
  repeated bytes storage_keys = 2;
}

message Authorization {
  bytes chain_id = 1;
  bytes address = 2;
  uint64 nonce = 3;
  uint32 y_parity = 4;
  bytes r = 5;
  bytes s = 6;
}

message EvmTransaction {
  uint32 type = 1;
  bytes from = 2;
  optional bytes to = 3;
  uint64 nonce = 4;
  bytes value = 5;
  uint64 gas_limit = 6;
  optional bytes gas_price = 7;
  optional bytes max_fee_per_gas = 8;
  optional bytes max_priority_fee_per_gas = 9;
  optional bytes max_fee_per_blob_gas = 10;
  bytes input = 11;
  repeated AccessTuple access_list = 12;
  repeated bytes blob_versioned_hashes = 13;
  repeated Authorization authorization_list = 14;
  optional bytes signature_v = 15;
  optional bytes signature_r = 16;
  optional bytes signature_s = 17;
  bytes raw_transaction = 18;
  repeated google.protobuf.Any extensions = 30;
}

message Log {
  uint64 log_index = 1;
  uint64 transaction_log_index = 2;
  bytes address = 3;
  repeated bytes topics = 4;
  bytes data = 5;
}

message Receipt {
  oneof result {
    bool success = 1;
    bytes post_state_root = 2;
  }
  optional uint64 cumulative_gas_used = 3;
  uint64 gas_used = 4;
  optional bytes effective_gas_price = 5;
  optional bytes contract_address = 6;
  repeated Log logs = 7;
  optional uint64 blob_gas_used = 8;
  optional bytes blob_gas_price = 9;
  optional bytes raw_receipt = 10;
  optional bytes logs_bloom = 11;
  repeated google.protobuf.Any extensions = 30;
}

message CallFrame {
  uint64 id = 1;
  optional uint64 parent_id = 2;
  uint32 depth = 3;
  uint64 order = 4;
  CallType type = 5;
  bytes from = 6;
  optional bytes to = 7;
  optional bytes context_address = 8;
  optional bytes code_address = 9;
  optional bytes msg_sender = 10;
  optional bytes value = 11;
  bytes input = 12;
  bytes output = 13;
  optional uint64 gas = 14;
  optional uint64 gas_used = 15;
  CallOutcome outcome = 16;
  bool effects_committed = 17;
  optional string error = 18;
  optional bytes revert_data = 19;
  optional bytes created_address = 20;
  string runtime = 21;
  optional string native_type = 22;
  repeated google.protobuf.Any extensions = 30;
}

message SelfDestructAction {
  uint64 order = 1;
  uint64 frame_id = 2;
  bytes address = 3;
  bytes beneficiary = 4;
  bytes value = 5;
  bool effects_committed = 6;
  bool account_deleted = 7;
}

message StorageChange {
  bytes key = 1;
  bytes before = 2;
  bytes after = 3;
}

message AccountChange {
  bytes address = 1;
  bool existed_before = 2;
  bool exists_after = 3;
  optional bytes balance_before = 4;
  optional bytes balance_after = 5;
  optional uint64 nonce_before = 6;
  optional uint64 nonce_after = 7;
  optional bytes code_hash_before = 8;
  optional bytes code_hash_after = 9;
  optional bytes code_before = 10;
  optional bytes code_after = 11;
  repeated StorageChange storage = 12;
  bool storage_reset = 13;
  repeated google.protobuf.Any extensions = 30;
}

message FinalityStage {
  string stage = 1;
  bool reached = 2;
  optional bytes evidence_hash = 3;
  optional uint64 evidence_number = 4;
  optional string evidence_domain = 5;
  repeated google.protobuf.Any extensions = 10;
}

message TransactionRecord {
  InclusionRef inclusion = 1;
  BlockContext block = 2;
  oneof transaction {
    EvmTransaction evm = 3;
    google.protobuf.Any native = 4;
  }
  optional Receipt receipt = 5;
  repeated CallFrame calls = 6;
  repeated SelfDestructAction selfdestructs = 7;
  repeated AccountChange state_changes = 8;
  repeated FinalityStage finality = 9;
  repeated google.protobuf.Any extensions = 30;
}

message TransactionApplied {
  bytes activation_id = 1;
  TransactionRecord transaction = 2;
}

message TransactionRemoved {
  bytes activation_id = 1;
  InclusionRef inclusion = 2;
  string reason = 3;
}

message TransactionStatusChanged {
  bytes activation_id = 1;
  InclusionRef inclusion = 2;
  repeated FinalityStage finality = 3;
}

message Envelope {
  uint32 schema_version = 1;
  bytes event_id = 2;
  ChainIdentity chain = 3;
  Source source = 4;
  uint64 source_sequence = 5;
  CaptureMode capture_mode = 6;
  oneof payload {
    TransactionApplied transaction_applied = 10;
    TransactionRemoved transaction_removed = 11;
    TransactionStatusChanged transaction_status_changed = 12;
  }
}

message TransportChunk {
  bytes event_id = 1;
  bytes payload_sha256 = 2;
  uint32 part_index = 3;
  uint32 part_count = 4;
  uint64 total_size = 5;
  bytes data = 6;
  uint64 chain_sequence = 7;
}

message IngestAck {
  bytes event_id = 1;
  bytes payload_sha256 = 2;
  uint64 chain_sequence = 3;
}

service IngestService {
  rpc Publish(stream TransportChunk) returns (stream IngestAck);
}
~~~

Для NativeTransaction, не имеющей EVM receipt или call tree, они отсутствуют по семантике типа; это не допускает пропуска применимых данных обычной EVM/ EraVM транзакции.

## 14. Typed extensions

Extensions используют google.protobuf.Any с зарегистрированными type_url и protobuf-схемами. Произвольные JSON-строки вместо typed fields не допускаются.

Обязательные группы extension:

| Группа | Что необходимо сохранить |
|---|---|
| Geth forks | Родной subtype и дополнительные поля транзакции/receipt, специфические fee components |
| OP Stack / Base | Deposit transaction fields, source hash, mint, L1 origin и фактические L1/operator fee components |
| Nitro | ArbOS version, subtype, L1 origin, message identity, retryable identity и применимые fee components |
| ZKsync | Protocol version, L1 batch и позиция, L1-origin flag, gasPerPubdata, paymaster, custom signature, factory dependencies, L2→L1 logs, относимые к транзакции pubdata и storage logs |
| Avalanche | Acceptance/settlement metadata, реальные fee fields и atomic import/export |

Пример основных сетевых сообщений:

~~~proto
message NitroTransactionExtension {
  uint32 arbos_version = 1;
  string native_transaction_type = 2;
  optional uint64 l1_block_number = 3;
  optional bytes l1_block_hash = 4;
  optional uint64 message_index = 5;
  optional bytes retryable_id = 6;
  optional bytes l1_fee = 7;
  repeated google.protobuf.Any native_fields = 20;
}

message ZksyncTransactionExtension {
  uint32 protocol_version = 1;
  optional uint64 l1_batch_number = 2;
  optional uint64 l1_batch_transaction_index = 3;
  bool is_l1_originated = 4;
  optional bytes gas_per_pubdata = 5;
  optional bytes paymaster = 6;
  optional bytes paymaster_input = 7;
  optional bytes custom_signature = 8;
  repeated bytes factory_dependencies = 9;
  optional bytes transaction_pubdata = 10;
  repeated google.protobuf.Any l2_to_l1_logs = 11;
  repeated google.protobuf.Any storage_logs = 12;
  repeated google.protobuf.Any native_fields = 20;
}

message AtomicEntry {
  bytes asset_id = 1;
  bytes address = 2;
  bytes amount = 3;
  optional bytes utxo_id = 4;
  bytes raw_entry = 5;
}

message AvalancheAtomicTransaction {
  bytes transaction_id = 1;
  uint32 network_id = 2;
  bytes blockchain_id = 3;
  string atomic_type = 4;
  optional bytes source_chain = 5;
  optional bytes destination_chain = 6;
  repeated AtomicEntry inputs = 7;
  repeated AtomicEntry outputs = 8;
  bytes raw_transaction = 9;
}

message AvalancheTransactionExtension {
  optional bytes settlement_block_hash = 1;
  optional uint64 settlement_block_number = 2;
  optional bytes actual_fee = 3;
  repeated google.protobuf.Any native_fields = 20;
}
~~~

Сообщения из этого раздела размещаются в том же package или в отдельных versioned extension packages с соответствующими imports.

В ходе разработки адаптера native_fields, L2→L1 logs, storage logs и fee components получают отдельные зарегистрированные typed-схемы для закреплённой версии клиента. До приёмки адаптера все применимые родные поля должны иметь определённое представление.

Общие сведения batch/block нельзя искусственно приписывать одной транзакции. Если часть pubdata относится только к batch, она не выдаётся за transaction_pubdata.

## 15. Большие записи

Одна транзакция может превышать допустимый размер сообщения NATS.

Порядок передачи:

1. Сформировать полную Envelope.
2. Один раз сериализовать её в protobuf.
3. Рассчитать SHA-256 исходных сериализованных байтов.
4. Разбить байты на TransportChunk.
5. Передать все части с одинаковыми event_id, digest и total_size.
6. Собрать и проверить полную запись на принимающей стороне.

Даже маленькая запись передаётся как TransportChunk с part_count=1.

part_index начинается с нуля. Размер каждой сериализованной части, включая заголовки, должен укладываться в действующий лимит NATS max_payload; предел gRPC настраивается согласованно.

chain_sequence=0 на участке Adapter → Gateway. При публикации Gateway устанавливает присвоенный номер во всех частях. Сама Envelope при этом не изменяется.

Consumer применяет событие только после получения всех частей и проверки размера и SHA-256. Отдельные части не являются отдельными транзакциями.

Запрещены молчаливое усечение input/output, удаление call frames, сокращение storage diff и публикация неполной записи как полной.

Дедупликационный идентификатор части в NATS: event_id + part_index.

## 16. Транспорт, WAL и ACK

На одном сервере предпочтителен gRPC через Unix Domain Socket. Между серверами — gRPC/TCP с mTLS.

Execution hooks выполняют сбор данных в ограниченные буферы. Сетевой I/O вынесен в отдельную задачу.

Adapter WAL хранит:

- полные исходные Envelope;
- event_id и digest;
- writer_epoch и sequence;
- канонические активации и checkpoints;
- состояние подтверждения доставки.

Gateway надёжно хранит mapping event_id → chain_sequence и состояние публикации.

Подтверждение:

1. Adapter надёжно сохраняет событие.
2. Gateway получает и проверяет все части.
3. Gateway фиксирует событие и chain_sequence.
4. Gateway публикует части в JetStream.
5. JetStream возвращает PubAck для каждой части.
6. Gateway отправляет IngestAck.
7. Adapter помечает событие подтверждённым и позднее удаляет его из WAL.

IngestAck означает, что все части сохранены JetStream. Он не означает, что downstream уже обработал транзакцию.

Модель доставки — at-least-once. Consumers обязаны хранить собственную идемпотентность; окно дедупликации JetStream не является бессрочной гарантией.

При заполнении очередей/WAL приостанавливается historical replay или дальнейший import в контролируемой точке. Требования к сохранности имеют приоритет над продолжением выгрузки. Неограниченные буферы и молчаливое удаление данных запрещены.

## 17. NATS subjects

| Subject | Payload |
|---|---|
| evm.v2.<chain>.tx.applied | Полная TransactionApplied в TransportChunk |
| evm.v2.<chain>.tx.removed | TransactionRemoved в TransportChunk |
| evm.v2.<chain>.tx.status | TransactionStatusChanged в TransportChunk |

Примеры:

- evm.v2.ethereum-mainnet.tx.applied
- evm.v2.bsc-mainnet.tx.applied
- evm.v2.base-mainnet.tx.applied
- evm.v2.arbitrum-one.tx.applied
- evm.v2.zksync-era.tx.applied
- evm.v2.avalanche-c.tx.applied

Все subjects конкретной сети входят в один JetStream stream. Storage — file, acknowledgements — explicit, replicas и retention задаются инфраструктурной конфигурацией.

Для consumers, которым важны reorg и finality, обязательна подписка на все три subjects. Упорядоченная публикация выполняется через одного active publisher сети с fencing при смене writer.

## 18. Reorg и finality

При reorg:

1. Определить старые включения в отменяемой ветке.
2. Для ранее опубликованных включений выдать TransactionRemoved.
3. Отменять старую ветку от вершины вниз; транзакции внутри блока — в обратном порядке.
4. Выдать TransactionApplied для новой ветки по возрастанию высоты и позиции.
5. Сохранить соответствующий порядок chain_sequence.

Транзакция, переехавшая в другой блок, получает новое включение и activation_id. Повторное возвращение того же включения после удаления также получает новую активацию.

Receipt status не меняется при reorg: меняется каноничность включения.

Finality хранится набором именованных стадий. Примеры:

| Семейство | Native stages |
|---|---|
| Ethereum | included, safe, finalized |
| BSC | included, native fast-finality stages |
| OP Stack / Base | unsafe, safe, finalized |
| Nitro | included и подтверждения L1/родной finality модели |
| ZKsync | included, committed, proven, executed, settlement_finalized |
| Avalanche | accepted и применимое подтверждение результатов settlement |

Названия и mapping закрепляются профилем сети. Число confirmations не подменяет native finality.

TransactionStatusChanged содержит полный актуальный набор стадий для activation_id. Возможные регрессии отражаются reached=false; нельзя предполагать монотонность промежуточных L1 статусов.

Для L2 завершение native стадии на текущей L1 ветке и финальность соответствующего L1 блока различаются. Evidence включает domain, block/batch number и hash, когда применимо.

## 19. Историческая выгрузка

Поддерживаются два способа:

**Захват при синхронизации.** Адаптер установлен до начала нативного исполнения нужного диапазона и собирает транзакции в том же проходе.

**Нативный backfill.** Worker открывает historical state перед диапазоном и последовательно исполняет его родным executor, собирая записи транзакций.

Наличие bodies и receipts не доказывает наличие call tree. Snapshot/state sync может пропустить исполнение старой истории.

Режим archive, включённый после pruning, сам по себе не восстанавливает удалённое состояние.

Требования к backfill:

- зафиксировать сеть, chain configuration, VM versions и граничные block hashes;
- использовать существующее состояние перед диапазоном либо последовательное восстановление от доступной контрольной точки;
- не запускать независимое восстановление старого state для каждой транзакции;
- обрабатывать диапазон последовательно;
- учитывать системные переходы между транзакциями и блоками для корректного состояния, хотя они не публикуются отдельно;
- проверять результаты средствами родного клиента;
- сохранять собственные checkpoints и уже подготовленные записи.

Параллельные диапазоны допустимы только при доступных корректных стартовых состояниях и поддержке конкурентного чтения. Нельзя параллелить зависимые транзакции одного диапазона поверх общего изменяемого state.

Предпочтительно брать финализированный диапазон. При отсутствии соответствующей гарантии используется pinned canonical snapshot и контроль изменений ветки.

Плановое повторное исполнение обработанной истории не требуется. Повторное исполнение ограниченных участков допустимо для восстановления после сбоя, проверки или изменившейся ветки.

Live и backfill имеют разные writer epochs и ресурсы. Coordinator устраняет пересечения: историческая выгрузка не должна повторно активировать уже опубликованное включение.

## 20. Ethereum / Geth

Adapter реализуется на Go с интеграцией в Geth live tracing и нативный state/receipt path.

Geth предоставляет live tracing для транзакций, call frames и изменений state. Hooks исполняются синхронно, поэтому внутри них должен оставаться минимальный сбор данных. Источник: [Geth live tracing](https://geth.ethereum.org/docs/developers/evm-tracing/live-tracing).

Используемые точки:

- OnTxStart / OnTxEnd;
- OnEnter / OnExit;
- OnBalanceChange;
- OnNonceChange;
- OnCodeChange;
- OnStorageChange;
- нативный receipt/log result;
- notifications о канонической цепочке.

Конкретные сигнатуры и варианты hooks закрепляются версией Geth.

Для history предпочтителен захват при full sync от genesis. Snap sync не исполняет всю предшествующую историю. Источник: [Geth sync modes](https://geth.ethereum.org/docs/fundamentals/sync-modes).

Нужны корректные исторические receipts, authorization transactions, delegated code, precompiles и все применимые hard forks.

Балансовые hooks сами по себе не являются готовым итоговым diff: адаптер учитывает rollback и реальные границы транзакции.

## 21. BSC и другие форки Geth

BSC основан на форке go-ethereum. Источник: [BNB Chain client](https://github.com/bnb-chain/bsc).

Общий Go Core переиспользуется для call tree, state diff, protobuf, WAL и transport. Интеграция исполнения и canonical/finality остаётся сетевой.

Требования:

- родной chain configuration и executor;
- chain-specific transaction types, system transactions и receipts;
- сохранение нативных fee/reward effects, относимых к транзакциям;
- native finality notifications;
- compatibility shim при отличающихся hooks.

Наличие происхождения от Geth не доказывает полной совместимости API или семантики.

Для каждого подключаемого форка фиксируются repository, commit, VM/hard-fork profile, supported history и fixtures. Совместимость принимается только после проверки этих условий.

## 22. Reth / Base / OP Stack

Adapter реализуется на Rust.

Call tree собирается на уровне родного EVM/REVM Inspector. State diff снимается на границах транзакции из journal/state transitions; готового block-wide diff недостаточно. Источники: [REVM Inspector](https://reth.rs/docs/reth_revm/inspector/index.html), [Reth custom EVM example](https://github.com/paradigmxyz/reth/blob/main/examples/custom-evm/src/main.rs).

ExEx используется для canonical notifications: ChainCommitted, ChainReorged и ChainReverted. Само уведомление ExEx не заменяет захват внутренних вызовов. Источник: [ExExNotification](https://reth.rs/docs/reth_exex/enum.ExExNotification.html).

Требования:

- подключить Inspector ко всем используемым execution paths, включая historical execution;
- сохранять результаты до canonical notification;
- применять native transaction semantics выбранного rollup-клиента;
- сохранять deposit fields, L1 origin и реальные fee components;
- не задерживать обработку canonical notifications долгим backfill;
- корректно подтверждать progress ExEx после надёжного сохранения данных, необходимых для восстановления.

Base и OP Stack подключаются через соответствующий клиент и EVM configuration; ordinary Ethereum Reth не подменяет родной rollup executor.

## 23. Arbitrum Nitro

Adapter реализуется на Go внутри Nitro/gethexec/ArbOS.

Nitro принимает tracing.Hooks в GetBlockChain и передаёт tracer в vm.Config. Это точка интеграции call capture, но её одной недостаточно для всех ArbOS и canonical metadata. Источник: [Nitro gethexec](https://github.com/OffchainLabs/nitro/blob/master/execution/gethexec/blockchain.go).

Требования:

- использовать родной Nitro executor;
- учитывать ArbOS version и transaction subtype;
- сохранять L1-origin/message metadata;
- сохранять retryable lifecycle для соответствующих реальных транзакций;
- учитывать комиссии и действия ArbOS, относящиеся к транзакции;
- получать canonical и finality из Nitro/родного L1 derivation path;
- сохранять межruntime вызовы Stylus, если они доступны в поддерживаемом диапазоне.

Нельзя объявлять Stylus-транзакции полностью поддержанными только на основании EVM hooks: полнота межruntime дерева проверяется отдельно.

Для Arbitrum One история до Nitro относится к Classic. Полная история сети требует отдельной нативной интеграции Classic; Nitro-only адаптер обязан явно указать нижнюю границу покрытия. Источник: [Arbitrum archive nodes](https://docs.arbitrum.io/run-a-node/more-types/run-archive-node).

## 24. ZKsync Era / EraVM

Adapter реализуется на Rust в execution path External Node / State Keeper / MultiVM соответствующей версии.

Требования:

- снимать данные при нативном исполнении;
- сохранять полное родное дерево вызовов;
- учитывать system contracts, native account abstraction и paymaster;
- сохранять gasPerPubdata, custom signature, factory dependencies;
- сохранять L1 batch number, позицию и L1-origin;
- сохранять относящиеся к транзакции L2→L1 logs и native storage information;
- рассчитывать итоговый diff с учётом VM rollback;
- поддерживать исторические protocol versions;
- не приписывать batch-level эффекты произвольной транзакции.

EraVM deployment и транзакционные поля имеют собственную семантику. Источник: [ZKsync transaction lifecycle](https://docs.zksync.io/zksync-protocol/era-vm/transactions/transaction-lifecycle).

Snapshot recovery не гарантирует полную transaction history. Официальный External Node guide указывает DB dumps для historical transaction data. Наличие dump дополнительно проверяется на доступность стартового state, bytecode и VM versions для backfill. Источник: [External Node quick start](https://github.com/matter-labs/zksync-era/blob/main/docs/src/guides/external-node/00_quick_start.md).

Стадии committed/proven/executed и финальность соответствующей L1 ветки сохраняются раздельно. ZKsync OS требует отдельной интеграции; совместимость с EraVM не предполагается.

## 25. Avalanche C-Chain

Adapter реализуется на Go в родном C-Chain execution stack выбранной версии AvalancheGo.

Нужно поддержать исторический Coreth path и применимый современный SAEVM path. После Helicon consensus acceptance предшествует исполнению, а settlement отражается позже. Источник: [C-Chain architecture](https://build.avax.network/docs/primary-network/coreth-architecture).

Требования:

- публиковать транзакцию только после acceptance и появления полного результата;
- связывать receipt и diff с исходным включением транзакции;
- сохранять ссылку на settlement, когда она применима;
- сохранять фактические комиссии из родного результата;
- не вычислять их только из header base fee;
- различать принятие включения и готовность/подтверждение execution results;
- сохранять native acceptance/finality metadata.

Atomic import/export представлены NativeTransaction с AvalancheAtomicTransaction. Для них сохраняются transaction id, network/blockchain ids, source/destination chain, inputs/outputs, assets, amounts и исходные байты. Искусственный EVM call tree или Ethereum receipt не создаётся.

Для historical replay state-sync snapshot не заменяет старые состояния. Источник: [Avalanche exchange integration](https://build.avax.network/docs/primary-network/exchange-integration).

## 26. Внутренние checkpoints и восстановление

Adapter хранит вне прикладного NATS-потока:

- обработанные block hashes, parent links и transaction counts;
- высоту и hash выгруженного диапазона;
- canonical activation registry;
- финальность и подтверждающие native данные;
- привязку execution capture к включению;
- WAL offsets и ACK status;
- состояние backfill jobs;
- coverage gaps.

Пустые блоки продвигают checkpoint, но не создают прикладных сообщений.

Одних NATS transaction messages недостаточно для доказательства отсутствия пропущенных пустых блоков. Полнота контролируется внутренним журналом coverage и сверкой с нодой.

Сохранение state ноды и adapter WAL не считается автоматически атомарным. После рестарта выполняется reconciliation от последней согласованной контрольной точки; для этого удерживается состояние или данные для восстановления соответствующего диапазона.

При отсутствии необходимых данных адаптер останавливает продвижение coverage и сообщает ошибку.

## 27. Ограничения и эксплуатационные требования

Adapter:

- не декодирует ABI;
- не определяет ERC-20/USDT/USDC;
- не делает бизнес-выводы о переводах;
- не пишет в аналитическую БД;
- не исполняет повторно live-транзакции только ради capture;
- не меняет consensus и VM semantics;
- не теряет данные при временной недоступности Gateway/NATS.

Gateway проверяет:

- schema version и chain identity;
- frame ids, parent links, depth и отсутствие циклов;
- корректность длин общих полей;
- transaction/receipt consistency;
- целостность частей;
- жизненный цикл activation_id;
- дубли и конфликты event_id;
- writer fencing и порядок публикации.

Родная семантика и native fields проверяются сетевым адаптером и versioned extension validators.

Метрики:

- capture duration и overhead;
- prepared/published transactions per second;
- live lag и backfill progress;
- объём WAL и свободное место;
- queue size;
- ACK latency и retries;
- количество missing captures, coverage gaps, reorg events;
- размер call trees и transaction records;
- validation failures.

Числовые performance limits закрепляются до приёмки по итогам benchmark на согласованном hardware и fixtures; неподтверждённые универсальные TPS не используются.

## 28. Структура кодовой базы

| Каталог | Содержание |
|---|---|
| protocol/proto | Общая схема и typed extensions |
| gateway | gRPC, durable publication journal, NATS publisher |
| adapters/go-core | Calls, state normalization, protobuf, WAL, transport |
| adapters/geth | Geth integration |
| adapters/bsc | BSC integration и compatibility shims |
| adapters/nitro | Nitro/ArbOS integration |
| adapters/avalanche | Исторический Coreth и современный C-Chain path |
| adapters/rust-core | Общие Rust protocol/WAL/transport components |
| adapters/reth | Inspector и ExEx integration |
| adapters/zksync | EraVM/MultiVM integration |
| backfill | Планирование диапазонов и native replay workers |
| fixtures | Эталонные транзакции и сценарии |
| docs | Клиенты, версии, coverage и инструкции эксплуатации |

Общий код отделяется от client-specific integration. Для каждого клиента поддерживается минимальный набор изменений, закреплённый upstream commit и проверка совместимости обновлений.

## 29. Проверка и критерии приёмки

Обязательные сценарии:

1. Простой native transfer.
2. Contract call с несколькими вложенными вызовами.
3. DELEGATECALL, CALLCODE и STATICCALL.
4. CREATE/CREATE2, успешные и неуспешные.
5. SELFDESTRUCT по различным историческим правилам.
6. Перехваченный revert.
7. Revert родителя после успешного дочернего вызова.
8. Неуспешная внешняя транзакция с сохранёнными fee/nonce effects.
9. Несколько изменений одного storage slot.
10. Precompile calls.
11. Authorization transactions там, где поддерживаются.
12. Native protocol transactions и сетевые extensions.
13. Повторная доставка.
14. Crash/restart на разных этапах WAL/publish/ACK.
15. Reorg, reinclusion и возврат прежней ветки.
16. L1 finality regressions для применимых L2.
17. Пересечение live и backfill.
18. Пустые блоки и checkpoints.
19. Запись крупнее max_payload.
20. Потеря, повтор или перестановка transport parts.

Для каждого семейства выбирается собственный набор native fixtures. Сверяются транзакции, receipts, logs, call tree и итоговый diff с эталонным родным исполнением. RPC tracer допускается как тестовый reference с учётом его семантики и ограничений.

Критерии готовности:

- все транзакции согласованного диапазона присутствуют;
- internal calls и их результаты сохранены полностью;
- отменённые эффекты не попадают в итоговый diff;
- receipt logs соответствуют native результату;
- сетевые особенности не теряются;
- повторная доставка не создаёт повторных бизнес-эффектов;
- reorg корректно отменяет прежние активации;
- рестарт продолжает выгрузку без необнаруженных пропусков;
- крупные записи передаются и проверяются без усечения;
- прикладной поток содержит только транзакционные события;
- benchmark и ограничения покрытия документированы.

## 30. Порядок реализации и результаты разработки

Порядок:

1. Общая схема, transport/WAL, Gateway и reference Geth adapter.
2. BSC и согласованные Geth forks.
3. Reth и согласованные Base/OP Stack clients.
4. Arbitrum Nitro.
5. Avalanche C-Chain.
6. ZKsync Era.

Для каждого адаптера результат включает:

- исходный код и воспроизводимую сборку;
- закреплённые версии клиентов;
- compiled protobuf bindings;
- все необходимые typed extensions;
- live capture и native historical replay;
- конфигурацию и инструкции запуска;
- fixtures и результаты сверки;
- проверку crash/reorg/retry;
- benchmark;
- точные границы historical/runtime coverage.

Основной результат работы системы: завершённые транзакционные записи в NATS.IO с полным деревом внутренних вызовов, receipts, logs, state diff и сетевыми данными, сопровождаемые отменами включений и обновлениями финальности.

