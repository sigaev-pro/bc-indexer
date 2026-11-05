# Архив proto-заготовки до E00-14

Исторический материал, не действующий контракт. Сохранён 2026-10-15, чтобы не потерять прежний эскиз. Flat EvmTransaction, Any extensions и обязательная TransportChunk-обёртка не применяются как актуальная схема. Действующий проект: [MESSAGE_CONTRACT.md](../MESSAGE_CONTRACT.md) и [TRANSACTION_TYPE_REGISTRY.md](../TRANSACTION_TYPE_REGISTRY.md). Код ниже не компилировался; не использовать для генерации bindings.

## 13. Protobuf-протокол

Формат — Protocol Buffers v3. Ниже приведена общая схема протокола версии 3.

Статус схемы: рабочий проект wire contract. D17/D18 утверждают строгую общую типизацию, block/coverage/lifecycle и разбиение крупных записей; конкретные balance actions, поля блоков и transport union ещё требуется завершить. Proto ниже не объявлен финальным.

Для чисел uint256 используются ровно 32 байта unsigned big-endian; адреса EVM — 20 байт; hashes и storage keys/values — 32 байта. Native identifiers имеют длины, заданные сетевыми extension.

Поля optional не заполняются, если значение неприменимо или отсутствует в родном формате. Применимые обязательные данные нельзя пропускать.

~~~proto
syntax = "proto3";

package evm.extract.v3;

import "google/protobuf/any.proto";

// Envelope.schema_version = 3; пространства package и subjects отделены от v2.

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

message ExecutionPosition {
  uint64 operation_index = 1;
  optional bytes execution_block_hash = 2;
  optional uint64 execution_block_number = 3;
}

message NativeBalanceChange {
  bytes address = 1;
  bytes balance_before = 2;
  bytes balance_after = 3;
}

message SystemBalanceRef {
  bytes block_hash = 1;
  uint64 block_number = 2;
  uint64 operation_index = 3;
}

message SystemBalanceRecord {
  SystemBalanceRef inclusion = 1;
  BlockContext block = 2;
  // Тип из versioned network profile, без UNKNOWN для принятого покрытия.
  string operation_type = 3;
  repeated NativeBalanceChange balance_changes = 4;
  repeated FinalityStage finality = 5;
  ExecutionPosition execution_position = 6;
  repeated google.protobuf.Any extensions = 30;
}

message SystemBalanceApplied {
  bytes activation_id = 1;
  SystemBalanceRecord operation = 2;
}

message SystemBalanceRemoved {
  bytes activation_id = 1;
  SystemBalanceRef inclusion = 2;
  string reason = 3;
}

message SystemBalanceStatusChanged {
  bytes activation_id = 1;
  SystemBalanceRef inclusion = 2;
  repeated FinalityStage finality = 3;
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
  ExecutionPosition execution_position = 10;
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
    SystemBalanceApplied system_balance_applied = 13;
    SystemBalanceRemoved system_balance_removed = 14;
    SystemBalanceStatusChanged system_balance_status_changed = 15;
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

Для NativeTransaction, не имеющей EVM receipt или call tree, они отсутствуют по семантике типа; это не допускает пропуска применимых данных обычной EVM/ EraVM транзакции. Изменения нативных балансов такой транзакции всё равно обязательны в state_changes по правилам профиля сети.

Для любого изменённого нативного баланса в AccountChange обязательны balance_before и balance_after; присутствие только одного запрещено. Для SystemBalanceRecord список balance_changes непустой, адреса не повторяются, before != after; суммы задаются в минимальных единицах нативного актива как uint256. Нулевой баланс задаётся 32 нулевыми байтами. Знак изменения вычисляется как after - before в знаковой арифметике достаточной разрядности, без uint256 underflow.

execution_position обязателен для обоих видов записей. В SystemBalanceRecord operation_index должен совпадать с inclusion.operation_index. execution_block_hash и execution_block_number либо отсутствуют оба, либо заданы оба. Отсутствие означает блок inclusion. Неизвестная родная семантика останавливает продвижение соответствующего coverage и не подменяется пустыми данными.

## 14. Typed extensions

Extensions используют только зарегистрированные versioned protobuf-типы. Текущий проект Any допустим лишь с закрытым реестром type_url, десериализацией и семантической валидацией. Неизвестные типы/версии нельзя пропускать или публиковать как полностью нормализованные. Произвольный JSON и opaque extension bytes не заменяют схему; raw calldata, hashes и транспортные части могут оставаться bytes.

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

До фиксации контракта каждого адаптера native_fields, L2→L1 logs, storage logs и fee components получают зарегистрированные типизированные схемы для закреплённой версии клиента. Общая семантика полей одинакова; неприменимость явно отличается от потери обязательного значения.

Общие сведения batch/block нельзя искусственно приписывать одной транзакции. Если часть pubdata относится только к batch, она не выдаётся за transaction_pubdata.
