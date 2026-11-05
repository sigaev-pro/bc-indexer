# Техническое задание: native EVM transaction extractor

Чистовая редакция v3 · 2026-11-05 · DOC-SCOPE-01 · решения D16–D64.

Это единый нормативный контракт текущей поставки. Тематические файлы docs содержат представления отдельных разделов и обновляются с этим файлом в одном commit. DECISIONS хранит принятые решения; PROJECT_STATUS — точку продолжения; research и архивные проектные материалы — происхождение решений, а не дополнительные требования. Обнаруженное расхождение исправляется до реализации затронутого механизма. Исходный v2 сохранён без изменений.

D64: единственная цель нашей работы — идеальное ТЗ: полное, непротиворечивое, однозначное и проверяемое. Реализации не будет никогда. E01–E07, сборки, runtime proofs и deployment описываются как требования к проектируемой системе, не как исполняемый backlog агента. Все runtime verdict остаются NOT_RUN; исследования исходников и проверки документации не заменяют их.

## Содержание

1. [Назначение и сквозной процесс](#назначение-и-сквозной-процесс)
2. [Семантика исходных данных](#семантика-исходных-данных)
3. [Нативные балансы и причины изменений](#нативные-балансы-и-причины-изменений)
4. [Контракт сообщений](#контракт-сообщений)
5. [Реестр native типов](#реестр-native-типов)
6. [Точные данные и финальность текущих сетей](#точные-данные-и-финальность-текущих-сетей)
7. [Native capture и lifecycle recovery](#native-capture-и-lifecycle-recovery)
8. [Durable delivery и смена назначения](#durable-delivery-и-смена-назначения)
9. [Идентификаторы и служебный wire protocol](#идентификаторы-и-служебный-wire-protocol)
10. [Live YAML и команды](#live-yaml-и-команды)
11. [Состав и размещение нод](#состав-и-размещение-нод)
12. [Bootstrap и окружение](#bootstrap-и-окружение)
13. [Порты и экспонирование](#порты-и-экспонирование)
14. [Стек и границы сборок](#стек-и-границы-сборок)
15. [REC сценарии](#rec-сценарии)
16. [План доказательств и границы модулей](#план-доказательств-и-границы-модулей)
17. [Задачи разработки E01–E07](#задачи-разработки-e01e07)
18. [Требования и приёмка](#требования-и-приёмка)

## Назначение и сквозной процесс

### Граница поставки

Обязательный первый контур: **Ethereum Mainnet, BSC Mainnet, Polygon PoS Mainnet**. Встроенные native adapters собирают все включённые успешные/неуспешные транзакции, включая native system transactions, их применимые поля, calls/actions/receipt/logs/state changes, block metadata пустых блоков и lifecycle. Нормализованные данные доставляются в NATS JetStream. Приёмочный считыватель — тестовый инструмент, не промышленный downstream.

Только live после синхронизации: под native guard фиксируется H/hash, новый сбор начинается H+1. Pending/recovery obligations восстанавливаются независимо. История N–M, первоначальная sync-выгрузка и сканирование downtime gaps отложены. Targeted native replay начатого capture/изменившейся ветки допускается по recovery contract и не является загрузкой истории.

Обязательны transaction balance actions и before/after; самостоятельные изменения нативных балансов вне транзакций — optional D17. Native system tx BSC/Bor не становятся optional. Начальный snapshot всех адресов не требуется; отсутствие наблюдений не означает нулевой баланс. При выключенном outside-tx расширении нельзя обещать полную историю всех балансов или равенство after-last-tx и end-of-block.

Не входят: ABI/ERC-20/business decoding, mempool/симуляции, аналитическая БД, downstream consumer/dedup, production completeness verifier, HA/backup/multi-DC, post-ACK retention, performance SLO/hardware thresholds и production-методика. Base/Arbitrum и остальные будущие клиенты, Rust adapters, history/backfill, лицензия и публичная open-source упаковка отложены. Справочные типы этих профилей ниже не создают обязательных задач первого контура.

### Сквозной flow и ответственность

1. Окружение подготавливает config/secrets/volumes/native bootstrap; ноду запускает штатная команда клиента с нашим adapter. Сам extractor не управляет Docker/Kubernetes API.
2. Native bridge удерживает source state/inputs и sync сохраняет ADMITTED до принятого исполнения. Hooks передают owned данные, допускается safe-point yield вне callbacks.
3. Corpus EXEC_READY сохраняется до authoritative native commit; native decision marker и adapter mirror закрывают crash boundary.
4. Canonical/lifecycle решение создаёт immutable records/IDs и durable delivery READY. Спекулятивный corpus сам по себе не публикуется.
5. Gateway Prepare фиксирует reservation/layout/destination; producer сохраняет SEALED exact frames/headers. Gateway принимает полные CLOSED batches и публикует в NATS.
6. IngestAck разрешён только после PubAck всех частей одного назначения. Producer ACKED cleanup, ordered Release, Gateway RELEASING и retired frontier очищают payload; lifecycle/pins сохраняются по отдельным правилам.
7. Reorg/Status создаются native bridge, включая отмену ещё не опубликованного READY Applied. Доставка at-least-once: повторы допустимы, retry bytes/IDs неизменны.

Каждая сеть имеет собственные native node/adapter, WAL, Gateway journal, writer/progress и процессы. Один общий NATS — транспортная зависимость; outage вызывает независимое накопление. Нет общего multi-network WAL/coordinator и ожидания прогресса соседней сети. Polygon имеет собственную L1 Geth/Prysm пару без Ethereum adapter, не использует основной Ethereum контур.

Local fatal capture/WAL/normalization останавливает native процесс соответствующей сети. Gateway WAL fatal останавливает Gateway; безопасное local capture при outage сохраняется. Pending без TTL/прикладной квоты; disk exhaustion не разрешает drop. Drain — отдельный запуск той же сборки без новых admissions; logical NATS destination меняется только после завершения obligations обоих owners. ACKed история не переносится, новые lifecycle refs могут ссылаться на старый stream.

D58/D60: hash capture provider с durable pins, обычный import; Bor без pipelined/stateless/preconfirmation paths и BlockSTM. Heimdall кандидат v0.13.2. Эти ограничения проверяются effective preflight; source-checked профиль ещё не является runtime-proven.

D62: BSC early/header-attested finality различаются; GC только по header-attested Source. Все фактические комиссионные движения сохраняются отдельно от receipt summary. D63: Polygon GC ждёт checkpoint в finalized-состоянии собственной supporting L1-ноды; tx/status публикуются независимо. Полные правила — в точной семантике ниже.

## Семантика исходных данных

### Источник данных

Экстрактор получает данные через:

- нативные execution hooks;
- интерфейсы клиента для чтения транзакций и receipts;
- доступ к нативному состоянию и журналам его изменений;
- уведомления клиента об изменении канонической цепочки.

Local и remote JSON-RPC, WebSocket RPC и debug_trace* не используются экстрактором как рабочий источник транзакционных данных.

RPC разрешается для диагностики и независимой тестовой сверки. Использование RPC самим клиентом для L1 derivation или штатной синхронизации не является нарушением этого ограничения.

Каждая текущая сеть исполняется своим native executor; BSC/Bor не исполняются через обычный Ethereum executor.

### Когда транзакция готова к публикации

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

Полная финальность не является обязательным условием первоначальной публикации: она отражается отдельным статусом.

SystemBalanceApplied публикуется после завершения родной операции, учёта rollback, подтверждения каноничности соответствующего включения и надёжного сохранения полной записи. Сам вызов balance hook не является основанием для публикации.

### Данные транзакции

Сбор включает все транзакции, предоставленные native исполнением обработанного live-блока, с их исходными hash, index и родным type: успешные, неуспешные и родные системные транзакции. Internal calls остаются деталями родительской записи. По D25 отдельная production-сверка состава/числа записей и доказательство полноты блока в сборщик не входят; корректность capture проверяется на fixtures. Неизвестный тип или отсутствие обязательного поля дают явную ошибку, не молчаливый пропуск или успешную доставку проблемной записи. Неприменимое поле отличается от неизвестного/потерянного значения. Mempool и невключённые кандидаты вне scope.

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

### Internal transactions

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

Будущие runtime требуют отдельного профиля; их варианты не входят в первый контур.

### Ошибки и отменённые внутренние вызовы

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

### SELFDESTRUCT и создание контрактов

SELFDESTRUCT хранится как отдельное действие внутри транзакции с привязкой к frame:

- order;
- frame_id;
- contract address;
- beneficiary;
- value;
- effects_committed;
- факт удаления account, если оно действительно произошло.

Для CREATE/CREATE2 сохраняются input создания, output, результат и созданный адрес; итоговый code включается в AccountChange.

Точный логический SelfDestructAction и связь с денежной проекцией определены в [MESSAGE_CONTRACT](#контракт-сообщений). Нулевой balance не удаляет действие; fork eligibility, сохранённый deletion effect и конечный AccountChange различаются. Для authorization source tuple используется отдельная AuthorizationSignature с uint8 parity; ошибка отдельного tuple не является ошибкой всей включённой транзакции. Applied/StatusChanged/Removed имеют правила раннего Status, полных evidence snapshots и terminal Removed в том же контракте.

Нельзя считать каждое действие SELFDESTRUCT удалением контракта: правила EIP-6780 сохраняют code и storage во многих случаях. Семантика определяется активными правилами конкретной сети и исторической высоты. Источник: [EIP-6780](https://eips.ethereum.org/EIPS/eip-6780).

### Logs и state diff

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

## Нативные балансы и причины изменений

### Полнота нативных балансов

#### Обязательная граница покрытия

Для каждой поддержанной сети обязательны все транзакции и их применимые balance actions/before/after в текущем live-профиле. Сбор изменений вне транзакций необязателен по D17; допустимо отказаться от него при усложнении кода. Далее проверки самостоятельных системных операций относятся только к включённому расширению.

Нативный баланс — канонический баланс нативного актива адреса по модели конкретной сети. Профиль фиксирует актив, минимальную единицу, способ чтения и представление в state. Нельзя предполагать, что все runtime хранят его в том же account.balance, что Ethereum. ERC-20 balances, wrapped native tokens, consensus-layer validator balances и внешние UTXO сами по себе в это определение не входят. Родные atomic-транзакции и их изменения нативного баланса адресов поддерживаемой сети остаются обязательными.

Транзакционные изменения берутся из AccountChange. Для протокольных операций вне транзакций используется SystemBalanceRecord. Нельзя повторно публиковать транзакционные изменения в системном потоке или считать value каждого CallFrame самостоятельным переводом.

#### Гранулярность и rollback

Необходимо сохранять действия, вызвавшие изменения нативных балансов, и их причинную связь с транзакцией, внутренним вызовом или протокольной операцией. Итоговый before/after на границах транзакции или системной операции сохраняется дополнительно для сверки состояния; он не заменяет историю действий.

Если в одной успешной транзакции адрес получил 10 и отправил 10, обе причины изменения должны остаться в данных, несмотря на нулевой итоговый diff. Для одной завершённой операции сохраняется одна итоговая пара before/after адреса; отдельные действия внутри неё не должны теряться при свёртке. Транзакционная трассировка сохраняется полностью.

Изменение в успешно завершившемся дочернем вызове может быть отменено родительским revert: локальный outcome и итоговая применимость эффекта различаются. Отменённые действия остаются в трассировке с соответствующей семантикой и не участвуют в расчёте канонического баланса.

Действие не приравнивается автоматически к каждой временной записи native balance hook. Отображение gas debit/refund, fees, mint/burn и системных операций в действия определяется профилем клиента; нельзя вычислять переводы суммированием CallFrame.value. Идентичность действия, причинная ссылка, execution position, применимость и адресные effects определены в контракте сообщений ниже; compiled schema/validators — E02.

Сохранённые fee/nonce effects неуспешной транзакции учитываются по существующим требованиям. Системный поток не должен компенсировать ошибочно неполный транзакционный diff.

#### Захват и классификация

Каждый адаптер проверяет все транзакционные execution/state paths, включая родные системные транзакции текущего live-профиля. Самостоятельные системные balance paths исследуются для optional расширения и не блокируют основную поставку.

При реализации optional расширения для каждой поддерживаемой системной операции профиль задаёт:

- operation_type и typed metadata причины;
- границы начала, завершения и rollback;
- привязку к блоку, порядок operation_index и native identifiers, если они существуют;
- источник balance_before/after и каноничности;
- обязательные native capture paths выбранного live-профиля;
- правила finality, reinclusion и отложенного исполнения, если применимо.

Изменения code/storage, необходимые для нативного расчёта баланса, могут читаться и учитываться адаптером; это не расширяет публичный контракт до полного системного state diff. Нельзя приписывать изменение случайной соседней транзакции для удобства формата.

Неизвестный транзакционный тип/путь даёт явную ошибку обработки блока. Неизвестная операция необязательного системного расширения не блокирует транзакции при отключённом расширении. Успешная доставка не объявляется доказательством полноты.

#### Проверка нормализации

На разработческих fixtures проверяются before/after, actions, fees, rollback и сохранение +10/−10 при нулевом итоге. Это не обязательный production block/balance completeness verifier. При известной ошибке получения/декодирования запись не считается успешно доставленной; никаких выводов о полноте диапазона по NATS_ACKED.

#### Граница собранных наблюдений

Наблюдаемый live-период не задаёт начальных балансов ненаблюдавшихся адресов. Доступное исходное состояние необходимо native исполнению и проверяется отдельно.

По решению D10 полный начальный balance snapshot всех адресов не входит в обязательный старт. Собираются действия live-периода с родными before/after затронутых адресов. Для адреса без наблюдений в покрытом диапазоне абсолютный баланс может оставаться неизвестным; отсутствие строки не кодируется как ноль. Это не ограничивает корректное родное исполнение, которому необходимо доступное входное состояние.

Публикуемые наблюдения содержат точные before/after и границы исполнения. Вычисление представлений после NATS не проектируется.

Отсутствие наблюдений об адресе не кодируется как нулевой баланс. Конец блока отличается от конца последней транзакции, если после неё есть системные действия.

## Контракт сообщений

### 1. Типы и присутствие

| Тип | Инвариант |
|---|---|
| Hash32 / Address20 | Ровно 32 / 20 bytes; не произвольные строки |
| UInt256 | Ровно 32 bytes, unsigned big endian; арифметика без float |
| Delta256 | знак плюс UInt256 magnitude; отрицательный ноль запрещён |
| Timestamp | seconds:u64 + nanos:u32 < 1e9 + precision=`SECONDS \| MILLISECONDS \| NANOSECONDS`; сохраняется доступная source precision |
| NetworkIdentity | family=`EVM`, chain_id:UInt256, genesis_hash:Hash32; chain_key — только alias для маршрутизации |
| ProfileRef | network profile, client commit, adapter version, fork rules version, normalization version; зарегистрированный профиль |
| FieldAvailability | `VALUE(T) \| NOT_APPLICABLE \| UNAVAILABLE(reason_code)`; применяется только к полям с определённой семантикой отсутствия |

Отсутствующий scalar и корректный ноль различаются. Proto scalar с обязательным присутствием использует explicit presence и validator. Для `oneof` требуется ровно один вариант; неизвестные enum values, profile/type/schema versions не публикуются как нормализованные данные. Empty list означает, что источник вернул пустой применимый набор; известная ошибка получения не маскируется пустым списком. Независимый verifier полноты не требуется. `UNAVAILABLE` у обязательных transaction details означает ошибку обработки; неприменимость доказывается профилем.

Произвольные JSON, `Any`, map<string,bytes> не являются обходом типов. Bytes разрешены для hashes/addresses, calldata, code, logs data, source encoding и сериализованного транспортного payload. Каждое сетевое расширение имеет конкретную схему и version gate.

Основание: [Protobuf field presence](https://protobuf.dev/programming-guides/field_presence/). Protobuf по умолчанию не проверяет эти доменные ограничения; нужен validator E02.

### 2. Envelope и идентичность

`Envelope={schema_version, network, profile_ref, event_id, producer_id, run_id, source, capture_mode, payload}`.

`source={node_instance_id,writer_epoch,source_sequence}`. Текущий capture_mode=LIVE; recovery сохранённого live события не меняет его mode. SYNC/BACKFILL отложены и не принимаются первым live profile. `payload` — закрытый union ниже. Версия wire schema не выводится из номера Markdown v3.

`BlockRef={number:u64,hash:Hash32,parent_hash:Hash32,timestamp:Timestamp}`. `InclusionRef={network,block_hash,native_origin=BLOCK_BODY,native_position:u64,transaction_hash:Hash32}`. Позиция — native body index, не номер receipt/прихода сообщения. Один tx hash не идентифицирует включение в ветку. Optional legacy receipt — отдельный auxiliary record, не fake body tx.

`ActivationRef={inclusion_ref,producer_id,run_id,activation_id}`; `BlockActivationRef={network,block_hash,producer_id,run_id,activation_id}`. Разные lifecycle события имеют разные event_id; retries сохраняют ID и bytes. Persistent counters, fixed encodings и semantic/public identity boundary — CONTROL_WIRE_CONTRACT. Новый случайный event ID при retry запрещён.

### 3. TransactionRecord

| Поле | Состав и правило |
|---|---|
| inclusion, block_activation | Связь с конкретной веткой и активацией блока |
| native_payload | Ровно один вариант из [реестра типов](docs/TRANSACTION_TYPE_REGISTRY.md); native type проверяется с сетью/fork |
| source_hash | Native Hash32, hash_kind=NATIVE_TRANSACTION; Bor legacy receipt имеет отдельный auxiliary origin |
| raw_encodings | Непустой список `{kind,bytes}`: EVM_LEGACY_RLP / EIP2718_ENVELOPE / BOR_STATE_SYNC_ENVELOPE по native варианту; source bytes без перекодирования |
| execution_projection | Typed effective sender/target/value/input и provenance=`ENCODED \| NATIVE_DERIVED \| NOT_APPLICABLE`; исходные native поля не перезаписываются |
| receipt | Typed outcome, fees/gas, committed logs и применимые network receipt details |
| calls | Упорядоченные execution frames, включая failed/reverted calls |
| actions | Причинные native balance actions, включая fee/refund/mint/burn и компенсирующие действия |
| execution_actions | Типизированные действия исполнения, не сводимые к balance transfer; обязательный SelfDestructAction при его native исполнении |
| state_changes | Итоговый transaction-boundary diff затронутых accounts/storage после rollback, включая реально сохранённые fees/nonce |
| execution_position | operation_index:u64 и execution BlockRef; при отличии от inclusion block связь задаёт профиль |
| capture_status | По receipt/calls/actions/state_changes/network details: CAPTURED либо NOT_APPLICABLE по profile. Известная ошибка — local fatal до READY, не публичная успешная запись с ERROR/пустыми обязательными details |

`ExecutionProjection` — union EvmMessage / NativeProtocolExecution. `Effective<T>={value:T,provenance=ENCODED|NATIVE_DERIVED}`. EvmMessage: sender:Effective<Address20>, target?:Effective<Address20>, value:Effective<UInt256>, input:Effective<bytes>, nonce:Effective<u64>, gas_limit:u64, effective_gas_price:UInt256. Original caps/access/blob/authorization/signature остаются в native_payload. NativeProtocolExecution первого профиля: operation=BOR_STATE_SYNC, record_count:u64; source records — typed Bor payload. Отсутствующие в native протоколе sender/nonce/signature не заполняются нулём и не выдумываются.

`Receipt`: native outcome=`Status(success:bool) | PreStatusStateRoot(Hash32)`; execution result=`SUCCESS | REVERT | FAILURE`; cumulative_gas_used и gas_used по применимости; effective_gas_price, blob_gas_used, blob_gas_price с явным присутствием. Отдельные optional Address20: attempted_create_address и created_contract (только успешное создание); итоговый existence находится в AccountChange. Logs, logs_bloom, source_receipt_encoding с encoding kind сохраняются отдельно от projection. fee_summary и отдельные фактические BalanceEffects заданы в CURRENT_SEMANTICS; summary повторно не суммируется. Pre-Byzantium source root не превращается в RPC status: результат берётся из native execution. Field provenance и границы Bor/BSC — [NATIVE_CAPTURE_PROTOCOL](docs/NATIVE_CAPTURE_PROTOCOL.md). Network details — закрытые варианты ETH/BSC/Polygon.

`BorReceiptDetails.source_log_scopes` — список `{transaction_log_index, scope=SYSTEM_PRE | TX_STATE_SYNC | SYSTEM_POST}` для source logs, атрибутируемых native instrumentation; порядок/bytes исходных logs не меняются. Native system log не выдаётся за действие StateSync tx. Полнота этой provenance проверяется SCOPE-01; отдельная публикация outside-tx событий остаётся optional. Field numbers и generated bindings — E02.

`Log={address,topics:Hash32[],data,transaction_log_index,block_log_index}`. Receipt содержит только committed logs. Reverted log attempts не входят в receipt logs и не являются обязательным отдельным payload первой версии.

`CallFrame={frame_id,parent_id?,execution_order,depth,engine,kind,caller_address?,context_address?,code_address?,msg_sender?,target?,value?,input,output,gas?,gas_used?,outcome,effects_committed,error_code?,revert_data?,attempted_create_address?,created_address?}`. IDs/order/depth/gas — u64; addresses — Address20, value — UInt256, input/output/revert_data — bytes; outcome=SUCCESS|REVERT|FAILURE. error_code, если присутствует, — UTF-8 native diagnostic string в namespace profile_ref, без обещания общего межсетевого enum; revert_data сохраняется отдельно. Текущие engine=EVM|NATIVE_PROTOCOL и kind=CALL|CALLCODE|DELEGATECALL|STATICCALL|CREATE|CREATE2|NATIVE_PROTOCOL. Native protocol frame создаётся только при зарегистрированном source execution boundary. Поля creation/caller уточняет CURRENT_SEMANTICS. Успешный child внутри reverted ancestor имеет effects_committed=false; сортировка по depth не заменяет order.

BalanceAction/BalanceEffect и closed TransactionProtocol operations заданы в CURRENT_SEMANTICS по D62. Cause включает Frame, ExecutionAction и TransactionProtocol. Все фактические gas purchase/return/fee credits сохранены, receipt fee_summary отдельный агрегат. Никакого OTHER для неизвестного source path и двойного учёта transfer callbacks. +10 и −10 остаются отдельными actions при нулевом итоге.

`ExecutionAction={execution_action_id,execution_order,frame_id,payload}`: закрытый union, текущий обязательный вариант — `SelfDestructAction={contract_address,beneficiary,balance_at_entry,effects_committed,deletion_eligible_by_rules,deletion_committed}`. Все scalar имеют presence; addresses/amount соответствуют общим типам. `frame_id` ссылается на исполнявший opcode frame, а не на искусственный CALL. Native pseudo enter/exit для SELFDESTRUCT нормализуются в это действие и не создают второй transfer. `deletion_eligible_by_rules` описывает fork/creation eligibility, `deletion_committed` — сохранённый результат этого действия после rollback/finalization; при effects_committed=false deletion_committed=false. Конечное существование account проверяется отдельно через AccountChange, а не выводится только из opcode. Повторные/отменённые действия сохраняются со своим порядком, даже если сумма равна нулю. Денежная проекция использует `BalanceAction.cause=ExecutionAction(execution_action_id)` (добавленный вариант cause); execution action и его balance projection не суммируются как два изменения. Self-beneficiary/возможный burn следуют native fork rules, не универсальному правилу transfer. [EIP-6780](https://eips.ethereum.org/EIPS/eip-6780). Точные field numbers — E02; смысл полей фиксирован этим контрактом.

`AccountChange={address,existed_before,exists_after,balance_before,balance_after,nonce_before,nonce_after,code_hash_before,code_hash_after,code_before,code_after,storage_reset,storage_changes[]}`. Address20, bool exists/reset, UInt256 balances, u64 nonce, Hash32 code hashes, bytes code. `StorageChange={key:Hash32,before:Hash32,after:Hash32}`. CURRENT_SEMANTICS определяет presence/order/reset. Данные относятся к tx boundary; actions и diff не суммируются как независимые изменения.

### 4. События и передача блока

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

Optional BorLegacyReceiptRecord описан в [POLYGON_LEGACY_CAPTURE.md](docs/research/POLYGON_LEGACY_CAPTURE.md): source receipt/hash/index/encoding, без фиктивной transaction envelope. Его source-only ограничения сохраняются, auxiliary completeness verifier не вводится.

### 5. Lifecycle, finality и поздняя metadata

| Текущее состояние | Событие | Результат |
|---|---|---|
| ABSENT | Applied(A, baseline revision=0) | ACTIVE(A), исходный typed lifecycle snapshot |
| ABSENT | StatusChanged(A, revision>0) | PENDING_APPLIED(A), сохранить последнее полное evidence по revision; не выдумывать TransactionRecord |
| PENDING_APPLIED(A) | Applied(A, revision=0) | ACTIVE(A), исходная запись плюс уже полученное более позднее evidence |
| PENDING_APPLIED(A) | StatusChanged(A, revision выше) | PENDING_APPLIED(A), заменить snapshot более поздним |
| ABSENT / PENDING_APPLIED(A) | Removed(A) | REMOVED(A), tombstone сохраняет смысл отмены до позднего Applied |
| ACTIVE(A) | retry Applied(A), те же bytes | Тот же факт, не новое включение |
| ACTIVE(A) | StatusChanged(A, revision выше) | ACTIVE(A), новое evidence |
| ACTIVE(A) | Removed(A) | REMOVED(A), терминальное для A |
| REMOVED(A) | поздний StatusChanged/Applied(A) | Не реактивирует A |
| REMOVED(A) | возвращение того же блока | Новый Applied(B), B≠A |

Пересечение live/history может создать разные наблюдения и активации одного включения в разных producer/run. Общий registry и подавление повторов не требуются. При доставке Removed раньше Applied ссылка сохраняет смысл отмены; поздний Applied не оживляет удалённую активацию. Revision монотонна внутри объекта lifecycle данного producer/run, не между независимыми загрузками или сетями. Порядок history относительно live не определяет состояние цепочки.

Applied несёт initial lifecycle snapshot с revision=0; неизвестное на этом этапе finality evidence имеет явный профильный статус, не выдуманный finalized. Каждый StatusChanged несёт полный replacement snapshot evidence своей активации, не delta patch, и durable положительную revision. Более старая revision не заменяет новую; одна и та же revision с разным содержимым — конфликт/ERROR, не last-write-wins. Removed терминален независимо от порядка доставки; более поздняя запись не реактивирует A, новый период каноничности имеет другую activation_id. Writer не создаёт новые Status для terminal A. Эти правила задают смысл wire events и тестового валидатора, не добавляют production consumer.

Native bridge отслеживает любую durable активацию с READY Applied, которая может быть опубликована; reorg создаёт Removed даже до первого Publish/PubAck Applied. Факт внешней доставки не угадывается. Прогресс наблюдения имеет batch/activation identity: одного `(run,block_number,block_hash)` недостаточно для двух активаций одной ветки. Applied, lifecycle state и его исходная revision фиксируются атомарно по протоколу WAL. Для событий до READY, не допускавшихся к публикации, применяются правила незакрытой попытки.

Finality — closed EthereumEvidence/BscEvidence/PolygonEvidence по CURRENT_SEMANTICS. D62 различает BSC early votes/header-attested; D63 отделяет native Polygon status от L1 checkpoint GC. Safe/justified/milestone не объявляются универсально эквивалентными finalized; точные поля, availability, ancestry и regression rules определены, E02/E05 проверяют их реализацию.

Поздняя finality передаётся StatusChanged. Отсутствующие обязательные execution details не разрешается выдавать как полную транзакцию с обещанием дослать. Если потребуется изменяемая non-finality metadata, до реализации вводится отдельный typed event/version с fixture; свободного patch JSON нет. Fork-specific L1/batch сведения, недоступные на данном этапе, имеют явно определённый pending статус, не нулевое выдуманное значение.

### 6. Транспорт и порядок

Полный Prepare/Publish/Release, immutable header contract и bounded serialization задаёт [DELIVERY_STATE_MACHINE](docs/DELIVERY_STATE_MACHINE.md). Нормативная единица retry — сохранённый publication plan с destination и exact frame/header bytes, а не повторная сериализация record новой сборкой.

`TransportFrame={transport_version,network,event_id,payload_digest,publication_epoch,chain_sequence,payload}`.

payload — ровно `CompleteRecord{envelope_bytes}` либо `RecordPart{part_index,part_count,total_bytes,data}`. envelope_bytes — bytes конкретной versioned Envelope, прошедшей validator; это транспортное кодирование, не разрешение opaque native payload. Маленькая запись передаётся одним CompleteRecord. Большая — частями с индексами 0..count−1. Каждая часть содержит одинаковые event_id/digest/count/total_bytes. Смешивать complete и parts в одном retry события нельзя.

SHA-256 вычисляется по исходным сериализованным Envelope bytes. Они фиксируются один раз до передачи и повторяются побайтно. Part layout также фиксируется для retry. Digest проверяет транспорт; он не является универсальным tx hash или semantic event ID. [Protobuf serialization is not canonical](https://protobuf.dev/programming-guides/serialization-not-canonical/): повторная сериализация другим runtime/version не обязана дать те же bytes.

Лимит учитывает полную сериализованную TransportFrame и NATS headers, а не только data. D52: default `transport.max_frame_bytes=2097152` (2 MiB) задаётся YAML Gateway и валидируется до отправки; NATS server/stream настроены на этот предел, gRPC учитывает собственную обёртку. Большая нормализованная транзакция передаётся несколькими RecordPart. Изменение лимита относится к новым планам; сохранённые pending bytes не перекодируются. При несовместимом уменьшении лимита — явная ошибка конфигурации до публикации, pending сохраняются до восстановления достаточного лимита. [Подробности](docs/LIVE_CONFIG_SPEC.md). Pending spool без TTL/прикладной квоты по D28. Post-ACK retention вне scope. При невозможности разместить запись — backpressure/явная ошибка и незавершённый блок; обрезать данные запрещено.

Gateway назначает chain_sequence в пределах publication_epoch своего store; все части имеют одну пару epoch/sequence. Epoch сохраняется при restart с диском и меняется при создании нового store. Publication plan и точные frame bytes сохраняются producer до первой публикации; после потери Gateway store replay использует прежнюю пару и bytes. Это порядок регистрации в одном epoch, не block chronology, не NATS stream sequence и не глобальный порядок через потерю диска. Части history/live могут чередоваться. Протокол Prepare/Publish/IngestAck/Release: [DELIVERY_STATE_MACHINE](docs/DELIVERY_STATE_MACHINE.md).

Execution chronology задают block parent links, native transaction position, operation_index и action/frame order. Delivery chronology задают chain_sequence и transport metadata. `part_index` задаёт только сборку bytes. Нельзя сортировкой chain_sequence восстанавливать blockchain chronology.

NATS message ID должен стабильно различать `(network,event_id,complete-or-part-index)`. После PubAck всех сообщений/частей закрытого block batch Gateway sync фиксирует NATS_ACKED и подтверждает adapter batch_id/digest/plan identity. ACK одного event не разрешает очистить весь блок. NATS deduplication window не обеспечивает вечный exactly-once: контракт допускает повторы с тем же ID/bytes. ACK не подтверждает работу downstream consumers. Durable plan/WAL, повторный запуск, потеря состояния и block-batch IngestAck/Release определены в ROCKSDB_DELIVERY_PROTOCOL.

### 7. Маршрутизация

Для каждой сети отдельные streams/subjects/limits: `evm.v3.<chain_key>.<event_family>.<event_kind>`. Текущие family=tx|block; optional balance.system|bor.legacy_receipt. Kind=applied|removed|status. Optional families разрешены только при declared profile feature; default первая версия outside-tx публикацию не включает, её включение требует соответствующих schema/fixtures. Markdown v3 не задаёт wire version.

Полная запись и её части направляются в один subject своего logical event; identity сети из frame проверяется против route. D48: stream и subject prefix задаются в YAML Gateway. D54: logical destination (account/stream/subject mapping) фиксируется в durable publication plan и ACK; изменение назначения допускается только после завершения pending адаптера и Gateway. Смена URL того же назначения допустима после проверки идентичности. Подтверждения разных назначений не образуют общий NATS_ACKED. Перенос pending не проектируется; точная идентификация назначения и handshake/drain checks остаются E03. Post-ACK retention не входит в гарантию сборщика.

### 8. Проверки для последующих этапов

| Fixture | Обязательный результат |
|---|---|
| Failed tx + успешный child в reverted parent | Gas/nonce сохранены по native правилам; child actions не committed |
| Компенсирующие transfers | Оба actions есть, net diff может быть 0 |
| BSC Parlia system | Фактический wire type/signature не потеряны; native Finalize capture проверен |
| DEFERRED D38/D42: Classic native result ↔ Ethereum projection | Все source results учтены; CL-01…08 проверяют archive, декодирование, observer и неизменность native исполнения |
| Bor StateSync / legacy synthetic | Body 0x7f и optional legacy source receipt не смешаны; fixtures POLY-L01…07 из исследования E00-15 |
| Пустой блок / ошибка нормализации | Metadata пустого блока передаётся; известная ошибка даёт ERROR, а не NATS_ACKED |
| Parts reorder/duplicates/corruption | Сборка по индексу, повторы идемпотентны, конфликт bytes/digest отклоняется |
| Live reorg во время публикации; history overlap DEFERRED D38 | Дубли разных runs допустимы; lifecycle references не смешиваются, ACK учитывает все части |
| Неизвестный тип/версия/fork | Явная ошибка нормализации, без фиктивного поддержанного payload |
| Optional balances выключены | Передаётся заявленный transaction scope; полнота всех балансов не заявляется |

Это acceptance matrix будущих E01/E02/E03/E04/E05, не отчёт о пройденных тестах. E02 фиксирует compiled field numbers/closed validators текущих профилей; E03 проверяет delivery и bounded collector memory, без performance SLO. Текущий Bor StateSync обязателен. Историческая выгрузка не входит в active backlog.

## Реестр native типов

### Правила профиля

Ключ варианта: `(network identity, execution profile, native type)`. Один byte type без сети и fork profile недостаточен. Профиль содержит commit клиента, версию адаптера, fork schedule и версию нормализации. Применимость определяется нативными правилами конкретного блока, включая timestamp/height, а не текущими настройками head.

В общей записи нет обязательных для всех типов signature, account nonce, gas price или получателя. Поля располагаются в конкретном варианте `oneof`; отсутствующее по модели поле не подменяется нулём. Типизированная execution projection отделена от исходных полей. Неизвестный тип, неподдержанный fork или отсутствие обязательного поля дают явную ошибку обработки; успешная доставка такого блока не объявляется.

`Q` — UInt256; `A` — Address20; `H` — Hash32; `B` — bytes. `Target` — `Call(A) | Create`; это явное различие, не нулевой адрес. Все поля варианта обязательны, кроме явно отмеченных optional. `[]` — упорядоченная последовательность, в том числе пустая. Нативные более узкие целые дополнительно проверяются на исходную разрядность.

### Стандартные EVM-варианты

| ID / payload | Поля |
|---|---|
| `0x00 EvmLegacy` | nonce:u64, gas_price:Q, gas_limit:u64, target:Target, value:Q, input:B, signature:{v:Q,r:Q,s:Q}; replay_protection=`Unprotected \| Eip155(chain_id:Q)` |
| `0x01 EvmAccessList` | chain_id:Q, nonce:u64, gas_price:Q, gas_limit:u64, target:Target, value:Q, input:B, access_list:AccessEntry[], signature:TypedSignature |
| `0x02 EvmDynamicFee` | chain_id:Q, nonce:u64, max_priority_fee_per_gas:Q, max_fee_per_gas:Q, gas_limit:u64, target:Target, value:Q, input:B, access_list:AccessEntry[], signature:TypedSignature |
| `0x03 EvmBlob` | поля EvmDynamicFee, но target только `Call`; дополнительно max_fee_per_blob_gas:Q, blob_versioned_hashes:H[] |
| `0x04 EvmSetCode` | поля EvmDynamicFee, но target только `Call`; дополнительно authorization_list:Authorization[] |

`AccessEntry={address:A, storage_keys:H[]}`. `TypedSignature={y_parity:0|1,r:Q,s:Q}` относится к внешней typed transaction. `AuthorizationSignature={y_parity:u8,r:Q,s:Q}` и `Authorization={chain_id:Q,address:A,nonce:u64,signature:AuthorizationSignature}` сохраняют исходный authorization tuple. Порядок и дубликаты source lists сохраняются. Нельзя отвергать целую включённую native транзакцию потому, что отдельный tuple имеет parity вне 0/1, неверную подпись, chain_id/nonce или неприменимую authority: native может пропустить tuple и продолжить обработку. Проверки исходной разрядности остаются обязательными; source tuple не заменяется результатом signature recovery. Валидность внешней транзакции и применимость tuple — разные проверки. [EIP-7702](https://eips.ethereum.org/EIPS/eip-7702), [выбранный Geth source](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/types/tx_setcode.go).

Blob sidecars не являются обязательной частью включённой execution transaction: сохраняются поля native body, blob hashes и применимый receipt; доступность blob data не подменяется обещанием полного DA-архива. Fixtures AUTH-01–04 определены в [реестре исправлений](docs/DESIGN_REPAIR_STATUS.md); runtime-проверки ещё не выполнены.

Это библиотека вариантов, а не разрешение всех пяти типов во всех сетях. ETH/BSC исходники определяют ID 0–4; Base/Nitro/Bor используют только варианты, разрешённые их собственными правилами в данном блоке. Fork validator обязателен. Наличие типа в исходнике само по себе не доказывает активацию.

Источники: [Geth types](https://github.com/ethereum/go-ethereum/blob/a579077007b98217c3e253a66e4b452ca0c32b96/core/types/transaction.go), [BSC types](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/types/transaction.go).

### BSC / Parlia

`BscClassification = Ordinary | ParliaSystem`. Это типизированная классификация поверх фактического стандартного wire type; signature, nonce и исходный payload сохраняются. Критерий pinned клиента: recipient является system contract, effective gas price равен нулю, recovered sender равен block Coinbase. Проверяется нативным classifier на конкретном блоке. Нельзя классифицировать все zero-fee транзакции или все вызовы системного адреса как ParliaSystem.

Native `Finalize/applyTransaction` является обязательным capture path наряду с обычным циклом. Источник: [classifier и execution](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/consensus/parlia/parlia.go).

### Polygon / Bor

| ID / payload | Native поля |
|---|---|
| `0x7f BorStateSync` | state_sync_data:StateSyncItem[]; StateSyncItem={id:u64,contract:A,data:B,l1_tx_hash:H} |

Тип после Madhugiri — реальная body transaction, её нельзя исключить как optional вне-транзакционный эффект. Нативный массив кодируется `0x7f || RLP(items)`, без дополнительной RLP-обёртки структуры. `TxHash` элемента — L1 transaction hash, не хеш включения Polygon.

Нет native signature/chain ID/account nonce/gas/calldata обычной транзакции. Network identity остаётся обязательной в envelope. Zero-address значения accessor не обозначают фактические execution sender/receiver: исполнение использует BorSystemAddress и настроенный state receiver; они отражаются в execution projection/calls.

До Madhugiri (Mainnet boundary 80 084 800) отдельный synthetic receipt имеет derived hash `keccak("matic-bor-receipt-" || uint64BE(block_number) || block_hash)` и позицию после обычных receipts. Он не является body transaction типа 0x7f; dummy LegacyTx из helper не превращается в настоящую подписанную транзакцию. E00-15 определяет optional `BorLegacyReceiptRecord` с origin `BOR_LEGACY_SYNTHETIC_RECEIPT`, storage receipt encoding и отдельным inventory: [исследование и схема](docs/research/POLYGON_LEGACY_CAPTURE.md). Body transactions остаются обязательными на всей истории; optional публикация legacy records не означает optional исполнение state sync самим узлом.

Legacy source receipt содержит native status/logs, но не доказывает полноту calls/actions/diff. Writer создаёт receipt только при дополнительных logs; отсутствие записи не доказывает отсутствие system actions. Поэтому scope source receipt и scope полного system execution разделяются. Схема не обязывает включать это расширение вопреки D17.

Источники: [state sync payload](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/types/tx_state_sync.go), [items](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/types/state_data.go), [legacy receipt](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/types/bor_receipt.go).

### Capture engines и граница готовности

Current EngineKind = EVM | NATIVE_PROTOCOL. Native protocol frames требуют явного зарегистрированного source boundary; это не фиктивный EVM call. Payload сохраняет raw source encoding и профильный native type. E01 доказывает реальные capture paths закреплённых клиентов; E02 фиксирует generated schema/validators; E04 проверяет каждую применимую разновидность, включая failed и native system execution. Неизвестный тип — явная ошибка, без skip. Runtime verdict NOT_RUN.

## Точные данные и финальность текущих сетей

### Block, calls и account changes

BlockRef.Timestamp сохраняет source precision. ETH/Bor: `seconds=header.Time`, `nanos=0`, precision=SECONDS. BSC: взять native `MilliTimestamp()`; `seconds=ms/1000`, `nanos=(ms%1000)*1000000`, precision=MILLISECONDS, с checked arithmetic. Pinned BSC вычисляет `Time*1000 + low_u64(MixDigest)`; это правило BSC, не интерпретация MixDigest как времени в Geth/Bor. Raw header.Time/MixDigest сохраняются без изменений. [BSC source](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/types/block.go#L166-L172).

`BlockRecord={block_ref,block_activation,header,source_transaction_count:u64,initial_lifecycle}`; `BlockRef` содержит number:u64, hash/parent_hash:Hash32 и Timestamp. Source count берётся из native body представления выбранного клиента, не из независимого completeness verifier. Пустой блок даёт BlockApplied с count=0. `header.source_rlp` обязателен для текущих EVM profiles; hash из native source сохраняется, собственная перепроверка всего блока не вводится.

Header common fields: parent_hash/uncle_hash/state_root/transactions_root/receipts_root/mix_digest:Hash32; beneficiary:Address20; logs_bloom:bytes256; difficulty:UInt256; number/gas_limit/gas_used/timestamp_seconds:u64; extra_data:bytes; nonce:bytes8. Optional fork fields: base_fee:UInt256; withdrawals_root/parent_beacon_block_root/requests_hash:Hash32; blob_gas_used/excess_blob_gas:u64. Geth дополнительно optional block_access_list_hash:Hash32 и slot_number:u64. Presence определяется native header/fork; отсутствующее поле не заполняется нулём. Bor ActualTime/AbortRecovery — локальные runtime поля, не consensus header и не source RLP; в публичный Header не включаются.

Точные поля проверены по pinned [Geth Header](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/types/block.go), [BSC Header](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/types/block.go), [Bor Header](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/types/block.go). Наличие future-fork поля в source не означает его текущую активацию Mainnet.

CallFrame сохраняет отдельно caller_address (native from), context_address, code_address, msg_sender и target; нельзя подменить одно другим при DELEGATECALL/delegated code. Для CREATE/CREATE2 `attempted_create_address` хранится, когда native определил адрес; `created_address` присутствует только при локальном успешном создании. `effects_committed=false` может отменять локальный успех ancestor revert; final existence берётся из AccountChange. Применимые input/output/gas/revert данные не обрезаются. Root frame id=0, parent отсутствует, depth=0; остальные parent IDs существуют, order соответствует native входу.

AccountChange дополнительно содержит code_hash_before/code_hash_after:Hash32. Before/after code bytes обязательны при изменении; отсутствие account отделено exists-флагами, не пустым code. StorageChange отсортированы по raw key bytes для детерминированной нормализации; каждый key появляется один раз с tx initial/final значением. AccountChange упорядочены по raw address bytes; execution/action lists сохраняют native chronology. `storage_reset=true` обозначает удаление всего прежнего storage без перечисления всех ключей; subsequent writes после reset представлены явно. Общий state diff — только committed tx result. Reverted log attempts не входят в первый обязательный contract; receipt logs остаются source committed logs.

### Balance actions и комиссия D62

`BalanceAction={action_index:u64,execution_order:u64,cause,engine,kind,from?,to?,amount:UInt256,asset=NATIVE,effects_committed,rollback_scope?,balance_effects[]}`. `BalanceEffect={effect_index:u64,address:Address20,before:UInt256,after:UInt256,native_reason_code:u32}`. Reason code интерпретируется только вместе с ProfileRef/pinned native enum; raw unknown code нового профиля не принимается автоматически. BalanceEffect before/after относятся к самому действию; AccountChange — к целой tx.

Cause — закрытый union: `Frame(frame_id)` / `ExecutionAction(execution_action_id)` / `TransactionProtocol{operation,operation_index,related_execution_actions[]}`. Operation registry v1: GAS_PURCHASE, GAS_RETURN, FEE_RECIPIENT_CREDIT, BSC_SYSTEM_EXECUTION, BOR_STATE_SYNC_EXECUTION, SELFDESTRUCT_FINALIZATION_BURN. BSC/Bor варианты допустимы только в source-registered native system path, не как OTHER для неизвестного действия. kind: TRANSFER/MINT/BURN/FEE/REFUND/SELFDESTRUCT_TRANSFER/NATIVE_PROTOCOL_CHANGE; последний сохраняет фактические effects известной native system операции без выдумывания экономической причины. Для нового обязательного source path расширяется versioned registry до выпуска supported profile.

| Действие | Представление / правило |
|---|---|
| Native value transfer | Одна action объединяет debit/credit одного native transfer invocation; effects содержат обе стороны, в том числе self-transfer. Не образовывать две одинаковые transfers из пары callbacks |
| Покупка газа | operation=GAS_PURCHASE, kind=FEE, from=payer, to отсутствует; amount=фактическое списание, один payer effect |
| Денежный возврат | GAS_RETURN/REFUND, from отсутствует, to=payer; amount=фактический возврат. EVM gas refund counter units — другая величина |
| Вознаграждение из комиссии | FEE_RECIPIENT_CREDIT/FEE, from отсутствует, to=native recipient; amount/credit effect точные. Отсутствующий from не означает mint или zero address |
| SELFDESTRUCT | ExecutionAction сохраняет сам opcode, даже при balance=0; связанная monetary action содержит реальные native effects, не второй выдуманный CALL |
| Finalization burn | SELFDESTRUCT_FINALIZATION_BURN/BURN с related action refs и фактическим debit; сам факт отсутствия recipient не доказывает burn |
| Native system tx | Полная tx/calls, known protocol operations и реальные effects. Не добавлять самостоятельный outside-tx поток вместо обязательной tx |

Effects одного transfer invocation собираются по integration operation ID; не угадываются по соседству одинаковых сумм. Каждый captured balance mutation принадлежит ровно одной action/effect. Одинаковый address может встретиться в нескольких effects по native порядку. Native rollback помечает исходные actions неcommitted; техническое восстановление journal не создаёт второй экономический перевод. Нулевой итог tx не удаляет её ненулевые встречные actions. CallFrame.value, ExecutionAction, BalanceAction и AccountChange — связанные представления; они не складываются друг с другом.

`Receipt.fee_summary={payer?,gas_purchase_debit,gas_return_credit,net_charge,recipient_credits[]}`; все суммы UInt256, credits — `{address,amount}` в native order. Для применимой обычной tx `net_charge = gas_purchase_debit − gas_return_credit`, включает фактические native gas/blob charges из этих operations. Zero-fee system tx хранит применимые нули; неприменимый payer отсутствует. Native implicit burn определяется fee rules и сверяется отдельно; не выдумывать дополнительный payer debit поверх уже списанного газа. Receipt blob_gas_used/blob_gas_price и effective_gas_price сохраняются по применимости. Fee summary — агрегат actions, не дополнительное денежное событие.

На fixtures сумма committed **address effects** согласуется с AccountChange каждой tx; сумма action.amount сама по себе не является суммой переводов или комиссией. Это test oracle, не production balance completeness verifier. Native balance reasons подтверждены [Geth](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/tracing/hooks.go), [BSC](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/tracing/hooks.go), [Bor](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/tracing/hooks.go). Данные из hook — основание capture; соответствие всех native paths доказывается E01/E04.

### Общий lifecycle и evidence

`AnchorRef={height:u64,hash:Hash32}`. `NativeDecisionRef={node_instance_id,producer_id,run_id,decision_sequence:u64}`. `EvidenceOrigin=NATIVE_LIVE|NATIVE_RESTART|RETAINED_DURABLE`. `ReferenceObservation=Known{block:AnchorRef,origin}|Unknown{reason=NOT_OBSERVED|NATIVE_REFERENCE_UNSET}`. All-zero hash не кодирует известный genesis; настоящий genesis имеет source hash.

`LifecycleSnapshot={native_decision,observed_execution_head:AnchorRef,evidence}`; evidence — ровно EthereumEvidence/BscEvidence/PolygonEvidence, соответствующее profile. Applied revision=0; StatusChanged={activation_ref,revision>0,snapshot} — полная замена. Removed={activation_ref,revision>0,native_decision,reason=NATIVE_CANONICAL_CHANGE}; revision выделяется из того же activation counter, но terminal смысл не зависит от порядка доставки. Повтор native decision не создаёт новую revision. Projection статуса определяется ancestry точного activation block hash, не одним height.

Неизвестный optional finality reference допустим на Applied; неизвестные обязательные execution details — capture error. Неудачный lookup известного обязательного header/snapshot не превращается в Unknown/genesis. Stale callback сам по себе не меняет snapshot: native bridge под observation guard читает согласованное состояние, сохраняет owned evidence и marker. После restart выполняется refresh и без нового head event; повтор уже сохранённого решения идемпотентен.

### Ethereum evidence

`EthereumEvidence={safe:ReferenceObservation,finalized:ReferenceObservation,safe_basis=CONSENSUS_SETTER|STARTUP_FINALIZED_FALLBACK|UNSET}` плюс common observed head. Known finalized покрывает activation, когда `isAncestorOrSelf(activation.block, finalized)` → ETH_FINALIZED; иначе такое же покрытие known safe → ETH_SAFE; известные references без покрытия → ETH_CANONICAL_UNFINALIZED; отсутствие необходимых references → ETH_FINALITY_UNKNOWN. Unknown safe не отменяет known finalized.

Safe в pinned Geth может восстановиться из finalized после restart и стать ниже прежнего safe; новый snapshot с NATIVE_RESTART допускается. Zero hash в Engine forkchoice input — native no-op, не команда clear. Bridge наблюдает фактические SetSafe/SetFinalized/head mutations, включая их выполнение до возможной последующей FCU ошибки; одного успешного RPC status или ChainHeadEvent недостаточно. GC anchor — только durable accepted finalized на canonical execution ancestry, никогда safe. [Pinned source evidence и seams](docs/research/ETH_BSC_FINALITY_CONTRACT.md).

### BSC evidence D62

`BscEvidence={observed_native_header_head:AnchorRef,header_finality,native_effective_finalized:ReferenceObservation,effective_basis,early_observation}`. effective_basis=HEADER_SNAPSHOT|VOTE_POOL_QUORUM|GENESIS_FALLBACK|UNSET.

`header_finality=AttestedSnapshot{snapshot_head,justified,finalized}|GenesisFallback{snapshot_head,genesis,reason=NO_ATTESTATION|PRE_PLATO}`; все refs — AnchorRef. Это merged native snapshot VoteData.Source/Target, не Source произвольно выбранной последней подписи.

`early_observation=None|EarlyVoteEvidence{origin,evaluation_head,source_justified,vote_target,finalized_candidate,validator_snapshot_parent,validator_count:u64,quorum_count:u64,matching_vote_count:u64,evidence_digest:Hash32}`. Refs — AnchorRef. Bridge до публикации сохраняет owned snapshot фактически проверенных native vote envelopes/validator context в local evidence spool; digest связывает его, но не выдаётся за независимо проверяемый consensus certificate. После restart прежний vote observation может сохраняться только как RETAINED_DURABLE, не выдаваться за текущий VotePool.

Для каждой activation отдельно вычисляются `header_status=FINALIZED_HEADER_ATTESTED|JUSTIFIED|NO_ATTESTED_FINALITY|CANONICAL_UNFINALIZED` и `early_status=VOTE_FINALIZED_OBSERVED|NOT_COVERED|NOT_OBSERVED`: сведения не сворачиваются в один безусловный finalized flag. Header Source должен покрывать activation по ancestry; header head и execution head предварительно согласуются. Early event side branch не финализирует canonical activation. Потеря transient early evidence допустима и не понижает durable GC anchor.

GC anchor — только header-derived AttestedSnapshot.finalized, canonical ancestry и durable evidence. CurrentFinalBlock/LastFinalized hash сами по себе недостаточны: они могут содержать vote-derived результат. Нужен error-aware native snapshot accessor, не пересчёт через public getter, потерявший provenance. [Pinned исследование](docs/research/ETH_BSC_FINALITY_CONTRACT.md).

### Polygon evidence и L1 GC D63

`PolygonEvidence={milestone,checkpoint,milestone_whitelist,checkpoint_whitelist,native_finalized,l1_checkpoint}`. Для этих полей `Evidence<T>=Known(T)|NotObserved|Unavailable{reason=SOURCE_OFFLINE|SOURCE_SYNCING|STATE_UNAVAILABLE|CHECKPOINT_PENDING}`. Unavailable — конкретная попытка optional finality/GC lookup, не замена ошибке обязательного execution capture; не стирает retained accepted anchor. Native malformed/wrong identity — ошибка, не Unavailable.

`HeimdallIdentity={chain_id:string,genesis_document_sha256:Hash32}`; digest относится к проверенному bootstrap genesis JSON, не подменяет block hash. `SourceObservationRef={source_store_identity:Id16,heimdall_identity,observation_id,request_sequence:u64,operation,selector,committed_heimdall_block?,response_digest:Hash32}`. Operation/selector union: MilestoneLatest/CheckpointLatest/CheckpointBuffer/ChainParams/CheckpointParams без selector; MilestoneByNumber/CheckpointByNumber с u64 number; MilestoneEvent с `{height:u64,event_ordinal:u64}`. Optional committed block — `{height:u64,hash:Hash32}`, только если реально получен. Никакого timestamp вместо доказанного source height/hash. Exact responses хранятся local по observation/request, не передаются как arbitrary JSON публичного evidence.

| Evidence type | Точные поля |
|---|---|
| Milestone | source:SourceObservationRef, number?:u64, milestone_id:string, proposer:Address20, start/end/timestamp/total_difficulty:u64, end_hash:Hash32, outcome=OBSERVED\|FUTURE_QUEUED\|LOCALLY_VERIFIED\|REJECTED |
| Checkpoint | source, checkpoint_id?:u64, proposer, start/end/timestamp:u64, root_hash:Hash32, state=BUFFERED\|STORED_ACKNOWLEDGED, locally_verified_end?:AnchorRef |
| Whitelist | kind=MILESTONE\|CHECKPOINT, anchor:AnchorRef, native_decision, source_evidence?:SourceObservationRef, ancestry_binding_digest:Hash32; принят только после canonical match |
| NativeFinalized | basis=MILESTONE_WHITELIST\|CHECKPOINT_WHITELIST, anchor:AnchorRef, whitelist_decision:NativeDecisionRef |
| L1Checkpoint | l1_network:NetworkIdentity, supporting_node_id:Id16, root_chain_address:Address20, checkpoint_id:u64, header_block_id:UInt256, child_block_interval:u64, bor_start/end:u64, checkpoint_root:Hash32, bor_end_ref:AnchorRef, bor_range_binding_digest:Hash32, finalized_l1_anchor:AnchorRef, verification:FinalizedContractState |
| FinalizedContractState | call_block_hash:Hash32, root_chain_params_digest/calldata_digest/result_digest:Hash32, result_created_at:UInt256, result_proposer:Address20 |

Milestone hash — Bor end block hash, checkpoint root — range commitment; они не взаимозаменяемы. Whitelist без исходного сохранённого ответа допускает narrower Known Whitelist с absent source_evidence, не выдуманный checkpoint. Polygon native finalized выбирает milestone whitelist, затем checkpoint fallback; это native observation, которое Bor может пересмотреть при восстановлении. Projection отдельно показывает NATIVE_MILESTONE_COVERED / NATIVE_CHECKPOINT_COVERED / CANONICAL_NO_NATIVE_FINALITY и L1_CHECKPOINT_COVERED / L1_NOT_VERIFIED; без сопоставления block ancestry coverage не присваивается.

Принятый D63 путь — `PolygonCheckpointObserver` внутри Bor adapter, без нового процесса или Ethereum tx extractor. Network reads выполняются вне hooks/native locks. Endpoint собственной Polygon L1-ноды и Heimdall задаётся окружением; expected chain/contract binding проверяется по bootstrap identity/params, не выбирается по случайному RPC ответу.

1. Получить native stored checkpoint и chain/checkpoint params от собственного Heimdall; сохранить exact responses, interval/RootChain address и source identity. Если callbacks не содержат number/height, не изобретать их; для L1 проверки нужен checkpoint ID из полного query.
2. Получить finalized F у собственной Polygon L1 Geth. При отсутствии F сохранить pins и повторить позже; не использовать latest/safe/число confirmations.
3. Вычислить checked UInt256 `checkpoint_id * child_chain_block_interval`; вызвать RootChain.headerBlocks с selector `0x41539d4a` и u256 аргументом через `eth_call` в `{blockHash:F.hash,requireCanonical:true}`.
4. Строго декодировать root/start/end/createdAt/proposer; сверить root/range/proposer с checkpoint и параметры до/после чтения. Проверить canonical ancestry F относительно текущего собственного L1 finalized, не только высоту. Несовпадение нового candidate не повышает anchor.
5. Native Bor verifier вычисляет checkpoint root по закреплённым header inputs и непрерывной canonical range; повторно сверяет end hash/ancestry под observation guard. Только после durable source evidence/decision/accepted anchor разрешён существующий GC protocol.

Own-L1 RPC здесь — служебная finality evidence нативной зависимости Polygon, не источник транзакций extractor. Receipt locator/subscription не требуются; receipt-based вариант в research — справочная альтернатива, не второй обязательный протокол.

При pruning state у F можно сделать **новое** observation с более новым finalized block; сохранённое old evidence не переписывается. Неизвестная сеть/contract, malformed result или конфликт ниже уже принятого anchor — source invariant error. Недоступность L1/Heimdall задерживает GC, обычная публикация готовых Polygon records продолжается. Drain не ждёт будущего checkpoint ради очистки уже завершённой доставки: остающиеся lifecycle/pins — retained native state, не новый delivery pending. Сохранённый незавершённый GC decision разрешается/отклоняется безопасно до seal; новый optional GC poll в DRAIN не запускается.

Checkpoint commits number/time/txRoot/receiptRoot; полный Bor header/stateRoot в leaf не входит. GC опирается на этот commitment **и** проверенную local Bor ancestry/native consensus, не обещает независимый криптографический proof всех state fields. Source fields, exact REST/RPC и граница — [pinned Polygon research](docs/research/POLYGON_FINALITY_CONTRACT.md).

### Provisioning supporting identities

`finality.polygon.sources_file` — строгий UTF-8 JSON без unknown/duplicate fields, комментариев и подстановок. Схема: `{schema_version:1,l1:{deployment_id:Id16,role:"POLYGON_L1_EXECUTION",endpoint:URI,network:NetworkIdentity},heimdall:{deployment_id:Id16,role:"POLYGON_HEIMDALL",endpoint:URI,chain_id:string,genesis_document_sha256:Hash32}}`; JSON-кодирование NetworkIdentity/Id16/Hash32 совпадает с DestinationBinding. Endpoint L1 точно совпадает с `l1_endpoint`, Heimdall — с effective native Bor config. Весь файл имеет `sources_binding_digest=SHA256(file_bytes)`, включённый в обе source evidence; whitespace меняет этот digest, не deployment ID. Evidence хранит digest и проверенные identity fields, поэтому последующая замена файла не меняет прежнюю provenance.

Environment bootstrap один раз создаёт crypto-random Id16 для каждого нового supporting datadir; сохраняет `extractor-deployment-id` внутри корня соответствующего native volume, без использования native DB keys. Эти IDs переживают restart/recreate контейнера с тем же volume. Новый store получает новый ID; клонирование одного ID для двух одновременно работающих stores запрещено. Bootstrap формирует `/etc/extractor/polygon-sources.json` (Compose read-only bind / Kubernetes ConfigMap) из этих IDs и проверенных network/genesis/roles; один и тот же artifact передаётся observer. Потеря identity-файла при существующем store — bootstrap error. После намеренной замены supporting store bootstrap обновляет manifest; старые evidence сохраняют старые IDs, новый source допускается только после identity/ancestry checks, без сброса retained irreversible anchor.

`supporting_node_id` и `source_store_identity` — эти deployment IDs, **не удалённая RPC-аттестация процесса**. Own-source routing обеспечивают private service network, service name и trust/config окружения. RPC проверяет chain/genesis/Heimdall identity, checkpoint и finalized ancestry; одинаковая Ethereum network identity основного и supporting Geth сама не доказывает роль. Никакого `admin` RPC, нового сервиса или вымышленного `getNodeId` не требуется.

### Durable anchor и очистка

Для каждого статуса направление ancestry одно: `isAncestorOrSelf(activation.block, evidence_anchor)`; номер блока сам по себе недостаточен. ETH safe/finalized, BSC attested Source/Target и Polygon locally verified projection применяют именно это направление. GC дополнительно требует соответствующего irreversible evidence и остальных условий ниже.

Authoritative native finalized setter, принявший высоту ниже durable irreversible anchor либо несовместимый hash/branch, — fatal invariant даже при прежнем canonical head. Это новое принятое native решение; его нельзя объявить stale getter/callback. Нулевой no-op, startup fallback/отсутствие RAM marker и delayed observation таким setter decision не являются. Fixture FINAL/GC включает same-head finalized regression и проверяет остановку до очистки/новой публикации.

Общий `AcceptedGcAnchor={network,bor_or_execution_anchor:AnchorRef,native_decision,evidence_digest,profile_ref}`; для Polygon содержит полный L1Checkpoint evidence. Native-only callbacks/safe/early votes/whitelist не заменяют указанный для сети anchor. Его принятие и последующее освобождение refs/pins — отдельные sync decisions; payload ACK не является GC.

Удаление требует одновременно: canonical-finalized либо terminal removed entity находится в покрытой anchor области; отсутствуют относящиеся pending capture/transition/replay/delivery; остаются достаточные compact anchor/ancestry/decision-frontier records. Noncanonical ветка ниже anchor освобождается только при доказанном расхождении с закреплённой canonical ancestry. Live pending pin никогда не снимается только из-за высоты. При отсутствии доказательств сохранить state, без TTL/угаданных confirmations.

Fatal regression — **новое authoritative canonical решение**, не содержащее сохранённый irreversible anchor либо имеющее другой hash на его высоте. Stale callback, lower safe, пропавший VotePool, отстающий Heimdall или недоступный header не являются доказанным regression. Они требуют refresh/retry или блокируют GC. После подтверждённого конфликта нода останавливается по D57; удалённое по принятой финальности прошлое состояние не обещается восстановить.

### Проверки текущей семантики

DATA-01: все применимые header/call/account fields, presence/ноль, пустой блок, CREATE attempt vs success, storage reset. FEE-01: gas purchase/return/recipient credits, blob charge, same payer/recipient, zero-fee system tx, revert и +10/−10; суммы address effects согласованы с tx state, receipt aggregate не удваивает поток. FINAL-ETH: safe fallback, setter до FCU error, finality-only без head, stale event. FINAL-BSC: header snapshot/early VotePool отдельно, restart loss, side branch, source lookup failure. FINAL-POLY: milestone purge, persisted whitelist без source reply, L1 pending/pruned state, exact hash-pinned call/root/ancestry, checkpoint params drift, native publication при unavailable GC. GC-01: crash каждого accepted-anchor/pin-release marker, active pending, removed branch, stale observation vs доказанный конфликт. Всё это будущие E01/E02/E04/E05 fixtures, не результаты реализации.

## Native capture и lifecycle recovery

Exact current data/finality/GC semantics — [CURRENT_SEMANTICS](docs/CURRENT_SEMANTICS.md), D62/D63. Semantic identity и связь decision→batch — [CONTROL_WIRE_CONTRACT](docs/CONTROL_WIRE_CONTRACT.md). GC не использует generic native finalized getter без provenance.

2026-11-05 · DOC-REPAIR-03 · D57/D58. Нормативное уточнение [архитектуры](docs/COLLECTION_ARCHITECTURE.md), [модели изменений](docs/BALANCE_MODEL.md) и раннего описания capture в [RocksDB protocol](docs/ROCKSDB_DELIVERY_PROTOCOL.md). Это проект native patches; наличие подходящих upstream API не доказывает работу будущей сборки. E01/E04/E05 остаются обязательными gates.

### Поддержанный профиль и владельцы

Первый профиль: обычный последовательный native import, hash state scheme и durable pin provider. Path backend до отдельного доказательства восстановления не поддерживается. Полный архив не требуется. Любая настройка pruning/сжатия/замены state, способная удалить данные живого pin, несовместима; preflight проверяет effective native config до нового capture. Нельзя молча включить менее надёжный режим.

| Клиент / source commit | Execution seam и дополнительные пути |
|---|---|
| Geth `9dffe6e976cbbb87a84c5fd395448f0950290def` | До `processor.Process`; `EXEC_READY` после native validation, до commit state/head; Engine forkchoice/SetCanonical также проходят decision bridge |
| BSC `cdb7548b5baacfdae92f9f63437d6456411665f3` | Import seam и `Parlia.applyTransaction`, включая actualTx substitution, nonce/finalise; отдельные native finality setters |
| Bor `a16f123d9b4f41a552a3a98d93a0340430838576` | Активный `processBlockForImport → ProcessBlock`, обычный commit; canonical/whitelist paths, StateSync и native Heimdall resolver |

Bor baseline отклоняет `EnablePipelinedImportSRC`, `EnableParallelStatelessImport`, `EnablePreconfs` и принудительный parallel processor. Проверяются effective config и реальные execution paths, не только названия CLI flags. `WaitForPipelinedStateCommit` не используется как durability barrier: выбранный source возвращает nil также по timeout. [Bor blockchain.go](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/blockchain.go).

D60 отдельно отключает BlockSTM/parallel EVM: `--parallelevm.enable=false --parallelevm.enforce=false`. Это не автоматическое следствие D58; пользователь подтвердил последовательное исполнение транзакций. Спекулятивные parallel attempts не входят в первый capture-профиль.

Один observation guard сериализует операции данной ноды, меняющие capture/canonical/finality state. Порядок locks: observation guard → native locks. Вложенный native вызов передаёт guard token, не захватывает его повторно. Worker не берёт native locks и не читает mutable VM/StateDB; он владеет только переданными immutable chunks. Полная карта callers и проверка deadlock входят в E01, нельзя считать обычный chain event feed достаточным.

### Admission и source pin

`observation_id=(producer_id,run_id,observation_sequence)` относится к native наблюдению, а не к transport batch. Observation counter выделяется durable; отсутствие публичных событий у rejected/side-branch observation не создаёт дыр в delivery sequence. Последний выделяется отдельно по [DELIVERY_STATE_MACHINE](docs/DELIVERY_STATE_MACHINE.md).

1. На native boundary проверить block/profile и необходимые до исполнения native условия. Начать `INPUT_PREPARING`; сетевой кандидат ещё не считается принятым capture.
2. Защитить parent root от native удаления. Для hash provider: удержать root в trie cache, выполнить `triedb.Commit(parentRoot,false)`, затем `nativeDB.SyncKeyValue()`. RAM `Reference` без flush/sync не является durable pin.
3. Сохранить pin ledger и input capsule фрагментами. Закрывающий sync `ADMITTED` с manifest/digest разрешён только после durable native state и inputs. До ADMITTED capture hooks/Process не выполняются.
4. Pin содержит native_store_id, chain/genesis, source/profile, state scheme, parent hash/root, обязательные header/code/trie/consensus input references. Pruning/delete paths обязаны учитывать ledger. После освобождения pin физическое удаление не обязано быть мгновенным; доступность старого состояния больше не обещается.

Сбой до ADMITTED допускает уборку только неопубликованного INPUT_PREPARING после сверки ссылок. Сбой после ADMITTED сохраняет обязательство завершить/разрешить наблюдение. Native datadir с другой identity при сохранённом WAL — ошибка, не новый H. Неподдерживаемый provider не заменяется вызовом `Journal`: path Journal предназначен для shutdown и в выбранных fork может переводить store в read-only. API: [Geth triedb](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/triedb/database.go), [SyncKeyValue](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/ethdb/database.go).

Input capsule разделена на sealed static-input manifest при ADMITTED и append-only native dependency log. Static manifest: исходные header/body/encodings, parent root/hash, chain config/fork digest, native commit и normalization profile, заранее известные ancestor headers/BSC snapshot dependencies. Log: request sequence, exact request digest, response chunks/digest и durable consumed frontier. Итоговый consumed-input manifest/digest закрывается при EXEC_READY; именно на него ссылается native decision marker. ADMITTED не объявляет ещё неизвестные внешние ответы уже сохранёнными. Внутренние offsets: observation_sequence, attempt_no, raw_record_sequence, raw_chunk_sequence, exec_ready_digest, native_decision_sequence; они не заменяют event/source/publication sequences.

Bor recording resolver сохраняет exact request identity и exact response/digest sync **до передачи результата native execution**. Повтор уже использованного запроса читает сохранённый ответ, не актуальный новый ответ Heimdall. Ещё не завершённый запрос после crash может требовать доступного Heimdall; полностью offline replay всех ADMITTED не обещается. Нет обязательного ответа — нет EXEC_READY; retry либо явная source error, без пропуска. Запрос зависит от native state, поэтому универсальный prefetch только из header не предполагается. [Bor Finalize/CommitStates](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/consensus/bor/bor.go).

При retry request sequence начинается с первого логического запроса observation. Сохранённый prefix проверяется по exact request identity; несовпадение не разрешает подставить другой ответ под прежний offset. Новый ответ append разрешён только за durable frontier. Crash после записи ответа до его передачи безопасно повторяет тот же ответ; поэтому log frontier отражает разрешённую передачу, а final manifest подтверждает фактическую последовательность завершённого execution.

### Durable состояния и native decision bridge

| State | Условие закрытия | Восстановление |
|---|---|---|
| INPUT_PREPARING | Capsule/pins ещё неполны | Проверить ownership; завершить подготовку либо убрать безопасные orphan fragments |
| ADMITTED / EXECUTING | Source pin и input manifest durable | Новая capture attempt из тех же inputs; старый source не освобождать до durable замены |
| EXEC_READY | Полный immutable normalized record corpus, receipts и snapshots, native validation прошла | Сохранить corpus; разрешить/восстановить native decision; native-only replay допустим для утраченного ещё не committed native state |
| NATIVE_DECIDED | Native authoritative marker durable | Идемпотентно создать lifecycle transition и delivery intent |
| DELIVERY_STAGING / READY | Registry/revisions и closing READY согласованы | Доставка по своему автомату; native execution не повторять |
| NATIVE_REJECTED | Авторитетный native rejection, включая validation failure | Не создавать Applied; закрыть observation и освободить неиспользуемые pins |
| FATAL_CAPTURE_ERROR | Нельзя безопасно сохранить/нормализовать данные | Остановить native процесс; сохранить восстановимые данные, не выдавать consensus-invalid |

EXEC_READY содержит **record corpus без публичной activation/event identity**, а не уже опубликованный Envelope. Спекулятивный branch не публикуется сам по себе. Только native canonical/lifecycle решение определяет activation, revisions и public events. При повторном применении той же branch возможен reuse corpus с новой activation.

Canonical decision сначала закрывает semantic descriptors: activation refs/revisions/source_sequence/kind и stable entity ordinals. Decision→batch allocation atomic; только затем DELIVERY_STAGING выделяет event IDs и строит Envelope. Retry этой стадии не меняет semantic IDs/revisions; повтор native marker находит прежний batch либо retained applied-decision frontier.

Crash после EXEC_READY до native state commit может уничтожить mutable StateDB. Corpus не заменяет полное native state: optional system scopes могут в нём отсутствовать. Для завершения штатного import разрешён native-only replay из parent pin и сохранённой capsule; он не заменяет corpus и не создаёт новые public IDs/bytes. Сравниваются profile/input digest, native resulting state root/receipt commitments и применимые captured outcomes. Несовпадение — source invariant error. READY/SEALED transport replay сам по себе native execution не требует.

Перед authoritative native decision marker выполняется durable child-root barrier: штатный StateDB commit, flush получившегося trie root через поддержанный hash provider и nativeDB.SyncKeyValue. Sync только KV metadata не сохраняет ещё не выгруженный trie cache. Пока child root/decision не durable, parent pin остаётся для native-only replay; при READY transport bytes никогда не регенерируются даже если отдельное native recovery требует исполнения. Этот порядок проверяется power-loss fixture без сохранённой RAM.

Перед native canonical/finality mutation адаптер sync сохраняет transition intent. Native patch добавляет в завершающий authoritative native DB batch marker `{native_store_id,decision_sequence,observation_id,old_head,new_head,decision_kind,evidence,input_digest,exec_digest}` и выполняет SyncKeyValue. После этого адаптер строит ограниченными фрагментами mirror: activation refs, revisions, Removed/Applied/Status descriptors и delivery payload. Closing sync batch атомарно делает новую registry generation и READY видимыми; большой reorg не обязан помещаться целиком в WriteBatch. После durable mirror native marker удаляется отдельным sync шагом.

Повтор marker/intent узнаётся по decision_sequence и digest и не создаёт вторую revision. Адаптер хранит applied-decision frontier; уборка payload по ACK этот frontier не удаляет. Сбой после READY до удаления marker вызывает проверку уже завершённого mirror. Для decision без публичных событий всё равно фиксируется durable decision checkpoint; пустой transport batch не требуется.

Многошаговая native перестройка canonical mappings до конечного marker не считается завершённым решением. Startup сначала выполняет штатное native DB recovery; затем сопоставляет pending intent, восстановленный head/finality и marker. Отсутствие marker не доказывает успех: bridge либо завершает допустимую native операцию из intent, либо фиксирует её native rejection. Если исход нельзя однозначно установить по native данным, остановка с source invariant error; запрет угадать новый head и забыть intent. Атомарность конечного native decision/marker для каждого caller — обязательный integration gate E01/E05.

### Owned capture и bounded RAM

Hooks не выполняют дисковые/сетевые операции, не ждут worker и не передают borrowed pointers за пределы callback. Native safe point **до hook/до mutation** подготавливает owned snapshot. Пока исходный VM view стабилен, крупное поле копируется ограниченными chunks; между chunks runner может ждать writer по D58. Пауза не меняет native результат и не выполняется внутри hook. Native heap/state cache — отдельная память самой ноды; bounded collector RAM не означает ограничение общего VM heap.

Raw spool: BEGIN_FIELD/CHUNK/END_FIELD, sequence, length, digest. Worker обрабатывает только owned chunks. Нет полной копии всей транзакции, manifest или одного большого byte field в памяти сборщика. Общие chunk/control/WriteBatch границы заданы в автомате доставки. Для before/touched/parent indexes используются disk-backed keys и ограниченные страницы.

| Наблюдение | Точка чтения / правило |
|---|---|
| existence и initial account values | Explicit pre-mutation bridge перед первым CreateAccount/CreateContract/balance/nonce/code/storage/selfdestruct mutation; первый before по raw sequence |
| storage/code bytes | Snapshot до соответствующего изменения, chunks owned; не читать позднее изменившийся StateDB из worker |
| rollback | Native snapshot/revert scope IDs и execution order; attempted actions сохраняются, committed flags вычисляются по scopes |
| tx after | После native tx Finalise, до следующей tx/system scope; native thread перечисляет touched keys страницами и читает конечный StateDB |
| deletion | Отдельно fork eligibility, native finalization и итоговый exists; нулевой balance/code не заменяет exists |
| EIP-7702 | Authorization processing входит в tx scope до root call; surviving authorization changes не отменяются только из-за root revert |

Существующие balance/code hooks недостаточны: [Geth statedb_hooked](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/state/statedb_hooked.go) делегирует создание account без полного нужного bridge. Патчи должны покрывать также finalization deletion и no-op/empty account cases. Самостоятельный production completeness verifier этим не вводится.

### Bor/BSC boundaries и receipt provenance

Bor сохраняет native порядок: `SYSTEM_PRE(checkAndCommitSpan) → TX_STATE_SYNC(CommitStates) → SYSTEM_POST(block-alloc changeContractCodeIfNeeded) → receipt construction`. Before/after обязательной StateSync tx снимаются вокруг CommitStates, **after до block-alloc**. Receipt присоединяется после штатного формирования. Native операции не переставляются; optional публикация outside-tx scopes не влияет на их native исполнение. Source receipt bytes/logs сохраняются точно; protocol-origin logs при необходимости имеют отдельный typed Bor provenance, а не переписанный receipt. [Bor source](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/consensus/bor/bor.go).

BSC system tx capture использует реальные hash/index/payload после actualTx substitution в Parlia.applyTransaction. Final after берётся после native nonce/finalise, до выхода из tx boundary. [Parlia source](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/consensus/parlia/parlia.go).

| Поле projection | Источник и различие |
|---|---|
| effective_gas_price | Native Message.GasPrice и активные fee rules; не неинициализированное поле receipt struct |
| gas_used / cumulative | Native receipt execution accounting; не сумма guessed call gas |
| execution failure/revert | Native root execution result + receipt status; Go callback err не единственный признак EVM failure |
| attempted CREATE address | Execution attempt, может существовать при failed constructor |
| created contract | Только успешное native создание; отдельно final existence после rollback/SELFDESTRUCT |
| source receipt | Исходное кодирование сохраняется отдельно от производной projection; адрес попытки не переименовывается в успешное создание |

[Geth state_processor](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/state_processor.go) — источник makeReceipt; публичные поля задаёт [MESSAGE_CONTRACT](docs/MESSAGE_CONTRACT.md).

### Startup, known branch и lifecycle GC

До нового import: compatibility preflight → native/adapter store identity → установка hooks/decision bridge в RECOVERY_ONLY → local recovery ADMITTED/EXEC_READY/decision markers → reconciliation сохранённых activations с native canonical/finality. В RECOVERY_ONLY разрешены сохранённые obligations, но не новые live admissions. READY delivery идёт независимо от новой синхронизации. После initial native sync под observation guard завершаются текущие mutation, читается H/hash, sync сохраняется live boundary и admission переключается в LIVE для H+1. Hooks уже установлены; sync до boundary не создаёт публичный capture. Нельзя поставить H двумя асинхронными подписками с окном между ними. DRAIN остаётся RECOVERY_ONLY.

Known block/side branch может стать canonical без execution callback. До подтверждения canonical transition bridge проверяет наличие соответствующего corpus; при отсутствии выполняет targeted native replay нужной ветки с удержанными inputs/state. Это исключение для live branch/recovery из v2, не history CLI и не сканирование downtime gap. Недоступные необходимые inputs/state дают source error, не silent skip/new H. Данные ветки должны быть закреплены до native операции, которая сделает их необходимыми для принятого transition; pin после pruning не восстанавливает потерянное.

При restart сверяются только сохранённые obligations/activation refs и текущая native evidence. Новые данные периода простоя не разыскиваются. Finality change обрабатывается и без следующего block event. DRAIN восстанавливает прежние obligations и доставку; новый live boundary/admission не создаёт.

ACK payload не удаляет lifecycle refs, revisions и нужные replay pins. GC требует durable terminal evidence (включая окончательную native finality для активной записи), отсутствия связанных pending transitions/replay/delivery и irreversible anchor поддержанного native profile. Одного Removed без такого anchor недостаточно для уборки всех branch dependencies. При отсутствии anchor refs/pins сохраняются; TTL/произвольное число confirmations не подставляются. Regression ниже уже принятого irreversible anchor — source invariant error. После закрытия pin replay именно этого старого corpus больше не обещается.

### Проверяемые gates

| Fixture family | Обязательное доказательство |
|---|---|
| CAPTURE-01 | Power loss на каждом pin/capsule/ADMITTED/EXEC_READY/native marker/READY/cleanup шаге; нет пропуска или false Applied |
| PIN-01 | Потеря RAM trie cache, pruning живого pin, wrong datadir, несовместимый path backend; replay либо preflight reject |
| OWNED-01 | Overlap CALL input/return memory, задержанный worker, две tx на один key, единичное поле больше RAM буфера |
| EXIST-01 | Empty CREATE, create/delete/revert, существующий пустой account, zero changes; before/after точной tx |
| SCOPE-01 | Bor StateSync + block-alloc; BSC system actualTx/nonce/finalise; outside-tx optional выключен |
| RECEIPT-01 | Failed/success CREATE, constructor revert, fee variants, included failed tx, source encodings |
| BRANCH-01 | Known block без callback, targeted replay, reorg и finality во всех окнах H bootstrap; без gap scan |
| DECISION-01 | Много-блочный reorg, marker cleanup crash, finality за downtime без нового head; повтор не увеличивает revision |
| PROFILE-01 | Отказ всех запрещённых Bor paths/config overrides, отсутствие deadlock и неизменность native execution results |

Source inspection выполнена; сборки, fault injection и эти fixtures пока не запускались. При невозможности pin/decision bridge хотя бы для одного обязательного path профиль не принимается; ослабление guarantees требует нового решения пользователя.

## Durable delivery и смена назначения

Точные identities, binding JSON/digests, manifest/reservation bytes, wrapper layout, RPC/errors и исполнимый cutover заданы в [CONTROL_WIRE_CONTRACT](docs/CONTROL_WIRE_CONTRACT.md). Ниже — автомат состояний этого же протокола, не отдельная альтернативная схема.

2026-11-05. D26–D33, D52, D54, D57–D59. Этот документ уточняет и заменяет неоднозначные переходы [ROCKSDB_DELIVERY_PROTOCOL](docs/ROCKSDB_DELIVERY_PROTOCOL.md). Только технический проект; реализация/crash fixtures не выполнены. Native admission и source retention — отдельный контракт; сюда поступают durable capture obligations, затем immutable Envelope bytes.

### Identity, режимы и atomicity

`batch_id=(producer_id,run_id,batch_seq)`. batch_seq выделяется атомарно с durable **delivery intent** для уже определённого native canonical/lifecycle результата; отсутствие delivery intent означает отсутствие выделенного batch. Ранний native admission имеет отдельный observation_sequence: speculative/отклонённый native input не расходует batch_seq. Повтор сборки delivery STAGING сохраняет batch_id, увеличивает attempt_generation и резервирует свежие event_sequence. Номера event могут иметь дыры; номера существующих batch не бросаются без завершения. CAPTURE_STAGING относится к native observation, DELIVERY_STAGING — к batch; ни одно не публикуется.

Режим producer: `LIVE | DRAIN | DRAINED | BLOCKED`. `LIVE` разрешает новые capture после native barrier H. `DRAIN` восстанавливает только уже сохранённые обязательства, доставляет их и завершает release. `DRAINED` сохраняется durable; обычный restart без явного разрешённого возврата в LIVE не начинает новый capture. BLOCKED сохраняет причину, если запись возможна; отсутствие места не должно мешать самому прекращению небезопасной работы. Identity/counters/lifecycle references не удаляются при drain.

Один serialized writer на store. Все изменения recoverability/прав на удаление и ответы, разрешающие следующий шаг, имеют sync commit; отдельно fsync memtable на каждой отправке не требуется. Данные/chunks могут записываться несколькими bounded WriteBatch, но закрывающий marker атомарно связывает manifest/identity/checkpoint. Незакрытый marker не разрешает публикацию.

Producer prefixes: `meta/{identity,counters,destination_binding,retired_prefix,mode}`, `batch/<seq>/intent`, `batch/<seq>/attempt/<generation>/...`, `ready_manifest`, `plan/<seq>/{header,chunks,SEALED}`, `cleanup/<seq>`, `release_outbox/<seq>`, `cutover/<generation>`.

Gateway prefixes: `meta/{store_epoch,destination_binding}`, `session/<producer,run>`, `retired_prefix/<producer,run>`, `batch/<id>/{prepare_chunks,reservation,frame_chunks,CLOSED,ackset,NATS_ACKED}`, `sealed_generation/<producer,run>`, `cutover/<generation>`. Prefixes — logical contract, физическое кодирование/field numbers фиксируются до E03.

### Таблица переходов

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

IngestAck проверяет batch/manifest/plan/destination; ACK части либо успешный gRPC write не заменяет его. Перед удалением любых recovery fragments должна существовать durable замена либо достаточные replay prerequisites. Старый attempt не смешивается с новым manifest. Любые bytes под существующим immutable event_id неизменны, даже до первой публикации.

До DELIVERY_STAGING уже durable semantic descriptors с activation/revision/source identity; retry attempt меняет только public event reservations. Decision→batch_id и batch counter сохраняются атомарно, повтор native marker не выделяет новый batch. В ACKED/outbox сохраняется весь IngestAck, включая issuer_store_epoch и layout_manifest_digest, до producer cleanup; он требуется RecoveryRelease после Gateway loss.

### Prepare и повторные попытки

Prepare передаёт описания immutable Envelope: event_id, digest, byte length, event family/kind и ordinal. Весь manifest не помещается в один RPC/WriteBatch. Каждый chunk имеет стабильные offset/digest; closing manifest digest фиксирует порядок и состав. Повтор с тем же batch_id/manifest_digest возвращает сохранённую reservation. Другой digest после READY — ERROR.

Reservation seed: `store_epoch,reservation_id,manifest_digest,event_count,sequence_base,destination_binding_digest,frame_limit,layout_version,header_version`. Событие получает chain_sequence=sequence_base+ordinal; это порядок регистрации, не blockchain chronology. Counter overflow проверяется до commit, значения не переиспользуются.

Layout определяется Gateway по фиксированной версии алгоритма и фактическим длинам Envelope/headers. Producer сериализует frames и перед SEALED проверяет actual byte size. Изменение текущего лимита не меняет существующую reservation. Пока producer plan не SEALED, ни одна его часть не могла публиковаться. При потере Gateway такой незакрытый plan строится в новой plan generation после durable отказа от старой неопубликованной попытки; recovery inputs остаются. SEALED всегда повторяется с прежними epoch/sequence/layout/headers/bytes и не проходит новое распределение sequence.

Partial uploads возобновляются по сохранённым offsets. Orphan PREPARING/PREPARED удаляется только через fenced owner handshake, доказывающий, что SEALED/публикации не было; неизвестность не является разрешением очистки. Потерянный producer означает ручную диагностику/abandon по прежнему контракту, не таймерное удаление CLOSED payload.

### Release и потеря Gateway store

Публикация/ACK разных batch может завершаться вне порядка. **Release идёт строго по batch_seq, один in-flight Release на run.** Поздний ACK не перепрыгивает ранний незавершённый batch; поздние outbox/receipts хранятся на диске до своей очереди. Producer навсегда в рамках run сохраняет только compact retired_prefix, не архив payload/digests.

Handshake перед replay передаёт authenticated producer/run identity, destination binding и durable retired_prefix. Новый Gateway принимает этот prefix как baseline только от текущего fenced owner; затем восстанавливает pending >prefix. При потерянном ReleaseAck Gateway может быть на один Release впереди producer: это штатный случай, producer завершает свой ACKED outbox по повторному ReleaseAck, а не получает ошибку за меньший prefix. Gateway не откатывает свой prefix. При восстановлении от producer prefix, который выше локального Gateway, это owner recovery assertion о завершённых Release; старые операции сначала fenced, затем baseline устанавливается sync. Конфликт с локальным неподтверждённым producer batch, другой identity/generation или недоказанный произвольный скачок — ERROR, не удаление pending. Lost ReleaseAck разрешает повтор Release; уже удалённые batch 1…N повторно не требуются.

Prepare/Publish с seq<=retired_prefix возвращает ALREADY_RETIRED, не создавая keys. Producer не признаёт неподтверждённый batch доставленным по этому ответу: он применим только к локальному ACKED/outbox либо уже retired. Конфликт с неподтверждённым state — ERROR. Архив старых digests для сравнения намеренно не создаётся.

Если Gateway потерял store после IngestAck, producer уже мог удалить payload, сохранив ACKED/outbox следующего N при prefix=N−1. Для отсутствующего batch разрешён RecoveryRelease ровно N от текущего authenticated/fenced producer: его durable ACKED/outbox assertion содержит batch/plan/binding identity и полученный IngestAck. Gateway sync принимает assertion, затем продвигает prefix и отвечает ReleaseAck без требования повторить удалённый payload. Произвольный скачок, unACKed producer и конфликт существующей локальной записи запрещены. Это продолжение уже завершённой доставки, не новое подтверждение от NATS. RELEASING marker сохраняется до конца всей bounded уборки; отсутствие части keys после crash не повод публиковать снова.

### Fencing

Gateway boot_nonce меняется при каждом процессе; producer хранит монотонную connection_generation. Challenge-response handshake аутентифицирован mTLS/UDS owner и связан с network/role/producer/run/binding; старый challenge не принимается в новом boot. Session switch и DB mutation проходят общий serialized ordering. Проверка token выполняется вместе с admission/commit, а не только при чтении RPC.

Internal publisher worker имеет publisher_generation и работает только с существующим CLOSED batch. PubAck callback связан с batch_id/plan_digest/publisher_generation, не создаёт отсутствующие записи. Release и SealGeneration ставят barrier: старый callback не может воскресить state. Уже ушедший NATS запрос может дать допустимый дубль; нельзя обещать отозвать отправленные сетевые bytes.

### DestinationBinding до первого capture

Окружение создаёт общий immutable descriptor `DestinationBinding={destination_id,generation,network,expected_account,stream,subject_prefix,descriptor_digest}`. Оба процесса получают ссылку на него в YAML и сохраняют его до любых новых обязательств. Адаптер не получает NATS credentials. Первый offline capture уже имеет заданное назначение; это не онлайн-зависимость от Gateway/NATS. Потеря Gateway store не меняет binding READY/SEALED producer.

Gateway при подключении проверяет account, StreamInfo, subjects/limits и binding metadata, заданные environment bootstrap; URL не является identity. Ответы UserInfo/AccountInfo/StreamInfo и необходимые request/reply permissions описывает deployment-контракт. Недоступная проверка — publication blocked; заведомое несовпадение — CONFIG_BLOCKED, без переназначения pending. Замена URL/credentials допустима только после проверки того же binding/account/stream/route. Потеря NATS store после ACK по-прежнему вне гарантии D30–D31; bootstrap не маскирует её автоматическим разрушительным recreate/reset.

### Drain и cutover D54/D57/D59

1. Остановить LIVE штатным способом и подтвердить отсутствие старого writer. Запустить ту же совместимую native сборку с `--extractor-mode=drain`; YAML содержит тот же store/binding. Перед recovery sync установить DRAIN, если он ещё не сохранён. DRAIN не включает новый H+1 capture и не выполняет sync-range выгрузку.
2. Восстановить сохранённые capture/lifecycle intents; новые непредусмотренные live callbacks не создают обязательств. Доставить READY/SEALED; закончить ACKED_CLEANUP и ordered Release. Native источник нужен для незакрытого capture; его недоступность не даёт DRAINED.
3. Только после исчерпания native obligations, ещё способных создать batch, producer фиксирует drain_id/highwater и отправляет SealGeneration(binding_generation,admission_generation,drain_id,highwater). Gateway barrier проверяет отсутствие незавершённых операций <=highwater и совпадение retired_prefix. Sync SEALED_GENERATION запрещает Prepare этой admission generation; повтор возвращает тот же SealAck. Retained pins, ожидающие будущего finalized anchor, не требуют нового capture и не блокируют DRAINED.
4. Producer sync сохраняет DRAINED с SealAck и завершается. Crash до этого продолжает DRAIN; новый LIVE не включается автоматически. Оба owners сохраняют identity/lifecycle, terminal prefix и seal evidence, очищают временные delivery payload/receipts. «Оба журнала очищены» не означает удалить DB/ключи.
5. Для смены destination обе роли получают desired descriptor следующей generation через binding_file; old binding/seal/drain_id берутся из store. Gateway desired mismatch входит в AWAIT_SWITCH, сохраняя прежнее active назначение. Native explicit LIVE sync создаёт SwitchIntent; PrepareSwitch → producer binding commit → CommitSwitch завершают переход. Затем Resume до снятия DRAINED/фиксации H. Для прежнего binding Switch пропускается, Resume обязателен. Необъявленные YAML keys old/new/drain_id не вводятся.
6. Старые SEALED generations не принимают новых данных. Упавший процесс завершает сохранённый switch, не угадывает нужную сторону по более свежему YAML. Если последний owner store потерян, операция требует восстановления seal/binding evidence от surviving owner; при недостатке evidence — ERROR, не bypass.

D59: будущие Removed/Status сохраняют activation references, даже если исходный ACKed Applied был в прежнем назначении. Уже подтверждённая история не копируется. Новому stream не обещается самостоятельная полнота старых ссылок; downstream/copy вне scope.

Resume использует отдельную монотонную admission_generation, не подменяет binding generation или connection_generation. Producer sync сохраняет ResumeIntent(old_seal,new_admission_generation,binding_digest), Gateway под barrier sync принимает ровно следующую generation, producer sync сохраняет LIVE после ответа. Повтор intent возвращает тот же результат. Capture заблокирован до финального producer commit. Все mutating RPC/session tokens привязаны также к admission_generation; поздние RPC старого seal отвергаются. При прежнем назначении binding не меняется. Crash между ответом Gateway и producer commit продолжает тот же ResumeIntent, а не увеличивает generation повторно. Store loss восстанавливает generation/intent от surviving owner до admission.

Дополнительные FRONTIER/DRAIN fixtures: IngestAck → producer cleanup → потеря Gateway → RecoveryRelease без payload; crash после каждого Gateway cleanup chunk; drain → LIVE в прежний binding → новый batch → поздний RPC прежней admission generation.

### Размеры, headers, память

Единица control/chunk RPC ограничена 256 KiB включая wrapper; payload fragment <=64 KiB. Эти технические defaults ограничивают одно сообщение/рабочий буфер, не итоговый event/block. Большие manifests/plans/records состоят из произвольного числа chunks. Bounded WriteBatch <=1 MiB включает metadata/marker; большой список references хранится вне closing batch. Любая конфигурация должна учитывать фактические wrapper bytes; невозможный frame limit отклоняется до создания нового plan.

Immutable publication descriptor содержит exact subject и header template: `Nats-Msg-Id`, `Nats-Expected-Stream`. Другие прикладные headers запрещены без новой header_version. Message ID — версия кодирования + network digest + length-delimited producer/run/event_sequence + complete-or-part-index; конкретный бинарный tuple кодируется base64url без неоднозначной конкатенации. Reply inbox, connection ID и timeout — транспортные свойства, не часть immutable logical request.

Plan сохраняет header bytes/версию и layout; actual TransportFrame+headers <=transport.max_frame_bytes (default 2 MiB). PubAck.Stream проверяется против plan. Полный Envelope digest не подменяется digest публикационного запроса. NATS documented expectation header: [официальный контракт](https://docs.nats.io/nats-concepts/jetstream/headers).

Producer строит Envelope через disk spool/two-pass lengths и потоковый digest; не требует полного protobuf object graph в RAM. Gateway проверяет field chunks потоково; parent/cycle/unique-address проверки используют bounded disk-backed temporary indexes. Index keys очищаются после валидации/recovery; marker полного validation обязателен перед CLOSED. Capture snapshot/VM-safe yield и ограничение дополнительной памяти коллектора — native контракт D58; native память самой VM не считается ограниченной размером нашей transport queue.

### Errors, ENOSPC, совместимость

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

### Приёмочные fixtures будущей реализации

ATTEMPT-01 — повторные STAGING, pruning source, сохранение последней копии. PREPARE-01 — lost chunks/replies, conflict digest, новый limit. FRONTIER-01 — 100 retired, потеря Gateway, batch101 и поздний Publish50. FENCE-01 — barriers token/commit/Release/PubAck. DEST-01 — READY до Prepare, иной target после loss. DRAIN-01 — crash каждого seal/switch шага, оба owners, постоянный live вход до остановки. MEM-01 — большой record/manifest без роста working set. UPGRADE-01 — reader capabilities, pending старой версии, отказ persistent format change даже после drain. POISON-01 и ENOSPC-01 — отсутствие false ACK/drop и корректный recovery. Эти fixtures не запускались; DOC-REPAIR закрывает проектные переходы, не E03 runtime gate.

## Идентификаторы и служебный wire protocol

DOC-FINAL-01 · 2026-11-05. Точный логический контракт первой версии для ETH/BSC/POLY. Дополняет [автомат доставки](docs/DELIVERY_STATE_MACHINE.md) и [YAML](docs/LIVE_CONFIG_SPEC.md). Это документация, не созданные proto/bindings или выполненные тесты.

### Базовые кодирования и идентичности

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

### DestinationBinding file

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

Digest не включает собственное поле, URL, TLS/credentials или frame limit. Changing account/stream/prefix требует следующей binding generation той же destination_id и DRAINED. Изменять network identity в существующем store нельзя. Same generation+different content — конфликт; неожиданный скачок generation запрещён. Gateway YAML stream/subject_prefix должны точно совпасть с descriptor: это проверяемые дубли, не альтернативный route. Fresh stores сверяют desired descriptor между owners до первого сетевого обмена; offline adapter уже сохраняет binding до capture.

### READY manifest и reservation

Descriptor события: `{ordinal:u64,event_id:40 bytes,event_family:u16,event_kind:u16,envelope_length:u64,envelope_sha256:Hash32}`. Family registry v1: tx=1, block=2, balance.system=3, bor.legacy_receipt=4; последние два optional. Kind: applied=1, removed=2, status=3. Unknown value — ошибка, OTHER отсутствует.

Manifest bytes — `UTF8("extractor.manifest.v1") || 0x00 || event_count_u64be || descriptors_in_ordinal_order`, каждый descriptor — перечисленные поля в фиксированном порядке и big endian. Manifest digest — SHA256 этих bytes. Ordinals непрерывны 0…N−1, event IDs уникальны; empty public batch не создаётся. Entries/chunks хранятся на диске; closing marker содержит count/digest, не весь список.

Reservation seed binary v1: domain `extractor.reservation.v1` + zero byte, затем `reservation_id16,issuer_store_epoch16,batch_id40,manifest_digest32,event_count_u64be,sequence_base_u64be,binding_digest32,frame_limit_u64be,transport_version_u32be,layout_version_u32be,header_version_u32be`. Все версии первой схемы =1. `plan_digest=SHA256(seed_bytes)`; seed неизменен и возвращается побайтно при Prepare retry. Record chain_sequence=base+ordinal с overflow check. Gateway может импортировать прежний seed из SEALED producer после store loss; issuer epoch не переписывается на новый.

### TransportFrame layout v1

Этот небольшой transport wrapper имеет закреплённые field numbers; общие Envelope field numbers генерируются в E02 и не нужны для вычисления длины уже сохранённых envelope_bytes.

| Message | Fields: tag → type |
|---|---|
| NetworkIdentity | 1 family(enum EVM=1), 2 chain_id(bytes32), 3 genesis_hash(bytes32) |
| TransportFrame | 1 transport_version(u32), 2 network(message), 3 event_id(bytes40), 4 payload_digest(bytes32), 5 publication_epoch(bytes16), 6 chain_sequence(u64), oneof: 7 complete(message) / 8 part(message) |
| CompleteRecord | 1 envelope_bytes(bytes) |
| RecordPart | 1 part_index(u64), 2 part_count(u64), 3 total_bytes(u64), 4 data(bytes) |

Каждый указанный scalar присутствует, включая part_index=0. Canonical wrapper encoding v1: ascending tag order, minimal unsigned varints, обычные protobuf length-delimited fields, no unknown fields/duplicates. Envelope внутри — исходные immutable bytes, их независимая reserialization запрещена. Это локально определённое wrapper encoding, не утверждение о каноничности произвольного Protobuf. `EncodedFrameSize` — точная сумма key/varint/length/data sizes этой таблицы, без приблизительного overhead.

`Nats-Msg-Id = "ex1." + base64url_no_padding(network_digest32 || producer_id16 || run_id16 || event_sequence8 || form1 || part_index8)`. COMPLETE: form=0,index=0; PART: form=1,index=i. Header bytes ровно `NATS/1.0\r\nNats-Msg-Id: <id>\r\nNats-Expected-Stream: <stream>\r\n\r\n` в этом порядке, без status line/дополнительных headers. Reply inbox не входит в эти bytes. `F=transport.max_frame_bytes` учитывает wrapper + эти header bytes.

Layout v1:
1. Если точный COMPLETE с фактическими metadata/headers помещается в F, выбрать COMPLETE.
2. Иначе найти наибольшее целое C>=1, для которого PART с data длиной C, part_index=U64_MAX, part_count=U64_MAX и фактическим total_bytes помещается в F. Это консервативная проверка overhead без циклической зависимости count/size. Размер headers фиксирован по длине tuple, stream из seed binding.
3. Count=ceil(total_bytes/C), part i содержит bytes `[i*C,min((i+1)*C,total_bytes))`. Checked arithmetic для всех длин/offsets; overflow отвергается без truncate.
4. Before SEALED проверить фактический размер/digest каждой frame. При C<1 вернуть LIMIT_INCOMPATIBLE, сохранить READY; pending SEALED не перепланировать.

Layout manifest entries: `{ordinal,form,capacity,part_count,total_bytes}` в фиксированном порядке, u64 big endian кроме form byte; COMPLETE capacity=total_bytes,count=1. `layout_manifest_digest=SHA256(domain "extractor.layout.v1" + zero + plan_digest + count_u64be + entries)`. При BeginPublish Gateway сверяет детерминированный layout; неизвестная version не принимается. Control/chunk RPC<=256 KiB с wrapper, data chunk<=64 KiB; fragment offset относится к одной frame, не обязательно совпадает с RecordPart boundary. Максимум WriteBatch — 1 MiB. Это ограничения рабочих буферов, не предел размера транзакции.

### Служебные запросы и ответы

После authenticated handshake mutating RPC включает `{protocol_version=1,request_id,session_token,producer_id,run_id,connection_generation,admission_generation,binding_digest}`. Token check и commit сериализованы. mTLS проверяет deployment/network/role; challenge echo внутри authenticated transport подтверждает текущий boot, дополнительный неописанный signing key не вводится. Business idempotency — identity/manifest/offset/intent, а не только request_id.

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
| PublishChunk | batch_id, plan_digest, event ordinal, frame index, offset, chunk_sha256, bytes | durable next_offset |
| FinalizePublish | batch_id, plan_digest, manifest_digest, layout_manifest_digest | CLOSED; не IngestAck |
| GetBatchState | batch_id, plan_digest | PREPARED/RECEIVING/CLOSED/NATS_ACKED/RELEASING/RETIRED либо NOT_FOUND; IngestAck только если durable |
| Release | batch_id, manifest_digest, plan_digest | ReleaseAck(batch_id,retired_prefix) |
| RecoveryRelease | batch_id, exact saved IngestAck, durable ACKED/outbox assertion | ReleaseAck после sync, только следующий seq и отсутствие conflicting pending |
| SealGeneration | binding/admission generation, drain_id, batch_highwater | SealAck(drain_id,binding_digest,admission_generation,highwater,issuer_store_epoch) |
| PrepareSwitch | old SealAck, old binding digest, desired descriptor, switch_id | SwitchPrepared(switch_id,old/new digest,drain_id,admission_generation,issuer_store_epoch) |
| CommitSwitch | switch_id, exact SwitchPrepared, producer durable-commit assertion | SwitchCommitted с той же identity |
| Resume | old SealAck, resume_id, active binding digest, next admission generation | ResumeAck(resume_id,binding_digest,old/new admission generation,drain_id,issuer_store_epoch) |

`IngestAck={batch_id,manifest_digest,plan_digest,layout_manifest_digest,binding_digest,issuer_store_epoch}`. Producer сохраняет **весь ACK** в ACKED/outbox до cleanup, чтобы RecoveryRelease не требовал утраченного payload. Seal/Switch/Resume ответы связывают весь intent; совпадения одного номера недостаточно. Handshake/control evidence имеет fixed versioned schema, не JSON opaque map. Конкретные generated RPC tags — E02; значения/validation/порядок commit заданы здесь.

Chunk retries: одинаковые offset/bytes/digest возвращают тот же next_offset; конфликт записанного диапазона — IMMUTABLE_CONFLICT; пропуск offset — OFFSET_MISMATCH с ожидаемым cursor. Закрытие manifest/batch возможно только после проверки всех contiguous chunks. GET/ChunkAck не является разрешением удалить producer payload.

### Ошибки протокола

Ответ error: `{code,request_id,owner_state,field_path?,retry_action,detail_redacted}`. Code registry v1: TRANSIENT_UNAVAILABLE, SESSION_STALE, OFFSET_MISMATCH, IDENTITY_MISMATCH, BINDING_MISMATCH, MODE_CONFLICT, DRAIN_REQUIRED, GENERATION_CONFLICT, IMMUTABLE_CONFLICT, INVALID_DESCRIPTOR, INVALID_PAYLOAD, LIMIT_INCOMPATIBLE, VERSION_UNSUPPORTED, ALREADY_RETIRED, STORE_FATAL. Retry action: RETRY_BACKOFF / RECONNECT / RESUME_AT_OFFSET / FIX_CONFIG / DRAIN_OLD_BUILD / STOP_LOCAL_OWNER / DIAGNOSE_IMMUTABLE.

ALREADY_RETIRED допустим только для producer ACKED/outbox/retired, не подтверждает unACKed batch. CONFIG_BLOCKED/invalid pending не меняет bytes и не удаляет batch. STORE_FATAL прекращает соответствующего owner по D57. Неизвестный code/version обрабатывается как protocol incompatibility с сохранением данных; никакого fallback success. gRPC status является transport categorization, не заменяет typed error и durable ACK.

### Управляемый restart, drain, cutover

1. Stop LIVE и подтвердить отсутствие старого writer. С прежним binding запустить ту же совместимую native сборку `--extractor-mode=drain`.
2. Закрыть сохранённые **native** obligations, способные создать delivery batch; доставить/очистить все batches и ordered Release. Только затем сохранить drain_id/highwater и выполнить SealGeneration.
3. Sync DRAINED+SealAck; native процесс завершается. Для обычного resume оставить desired binding прежним. Для cutover подготовить следующий descriptor, новый stream окружением и обновить `binding_file` обоих owners; Gateway stream/prefix согласовать.
4. Gateway с отличающимся desired binding открывается в AWAIT_SWITCH: старый binding остаётся active durable, разрешены только recovery/maintenance handshake, новые публикации в desired route запрещены. Наличие файла нового descriptor не активирует его.
5. Запустить native `--extractor-mode=live`. Из DRAINED store получить old binding/seal/drain_id, sync SwitchIntent, пройти PrepareSwitch → producer durable binding commit → CommitSwitch. Никаких old_binding/drain_id YAML keys нет.
6. При прежнем binding Switch пропускается. В обоих случаях durable ResumeIntent/ResumeAck открывают следующую admission generation; только после producer LIVE commit и нового H разрешён capture. Crash продолжает тот же intent, не увеличивает generation снова.

Если desired config меняется ещё раз во время switch, сначала требуется вернуть config к сохранённому intent и закончить его; новый switch не перепрыгивает незавершённый. Gateway store loss восстанавливает identity/prefix/seal/switch evidence от surviving producer; insufficient evidence блокирует переход. Producer loss не восстанавливает старый run из Gateway: новый run и явная диагностика оставшихся obligations по принятой политике потери диска. Forced discard/migration не добавляются.

### Проверки

Обязательные граничные случаи RPC/CUTOVER: потерянный CommitSwitch/Resume ответ после смены session identity; recovery через MAINTENANCE без приёма data RPC; fresh-store IMPORT_SEALED с исходным chunked manifest; descriptor 16 KiB плюс полный wrapper, превышение bound отвергается config check.

WIRE-01: binding digest не зависит от JSON formatting, меняется при любом identity поле; duplicate keys/unknown fields/overflow отвергаются. ID-01: restart/attempt/decision replay сохраняет semantic identity и не создаёт второй batch. LAYOUT-01: COMPLETE на границе F, PART на 1 byte больше, большие varints/counts, C<1, override limit и exact headers. RPC-01: chunks/retries/errors, lost replies и saved ACK. CUTOVER-01: native pending перед seal, AWAIT_SWITCH, crash каждого switch/resume commit, desired config drift, store loss. Все fixtures будущие; этот документ не объявляет PASS.

## Live YAML и команды

> Нормативные effective native configs, mounts/UID, trust bootstrap, peer role/network admission и startup — [DEPLOYMENT_BOOTSTRAP_CONTRACT](docs/DEPLOYMENT_BOOTSTRAP_CONTRACT.md). Для поставляемого окружения credentials/TLS обязательны по этому профилю, даже если общий YAML допускает иные явно выбранные transport modes.

> D52: операционная готовность/синхронизация наблюдается по логам; управление штатными средствами Docker/Kubernetes, без отдельного API и обязательной детализации probes/shutdown таймаутов. Native граница H+1 и сохранение pending остаются. Default размера frame — 2 MiB, правила ниже.

> D51: штатные native configs можно заменить целиком и дополнить аргументами. Окружение выполняет отдельный init-шаг, первый старт создаёт необходимые native файлы, рестарт сохраняет state/ключи. Исходники `/config` read-only; изменяемые native файлы находятся в `/data/node`. Это подготовка окружения, не новая команда CLI сборщика. Точные filenames/init/mount mappings остаются в [deployment-задачах](docs/NODE_DEPLOYMENT_SPEC.md).

> D50: [таблица endpoints и экспонирования](docs/NETWORK_PORTS.md) закреплена по поручению пользователя. P2P overrides задаются native конфигурацией/окружением, не новыми произвольными ключами YAML сборщика. Gateway каждой сети использует внутренний service DNS и порт 50051, NATS — внутренний 4222. Значения настраиваемы; P2P NodePort не затрагивает приложение и не открывает его API наружу.

> D49: defaults deployment — Gateway `50051/TCP`, WAL `/data/wal`, журнал Gateway `/data/journal`, configs `/config` и secrets `/run/secrets` только для чтения. Процессы имеют собственные volumes; пути/порты переопределяемы. RPC/Engine API/Gateway/NATS не публикуются наружу по умолчанию. Это значения шаблонов окружения, не отмена обязательных полей схемы YAML. [Полный контракт defaults](docs/NODE_DEPLOYMENT_SPEC.md).

> D48: каждая сеть имеет собственный YAML сборщика и отдельный YAML Gateway. Штатные native configs и дополнительные аргументы передаются окружением, не включаются в схему сборщика. Образы, порты/P2P, каталоги, ресурсы и размещение доступны через Compose overrides/Kubernetes values. Секреты отдельными файлами; изменения применяются перезапуском компонента. Проект предоставляет defaults для трёх сетей; конкретные значения ещё уточняются в [NODE_DEPLOYMENT_SPEC](docs/NODE_DEPLOYMENT_SPEC.md).

> D46: endpoints используют service DNS, configs отделены от секретов, JWT у Geth/Prysm пар различаются. Каждый компонент — StatefulSet replicas=1 со своими Service/PVC, в Compose — сервис с named volumes. Готовность публикации не блокирует синхронизацию/capture и запись в WAL. Версии и точные native/probe-параметры ещё не утверждены; [deployment-контракт](docs/NODE_DEPLOYMENT_SPEC.md).

> D45: каждый самостоятельный компонент — отдельный Compose service и Kubernetes workload/Pod; адаптер остаётся встроенным в native клиент. Базовые связи adapter→Gateway идут по конфигурируемым сетевым endpoints (TCP/gRPC+mTLS), без обязательного общего хоста/UDS. Поддержка UDS в схеме не является требованием совместного размещения. Карта компонентов — [NODE_DEPLOYMENT_SPEC](docs/NODE_DEPLOYMENT_SPEC.md).

> D43: проект компонует и настраивает полный комплект нод для Compose/Kubernetes, включая supporting clients. «Запускает окружение» описывает механизм запуска, а не внешнюю ответственность. Native configs, связи и YAML адаптера входят в наш технический комплект: [NODE_DEPLOYMENT_SPEC](docs/NODE_DEPLOYMENT_SPEC.md).

По D42 текущие сети — Ethereum, BSC и Polygon PoS. Base/Arbitrum отложены; их YAML-профили текущая поставка отвергает как неподдерживаемые. Исследования сохраняются справочно.

2026-10-21. Технический проект для текущего scope D38. Заменяет активную часть [предыдущего draft](docs/CONFIG_CLI_DRAFT.md). Команды и поля — проектные интерфейсы, ещё не реализованы. История и загрузка пропусков не входят в этот документ.

### Команды и исполнение

| Команда | Назначение |
|---|---|
| Штатная команда native клиента с параметром `--extractor-config network.yml` | Запускается окружением; встроенный адаптер получает общий YAML. Имя параметра — проектный интерфейс нашей сборки, не существующий upstream флаг |
| Та же native команда с `--extractor-mode=drain` | D57: только восстановление сохранённых обязательств и доставка; нет нового live boundary/capture, exit после durable DRAINED |
| `extractor gateway --config gateway.yml` | Gateway одной сети, доставка в NATS; отдельный файл этого контура |
| `extractor config check --config network.yml --role live` | Проверка YAML и локальных предпосылок выбранной роли: live либо gateway |

По D39 ноду запускает окружение: Compose/Kubernetes либо штатная команда клиента. Общий CLI не выбирает и не запускает native executable. Hooks/Inspector работают внутри закреплённой native сборки, адаптер получает YAML при её запуске. Подключение hooks к произвольной уже работающей upstream ноде не обещается. Сигналы и lifecycle ноды обслуживает её штатный процесс и окружение.

CLI не скачивает клиенты, не создаёт pods и не вызывает API Kubernetes/Docker. Окружение запускает независимые процессы каждой сети. Добавление сети не меняет настройки других сетей. Native supporting clients запускаются окружением, не загрузчиком. Команд history/resume/apply и параметров диапазона блоков нет.

### Общая схема YAML v1

Один YAML — одна сеть и конфигурация соответствующей роли: файл сборщика и отдельный файл Gateway по D48. Ниже общий словарь полей с обязательностью по роли; отдельный файл не требует копировать настройки другой роли. `config check --config` получает путь к проверяемому файлу. Изменения применяются рестартом соответствующего компонента, watcher отсутствует. Неизвестные и дублирующиеся ключи запрещены на всех уровнях схемы сборщика/Gateway. Нет скрытой подстановки environment или произвольных CLI overrides этой схемы. Относительные пути разрешаются относительно YAML-файла. Это не ограничивает штатные native configs/args и настройки окружения: ими управляют Compose overrides/Kubernetes values отдельно от YAML сборщика.

| Поле | Тип | Обязательность / значение |
|---|---|---|
| `schema_version` | integer | Обязательно, ровно `1` |
| `network.profile` | enum | Обязательно, один из трёх профилей ниже |
| `destination.binding_file` | path | Обязательно обеим ролям: один immutable DestinationBinding данного контура, без NATS credentials; digest/network/generation сверяются с сохранённым state |
| `native.kind` | enum | Обязательно live, соответствует профилю |
| `delivery.gateway_endpoint` | URI | Обязательно live: `unix:///absolute/path` либо `https://host:port` |
| `delivery.tls` | tagged object | Обязательно live: disabled для UDS, mutual для TCP |
| `storage.live_dir` | path | Обязательно live: каталог RocksDB адаптера |
| `storage.gateway_dir` | path | Обязательно gateway: отдельный каталог RocksDB |
| `gateway.listen` | URI | Обязательно gateway: UDS либо TCP, те же формы адреса |
| `gateway.tls` | tagged object | Обязательно gateway: disabled для UDS, mutual для TCP |
| `gateway.nats.server` | URI | Обязательно gateway: текущий единственный NATS instance |
| `gateway.nats.tls` | tagged object | Обязательно gateway: disabled, server либо mutual |
| `gateway.nats.credentials_file` | path | Optional gateway; отсутствие — без credentials |
| `gateway.nats.stream` | string | Обязательно gateway: имя предварительно созданного stream этой сети; не wildcard |
| `gateway.nats.subject_prefix` | string | Обязательно gateway: префикс без wildcard; default-профили используют `evm.v3.<chain_key>`, suffix берётся из закрытого реестра MESSAGE_CONTRACT |
| `transport.max_frame_bytes` | positive integer | Optional gateway; default `2097152` (2 MiB), полная TransportFrame вместе с NATS headers; новые публикационные планы |
| `runtime.log_level` | enum | Optional: debug/info/warn/error, default info |
| `finality.polygon.l1_endpoint` | URI | Обязательно только polygon live: собственный supporting Geth, http/https JSON-RPC; не основной Ethereum extractor |
| `finality.polygon.sources_file` | path | Обязательно только polygon live: read-only `/etc/extractor/polygon-sources.json`, bootstrap identities/roles/endpoints по CURRENT_SEMANTICS; абсолютный path |
| `finality.polygon.l1_tls` | tagged object | Обязательно только polygon live: disabled при http, server/mutual при https; стандартные поля TLS ниже |
| `finality.polygon.expected_root_chain_address` | string | Обязательно только polygon live: 40 lowercase hex без 0x, address из проверенного mainnet bootstrap/chain params |
| `finality.polygon.expected_child_block_interval` | positive u64 | Обязательно только polygon live: значение из проверенного bootstrap/checkpoint params, без float/narrowing |

Объекты TLS: `mode: disabled` запрещает дополнительные поля; `server` требует `ca_file`; `mutual` требует `ca_file`, `cert_file`, `key_file`. Проверка имени сервера обязательна при TLS. Для NATS disabled соответствует `nats://`, остальные режимы — `tls://`. Server-only TLS для адаптер↔Gateway не разрешён. UDS защищается правами файловой системы. Противоречивые режимы — ошибка до запуска.

Структура всего документа проверяется целиком. Обязательные поля, локальные файлы и credentials проверяются только для запускаемой роли. Live не требует чтения NATS credentials; отсутствующая секция другой роли допустима. Секций ClickHouse, history, coverage и startup-уведомлений нет.

D63: finality.polygon разрешён только polygon live. Heimdall endpoint берётся из effective native Bor config; expected L1 network — Ethereum Mainnet profile собственного Polygon L1 комплекта. Expected RootChain address/interval генерируются окружением из проверенных bootstrap chain parameters и сверяются с online responses, не принимаются автоматически из произвольного ответа. Значения и supporting node identity сохраняются в evidence. Нет fallback на основной Ethereum extractor. Wrong configured identity — CONFIG error; временная недоступность correct supporting source задерживает только GC. Observer в LIVE выполняет один цикл не чаще раза в 10 секунд, deadline отдельного network read 10 секунд; это технические defaults, не SLO. DRAIN не начинает новые optional polls и разрешает только сохранённый GC decision.

### Размер сообщения D52

Gateway применяет `transport.max_frame_bytes` при формировании publication plan и определяет part layout. До Prepare адаптер хранит неизменные Envelope bytes; локальные фрагменты хранения не задают сетевое разбиение. Адаптер сериализует и сохраняет точные frames по полученному plan, Gateway проверяет их перед публикацией; второй независимый лимит разбиения в live YAML не вводится. Если полный record не помещается с envelope/headers, он разбивается на RecordPart, каждый в пределах лимита. Никакой размер транзакции не оправдывает усечение данных.

В поставляемой конфигурации NATS `max_payload` задаётся явно `2097152`; stream MaxMsgSize также `2097152`. Полный размер frame и headers проверяется до отправки; gRPC send/receive пределы должны вмещать размер соответствующего RPC сообщения с его обёрткой, а не только bytes frame. RPC не должен объединять неограниченное число frames в одно сообщение. Проверка фактического сериализованного размера обязательна; ограничения NATS и gRPC должны быть согласованы с override лимита.

Pending publication plan/bytes неизменны при restart и retry. Изменённый лимит применяется к новым планам; если сохранённые frames больше действующего лимита либо server/stream их не допускает, публикация останавливается с явной ошибкой конфигурации. Pending не удаляются и не разбиваются повторно; нужно восстановить достаточный лимит. Горячая перезагрузка не добавляется.

Штатный NATS default — 1 MB по [документации](https://docs.nats.io/running-a-nats-service/configuration); он не гарантирует размещение полной нормализованной транзакции с calls/logs/state changes. Поэтому 2 MiB — предел сообщения, не обещание «любая транзакция одним сообщением».

### Смена назначения NATS — D54

Точный нормативный порядок D54/D57/D59: [DELIVERY_STATE_MACHINE](docs/DELIVERY_STATE_MACHINE.md). Binding задаётся окружением до первого capture и сохраняется обоими owners; local READY без Prepare уже имеет destination. Drain и switch проходят durable generation barrier, а не проверку пустоты RAM.

`stream` и `subject_prefix` должны совпасть с desired DestinationBinding. Gateway проверяет точное account поле UserInfo, stream/subjects/limits и descriptor digest по [CONTROL_WIRE_CONTRACT](docs/CONTROL_WIRE_CONTRACT.md); URL не identity. Stream создаёт environment bootstrap, runtime Gateway не выполняет административный create/update.

Publication plan и ACK относятся к одному сохранённому логическому назначению. При наличии незавершённой доставки у адаптера или Gateway смена account/stream/subject mapping отклоняется до публикации; pending и исходные настройки назначения сохраняются. Смена адреса или credentials, по-прежнему ведущих в тот же account/stream/subjects, не является переносом данных, но требует проверки назначения. Неопределённость этой проверки не разрешает объединять ACK.

Для cutover выполнить старый binding drain до DRAINED; заменить binding_file обеих ролей на desired descriptor и согласовать Gateway stream/prefix. Старые binding/seal/drain_id читаются из store, дополнительных YAML keys нет. Gateway с desired mismatch входит в AWAIT_SWITCH; native explicit LIVE завершает persisted Switch/Resume до новой H. Полная последовательность — CONTROL_WIRE_CONTRACT. Пустой Gateway/RAM не доказывает завершение native/producer obligations. При недоступном старом назначении pending сохраняются, cutover блокируется; forced migration отсутствует. Post-ACK retention/потеря NATS store вне гарантии D30–D31.

### Native-профили

| network.profile | native.kind | Выбранная реализация |
|---|---|---|
| ethereum-mainnet | geth | Go/Geth: native hooks/state journal, canonical notifications |
| bsc-mainnet | bsc | Go/BSC: tx execution и Parlia Finalize |
| polygon-pos-mainnet | bor | Go/Bor: tx execution, StateSync/Finalize; supporting Heimdall |

Сборки и toolchains закрепляются по [CLIENT_MATRIX](docs/CLIENT_MATRIX.md) и [COMPONENT_STACK](docs/COMPONENT_STACK.md). YAML не выбирает произвольный executable/version и не добавляет новый тип блокчейна без адаптера. Исторические Classic/replay профили сейчас не предлагаются. Эта таблица использует ранее собранную документацию, не утверждает совместимость по результатам runtime-тестов.

**Общий интерфейс, native параметры.** Аргументы запуска, datadir, consensus/rollup/Heimdall соединения и JWT задаются штатной конфигурацией native клиента и окружением. Они не дублируются в YAML адаптера. Общий параметр подключения YAML является частью нашей native интеграции; команды Gateway/config check остаются отдельными. Native capture не заменяется RPC.

Встроенный адаптер сверяет network.profile/native.kind с эффективной конфигурацией запущенного клиента, используя результат его штатного parser. Несоответствие сети или сборки даёт ошибку до capture; адаптер не меняет native настройки. Datadir определяется из этой эффективной конфигурации. Отдельный config check проверяет YAML, но не обещает сверку с ещё не запущенной нодой.

Для каждого профиля при реализации потребуются шаблоны штатного запуска с подключённым YAML адаптера и проверки настоящим клиентом. Новое исследование версий и сборки в этом шаге не выполнялись.

### Состояние и запуск

1. Проверить CLI/YAML, пути, типы, профиль и эффективную native конфигурацию до открытия writable состояния. Ошибка не должна частично применять настройки.
2. Эксклюзивно открыть состояние owner. Native datadir, live_dir и gateway_dir не могут совпадать или быть вложены друг в друга; symlinks разрешаются перед сравнением. Второй writer получает ошибку.
3. Сверить сохранённые network/profile, роль, wire schema/build identity. Несовместимый WAL не удалять и не переинициализировать молча. Сеть нельзя сменить, сохранив прежний каталог.
4. До native recovery установить hooks/decision bridge в RECOVERY_ONLY; разрешить сохранённые ADMITTED/EXEC_READY/markers и lifecycle по NATIVE_CAPTURE_PROTOCOL. Независимо восстановить pending по [DELIVERY_STATE_MACHINE](docs/DELIVERY_STATE_MACHINE.md), сохраняя IDs/bytes/parts. Новый safe live capture не ждёт NATS, но не обходит незавершённые native obligations.
5. По D39 после синхронизации ноды зафиксировать текущую каноническую голову H (высоту и hash); начать новый live-сбор с H+1. Начальная синхронизация не выгружает прошлые блоки. Фиксация H согласована с native подпиской так, чтобы события после границы не потерялись. При рестарте для новых наблюдений фиксируется новая H, а pending WAL передаётся независимо. Это начальная граница, не постоянный фильтр по высоте: последующие reorg/finality обрабатываются native lifecycle bridge, в том числе относительно сохранённых активаций.
6. Получать native события, нормализовать, сохранять в WAL, передавать через Gateway до NATS PubAck. Не обходить диапазон старого checkpoint→head. Восстановление конкретного незакрытого capture, текущие reorg/finality сохраняют существующий контракт.

Не вводятся TTL, квота pending или удаление при недоступности NATS. Физическая ошибка записи явная; WAL_SAVED не объявляется без durable записи. Готовность источника и готовность публикации — разные состояния.

### Валидация и остановка

`--extractor-mode=live|drain` — именованный проектный параметр native сборки, не произвольный override YAML. Новый store по умолчанию LIVE. Сохранённый незавершённый DRAIN после crash возобновляется в DRAIN; явный live до его завершения отвергается. После DRAINED возврат в LIVE требует explicit mode=live, seal, применимого switch и Resume новой admission generation. Этот режим не существует в upstream без нашей интеграции.

D57: fatal локального WAL/normalization останавливает весь native процесс соответствующей сети; Gateway journal fatal останавливает Gateway, не все сборщики. Transient transport outage допускает продолжение safe local capture по D28. D58 разрешает VM-safe yield вне hooks для bounded snapshot/spooling; unsupported pruning/backend/import modes отвергаются preflight.

Upgrade preflight разделяет build ID и store/plan/wire/native profile capabilities. Совместимые версии повторяют старые bytes. Несовместимый wire/plan upgrade — после drain старой сборкой. Несовместимый persistent store format не становится допустимым после drain: lifecycle/identity ещё существуют; без поддержанной read compatibility такой переход/rollback запрещён до записи, auto-migration нет.

Ошибка содержит код, путь поля и причину, без секретов и полного дампа native argv. `config check` не создаёт DB/stream/run, не запускает ноду, не публикует данные и не обещает доступность сети. Online handshake выполняется при запуске выбранной роли.

Проектные exit codes: 0 — успешный check/штатное завершение; 2 — CLI/YAML/native config; 3 — занятое или несовместимое состояние; 4 — неподдержанная сборка/несовместимый источник; 5 — невосстановимая runtime/дисковая ошибка. Исходная native ошибка сохраняется в диагностике. Недоступность Gateway/NATS вызывает retry, не успешное завершение.

SIGINT/SIGTERM прекращает новые чтения и закрывает состояние, сохраняя durable pending. Gateway CLI возвращает 130/143; native процесс сохраняет штатную обработку сигналов и exit codes, адаптер использует его shutdown hook. Остановка не ждёт бесконечно NATS PubAck. STAGING восстанавливается по протоколу; неподтверждённый payload не удаляется. SIGKILL восстанавливается следующим запуском как crash.

Будущие проверки: одинаковые команды всех профилей; неверные kind/chain; неизвестные/дублирующиеся ключи; TLS mismatch; недоступные credentials; конфликты native args; общий/занятый datadir; pending при остановке и NATS outage; отсутствие стартовой истории; изоляция сетей. Это требования к будущим тестам, не проведённые проверки.

D40 исключает требования производительности и production-методику из ТЗ. Техническое разбиение payload и bounded-memory обработка сохраняют контракт протокола; это не latency/TPS/RAM SLO. У каждой сети отдельные live_dir и gateway_dir с независимыми RocksDB writers; общая multi-network DB запрещена. Версии YAML-библиотек и совместная сборка закрепляются на этапе реализации, без изменения native toolchains по умолчанию.

### Результат CFG-S04

CFG-S04/CFG-S05 завершены как документация с уточнениями D39: нода запускается окружением, live начинается с H+1 после синхронизации. CFG-R4 снят. E00-04 закрыт D40 в границе функционального ТЗ, без performance требований и production-методики. Runtime-проверки и реализация не выполнялись.

## Состав и размещение нод

### Состав и ответственность

| Контур | Что проект должен скомпоновать и настроить | Что ещё уточнить |
|---|---|---|
| Ethereum | Geth с native-адаптером + Prysm (D44), их соединение и credentials, собственные native datadirs, RocksDB WAL адаптера, Gateway и его журнал | Кандидат Prysm 7.3.0 принят D47; образы, совместимость и параметры запуска уточняются; отдельные контейнеры/Pods приняты D45 |
| BSC | BSC-клиент с native-адаптером и штатным консенсусом, native datadir, собственные WAL/Gateway/журнал | Точная контейнерная конфигурация закреплённого клиента |
| Polygon PoS | Bor с native-адаптером + Heimdall-v2, отдельные Geth/Prysm для Polygon L1 по D44, необходимые соединения, собственные native datadirs, WAL/Gateway/журнал | Кандидаты версий приняты D47; точная конфигурация и образы уточняются; отдельный контейнер/Pod каждого компонента принят D45 |
| NATS | Один ранее принятый NATS JetStream внутри Compose/Kubernetes, persistent storage, подключение независимых Gateway, раздельные streams/subjects | Полная связанная конфигурация; HA/DC-топология не вводится |

Исходные native candidates сохраняются в [CLIENT_MATRIX](docs/CLIENT_MATRIX.md), стек компонентов — [COMPONENT_STACK](docs/COMPONENT_STACK.md). Кандидат версии не означает доказанную совместимость сборки. Добавление validator duties/staking не требуется для сбора данных и не предполагается автоматически.

### Обязательное описание для обеих сред

D44: L1-нода Polygon — самостоятельный Geth/Prysm комплект внутри Polygon-контура, без нашего Ethereum-адаптера. Она не публикует Ethereum-транзакции в NATS и не использует WAL/Gateway Ethereum-контура. Datadirs, credentials и процессы отделены от основной Ethereum-ноды. Общий выбор ПО не означает общий экземпляр или общее состояние. Остановка основного Ethereum-контура не должна останавливать источник L1 для Polygon.

D47/D58: приняты кандидаты Geth 1.17.9/Prysm 7.3.0 для обеих пар, BSC 1.7.8, Bor 2.10.3/Heimdall-v2 0.13.2. Совместимость и image digests ещё не подтверждены; [весь набор и источники](docs/research/DEPLOYMENT_VERSION_CANDIDATES.md). D45 выбирает отдельный Compose service и Kubernetes workload/Pod для каждого самостоятельного компонента. Совместное размещение компонентов в контейнере/Pod не требуется.

#### Карта размещения D45

Имена ниже — проектные идентификаторы. По D46 каждая строка соответствует отдельному сервису Compose и отдельному StatefulSet Kubernetes с replicas=1, собственными Service и PVC. Это 11 основных сервисов, не 11 обязательных серверов. Точные manifests ещё не реализованы.

| Контур | Компонент | Собственное состояние |
|---|---|---|
| Ethereum | eth-geth с нашим адаптером | Native datadir и отдельный RocksDB WAL адаптера |
| Ethereum | eth-prysm | Consensus datadir |
| Ethereum | eth-gateway | RocksDB publication journal |
| BSC | bsc-node с нашим адаптером | Native datadir и отдельный RocksDB WAL адаптера |
| BSC | bsc-gateway | RocksDB publication journal |
| Polygon | polygon-bor с нашим адаптером | Native datadir и отдельный RocksDB WAL адаптера |
| Polygon | polygon-heimdall | Собственный datadir Heimdall |
| Polygon | polygon-l1-geth без адаптера | Собственный Ethereum execution datadir |
| Polygon | polygon-l1-prysm | Собственный Ethereum consensus datadir |
| Polygon | polygon-gateway | RocksDB publication journal |
| Общая инфраструктура | nats | JetStream file storage одного инстанса |

Адаптер и embedded RocksDB не выделяются в дополнительные процессы/Pods. Каждый writable каталог принадлежит одному owner. D45 не разрешает параллельных writers, совместного datadir или автоматического multi-node failover. Размещение/перезапуск owner должно сохранять его состояние по ранее принятому recovery-контракту.

Компоненты связываются конфигурируемыми сетевыми endpoints: service DNS в Kubernetes/Compose либо заданные адреса. Связи не предполагают localhost или общий хост. Для adapter→Gateway применяется ранее принятый TCP/gRPC с mTLS; native связи используют штатную аутентификацию, в том числе Engine API/JWT для каждой отдельной Geth/Prysm пары. NATS остаётся общим транспортом. Точные адреса, порты и передача secrets — DEP-03.2.

Каждый компонент можно размещать и перезапускать отдельно. Это не устраняет зависимость Geth от Prysm или Bor от Heimdall/L1: их недоступность отражается в готовности соответствующей ноды. Остальные сетевые контуры не переконфигурируются и не ожидают её прогресса; pending WAL сохраняется независимо.

- Полный список services/workloads, их роли, образы/сборки и закреплённые версии. Сборки execution-клиентов содержат наш адаптер, остальные компоненты подключаются согласованными штатными интерфейсами.
- Штатные native configs, YAML адаптера/Gateway и способ передачи их процессам. Проект готовит согласованный комплект: пользователь не должен самостоятельно угадывать недостающие связи компонентов.
- Сетевые адреса/порты между компонентами, аутентификация, JWT/TLS/credentials и способ их предоставления без включения рабочих секретов в примеры.
- Persistent volumes/datadirs отдельно для native клиентов и RocksDB каждого owner. Общая writable DB сетей запрещена; нет второго writer одного каталога при рестарте/замене процесса.
- Условия готовности компонентов и порядок подключения, а не только факт старта контейнера. Live активируется с H+1 после синхронизации по D39. Pending WAL не ждёт синхронизации для своей повторной доставки, если компоненты доставки могут работать.
- Независимый запуск, остановка и изменение конфигурации контура каждой сети. Native dependencies внутри сети допускаются; общего координатора прогресса и зависимости от extractor другой сети нет. Общий NATS сохраняет согласованную роль транспорта.
- Штатная остановка, сохранение томов и восстановление pending по существующему RocksDB-протоколу. Автоматическое удаление данных и загрузка исторических пропусков не добавляются.

Docker Compose используется для локального/dev запуска, Kubernetes — для production-размещения. Один и тот же функциональный контракт применяется к обеим средам. Сборщик не вызывает их API для создания/управления процессами: окружение исполняет подготовленные проектом конфигурации. Выбор конкретного Kubernetes packaging и окончательных templates не объявляется принятым этим документом.

### Настройки размещения и связей D46

**Хранение.** StatefulSet каждого компонента владеет собственными PVC. В Compose используются собственные named volumes. Для execution-клиента с адаптером native datadir и RocksDB WAL размещаются раздельно; publication journal принадлежит отдельному Gateway. Количество PVC определяется набором этих отдельных хранилищ, а не правилом «ровно один том на сеть». Единственный writer сохраняется при обновлении/рестарте; replicas=1 не заменяет соблюдение этого правила. Перенос зависит от возможностей StorageClass/тома и не означает автоматический failover.

**Соединения.** Конфиги содержат service DNS и порты, не IP конкретного контейнера/Pod. Смена IP не требует правки клиентов. Основные связи:

| Отправитель/клиент | Получатель | Назначение |
|---|---|---|
| eth-prysm | eth-geth | Engine API своей Ethereum-ноды |
| polygon-l1-prysm | polygon-l1-geth | Engine API отдельной L1-ноды Polygon |
| polygon-heimdall | polygon-l1-geth и polygon-bor | Штатные L1/Bor подключения |
| polygon-bor | polygon-heimdall | Штатный интерфейс Bor/Heimdall |
| Адаптер внутри eth-geth / bsc-node / polygon-bor | eth-gateway / bsc-gateway / polygon-gateway соответственно | gRPC с mTLS |
| Каждый Gateway | nats | Публикация и получение PubAck |

Таблица не заменяет version-specific native настройки: точные методы, порты и дополнительные обязательные связи сверяются при закреплении версий. Внутренние service DNS не заменяют native P2P соединения; публикация P2P-портов также должна войти в полную конфигурацию.

**Конфиги и секреты.** Обычные параметры — отдельные YAML и штатные конфиги клиентов. JWT/TLS-ключи/NATS credentials передаются отдельными секретами или файлами. JWT основной Geth/Prysm пары не используется парой Polygon L1. Каждый процесс получает только нужные ему файлы. Конкретные mounts/пути описываются в шаблонах; генерация рабочих значений не выполняется на этапе ТЗ.

**Готовность.** Ноды начинают синхронизацию независимо от Gateway/NATS. Готовность к native capture наступает после синхронизации по сетевому профилю; фиксируются H/hash, новые live-блоки собираются с H+1. Pending WAL передаётся независимо от новой live-границы. Отказ Gateway/NATS не означает остановку готового capture, пока доступна durable запись. Native зависимости внутри сети сохраняются, но нет глобального барьера readiness между сетями. Временная недоступность downstream сама по себе не является причиной перезапускать исправный producer через liveness probe. D52: операционное наблюдение по логам и штатное управление окружением; отдельная детализация probes/shutdown не требуется.

### Гибкая конфигурация D48

Один сетевой контур получает собственный YAML сборщика и отдельный YAML Gateway. Конфиги Ethereum, BSC и Polygon не объединяются в обязательный общий файл; обновление настроек сети не требует перезапуска других сетей. У общего NATS собственный конфиг сервера, отличный от настроек подключения Gateway.

| Уровень | Что настраивается | Способ передачи |
|---|---|---|
| Встроенный сборщик одной сети | Сеть, WAL, endpoint своего Gateway, TLS, параметры доставки | Типизированный YAML через проектный `--extractor-config` |
| Gateway одной сети | NATS, subjects/streams, журнал, входящее соединение и TLS | Собственный YAML через `--config` |
| Native клиенты | Все поддерживаемые выбранной версией штатные настройки, дополнительные аргументы | Родные файлы конфигурации и аргументы запуска; без проектного allowlist и копирования всего upstream schema в YAML сборщика |
| Окружение | Версии/образы, порты, P2P-адреса, каталоги, ресурсы, размещение | Compose overrides либо Kubernetes values; приложение не читает API оркестратора |
| Секреты | JWT, TLS-ключи/сертификаты, NATS credentials | Отдельные файлы, ссылки в конфигурации; отдельный JWT у каждой Geth/Prysm пары |

Комплект должен содержать готовые согласованные defaults для трёх сетей и обеих сред. Пользователь меняет только нужные настройки. Произвольный выбор upstream параметров/образов не означает доказанную совместимость произвольной сборки с native адаптером; проверенные кандидаты D47 и обязательные интерфейсы сохраняются.

Изменение применяется перезапуском затронутого компонента. Watcher/hot reload не требуются. Сохранённые WAL/журнал обслуживаются по принятому протоколу; restart не добавляет загрузку истории или пропусков. Изменение native конфигурации не выполняется командой сборщика: процесс запускает окружение. Типизированный YAML сборщика/Gateway продолжает запрещать неизвестные ключи; свобода native аргументов относится к отдельному уровню.

Общие defaults портов и каталогов приняты D49 ниже. Таблица P2P/native endpoints закреплена D50 в NETWORK_PORTS; flags/mount mappings определены в DEPLOYMENT_BOOTSTRAP_CONTRACT; native parser/runtime verification остаётся E01/E06. Здесь описано ТЗ, не готовые deploy-файлы.

### Defaults портов, доступа и каталогов D49

Все значения этого раздела переопределяются через модель D48. Внутренние native порты сохраняются штатными: Geth HTTP RPC — `8545/TCP`, Engine API — `8551/TCP`; это относится к обеим независимым Geth/Prysm парам. Основание: [параметры Geth](https://geth.ethereum.org/docs/fundamentals/command-line-options). Gateway каждой сети использует проектный `50051/TCP` с ранее принятым gRPC/mTLS. Номер RPC-порта сам по себе не требует включать ненужные API. Остальные интерфейсы перечислены в NETWORK_PORTS по D50.

Одинаковые внутренние порты не конфликтуют между отдельными контейнерами/Pod с собственными сетевыми пространствами. Наружу по умолчанию публикуется только P2P; RPC, Engine API, Gateway и NATS доступны внутри окружения. Для локальной диагностики допускается опциональная публикация на `127.0.0.1`. Это не отменяет JWT/mTLS/credentials на внутренних соединениях.

Внешние P2P-адреса и порты, bootnodes/seeds задаются отдельно для каждого экземпляра. Если несколько клиентов используют один публичный IP, их внешние порты должны различаться для одного транспортного протокола. Это касается и основной Ethereum-ноды, и отдельного Ethereum L1-комплекта Polygon. Объявляемый клиентом адрес/порт должен соответствовать реально доступному endpoint; одной настройки перенаправления порта недостаточно для описания P2P-конфигурации. Таблица TCP/UDP и способ экспонирования в Kubernetes закреплены D50 в NETWORK_PORTS.

| Путь внутри контейнера | Назначение | Доступ |
|---|---|---|
| `/config` | Конфигурация соответствующего процесса | Только чтение |
| `/run/secrets` | Только необходимые процессу секреты | Только чтение |
| `/data/node` | Native данные конкретного клиента | Чтение/запись, собственный persistent volume |
| `/data/wal` | RocksDB WAL встроенного адаптера | Чтение/запись, отдельно от native datadir |
| `/data/journal` | RocksDB журнал Gateway своей сети | Чтение/запись, собственный persistent volume |
| `/data/jetstream` | File storage NATS | Чтение/запись, собственный persistent volume |

Каждый процесс получает только нужные каталоги. Повторение пути в разных контейнерах не означает общий volume/PVC: разные native клиенты, сети и владельцы состояния изолированы. Polygon L1 Geth не получает WAL адаптера. Физические пути хоста и storage параметры настраиваются отдельно; принятые пути не навязывают размещение на одном узле.

P2P-ключ/идентичность сохраняется в persistent native state либо связанном с ним постоянном расположении, соответствующем конфигурации клиента. Обычный перезапуск не генерирует новую идентичность. Точные имена native файлов и flags для кандидатов D47 ещё сверяются; проект не меняет upstream формат ключей.

### Подготовка конфигов и native файлов D51

Комплект содержит готовые штатные конфиги выбранных клиентов. Пользователь может заменить файл целиком и задать дополнительные native аргументы по D48. Исходные файлы монтируются в `/config` только для чтения; файлы, которые native клиент должен изменять, располагаются в его собственном `/data/node`. Секреты остаются отдельными файлами согласно D49.

Перед запуском native процесса окружение выполняет отдельный init-шаг: на первом старте создаёт необходимые каталоги и native файлы. Повторный запуск сохраняет существующее состояние цепочки и ключи, не выполняет разрушительную повторную инициализацию и не генерирует новую P2P-идентичность. Сборщик не управляет этим шагом и не запускает ноду.

Механика init для Compose/Kubernetes, точные native команды, filenames и mount mappings описываются в DEP-03.2b.2b.3b. Необходимо различить обновляемые пользовательские настройки и сохраняемые native state/ключи: применение нового конфига при рестарте не должно затирать состояние. D51 не выбирает молча универсальное копирование всех файлов `/config` поверх существующего datadir. Файлы реального deployment сейчас не создаются.

### Сохраняемые границы (D38–D42)

История и пропуски отложены D38; performance требования и production-методика исключены D40; лицензия и публичная поставка отложены D41; Base/Arbitrum отложены D42. Эти решения не отменяют описанный здесь полный технический комплект трёх нод. Функциональные проверки конфигурации/совместимости относятся к будущей реализации, не объявляются уже выполненными.

## Bootstrap и окружение

Polygon bootstrap также создаёт persistent supporting deployment IDs и read-only `/etc/extractor/polygon-sources.json` по CURRENT_SEMANTICS. Файл связывает роли/адреса/chain identities с deployment provenance; Geth RPC не выдаёт эти IDs и не доказывает роль supporting service. Identity-файлы находятся в существующих native volumes, новый volume не нужен.

D63 добавляет только control evidence внутри Bor adapter: доступ к собственному polygon-l1-geth HTTP RPC для hash-pinned finalized RootChain read. Новый сервис/volume/ETH tx extractor не добавляется. Строгие YAML keys — LIVE_CONFIG_SPEC; finality/GC — CURRENT_SEMANTICS. Bootstrap сохраняет проверенные RootChain address/child interval и supporting L1 identity вместе с конфигурацией; отсутствие online L1 задерживает GC, не готовую Polygon delivery.

2026-11-05 · DOC-REPAIR-04 · D43–D52/D57–D59. Нормативная детализация [состава нод](docs/NODE_DEPLOYMENT_SPEC.md), [портов](docs/NETWORK_PORTS.md) и [live YAML](docs/LIVE_CONFIG_SPEC.md). Здесь описаны будущие init/runtime contracts; Compose/Kubernetes файлы, секреты, образы и процессы ещё не создавались.

### Source baseline и артефакты

Geth 1.17.9 `9dffe6e976cbbb87a84c5fd395448f0950290def`; BSC 1.7.8 `cdb7548b5baacfdae92f9f63437d6456411665f3`; Bor 2.10.3 `a16f123d9b4f41a552a3a98d93a0340430838576`; Prysm 7.3.0 `d01132449756e217d7f6c460e780174c34139b41` для обеих независимых пар. Остальные версии — [принятые кандидаты](docs/research/DEPLOYMENT_VERSION_CANDIDATES.md). Source pin не заменяет patch commit/image digest/build proof.

D58 меняет только Heimdall на **v0.13.2**, commit `03090c94e7eaef344cd16efd6c968431c4041847`. Go 1.26.10, upstream replacements CometBFT `v0.3.11-polygon`, Cosmos SDK `v0.2.17-polygon`. [Release](https://github.com/0xPolygon/heimdall-v2/releases/tag/v0.13.2), [go.mod](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/go.mod). Изменение необходимо после Karwar; совместный runtime с Bor ещё не проверен.

| Проверенный source file v0.13.2 | SHA-256 |
|---|---|
| go.mod | `8f612e9d9eb21bc85eb22adf46455f3f22a638901884c49e12637c22e2edcd86` |
| packaging/templates/config/mainnet/config.toml | `a8e9e85f57140b71e615e122e496e4d116fd81ddb2f795bd55e6c7128fc98934` |
| packaging/templates/config/mainnet/app.toml | `6958ad6ac3f2ab548f307e574e8d984c114697cc07e54552de14bfd25eb87186` |
| packaging/templates/config/mainnet/client.toml | `5ff40f1aa8e4bac94c072d7d688553929d502f2d6bdd3cc00e6257e5a2a0c691` |

Mainnet configs побайтно совпали с прежним кандидатом. [Источник templates](https://github.com/0xPolygon/heimdall-v2/tree/03090c94e7eaef344cd16efd6c968431c4041847/packaging/templates/config/mainnet). Generic `packaging/templates/config/genesis.json` содержит testnet `heimdallv2-80001` и **не подходит Mainnet**.

Cold-start artifacts:

- Geth Mainnet genesis встроен в выбранный профиль; chain/genesis identity проверяются при открытии native store.
- Prysm Mainnet genesis встроен в обычную mainnet сборку; build tag `noMainnetGenesis` несовместим с этим default. Checkpoint sync URL и genesis-state override опциональны и требуют явного config, скрытого внешнего checkpoint нет.
- BSC [mainnet.zip v1.7.8](https://github.com/bnb-chain/bsc/releases/download/v1.7.8/mainnet.zip): 45720 bytes, SHA-256 `522cb9902437df2368008b28bebb6b5c35ec026bf2ec41af40117ff507f53fa7`. Извлечение проверяет allowlist файлов и chain identity; наличие каталога не заменяет успешный native init.
- Bor использует штатный `chain=mainnet`, выбранные genesis/fork settings и Heimdall endpoint; нельзя подменить native chain config под старым datadir.
- Heimdall использует canonical migrated Mainnet genesis из [официального руководства](https://forum.polygon.technology/t/joining-heimdall-v2/21073), immutable [GCS generation 1752157618295837](https://storage.googleapis.com/download/storage/v1/b/mainnet-heimdallv2-genesis/o/migrated_dump-genesis.json?generation=1752157618295837&alt=media). Проверенные metadata: 2358946007 bytes, MD5 `mTR88a1yCwnMMiE8NoOYUg==`, CRC32C `mzeWzw==`. Сам payload 2.36 GB здесь не загружен; SHA-256, chain_id `heimdallv2-137`, initial_height и успешный native parse — обязательный acquisition gate E01/E06. Metadata не выдаётся за проверку содержимого. Init-generated test/default genesis обязательно заменяется проверенным mainnet artifact **до первого native start**.

### Environment init и частичный запуск

Окружение владеет одноразовым init service/job каждой роли. Runtime не вызывает Docker/Kubernetes API и не создаёт соседние процессы. Автомат:

`VALIDATE_INPUTS → LOCK_OWNER → PREPARE → NATIVE_INIT → INSTALL_CONFIG → VERIFY → ENV_READY`.

Manifest: init_schema, deployment_id, owner_role, network/genesis identity, native source/patch commit, input config digest/generation, bootstrap artifact identity, phase и effective paths. Phase markers и ENV_READY fsync сохраняются вместе с необходимым directory sync. Это marker окружения, не обещание атомарности произвольной native DB.

PREPARE создаёт staging внутри того же writable volume. Init работает в staging home, проверяет результат, затем публикует завершённые paths атомарным rename внутри volume; mountpoint не переименовывается. Native keys генерирует штатный init, только для действительно нового home. После crash init сверяет phase/manifest/существующие ключи и продолжает безопасный шаг. Не перегенерировать ключи при retry, не удалять чужой/nonempty datadir, не считать один факт существования каталога ENV_READY. Native-opened state с утраченным marker проходит identity reconciliation; автоматического повторного init/reset нет.

Замена config не копирует весь home: allowlist config/app/client files, проверка parser/effective values, generation digest, atomic file installation до startup. Genesis/key/state paths не входят в произвольную config replacement. Неизвестный incompatible state/config вызывает явную ошибку. No hot reload; изменения применяются restart соответствующего owner.

### Пути, права и volume ownership

| Роль | Read-only inputs | Writable native/effective state |
|---|---|---|
| eth-geth, polygon-l1-geth | `/config/native/geth.toml`, собственный `/run/secrets/engine-jwt.hex` | `/data/node/client` |
| bsc-node | `/config/native/bsc.toml`, `/config/bootstrap/bsc/genesis.json` | `/data/node/client` |
| polygon-bor | `/config/native/bor.toml` | `/data/node/client` |
| eth-prysm, polygon-l1-prysm | `/config/native/prysm.yaml`, собственный JWT | `/data/node/client` |
| polygon-heimdall | `/config/native/heimdall/{config,app,client}.toml`, bootstrap genesis | `/data/node/heimdall`, effective files под `config/`, native keys/state |
| Встроенный adapter каждой из трёх сетей | `/config/extractor.yml`, binding, свои mTLS files | `/data/wal` отдельный volume |
| Gateway каждой сети | `/config/gateway.yml`, binding, mTLS/NATS credentials | `/data/journal` отдельный volume |
| NATS | `/config/nats.conf`, TLS/account auth files | `/data/jetstream` |

Каждое одинаковое внутреннее имя пути относится к **своему** volume. Итог: 11 runtime services и 14 writable state volumes: 7 native, 3 adapter, 3 Gateway, 1 NATS. Init jobs не считаются дополнительными постоянными сервисами. Polygon L1 Geth не содержит наш Ethereum adapter.

Deployment manifest сопоставляет owner → underlying volume identity → path → network/role. Разные mount paths не доказывают разные volumes. Init/preflight отвергает общее writable backing store несовместимых owners, сверяет persistent DB identity и single-writer lock. Для project-owned images baseline runtime UID:GID `10001:10001`; это наше packaging решение, не утверждение о UID upstream images. Init обеспечивает доступ только нужному owner; runtime non-root. Если выбран готовый upstream image, его реальный UID/entrypoint и mapping фиксируются в build manifest, не предполагаются. Writable temp/cache paths объявляются явно; read-only config mount не должен неожиданно стать местом записи native ключей.

### Native commands и effective config

Эти native interfaces проверены по выбранным sources; будущие generated команды проходят smoke/parse test E01/E06. Параметры нашего adapter (`--extractor-config`, `--extractor-mode`) появятся только после integration patches.

| Клиент | Обязательный mapping |
|---|---|
| Geth | `--config /config/native/geth.toml --datadir /data/node/client`; `--authrpc.addr 0.0.0.0 --authrpc.port 8551 --authrpc.jwtsecret /run/secrets/engine-jwt.hex`; vhosts содержит service DNS. `--port`, `--discovery.port`, `--nat` согласованы с P2P mappings |
| Polygon L1 Geth | То же со своим JWT; `--http --http.addr 0.0.0.0 --http.port 8545`, точный vhosts/API allowlist для Heimdall. Основному eth-geth HTTP не нужен для capture |
| Prysm | `--config-file /config/native/prysm.yaml --datadir /data/node/client --execution-endpoint http://<свой-geth>:8551 --jwt-secret /run/secrets/engine-jwt.hex`; `--p2p-tcp-port`, `--p2p-quic-port`, `--p2p-udp-port`; внутренний REST `--http-host 0.0.0.0 --http-port 3500` |
| BSC | Native init: `geth --datadir <staging> --state.scheme hash init <verified-genesis>`; runtime `geth --config /config/native/bsc.toml --datadir /data/node/client --state.scheme hash`; P2P/RPC только явные нужные overrides |
| Bor | TOML `chain="mainnet"`, `datadir="/data/node/client"`, `[p2p] port/nat`; `[heimdall] url="http://polygon-heimdall:1317"`; `[jsonrpc.http] enabled=true, host="0.0.0.0", port=8545`, api/vhosts allowlist для Heimdall |
| Heimdall init | `heimdalld init <moniker> --chain-id heimdallv2-137 --home <staging-home>`; затем проверенные mainnet configs/genesis |
| Heimdall start | `heimdalld start --home /data/node/heimdall --chain mainnet --bridge=false --rest-server=true`; `--all` не включать |

Geth interfaces: [flags.go](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/cmd/utils/flags.go); [BSC flags](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/cmd/utils/flags.go); [Prysm flags](https://github.com/OffchainLabs/prysm/blob/v7.3.0/cmd/flags.go), [beacon flags](https://github.com/OffchainLabs/prysm/blob/v7.3.0/cmd/beacon-chain/flags/base.go); [Bor config](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/builder/files/config.toml); [Heimdall commands](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/cmd/heimdalld/cmd/commands.go).

Heimdall effective overrides: config.toml `[p2p] laddr="tcp://0.0.0.0:30656"`, `external_address="<operator-address>:30656"`; app.toml `[api] enable=true,address="tcp://0.0.0.0:1317"`; `[custom] eth_rpc_url="http://polygon-l1-geth:8545"`, `bor_rpc_url="http://polygon-bor:8545"`, **`bor_grpc_flag="false"` строкового типа**, `comet_bft_rpc_url="http://127.0.0.1:26657"`. Comet RPC local listener соответствует этому URL. `genesis_file="config/genesis.json"`, `node_key_file="config/node_key.json"` разрешаются относительно explicit home; `--home` имеет приоритет над HD_HOME. Для этого non-validator профиля RabbitMQ/bridge не требуется.

Порты P2P точно из NETWORK_PORTS: eth-geth 30303; bsc-node 30311; Bor 30312; Polygon L1 Geth 30313; Heimdall TCP30656; eth-prysm TCP/QUIC31300 и discovery31200; Polygon L1 Prysm TCP/QUIC31310 и discovery31210. Listener/advertise/NAT/Compose mapping/NodePort меняются согласованно. RPC namespaces `eth/net/web3` и Bor `bor` включаются только по нужным native dependencies; wildcard CORS/admin/txpool не являются default.

Native parser определяет precedence TOML/YAML/args; preflight проверяет **effective** значения после этого precedence. Overrides не могут обойти chain identity, hash pin provider, ordinary Bor import и ownership ограничения [NATIVE_CAPTURE_PROTOCOL](docs/NATIVE_CAPTURE_PROTOCOL.md). Неизвестные upstream options проверяются самим pinned parser. Полный rendered config digest и redacted effective settings входят в manifest.

Capture baseline Geth/BSC задаёт `--state.scheme hash --gcmode full`; Bor TOML — верхнеуровневый quoted key `"state.scheme"="hash"`, `gcmode="full"`, `syncmode="full"`. `history.state` — настройка path backend, она не заменяет durable pins. Сам hash scheme без retention patches тоже недостаточен. У supporting Polygon L1 нет capture pins, его профиль остаётся отдельным.

State scheme задаётся уже при создании native DB/genesis и проверяется в записанном store до ENV_READY: runtime override не конвертирует ранее созданный path store в hash. В BSC пустой default init выбирает path, поэтому явный init flag обязателен. [BSC init](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/cmd/geth/chaincmd.go), [ParseStateScheme](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/rawdb/accessors_trie.go).

Точные Bor overrides: `--pipeline.enable-import-src=false`, `--witness.parallelstatelessimport=false`, `--witness.syncwithwitnesses=false`, `--relay.enable-preconfs=false`, `--accept-preconf-tx=false`, `--sequencer.enabled=false`; по D60 дополнительно `--parallelevm.enable=false --parallelevm.enforce=false`. Соответствующие проверенные TOML keys: `[pipeline] enable-import-src`, `[witness] parallel-stateless-import`, `[relay] enable-preconfs`, `[jsonrpc] accept-preconf-tx`, `[sequencer] enabled`. Для остальных используется проверенный CLI, неизвестные TOML аналоги не придумываются. [Bor flags](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/internal/cli/server/flags.go), [config](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/internal/cli/server/config.go).

### Trust bootstrap и NATS permissions

Окружение создаёт отдельную private CA либо принимает операторскую CA. Private CA key хранится у bootstrap/operator, не монтируется в runtime. Server certificate SAN соответствует реальному service DNS/endpoint; client identity содержит deployment/network/role. Gateway проверяет CA/expiry/EKU и allowlist adapter identity для своей сети; одного доверия общей CA недостаточно. Cross-network/role certificate отвергается до handshake/state mutation. TLS hostname verification нельзя отключать. UDS, если явно выбран, проверяет filesystem ownership/peer identity и не разрешает чужую сеть через общий socket.

У каждой Geth/Prysm пары свой 32-byte JWT secret в hex; Polygon L1 не использует основной Ethereum JWT. Gateway получает только свои network-limited NATS credentials; adapter их не получает. Runtime secrets read-only с правами минимального owner. Rotation готовит новый secret generation, атомарно устанавливает files и рестартует затронутые процессы; CA rotation допускает ограниченный trust overlap. Смена NATS credentials всё равно проходит проверку прежнего account/binding. Рабочие secrets сейчас не генерируются.

NATS environment bootstrap и runtime Gateway используют разные credentials. Bootstrap создаёт только configured streams, сверяет существующие; compatible UPDATE допускается только для явно заданной config revision. Никаких автоматических DELETE/PURGE/reset при несовпадении. Runtime: publish в закрытый subject namespace своей сети; request `$JS.API.INFO`, `$JS.API.STREAM.INFO.<stream>`, `$SYS.REQ.USER.INFO`; subscribe только в собственный reply inbox prefix. CREATE/UPDATE/DELETE/PURGE/consumer admin runtime не получает. Environment обязан обеспечить UserInfo responder/permissions и проверяемую account identity; невозможность проверки блокирует публикацию, не заменяется сравнением URL. [NATS account API](https://docs.nats.io/reference/jetstream/api/account), [stream API](https://docs.nats.io/reference/jetstream/api/stream), [UserInfo](https://github.com/nats-io/nats.docs/blob/master/release_notes/whats_new_210.md).

Bootstrap выпускает общий immutable DestinationBinding, который оба owners сохраняют до capture. Expected account/stream/subjects/metadata/limits проверяются Gateway online. NATS server max_payload и stream max_msg_size должны пропускать фактические frame+headers; default проекта 2 MiB, plan size immutable. Отсутствующий/чужой stream даёт CONFIG_BLOCKED; bootstrap созданного окружения не является разрешением уничтожить прежнюю историю. Drain/cutover/Resume — только по [DELIVERY_STATE_MACHINE](docs/DELIVERY_STATE_MACHINE.md).

В NATS v2.15.1 `b2fd3efc7456b11b06a54877ad6f6ccfb911edb4` JetStream создаёт default internal system account при его отсутствии, USER.INFO service автоматически импортируется в обычные accounts. Gateway не нужны system credentials или wildcard `$SYS.>`: сравнивается поле `account` ответа (не display `account_name`). Bootstrap проверяет фактический responder/import/ACL своей конфигурации. [JetStream source](https://github.com/nats-io/nats-server/blob/b2fd3efc7456b11b06a54877ad6f6ccfb911edb4/server/jetstream.go), [UserInfo routing/response](https://github.com/nats-io/nats-server/blob/b2fd3efc7456b11b06a54877ad6f6ccfb911edb4/server/events.go).

### Startup, restart и наблюдаемость

Два независимых автомата:

- Native: ENV_VALIDATED → NATIVE_OPEN → RECOVERY_ONLY → SYNCING → BOUNDARY_H_COMMITTED → CAPTURING.
- Delivery: STORE_VALIDATED → LOCAL_RECOVERY → CONNECTING → DESTINATION_VERIFIED → REPLAY/PUBLISH.

Transient Gateway/NATS outage не блокирует safe local native capture. READY replay не ждёт нового live sync; recovery незакрытого native capture может ждать source inputs. Wrong local store/profile или local fatal прекращает native процесс; Gateway WAL fatal прекращает Gateway, безопасное накопление adapter WAL регулируется D28/D57. Permanent destination mismatch блокирует publication без переписывания pending. Ни один network owner не ждёт прогресса другой сети.

Bor и Heimdall запускаются после собственной config/identity проверки, а не по циклической зависимости health-ready друг от друга. Их native retry/sync зависимости сохраняются; отсутствие source readiness не означает live capture готов. Init/config/transport readiness не заменяет native sync или boundary H.

Replacement: stop → подтвердить завершение старого writer/освобождение volume → start. StatefulSet replicas=1 и force-delete Pod сами по себе не доказывают отсутствие старого writer. Compose исключает второй проект/процесс на том же volume; Kubernetes использует соответствующую стратегию обновления и storage attach fencing. Нет concurrent rolling writers. При недоказанном завершении старого owner новый не стартует. Удаление WAL/state для ускорения restart запрещено.

Готовность и остановка видны в logs по D52, без отдельного API/probe/metrics продукта. Обязательные control events: CONFIG_VALIDATED, INIT_PHASE, INIT_READY, STORE_OPENED, RECOVERY_STARTED/COMPLETED, SOURCE_SYNCING, LIVE_BOUNDARY, CAPTURE_STARTED, TRANSPORT_RETRY, DESTINATION_VERIFIED, BATCH_DURABLE, DELIVERY_CONFIRMED, DRAIN_STARTED/COMPLETED, STOPPED, FATAL. Control outcomes не исчезают при отключении debug. Fields: timestamp, event_code, process_instance, network, role, build/config generation, producer/run; применимые H/hash, batch/plan/binding/drain identities и error class. Secrets, полные credentials/argv не логируются. STOPPED после graceful завершения не обещается при SIGKILL; следующий startup обязан показать recovery.

### Будущие проверки

BOOT-01 — interrupted init в каждой phase, сохранение keys/state, wrong genesis/owner, отсутствующий ENV_READY. CONFIG-01 — native parser/precedence всех overrides, mounted config replacement, mandatory profile reject. MOUNT-01 — 14 owner volumes, одинаковый backing volume под разными paths, UID/permissions, read-only inputs. TRUST-01 — wrong SAN/CA/role/network, rotation и разные JWT пары. NATS-01 — минимальные runtime/bootstrap permissions, UserInfo identity, wrong destination, полный frame с headers. START-01 — 11 services с Gateway/NATS outage, Bor/Heimdall взаимный retry без startup deadlock. REPLACE-01 — задержанный старый writer и новый Pod. LOG-01 — полный обязательный lifecycle по logs без debug/секретов. ARTIFACT-01 — hashes всех materialized artifacts, native init/parse и совместный запуск pinned builds.

Эти критерии закрывают проектные OPS-02…12; E01/E06/E07 не завершены. Image/toolchain/ABI locks, genesis payload proof и runtime smoke tests остаются явными gates, а не скрытыми предположениями о работающей поставке.

## Порты и экспонирование

> Точные native mappings, listener scopes и bootstrap — [DEPLOYMENT_BOOTSTRAP_CONTRACT](docs/DEPLOYMENT_BOOTSTRAP_CONTRACT.md). D58 обновил Heimdall до v0.13.2, порты сохранены. CometBFT RPC 26657 baseline слушает loopback внутри Heimdall. По D52 отдельная probe/shutdown спецификация не вводится, обязательные состояния отражаются в logs.

2026-10-21. DEP-03.2b.2b.2. Пользователь поручил выбрать удобный вариант самостоятельно. Только ТЗ; конфигурации не запускались. Все значения переопределяются по D48. Неизменны границы D49: наружу только P2P, внутренние API отдельно, собственная идентичность каждого клиента.

### P2P по экземплярам

| Компонент | TCP | UDP | Назначение |
|---|---|---|---|
| eth-geth | 30303 | 30303 | Execution peers и discovery |
| eth-prysm | 31300 | 31300, 31200 | TCP libp2p; UDP QUIC 31300 и discv5 31200 |
| bsc-node | 30311 | 30311 | Execution peers и discovery |
| polygon-bor | 30312 | 30312 | Bor peers и discovery |
| polygon-heimdall | 30656 | — | CometBFT peers |
| polygon-l1-geth | 30313 | 30313 | Отдельные Ethereum execution peers и discovery |
| polygon-l1-prysm | 31310 | 31310, 31210 | TCP libp2p; UDP QUIC 31310 и discv5 31210 |

Это проектные значения, не заявление о штатных портах upstream. Удобство: одинаковый номер в native listener, публикации Compose, Kubernetes Service/targetPort/nodePort и объявляемом endpoint. Поэтому для P2P выбираются явные overrides штатных портов там, где таблица отличается. Это уточнение D49 в рамках поручения пользователя; внутренние RPC/Engine API порты сохраняются. Все номера попадают в стандартный NodePort range 30000–32767; уже занятые порты или изменённый диапазон кластера требуют согласованного override всей цепочки.

Bootnodes/seeds берутся из сетевого профиля выбранной версии клиента с возможностью переопределения. Публичные IP из upstream примеров не копируются как адреса нашей ноды. P2P advertised IP задаётся для каждого экземпляра окружением, а не обнаруживается через Kubernetes API приложением. При NAT настроенный внешний порт должен совпадать с таблицей либо со всем её override. Готового универсального публичного IP в шаблонах быть не может: адрес задаёт оператор.

### Docker Compose

- Отдельная bridge-сеть каждого блокчейн-контура; внутри Polygon находятся его собственные L1 Geth/Prysm. Gateway дополнительно подключается к транспортной сети NATS. Имена сервисов разрешаются через DNS соответствующей сети.
- Только P2P публикуется на хост как `порт:тот_же_порт`, TCP/UDP отдельными mappings. Все компоненты можно запустить на одном хосте без взаимных конфликтов из таблицы.
- RPC, Engine API, Gateway и NATS не получают host port mappings по умолчанию. Опциональный диагностический override привязывает выбранный порт к 127.0.0.1; его host port выбирается без конфликтов.
- Конфигурация native listener должна принимать нужные соединения внутри контейнерной сети; upstream localhost-only default не используется для межконтейнерных связей. Доступные APIs ограничиваются нужными связями, JWT/mTLS сохраняются.

### Kubernetes

- Для внутренних endpoints — ClusterIP Services; для стабильной идентичности StatefulSet предусматривается governing headless Service. P2P каждого native экземпляра получает отдельный NodePort Service, не смешанный с API ports.
- P2P Service использует явные nodePort из таблицы, именованные TCP/UDP ports и `externalTrafficPolicy: Cluster`. Это позволяет принимать соединения на выбранном достижимом узле независимо от размещения единственного Pod. Сохранение исходного IP пира при таком маршруте не обещается.
- Advertised address — явно заданный достижимый IP узла либо внешний адрес с корректным TCP/UDP forwarding. Нужны сетевой маршрут и разрешённые порты. При потере этого адреса его обновляет оператор; автоматический failover публичного endpoint и HA не входят в текущий ТЗ.
- LoadBalancer доступен как override при наличии подходящего контроллера и поддержки нужных TCP/UDP ports. HTTP Ingress, hostNetwork и hostPort для P2P по умолчанию не используются. NodePort не требует cloud LoadBalancer, но сам по себе не создаёт публичный IP или NAT-маршрут.
- Для диагностики API можно использовать локальный port-forward на 127.0.0.1 без постоянной публичной публикации. Только P2P Service получает внешнюю доступность.

### Внутренние интерфейсы

Все порты ниже TCP, доступны только нужным компонентам внутри окружения. Наличие строки не означает обязательное включение всех RPC namespaces.

| Получатель | Порт | Клиент / назначение |
|---|---|---|
| eth-geth | 8551 | eth-prysm → Engine API с JWT основной пары |
| polygon-l1-geth | 8551 | polygon-l1-prysm → Engine API с JWT Polygon L1 |
| polygon-l1-geth | 8545 | polygon-heimdall → Ethereum JSON-RPC |
| polygon-bor | 8545 | polygon-heimdall → Bor JSON-RPC |
| polygon-heimdall | 1317 | polygon-bor → Heimdall REST |
| polygon-heimdall | 26657 | Внутренняя диагностика CometBFT / будущая проверка синхронизации |
| eth-prysm, polygon-l1-prysm | 3500 | Внутренний Beacon REST / будущая проверка синхронизации |
| eth-geth, bsc-node | 8545 | Внутренняя диагностика JSON-RPC при включении; не источник native capture |
| eth-gateway, bsc-gateway, polygon-gateway | 50051 | Только свой адаптер → gRPC/mTLS |
| nats | 4222 | Gateway → NATS с принятыми credentials/TLS настройками |

WebSocket, validator RPC, metrics/admin, NATS monitoring/cluster ports и дополнительные Heimdall gRPC endpoints не публикуются автоматически. Если конкретные flags/probes потребуют дополнительный внутренний интерфейс, его назначение явно фиксируется на следующем этапе. Staking/validator duties не добавляются.

### Проверенные источники и оставшаяся работа

- [Geth CLI](https://geth.ethereum.org/docs/fundamentals/command-line-options): настраиваемые P2P/discovery, HTTP RPC/Engine API, NAT и JWT.
- [BSC v1.7.8 flags](https://github.com/bnb-chain/bsc/blob/v1.7.8/cmd/utils/flags.go): P2P и RPC параметры native клиента.
- [Prysm v7.3.0 shared flags](https://github.com/OffchainLabs/prysm/blob/v7.3.0/cmd/flags.go): отдельные p2p-tcp-port, p2p-quic-port, p2p-udp-port; [сетевые интерфейсы](https://docs.prylabs.network/docs/prysm-usage/p2p-host-ip).
- [Bor v2.10.3 config](https://github.com/0xPolygon/bor/blob/v2.10.3/builder/files/config.toml): JSON-RPC и Heimdall REST; [Heimdall v0.13.2 config](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/packaging/templates/config/mainnet/config.toml): P2P external_address и CometBFT RPC.
- [Kubernetes Service](https://kubernetes.io/docs/concepts/services-networking/service/): NodePort range, TCP/UDP Services, ClusterIP и LoadBalancer.
- [NATS configuration](https://docs.nats.io/running-a-nats-service/configuration): внутренний клиентский listener.

Источники подтверждают интерфейсы; они не являются результатом запуска нашей конфигурации. Документальная привязка flags/config/init/mounts завершена в DEPLOYMENT_BOOTSTRAP_CONTRACT; parse/init/runtime tests остаются E01/E06. Готовность по logs и штатному управлению окружением соответствует D52.

## Стек и границы сборок

### Компоненты и интеграция

| Компонент | Baseline | Реализация / граница |
|---|---|---|
| Ethereum native adapter | Go внутри Geth v1.17.9 | Native hooks/state journal + canonical notifications; отдельный client build |
| Ethereum consensus | Prysm 7.3.0, кандидат D47; image digest не закреплён | Отдельный компонент основной Ethereum-ноды; отдельный service/StatefulSet по NODE_DEPLOYMENT_SPEC |
| Polygon L1 источник | Отдельный Geth + Prysm, D44 | Собственная Ethereum-нода Polygon без нашего Ethereum-адаптера, со своими процессами/datadirs; основная Ethereum-нода не используется |
| BSC native adapter | Go внутри BSC v1.7.8 | Обычные tx и Parlia Finalize capture; отдельный client build |
| Polygon adapter | Go внутри Bor v2.10.3, Heimdall-v2 v0.13.2 | Native tx и StateSync/Finalize, отдельный optional legacy scope; последовательный профиль D60 |
| Canonical Coordinator | Библиотечный модуль адаптера/контура соответствующей сети | Отдельный общий сервис для всех сетей не вводится. Каждый producer/run владеет своим lifecycle state; единого владельца для устранения пересечений нет; см. NATIVE_CAPTURE_PROTOCOL |
| Gateway/publisher | Go 1.27.2, gRPC-Go 1.84.0, nats.go 1.54.0; один экземпляр роли publisher на сеть | Единый исходный код/binary для всех сетей, отдельный процесс и конфиг каждой сети; NATS SDK только здесь |
| Wire schema | Protobuf proto3 с explicit presence и closed oneof | Одна versioned schema, native validators, transaction/block metadata/lifecycle/parts из MESSAGE_CONTRACT |
| Генерация Go | protoc 36.2 + protobuf-go/protoc-gen-go 1.36.12 | protoc-gen-go-grpc из согласованного source release grpc-go; точный module tag/commit и lock фиксируются E01/E02, не подменяются версией server SDK |
| Adapter → Gateway | gRPC streaming; локальный UDS при совместном размещении, TCP+mTLS при сетевом соединении | Оба конца проверяют schema/profile/размер/целостность; production topology определяет endpoint, не семантику данных |
| WAL/publication journal | RocksDB 10.4.2; Go grocksdb v1.10.2 | Sync WriteBatch, WAL enabled, отдельный owner; [нормативный автомат](docs/DELIVERY_STATE_MACHINE.md). BlobDB optional, backup исключён D27 |
| Приёмочный harness | Go + тот же protobuf decoder + JetStream client | Только проверка опубликованных сообщений против fixtures; не промышленный downstream |
| Локальный запуск | Docker Compose: все execution/consensus/supporting-компоненты трёх нод, адаптеры, WAL/Gateway и NATS | Полная компоновка и конфигурация проекта по D43; [детализация](docs/NODE_DEPLOYMENT_SPEC.md) |
| Production | Kubernetes: все компоненты нод, адаптеры, независимые workload/volume/config контуры сетей и NATS | D43: готовим весь технический комплект; NATS chart/server candidates и [исследование](docs/research/NATS_DEPLOYMENT_STACK.md) сохраняются; production-методика отдельно |

Native pins и расхождения toolchains подробно сохранены в [CLIENT_MATRIX.md](docs/CLIENT_MATRIX.md). Новый baseline Gateway не изменяет compiler versions native клиентов.

### Почему SDK не встраивается во все native clients

Pinned gRPC-Go 1.84.0 требует Go 1.25.0; nats.go 1.54.0 — Go 1.26.0. protobuf-go 1.36.12 требует Go 1.23. Поэтому импорт нового NATS SDK в Geth/BSC candidate с Go 1.25 создаёт лишнее требование смены toolchain. Адаптер знает gRPC/Protobuf, Gateway — NATS; процессная граница уже принята D19. Генерируемые Go bindings всё равно проверяются в каждом client module отдельно.

Источники: [grpc-go go.mod](https://github.com/grpc/grpc-go/blob/v1.84.0/go.mod), [nats.go go.mod](https://github.com/nats-io/nats.go/blob/v1.54.0/go.mod), [protobuf-go go.mod](https://github.com/protocolbuffers/protobuf-go/blob/v1.36.12/go.mod), [Go distributions](https://go.dev/dl/?mode=json). Go 1.27.2 — кандидат общего сервиса на дату проверки, не утверждение об установленном компиляторе проекта.


Источники: [tonic workspace](https://github.com/grpc/grpc-rust/blob/tonic-v0.14.6/Cargo.toml), [tonic-prost](https://github.com/grpc/grpc-rust/blob/tonic-v0.14.6/tonic-prost/Cargo.toml), [tonic-prost-build](https://github.com/grpc/grpc-rust/blob/tonic-v0.14.6/tonic-prost-build/Cargo.toml), [prost](https://github.com/tokio-rs/prost/blob/v0.14.4/Cargo.toml).

### Версионирование и проверка

- Исходники native clients остаются на отдельных закреплённых commits; общий транспортный модуль не требует слияния их несовместимых Go/Cargo dependencies в один build graph.
- Теги библиотек выше — кандидаты на дату проверки. Перед воспроизводимой сборкой E01 записывает commit/checksum, compiler, generator versions, dependency lock и image digest. В конфигурации поставки не используются плавающие `latest`.
- Версии protoc, Go generators, runtimes и wire schema — разные величины. Они явно перечисляются в build manifest, не выводятся друг из друга.
- Go fixtures проверяют поля/presence/oneof. Protobuf bytes независимых сериализаций не обязаны совпадать; retries сохраняют исходные bytes/digest события.
- NATS server version, Helm chart version и appVersion фиксируются отдельно. Наличие Helm chart не выбирает автоматически cluster sizing, storage class или политику потери диска.
- Конкретный Linux base image, CPU architectures и поддерживаемые Kubernetes/Compose versions закрепляются после build smoke tests; не заявляется поддержка каждой платформы из-за наличия Dockerfile.
- YAML/restart определены LIVE_CONFIG_SPEC; parser/implementation E06. Hot reload, metrics и численные performance thresholds не являются открытыми обязательными задачами. Собственный orchestration/operator не добавляется.

Релизы/модули для проверки: [gRPC-Go 1.84.0](https://github.com/grpc/grpc-go/releases/tag/v1.84.0), [nats.go 1.54.0](https://github.com/nats-io/nats.go/releases/tag/v1.54.0), [protoc 36.2](https://github.com/protocolbuffers/protobuf/releases/tag/v36.2), [protobuf-go 1.36.12](https://github.com/protocolbuffers/protobuf-go/releases/tag/v1.36.12), [grpc generator module](https://github.com/grpc/grpc-go/blob/v1.84.0/cmd/protoc-gen-go-grpc/go.mod).

### Проверяемый результат подэтапа

Для каждого компонента определён язык/runtime, boundary и version candidate. Это проект, не build/runtime gate. Проверки E01/E02/E03: отдельные builds ETH/BSC/Bor, native seams, bindings, large records, Gateway/NATS и validators. Независимые expected results/evidence — IMPLEMENTATION_GATES. Rust graph отложен.

.04/.06/.07 — исторические архитектурные подэтапы. Текущие normative contracts: NATIVE_CAPTURE_PROTOCOL, DELIVERY_STATE_MACHINE, DEPLOYMENT_BOOTSTRAP_CONTRACT и IMPLEMENTATION_GATES. Следующая задача документации определяется PROJECT_STATUS; CFG live описан, JOB/history deferred. D64 навсегда ограничивает нашу работу идеальным ТЗ.

## REC сценарии

### Сценарии приёмки будущей реализации

| ID | Точка/ошибка | Проверяемый результат |
|---|---|---|
| REC-T01 | Crash на native admission/исполнении/decision/delivery staging | NATIVE_CAPTURE_PROTOCOL задаёт pin + static capsule + ADMITTED до исполнения, dependency log, EXEC_READY, child-root/decision marker и READY mirror. E01 доказывает native seam, E03 delivery часть, E05 полный matrix. До ADMITTED кандидат не принят; после него obligation не теряется. Runtime proof ещё TODO |
| REC-T02 | Crash после READY, до Prepare | исходные bytes/ID восстановлены, публикация продолжается |
| REC-T03 | Потерян Prepare reply / Gateway store до producer plan commit | повтор reservation; прежний неподтверждённый plan ещё ничего не публиковал |
| REC-T04 | Gateway store потерян после публикации части | durable producer plan восстанавливает те же frame bytes/epoch/sequence; повторы допустимы |
| REC-T05 | Crash Gateway после PubAck, до записи bitmap | повтор тех же сообщений, IngestAck только после всех ACK |
| REC-T06 | Потерян IngestAck | Gateway повторяет receipt или сообщения; producer payload сохранён |
| REC-T07 | Crash внутри producer/Gateway cleanup/Release | ACKED_CLEANUP/outbox и RELEASING/cursor переживают каждый chunk; lost Gateway допускает authenticated RecoveryRelease следующего ACKed batch без удалённого payload |
| REC-T08 | Поздний блок ACKed раньше раннего | ранний payload сохранён, frontier не перескакивает дыру |
| REC-T09 | Пустой блок / multipart | metadata доставляется; ACK части не завершает batch |
| REC-T10 | NATS offline; затем reconnect | накопление без прикладной квоты, bounded RAM, retry/drain; новые блоки продолжают capture при доступном диске |
| REC-T11 | Disk-full / sync error / повреждение БД | явная ERROR, нет ложного READY/ACK и silent skip; automatic repair с отбрасыванием данных запрещён |
| REC-T12 | Второй writer / старый stream после reconnect | второй opener/старый session отвергнут; только подтверждённое завершение writer позволяет замену |
| REC-T13 | Producer volume потерян | Новый run с явной потерей прежних локальных obligations; старый Gateway state не уничтожается автоматически. YAML не восстанавливает cursor/payload. Историческая выгрузка исключена; гарантия восстановления после полной потери диска не добавляется |
| REC-T14 | Незакрытый ранее начатый capture / reorg после ACK cleanup и restart | восстановление конкретного batch/activation references либо ERROR при отсутствии state; нет сканирования всего пропущенного периода. Live продолжает текущие наблюдения по D37; отсутствие прежних данных не маркируется успешной доставкой |
| REC-T15 | Неверный digest, schema, ACK или лимит ниже pending part | явная ошибка, неизменные pending bytes сохранены |
| REC-T16 | Live drain/Seal/Resume и потеря ReleaseAck; history CloseRun DEFERRED | Durable DRAIN/DRAINED, admission generations, ordered Release/frontiers; нет новых admissions в drain, поздний old RPC не воскрешает state, pending без ACK не удаляется |
| REC-T17 | Часть batch подтверждена; изменены account/stream/subjects; restart или потеря Gateway store | несовместимая смена отвергнута до публикации, исходные bytes/plan сохранены; ACK разных назначений не закрывают batch. Смена адреса того же назначения проверяется отдельно; изменение назначения допускается после подтверждённого drain обоих owners |

Fixtures выполняются для текущих Go journals и Gateway/NATS в E01/E03/E05/E06 по IMPLEMENTATION_GATES; Rust/history variants DEFERRED D38/D42. Данные и expected имеют независимое происхождение. Это tests разработки, не production completeness verifier. Исторический .06 не закрывает ни один runtime gate; актуальный проект — NATIVE_CAPTURE_PROTOCOL/DELIVERY_STATE_MACHINE.

## План доказательств и границы модулей

D64: документальные требования к доказательствам для проектируемой системы. Порядок из [DEVELOPMENT_PLAN](docs/DEVELOPMENT_PLAN.md) **E01 → E02 → E03 → E04 → E05 → E06 → E07** сохраняется как содержание ТЗ; code/build/runtime tasks не выполняются в нашей работе и не ожидают разрешения. SPECIFIED в [реестре исправлений](docs/DESIGN_REPAIR_STATUS.md) означает готовый проверяемый проект.

### Ранний harness и зависимости gates

| Этап / существующие ID | Что доказать до выхода | Что ещё не считается доказанным |
|---|---|---|
| E01-01…05, отдельно .ETH/.BSC/.POLY | Изолированные pinned builds; минимальный native fixture runner; реальные execution/canonical/finality callers; admission/pin/owned snapshot/decision marker seam; native-only recovery; отсутствие bypass обязательного пути | Полный protobuf, production transport и вся семантика E04 ещё не готовы |
| E02-01…05/08/10 | Компилируемые схемы/валидаторы, AUTH/SELF/LIFE, receipt provenance, immutable IDs/bytes, transport/chunk layout, bounded serialization и spool; closed extension registry | Работа с настоящим NATS и весь native crash matrix ещё не приняты |
| E03-01…05/11 | Реальные RocksDB/gRPC/NATS, delivery-only fault matrix от durable intent/READY; Prepare/Release/fencing/destination/drain; integration smoke с E01 fixtures | Полная комбинация native multi-block reorg/finality+delivery не называется пройденной до E05 |
| E04-01/03/04 | Все обязательные native tx paths/fields выбранных трёх live profiles, включая native system tx; rollback/existence/fees/SELFDESTRUCT/Bor scopes; большой hook field и worker stalls | Исторические профили, optional outside-tx extension и performance SLO не являются gate |
| E05-01…04 | Полный native→WAL→Gateway→NATS crash/reorg/finality/restart matrix; durable H и known-branch replay; cleanup/GC/late callback, downgrade/upgrade/drain | HA, backup и восстановление потерянного ACKed NATS store не обещаются |
| E06-07…09/12/13 | Полный пользовательский YAML/native config/init/Compose/Kubernetes комплект; 11 services/14 volumes, artifact proof, wrong profile/peer, H+1 и независимый replay | Ранний fixture runner не заменяет этот комплект; history CLI не добавляется |
| E07-02/03/08/09 | End-to-end acceptance отдельно ETH/BSC/POLY; clean environment reproduction, runbook, isolation и опубликованные сообщения | Публичная open-source упаковка и performance/production-методика отложены |

E01-01 делится до начала исполнения на `.build.ETH/BSC/POLY`, `.supporting`, `.harness`, `.artifacts`. Native source state fixtures, bodies/headers/consumed Heimdall responses, fork config, secrets для тестов и команды запуска перечисляются **до** соответствующего proof. Нельзя требовать скрытую уже синхронизированную ноду автора. Test-only harness использует pinned native execution/decision paths и детерминированные inputs; соответствие реальному import path проверяется отдельно. Mainnet-only продукт не запрещает локальный test-only execution harness и fault injection.

E01-02 делится на `.admission`, `.ownership`, `.pin`, `.decision` для каждой сети; E01-04 проверяет их ранние power-loss seams до определения общей production модели. Минимальный внутренний spool/marker proof не обязан уже быть schema E02. E04 расширяет семантическое покрытие, E05 сочетает доказанные seams в полный lifecycle matrix; окно native/WAL нельзя впервые исследовать лишь в E05.

E03 использует настоящий NATS с минимальными test CA/credentials/stream bootstrap и независимыми test directories. Этот ранний harness создаётся в E01/E03, полный replaceable user deployment — E06. REC-T01 разбит: E01 доказывает native admission/state seam, E03 — delivery часть, E05 — совмещённые native crash interleavings. Ни один частичный PASS не обозначается полным REC-T01. Остальные REC-T01–17 применяются только в текущем live scope; historical variants помечаются DEFERRED.

### Независимые эталоны

Каждый semantic fixture содержит pinned source state/blocks/fork и независимо подготовленные expected outcomes: native receipts/state commitments плюс вручную проверяемый execution/action сценарий либо отдельный reference path. Проверка только hash корня не доказывает полноту trace; только receipts не доказывают before/after. Expected output не вычисляется тем же normalizer, который тестируется. Общий protobuf decoder допустим; общий ошибочный алгоритм expected semantics — нет.

Reorg/finality/known-branch/fault воспроизводятся сохранённым сценарием и seed; ожидание случайного mainnet события не является тестом. Fault harness умеет остановить процесс до/после каждого durable marker и storage sync, задержать callback/worker, потерять только один owner store, подменить destination/config и повторить сообщения. Crash с сохранённой OS cache отдельно от simulated power loss/потери volatile native trie: результаты не смешиваются.

Mutation sanity gate: намеренно испорченные parity handling, SELFDESTRUCT action, receipt created_contract, dropped raw chunk, неверный revision или early ACK должны давать FAIL соответствующего oracle. Проверка не сводится к тому, что serializer и decoder согласны друг с другом. Fixtures и expected artifacts — тестовые файлы разработки, не production consumer/полнота verifier.

### Будущие модули и независимые build graphs

| Каталог | Владение / интерфейс |
|---|---|
| `protocol/proto`, `protocol/validators` | Versioned logical/wire schema, presence/oneof, transport frame и bounded validators; не native DB |
| `core/capture`, `core/normalization` | Owned raw chunks/typed records, spool и disk indexes; только собственные DTO, без импортов внутренних типов сразу нескольких fork |
| `core/delivery`, `core/journal` | Producer intent/READY/plan/release, common error/compatibility contracts и RocksDB adapter |
| `adapters/geth`, `adapters/bsc`, `adapters/bor` | Native admission/mutator/decision/pin bridges, fork semantics, toolchain каждого клиента |
| `gateway` | Fenced ingest, reservations, immutable plans/frames, NATS publisher; отдельный process/store на сеть |
| `config` | Strict project YAML, role checks, effective native profile constraints и DestinationBinding |
| `deploy/compose`, `deploy/kubernetes`, `deploy/init` | Полный user stack, native config templates, artifact acquisition, secrets/bootstrap и owner mapping |
| `fixtures`, `test/harness`, `test/evidence` | Input/expected/fault scenarios, независимый считыватель, evidence manifests |
| `docs` | Решения, source provenance, runbooks и состояние |

Это проект будущего дерева, каталоги кода сейчас не создаются. Geth/BSC/Bor — отдельные Go modules/build graphs с native fork dependencies. Shared core не импортирует все fork state types и не заставляет их обновить SDK/toolchain ради Gateway. NATS SDK только в Gateway. Schema generation проверяется в каждом graph. Rust/Nitro/Reth/ZKsync/Avalanche/backfill не являются prerequisites первого контура. При будущих расширениях их модули добавляются отдельным решением.

### Evidence manifest и stop conditions

Один файловый manifest на fixture run, с schema_version и статусом PASS/FAIL/NOT_RUN/DEFERRED. Обязательные поля:

- requirement_ids и audit_finding_ids; task_id с network suffix; fixture_id/version;
- network/profile/fork; native source commit, patch commit, dependency lock digest, compiler/generator versions, image digest/architecture;
- input artifact paths + SHA-256; expected artifact paths + SHA-256 и независимый expected_source/method;
- scenario/seed, fault boundary, storage/cache model, run command, environment versions;
- actual output/log paths + SHA-256, normalized comparator version, verdict и объяснение различий;
- started/finished time, evidence producer, parent integration run при необходимости.

Артефакты не содержат secrets. Пустое поле image digest у source-only анализа явно означает NOT_RUN для runtime, не PASS. Новый native commit/profile/schema/plan/storage version инвалидирует связанные старые результаты до повторной проверки; несовместимость не маскируется переносом галочки.

При missing mandatory native path, неустранимом snapshot alias, потере recovery pin, нарушении marker ordering, неограниченном collector memory path, конфликте identity/immutable bytes или false ACK gate останавливается. Сохраняются минимальный counterexample и evidence; исправляется механизм и повторяются затронутые проверки. Нельзя молча снижать полноту, отключать обязательный native type, пропускать poison record или называть runtime-профиль supported. Изменение принятого поведения/клиента выносится пользователю; инженерное исправление в рамках контракта выполняется самостоятельно.

Требование ТЗ к описываемому процессу разработки: переход между этапами только после evidence всех обязательных подзадач ETH/BSC/POLY и сохранения результатов. Optional/deferred задачи не используются ни как ложный PASS, ни как блокер gate. По D64 наша работа ограничена проектированием этих правил; исполнять этапы не предполагается.

### Traceability 41 находки

| Finding | Нормативный контракт / fixture | Первый proof → итоговый gate |
|---|---|---|
| NAT-01 | TRANSACTION_TYPE_REGISTRY; AUTH-01…04 | E02 → E04 |
| NAT-02 | NATIVE_CAPTURE_PROTOCOL; OWNED-01 | E01 → E04 |
| NAT-03 | NATIVE_CAPTURE_PROTOCOL; EXIST-01 | E01 → E04 |
| NAT-04 | MESSAGE_CONTRACT; SELF-01…04 | E02 → E04 |
| NAT-05 | NATIVE_CAPTURE_PROTOCOL; SCOPE-01 | E01 → E04 |
| NAT-06 | MESSAGE_CONTRACT/native provenance; RECEIPT-01 | E01/E02 → E04 |
| NAT-07 | MESSAGE_CONTRACT; LIFE-01/02 | E02 → E05 |
| NAT-08 | MESSAGE_CONTRACT; LIFE-03/04 | E02 → E05 |
| NAT-09 | NATIVE_CAPTURE_PROTOCOL; BRANCH-01 | E01 → E05 |
| NAT-10 | NATIVE_CAPTURE_PROTOCOL; DECISION-01 | E01 → E05 |
| DLV-01 | NATIVE_CAPTURE_PROTOCOL; CAPTURE/PIN-01 | E01 → E05 |
| DLV-02 | DELIVERY_STATE_MACHINE; ATTEMPT-01 | E03 → E05 |
| DLV-03 | DELIVERY_STATE_MACHINE; PREPARE-01 | E03 |
| DLV-04 | DELIVERY_STATE_MACHINE; FRONTIER-01 | E03 |
| DLV-05 | DELIVERY_STATE_MACHINE; FRONTIER/FENCE-01 | E03 |
| DLV-06 | DELIVERY_STATE_MACHINE; FENCE-01 | E03 → E05 |
| DLV-07 | DELIVERY_STATE_MACHINE; DRAIN-01 | E03 → E05/E06 |
| DLV-08 | DELIVERY_STATE_MACHINE; DEST-01 | E03 → E06 |
| DLV-09 | DELIVERY_STATE_MACHINE; MEM/NATS-01 | E02 → E03 |
| DLV-10 | NATIVE_CAPTURE_PROTOCOL/DELIVERY_STATE_MACHINE; OWNED/MEM-01 | E01/E02 → E04 |
| DLV-11 | DELIVERY_STATE_MACHINE; UPGRADE-01 | E03 → E05/E06 |
| DLV-12 | DELIVERY_STATE_MACHINE; POISON-01 | E03 → E05 |
| DLV-13 | DELIVERY_STATE_MACHINE; ENOSPC-01 | E03 → E05 |
| OPS-01 | DELIVERY_STATE_MACHINE; DRAIN/DEST-01 | E03 → E06 |
| OPS-02 | DEPLOYMENT_BOOTSTRAP_CONTRACT; BOOT-01 | E01 harness → E06 |
| OPS-03 | DEPLOYMENT_BOOTSTRAP_CONTRACT; CONFIG-01 | E01 → E06 |
| OPS-04 | DEPLOYMENT_BOOTSTRAP_CONTRACT; MOUNT-01 | E01 → E06 |
| OPS-05 | DEPLOYMENT_BOOTSTRAP_CONTRACT; TRUST-01 | E03 harness → E06 |
| OPS-06 | DEPLOYMENT_BOOTSTRAP_CONTRACT; NATS-01 | E03 → E06 |
| OPS-07 | DEPLOYMENT_BOOTSTRAP_CONTRACT; START-01 | E01/E03 → E06 |
| OPS-08 | DEPLOYMENT_BOOTSTRAP_CONTRACT; REPLACE-01 | E06 |
| OPS-09 | DEPLOYMENT_BOOTSTRAP_CONTRACT; MOUNT-01 | E06 |
| OPS-10 | DEPLOYMENT_BOOTSTRAP_CONTRACT; LOG-01 | E03 → E06/E07 |
| OPS-11 | DEPLOYMENT_BOOTSTRAP_CONTRACT; ARTIFACT-01 | E01 → E06 |
| OPS-12 | DEPLOYMENT_BOOTSTRAP_CONTRACT; TRUST-01 | E03 → E06 |
| DEV-01 | Этот документ: ранние seams, partial REC-T01 | E01/E03/E05 gates |
| DEV-02 | Этот документ: входы/команды раннего harness | E01/E03 gates |
| DEV-03 | Этот документ: independent expected и mutation sanity | E01/E02 → E07 |
| DEV-04 | DEVELOPMENT_PLAN: статусы каждой задачи и live gates | Backlog export перед каждым этапом |
| DEV-05 | Этот документ: дерево и dependency boundaries | E01 build graph review |
| DEV-06 | Этот документ: evidence manifest/stop conditions | Каждый gate, финально E07 |

Ни одна строка этой таблицы не утверждает выполненный runtime proof. В каждом gate сохраняется конкретный verdict и ссылки на фактические артефакты.

## Задачи разработки E01–E07

### Правила выполнения

По D64 единственная цель нашей работы — идеальное ТЗ; реализации не будет никогда. Этапы E01–E08 ниже — спецификация порядка разработки и приёмки описываемой системы, не поручение их выполнять. Версии клиента, библиотек, runtime, хранилищ и инфраструктуры в ТЗ нельзя выбирать по наличию на машине.

Каждая задача имеет постоянный ID. Статусы: TODO, IN_PROGRESS, BLOCKED, DONE, DONE_DOCS_ONLY, DEFERRED_BY_USER, OPTIONAL, REMOVED/OUT_OF_SCOPE. По умолчанию незавершённая задача без явной пометки — TODO; deferred/optional не входят в обязательный gate. DONE требует артефакта и evidence по IMPLEMENTATION_GATES. Перед началом записать ID в PROJECT_STATUS; после завершения сохранить результат, проверки, следующий шаг и commit. SPECIFIED документации не является DONE реализации.

Крупную задачу перед исполнением разбивать на подзадачи с суффиксами .1, .2 и отдельными критериями; уже присвоенные ID не переиспользовать. Коммиты, публикации и деплой фиксировать только после фактического выполнения с hash/URL и результатом проверки. Сохранение файлов не означает наличие удалённой резервной копии.

### Принятая последовательность

E00 Решения → E01 Native-захват трёх сетей → E02 Общая типизированная схема → E03 Независимая доставка каждой сети через собственные WAL/Gateway в NATS → E04 Полные детали транзакций → E05 Reorg и восстановление каждой сети → E06 Live-конфигурация и доставка → E07 Функциональная приёмка трёх сетей → E08 Другие сети. История отложена, не блокирует цепочку.

Принято D16: конечный результат — унифицированные сообщения в NATS.IO. Последующая обработка исключена из этапов, стека и приёмки. Проверяющий считыватель — только тестовый инструмент.

Действующая граница D36–D42: live без полного snapshot адресов, без истории и поиска пропусков; хронологические связи и детали сохраняются в сообщениях. Open-source намерение сохраняется, лицензия/публикация отложены D41. YAML live/Gateway и применение перезапуском описаны в LIVE_CONFIG_SPEC по D39/D48; найденные технические пробелы не возвращают закрытый выбор hot reload.

Принято D42: первый контур — Ethereum Mainnet, BSC Mainnet и Polygon PoS. Все три проверяются в E01 до общей схемы E02; Base/Arbitrum отложены.

Задачи текущего этапа выполняются отдельно для ETH/BSC/POLY; родитель закрывается после трёх сетей. Подзадачи .BASE/.ARB отложены D42. Исторические задачи отложены D38. Каждая сеть имеет собственные WAL, журнал Gateway и процессы; evidence хранится раздельно.

Приняты D14/D15/D19: отдельный native node/adapter/WAL/Gateway каждой сети, общий NATS с изолированными streams/subjects. Команда N–M отложена D38; текущий live YAML и restart приняты D39/D48.

Принято D36: основной сборщик только live, history отдельным сервисом на узле ноды по N–M. Default три часа и история через start_block/reload/apply отменены. Единый интерфейс без Kubernetes-зависимости, независимые сети и RocksDB/ACK сохраняются. D37 закрывает CFG-R3: поиск пропусков и организация их восполнения вне scope; pending WAL доставляется автоматически.

### E01 — воспроизводимая сборка и доказательство захвата

Зависимости: E00; для всех трёх сетей определены варианты сети, кандидаты клиентов и границы прототипов. Стек: родной клиент и его язык для каждой сети. Минимальный тестовый execution harness, затем реальный client import path. Один общий Ethereum-прототип не закрывает E01.

- [ ] E01-01 Закрепить source/patch/toolchain/dependency/artifact pins, отдельные builds и минимальный fixture/bootstrap harness. Готово: clean build и команды, inputs/state/supporting services/test credentials известны до proof; нет скрытой авторской ноды. Подзадачи/manifest — IMPLEMENTATION_GATES.
- [ ] E01-02 Доказать карту native callers, admission/owned snapshots/state pins/decision markers всех трёх сетей по NATIVE_CAPTURE_PROTOCOL. Готово: обязательные типы/поля имеют источник; minimal bridge работает в реальном import path, bypass запрещён; outside-tx публикация optional.
- [ ] E01-03 Захватить transfer, nested call/revert, fee effects, родные системные транзакции и пустой блок. Готово: полные tx records/before/after сверены с независимым результатом. Вне-транзакционные начисления проверяются только при реализации расширения.
- [ ] E01-04 Проверить стабильные offsets и повтор fixture, power loss на ранних admission/pin/EXEC_READY/decision seams, native-only replay и H guard. Готово: независимый expected/evidence; ранняя часть REC-T01 доказана до E02, полный matrix остаётся E05.

- [ ] E01-05 Проверить извлечение поддержанных native типов на fixtures всех трёх сетей, включая failed/system для текущих live fork-профилей; исторические переходы отложены D38. Неизвестный тип даёт явную ошибку. Независимый production verifier состава каждого блока не требуется (D25).

Отложенный контекст D38/D42: Classic/исторические Polygon fixtures из E00-15 не обязательны для текущего live-этапа. Исследования и fixture IDs сохранены. В live остаются все действующие native tx/system types, включая Polygon StateSync, с корректной атрибуцией эффектов.

Gate: по каждой ETH/BSC/POLY доказаны обязательные observation/recovery seams и связь harness с реальным native path. Missing path/pin/decision barrier останавливает этап; E02 не строится на заведомо невосстановимом источнике. Полная семантика/transport matrix остаются E04/E05.

### E02 — модель событий и тестовый валидатор

Зависимости: E01, решения о гранулярности и порядке. Стек: protobuf, Go, детерминированные fixtures; пока без обязательного внешнего сервиса.

- [ ] E02-01 Зафиксировать transaction/system records, IDs, execution order, activation lifecycle и late metadata. Готово: таблица переходов и схемы покрывают выбранные fixtures без искусственных транзакций.
- [ ] E02-02 Создать компилируемые proto и versioned validators. Готово: bindings генерируются воспроизводимо; malformed/conflicting records отклоняются с конкретной причиной.
- [ ] E02-03 Реализовать тестовый валидатор событий. Готово: поля, apply/remove/reapply/status и стабильные IDs проверены на fixtures; промышленный consumer не создаётся.
- [ ] E02-04 Утвердить протокол v3 и правила совместимости. Готово: спорные поля из рабочей редакции разобраны для всех трёх сетей; сетевые различия имеют явное представление.
- [ ] E02-05 Определить модель транзакционных balance actions. Готово: причинные связи, rollback, fees и нулевой итог представлены без потери действий; самостоятельные системные эффекты — optional и не блокируют gate.
- [ ] E02-08 Добавить block metadata: hash, parent, height, native counts при наличии, lifecycle, включая пустые блоки. Локальный delivery batch связывает подготовленные сообщения с прогрессом доставки; без BlockComplete.

- [ ] E02-10 Завершить строгий общий контракт типов. Готово: versioned envelope, однозначный payload type, обязательные/неприменимые поля, зарегистрированные typed extensions, политика неизвестной версии и fixtures трёх сетей; произвольный JSON не заменяет обязательные поля. Свободные bytes только для исходных бинарных данных и транспортных частей.

Gate: модель позволяет восстановить корректные нативные балансы на выбранных сценариях и объяснить каждую отмену независимо от транспорта.

### E03 — надёжная доставка Adapter → Gateway → NATS JetStream

Зависимости: E02, решения о storage и отказах. Стек: Go, gRPC, выбранное durable storage, NATS JetStream; protobuf bytes из E02.

- [ ] E03-01 Реализовать RocksDB journals по DELIVERY_STATE_MACHINE: bounded closing commits, READY/SEALED, ACKED_CLEANUP/RELEASING, RecoveryRelease/frontiers, admission generations. Готово: pinned API/build и crash/reopen проверки, старый protocol — история и REC каталог.
- [ ] E03-02 Реализовать gRPC и формат размеров/частей по MSG-02. Готово: большие события и повторная публикация проверены без усечения; тест восстанавливает исходные bytes.
- [ ] E03-03 Подключить JetStream и сквозной ACK. IngestAck только после PubAck всех подготовленных сообщений/частей в одном logical destination durable plan; retry сохраняет ID, bytes и маршрут. D54: смена account/stream/subjects запрещена до завершения pending адаптера и Gateway; identity/reconnect/drain checks обязательны. Доставка at-least-once, дубли допустимы.
- [ ] E03-04 Реализовать накопление без TTL/прикладной квоты до NATS ACK, bounded RAM, retry и явные ошибки disk-full/config. Не удалять pending данные; post-ACK retention и HA не проектировать.
- [ ] E03-05 Пройти delivery часть REC-T01–17 и ATTEMPT/PREPARE/FRONTIER/FENCE/DRAIN/DEST/MEM/UPGRADE/POISON/ENOSPC от заданного durable intent/READY. Native admission proof уже в E01; совмещённый multi-block/native fault matrix — E05. History variants DEFERRED D38. Полный REC-T01 пока не отмечать PASS.

Gate: delivery-only guarantees и integration smoke с native fixtures трёх сетей доказаны отдельно. Тест сверяет expected fields/parts/ACK/bytes/destination; полный native crash matrix ещё TODO E05. Production completeness verifier не вводится.

- [ ] E03-11 Доставлять block metadata и lifecycle в NATS, включая пустые блоки. Проверять PubAck всех частей; независимая проверка полноты содержимого вне scope.

### E04 — полная транзакционная и балансовая семантика трёх сетей

Зависимости: E03. Стек: native hooks/journal/state APIs закреплённых клиентов, общий transport E03 и необходимые каждой сети consensus/derivation компоненты. Каждая задача имеет три сетевых подзадач.

- [ ] E04-01 Покрыть call tree, receipt/logs, create/selfdestruct, precompiles, authorization и rollback в согласованном fork profile. Готово: обязательные native fixtures проходят.
- [ ] E04-02 OPTIONAL по D17: оценить и при приемлемой сложности покрыть изменения балансов вне транзакций. Готово для включённого расширения: каталог paths и причин, сверка и отсутствие дублей tx/system. Допустимо NOT_IMPLEMENTED с явным scope; не блокирует основную поставку.
- [ ] E04-03 Проверить нормализацию balance actions, before/after и нулевых суммарных эффектов на выбранных native fixtures. Это тесты разработки, не production balance oracle.
- [ ] E04-04 Проверить реальный live/import path и ограничение работы hooks. Готово: сеть/диск транспорта не выполняются внутри VM hooks; перегрузка останавливает продвижение в определённой точке.

Gate: отсутствие необъяснённых capture gaps на выбранном контрольном диапазоне. Coverage всех исторических версий ещё не подразумевается.

### E05 — каноничность, reorg, finality и restart

Зависимости: E04 и durable guarantees E03. Стек: native notifications + lifecycle bridge каждого producer/run + RocksDB.

- [ ] E05-01 Реализовать canonical activation registry и финальные статусы. Готово: speculative capture не публикуется как canonical; статусы привязаны к правильной активации.
- [ ] E05-02 Реализовать rollback/reapply транзакций и блоков. Готово: reinclusion/возврат ветки дают корректные активации, включая обязательные BSC native system tx и Bor StateSync. Только самостоятельные outside-tx SystemBalanceEvent проверяются при включённом optional расширении.
- [ ] E05-03 Завершить комбинированный native/WAL/Gateway matrix на seams, уже доказанных в E01/E03: reorg/finality, child-root/marker, GC, restart после cleanup, DRAIN/Resume/upgrades. Недоступные inputs дают явную ошибку. Это полный REC-T01 proof, не первое исследование возможности восстановления.
- [ ] E05-04 Проверить fencing и конкурентные источники. Готово: старый writer не публикует после смены; гонки capture/reorg/publish/status покрыты тестами выбранной топологии.

Gate: crash/reorg/finality fixtures сохраняют correct references данного producer/run, включая restart после ACK cleanup. Retry сохраняет identity; разные runs вправе повторно публиковать то же включение. Production verifier/dedup не требуется.

### E06 — live-конфигурация и учёт доставки; исторические задачи отложены

Зависимости: E05 и ранний harness E01/E03. Стек: live YAML/native configs/init, Compose/Kubernetes, независимые owners, RocksDB/NATS. Полный user deployment по DEPLOYMENT_BOOTSTRAP_CONTRACT. История/диапазоны не prerequisites.

- [ ] E06-01 DEFERRED_BY_USER D38: исторический диапазон, state и исторические VM/forks.
- [ ] E06-02 DEFERRED_BY_USER D38: последовательный history replay с checkpoints.
- [ ] E06-03 DEFERRED_BY_USER D38: пересечения live/history и повторные history commands.
- [x] E06-04 REMOVED_BY_D25: coverage report и независимая проверка полноты диапазона исключены из сборщика. ID сохранён для истории решений; не является незавершённой задачей поставки.
- [ ] E06-05 DEFERRED_BY_USER D38: исторические тестовые диапазоны N–M.
- [x] E06-06 REMOVED_BY_D36: вычисление стартовой истории за три часа отменено; ID сохранён, задача не входит в поставку.
- [ ] E06-07 Реализовать YAML live/Gateway по LIVE_CONFIG_SPEC и D39: native запуск окружением, валидация профиля и подключения YAML адаптера, без history/apply и Kubernetes API.
- [ ] E06-08 Проверить сохранение live/delivery progress при restart, config generation replacement, DRAIN/Resume и несовместимых версиях. Hot reload отсутствует. History-часть DEFERRED D38.
- [ ] E06-09 Проверить live с H+1 после синхронизации: согласованность фиксации H и подписки, независимая доставка pending WAL, отсутствие выгрузки начальной синхронизации и влияния на другие сети.
- [ ] E06-10 DEFERRED_BY_USER D38: history CLI N–M и отдельный Job/process.
- [ ] E06-11 DEFERRED_BY_USER D38: параллельный live/history и resource overlap.
- [ ] E06-12 Подключить user config/logs к уже реализованным native/delivery журналам: ADMITTED/EXEC_READY/decision и READY/SEALED/NATS_ACKED/RETIRED. Не создавать второй параллельный журнал ради старых упрощённых названий RECEIVED/WAL_SAVED. Нет dedup/completeness verifier.
- [ ] E06-13 После согласования DEP-01–04 реализовать полный Compose/Kubernetes комплект трёх сетей по D43: native сборки с адаптерами, consensus/supporting clients, конфиги/секреты/соединения, независимые тома/WAL/Gateway и один NATS. Готово: конфигурации описывают все необходимые компоненты, функциональные startup/readiness/restart проверки подтверждают их связи. Performance и production-методика не входят.

Gate: полный live stack трёх сетей воспроизводится по сохранённым commands/config/artifact manifests, проходит BOOT/CONFIG/MOUNT/TRUST/NATS/START/REPLACE/LOG/ARTIFACT fixtures. Исторические задачи не участвуют. Ранний harness не заменяет пользовательский комплект.

### E07 — приёмка и первая поставка

Зависимости: текущая live-часть E06. Стек: закреплённые сборки/образы, native fixtures и функциональный тестовый считыватель NATS. Production-методика и performance harness вне текущего ТЗ.

- [ ] E07-01 OUT_OF_SCOPE_D40: hardware-профиль, производительность, численные пороги и production outage window исключены из текущего ТЗ; ID сохранён, поставку не блокирует.
- [ ] E07-02 Выполнить end-to-end native correctness и fault matrix. Готово: все обязательные проверки пройдены; отклонения не скрыты формулировкой supported.
- [ ] E07-03 Подготовить runbook запуска, остановки и диагностики ошибок доставки через логи и recovery drill по REC. UI/визуализация не входят в этап.
- [ ] E07-04 DEFERRED_BY_USER (D41): упаковка release bundle отложена вместе с open-source поставкой. Технические схемы, YAML, описание сборки и результаты функциональных проверок остаются самостоятельными задачами.
- [ ] E07-07 DEFERRED_BY_USER (D41): подготовка open-source поставки отложена. Технические схемы, YAML и описание работы сборщика остаются частью ТЗ; публичный release bundle сейчас не готовится.
- [ ] E07-08 Функционально проверить изоляцию: у каждой сети отдельные WAL адаптера и журнал Gateway; добавление, изменение конфига, остановка и рестарт одного экземпляра не требуют изменения/рестарта остальных. Нет общих writers и ожидания прогресса другой сети. Production-методика отдельно, вне scope.

- [ ] E07-09 Принять результат в NATS на fixtures трёх сетей: типизированные поля, части, lifecycle, допустимые повторы, metadata пустых блоков и PubAck. Без production verifier полноты и performance требований.

Gate: отдельно приняты Ethereum, BSC и Polygon; обязательны согласованные функциональные критерии всех трёх сетей. Base/Arbitrum этот gate не блокируют.

## Требования и приёмка

DOC-FINAL-01 · 2026-11-05. Реестр покрывает обязательное поведение текущего ETH/BSC/POLY live продукта, а не только 41 находку аудита. Нормативные детали — MESSAGE_CONTRACT, CURRENT_SEMANTICS, NATIVE_CAPTURE_PROTOCOL, DELIVERY_STATE_MACHINE, CONTROL_WIRE_CONTRACT, LIVE_CONFIG_SPEC и DEPLOYMENT_BOOTSTRAP_CONTRACT; полный нормативный текст включён в root v3.

Все runtime verdict сейчас **NOT_RUN**. Ни одна строка не означает успешный тест. Имена fixture families из предыдущих документов сохранены; новые WIRE/ID/LAYOUT/RPC/CUTOVER/DATA/FEE/FINAL/GC конкретизируют их. Сценарии должны быть воспроизводимы и проверяться независимым expected по IMPLEMENTATION_GATES. Для требований native semantics PASS нужен отдельно ETH/BSC/POLY; network-specific строки проверяются в своей сети. Optional extension проверяется только при объявленном включении и не блокирует default поставку.

### Scope и результат

| ID | Требование | Проверка и ожидаемый результат / этап |
|---|---|---|
| REQ-SCP-01 | Первый контур ровно ETH/BSC/POLY Mainnet | CONFIG-01: три accepted profiles; Base/Arbitrum/testnet/неизвестный profile отвергнут, не запускается другой клиент; E01/E06 |
| REQ-SCP-02 | Native источник, NATS — конечная граница | E07 path audit: hooks/StateDB/receipts native, JSON-RPC/debug_trace не основной tx feed; нет аналитического writer/consumer продукта |
| REQ-SCP-03 | Только новый live H+1, pending независимо | BRANCH/START: initial sync и downtime range не экспортируются; сохранённые obligations завершены, новый boundary записан без окна; E01/E05/E06 |
| REQ-SCP-04 | Успешные/failed/native system tx обязательны | E04 corpus: ordinary/failed/BSC system/Bor StateSync и пустой блок; unknown type вызывает error без skip |
| REQ-SCP-05 | Outside-tx balance/legacy receipt optional | E04-02: default выключен, source execution сохраняется, tx effects не теряются; при включении отдельные typed records/fixtures без fake tx |
| REQ-SCP-06 | Нет обещаний downstream/HA/полноты/истории/performance | E07 scope audit: ни PubAck, ни fixture PASS не обозначены archive/HA/global balance completeness; отсутствуют запрещённые компоненты |

### Native capture

| ID | Требование | Проверка и ожидаемый результат / этап |
|---|---|---|
| REQ-CAP-01 | Отдельные pinned client builds и реальные paths | PROFILE-01: transfer/system/canonical callers реально проходят bridge; synthetic runner сопоставлен native import; E01 |
| REQ-CAP-02 | Durable admission до принятого execution | CAPTURE-01: crash до/после ADMITTED; до него нет принятого obligation, после него replay не теряется; E01/E05 |
| REQ-CAP-03 | Hash provider и persistent state/input pins | PIN-01: потерять RAM trie cache, pruning живого pin/wrong datadir/path backend; recovery либо preflight reject, не silent skip; E01/E05 |
| REQ-CAP-04 | Static inputs + append-only consumed dependencies | CAPTURE: crash после ответа Heimdall до передачи; retry использует прежний ответ/sequence, final digest закрыт; E01/E05 |
| REQ-CAP-05 | Owned snapshot без mutable alias | OWNED-01: overlapping CALL input/output, delayed worker, соседние tx одного key; исходные значения не изменились; E01/E04 |
| REQ-CAP-06 | Bounded collector RAM, safe-point backpressure | OWNED/MEM: один field/tx/manifest больше буфера, slow disk/receiver; chunks/spool без потери, I/O/wait вне hooks; E01/E03/E04 |
| REQ-CAP-07 | Corpus/child-root/decision/mirror durable ordering | CAPTURE/DECISION: power loss у каждого marker, native-only replay не меняет public IDs/bytes; E01/E05 |
| REQ-CAP-08 | Semantic identity стабильна до transport attempt | ID-01: crash/retry DELIVERY_STAGING/native marker; прежние activation/revision/source, ровно один batch allocation; E02/E03/E05 |
| REQ-CAP-09 | Bor/BSC native scopes не смешиваются | SCOPE: StateSync after до block-alloc; BSC actualTx/nonce/finalise; execution result совпадает с native; E01/E04 |
| REQ-CAP-10 | Local fatal завершает соответствующую ноду | POISON/ENOSPC: normalization/corruption/sync failure — exit без false READY; соседние сети продолжаются; E03/E05/E07 |

### Данные и нормализация

| ID | Требование | Проверка и ожидаемый результат / этап |
|---|---|---|
| REQ-DAT-01 | Closed typed schema, presence и exact integer widths | DATA/schema: unknown enums/version, lengths, missing vs zero, malformed union; reject без ANY/JSON fallback; E02 |
| REQ-DAT-02 | Source tx/receipt encodings и native поля | AUTH/DATA: byte equality, тип/подпись/access/blob/auth fields и native-derived projection раздельны; E02/E04 |
| REQ-DAT-03 | Authorization tuples сохраняются без фильтрации | AUTH-01…04: invalid parity/nonce/chain tuple, duplicates, root revert; source tuples не теряются; E02/E04 |
| REQ-DAT-04 | Полное call tree применимых native calls | DATA: CALL/CALLCODE/DELEGATECALL/STATICCALL/CREATE/CREATE2/precompile, value=0, caller/context/code, parent/order; E04 |
| REQ-DAT-05 | Outcome/rollback/committed различаются | DATA/SELF: успешный child при reverted ancestor, caught failure; attempted trace сохранён, final state без отменённых effects; E04 |
| REQ-DAT-06 | SELFDESTRUCT отдельное действие | SELF-01…04: zero balance/self-beneficiary/old/new contract/ancestor revert; action, monetary effects и deletion не смешаны; E02/E04 |
| REQ-DAT-07 | Точный tx boundary state diff/existence | EXIST/DATA: empty CREATE, delete/reset/recreate, touched storage twice, fees/auth вне root; initial/final и code hashes точны; E04 |
| REQ-DAT-08 | Все monetary actions, компенсации и D62 fees | FEE-01: gross purchase/refund/credits, same recipient/payer, blob/zero-fee; address effects согласованы с state, aggregate не удваивает; E04 |
| REQ-DAT-09 | Receipt/log/create provenance | RECEIPT/DATA: native status/revert/gas price, failed CREATE attempt vs success, log indices/scopes, no reverted receipt logs; E02/E04 |
| REQ-DAT-10 | Полные native block metadata, включая empty | DATA: исходный header/fork fields/RLP/count, zero tx, optional fields отсутствуют корректно, не synthetic completeness report; E02/E04 |

### Lifecycle, finality и GC

| ID | Требование | Проверка и ожидаемый результат / этап |
|---|---|---|
| REQ-LIF-01 | Только native canonical activation публикуется | BRANCH: rejected/speculative block не Applied; known branch без callback получает corpus/replay до decision; E01/E05 |
| REQ-LIF-02 | READY Applied всегда может получить Removed | LIFE-01/02: reorg до publish/после одной part; отмена сохраняется независимо от PubAck; E02/E05 |
| REQ-LIF-03 | Reordered lifecycle детерминирован | LIFE-03/04: Status r2/r1 до Applied, Removed до Applied, same-revision conflict, reapply новый A; terminal смысл сохранён; E02/E05 |
| REQ-LIF-04 | Finality-only/restart события не теряются | FINAL/DECISION: setter/event без нового block, crash marker/mirror, повтор; ровно правильный snapshot/revision; E01/E05 |
| REQ-LIF-05 | Ethereum safe/finalized semantics точны | FINAL-ETH: safe startup fallback допустим, zero FCU no-op, actual setter before RPC error; GC только finalized ancestry; same-head authoritative finalized regression ниже accepted anchor — fatal; E01/E05 |
| REQ-LIF-06 | BSC early/header evidence разделены | FINAL-BSC: VotePool-only, merged header snapshot, side branch, restart loss, missing lookup; GC только accepted header Source; E01/E05 |
| REQ-LIF-07 | Polygon native statuses отдельно от D63 anchor | FINAL-POLY: milestone purge/whitelist-only, checkpoint root vs end hash, exact finalized contract read; публикация не ждёт L1 GC; E01/E05 |
| REQ-LIF-08 | GC не теряет pending/нужную ancestry | GC-01: no anchor/pending/replay сохраняют pins; crash release, removed branch, same-head finality regression и доказанный anchor conflict; нет TTL guesses; E05 |

### Доставка и обслуживание

| ID | Требование | Проверка и ожидаемый результат / этап |
|---|---|---|
| REQ-DLV-01 | Единственные durable owners, sync closing markers | REC/journal: reopen/power loss каждого state, незакрытые chunks не публикуются; E03 |
| REQ-DLV-02 | Stable batch/attempt/counters без sequence дыр | ATTEMPT/ID: native rejected/no-events не выделяет batch; attempt не меняет semantic IDs, overflow fatal; E02/E03 |
| REQ-DLV-03 | Prepare/reservation идемпотентны | PREPARE/RPC: потеря replies/chunks, same/conflicting digest, Gateway loss до SEALED; сохранённый seed повторяется; E03 |
| REQ-DLV-04 | Exact wrapper/layout/header/bytes | LAYOUT: COMPLETE/PART boundary, order/digest/size, SHA/MsgID vectors; после SEALED никакой reserialization; E02/E03 |
| REQ-DLV-05 | End-to-end ACK всех частей одного назначения | REC/NATS: partial PubAck, wrong stream, lost bitmap/IngestAck, reordered parts; ChunkAck/CLOSED не разрешают producer cleanup; E03 |
| REQ-DLV-06 | Producer/Gateway cleanup crash-safe | FRONTIER/REC: ACKED/outbox, RELEASING chunks, lost ReleaseAck, recovery без payload; prefix не перескакивает batch; E03 |
| REQ-DLV-07 | Store loss использует surviving owner evidence | REC/FRONTIER: Gateway rebuilt с prefix/SEALED plans; producer loss не получает ложный старый cursor; E03/E05 |
| REQ-DLV-08 | Late session/callback не воскрешает state | FENCE: пауза check→commit, reconnect/Release, late PubAck/Publish retired; old owner отвергнут; E03/E05 |
| REQ-DLV-09 | Binding задаётся до offline capture | WIRE/DEST: same account/different URL, wrong account/stream/prefix/metadata; canonical digest и pre-plan READY связаны; E02/E03/E06 |
| REQ-DLV-10 | Drain закрывает native и delivery obligations | DRAIN: pending native способен создать batch, highwater после него; no new admission/poll, retained GC pins допустимы; E05/E06 |
| REQ-DLV-11 | Cutover/Resume соответствуют YAML | CUTOVER: desired descriptor only, AWAIT_SWITCH, crash каждого intent/commit, old binding LIVE Resume и late old generation; E03/E06 |
| REQ-DLV-12 | ACKed history не мигрирует D59 | DEST/LIFE: новый Removed/Status с прежним Applied reference, только новое назначение; старый ACKed payload не экспортируется; E05/E06 |
| REQ-DLV-13 | Retry/ENOSPC/poison не удаляют pending | POISON/ENOSPC/MEM: transient backoff, CONFIG_BLOCKED, disk expansion/recovery, immutable conflict; нет drop/auto re-ID; E03/E05 |
| REQ-DLV-14 | Upgrade capabilities до mutation | UPGRADE: old-build drain для wire/plan, compatible replay exact bytes, incompatible remaining store reject даже DRAINED; E03/E05/E06 |

### Конфигурация и окружение

| ID | Требование | Проверка и ожидаемый результат / этап |
|---|---|---|
| REQ-OPS-01 | Strict role YAML и effective native validation | CONFIG: unknown/duplicate keys, relative paths, native overrides/profile/pruning/Bor parallel flags, check без mutation; E01/E06 |
| REQ-OPS-02 | 11 services/14 volumes, самостоятельные owners | MOUNT: actual backing volume identities, permissions/UID, separate Polygon L1 и JWT; одинаковый container path не означает shared DB; E06 |
| REQ-OPS-03 | Partial init не разрушает keys/state | BOOT: crash каждой phase, existing nonempty home/wrong genesis/missing ENV_READY, BSC hash init; safe resume/reject; E01/E06 |
| REQ-OPS-04 | Config generation заменяет только allowlist | CONFIG: изменённый native config применён после restart, ключи/genesis/state сохранены, невозможный mix отвергнут; E06 |
| REQ-OPS-05 | mTLS/SAN/EKU/network/role admission | TRUST: чужой CA/SAN/role/network/deployment, credential rotation и UDS ownership; reject до store mutation; E03/E06 |
| REQ-OPS-06 | Минимальные NATS ACL и проверка account | NATS: runtime publish/INFO/UserInfo/reply allowed, admin denied; bootstrap не PURGE/reset чужой stream; E03/E06 |
| REQ-OPS-07 | P2P mapping/advertise и внутренние API | CONFIG/START: назначенные ports во всех слоях, only P2P external, required native links reachable, no localhost inter-service assumption; E06 |
| REQ-OPS-08 | Independent startup и stop-confirm-start | START/REPLACE: NATS outage, Bor/Heimdall retry, delayed old writer, native sync отдельно от delivery; E06 |
| REQ-OPS-09 | Обязательные control logs без secrets | LOG: все states/errors видны при отключённом debug, правильная identity, SIGKILL не выдаёт graceful STOPPED; E06/E07 |
| REQ-OPS-10 | Артефакты/совместимость доказаны перед runtime acceptance | ARTIFACT: pinned source/patch/toolchain/image, mainnet genesis SHA/parse, wrong artifact reject, clean bootstrap; E01/E06 |

### Разработка и граница готовности

| ID | Требование | Проверка и ожидаемый результат / этап |
|---|---|---|
| REQ-DEV-01 | Порядок E01→E07 без позднего открытия source seam | План/evidence: native proof E01, delivery E03, combined E05; ни одного DONE по ещё будущему proof |
| REQ-DEV-02 | Independent expected и воспроизводимый harness | Fixture passport/seed/input hash + mutation sanity; clean environment без скрытой ноды автора; E01/E02/E07 |
| REQ-DEV-03 | Separate builds и bounded module interfaces | Dependency review: core без внутренних типов трёх fork сразу, NATS SDK только Gateway, Rust/history не prerequisites; E01 |
| REQ-DEV-04 | Evidence traceability и честные verdict | Каждый run с REQ/finding/task/fixture/input/expected/build/actual/fault; новый source/profile invalidates затронутый PASS; каждый gate |
| REQ-DEV-05 | Функциональная приёмка трёх независимых сетей | E07: все mandatory REQ имеют PASS по применимости, optional/deferred явно помечены; runtime не заменяется source inspection/MD lint |

### Definition of ready для документации

Перед завершением DOC-FINAL: один нормативный root; ни одной поведенческой развилки без ответа; current schemas/IDs/config/операционные переходы замкнуты; каждый обязательный REQ имеет ожидаемый результат и этап; future-proof задачи отделены от неопределённых требований; deferred scope вынесен из основного текста. Проверки ссылок/anchors, source-backed claims и неизменности v2 обязательны. Идеальность будущей реализации не обещается; новые факты возвращаются в decisions/requirements до изменения guarantees.

### Definition of done для реализации

D64: это критерии приёмки описываемой системы в составе ТЗ; реализация и исполнение E01–E07 никогда не входят в нашу работу. NOT_RUN не PASS; audit SPECIFIED не runtime DONE. Требования нельзя понижать ради успешного gate. Предписанный системой процесс проверки сохраняет counterexample input/actual/expected, устраняет причину и повторяет затронутые proofs; предусмотренный пакет приёмки содержит build/artifact locks, configs/runbook и воспроизводимые evidence. Здесь эти условия только проектируются и документируются.

## Документальная готовность и дальнейшая работа над ТЗ

Документальная готовность: 41/41 находки исходного аудита SPECIFIED; 63 требования имеют критерии приёмки и этапы; D62/D63 внесены, неотвеченных продуктовых вопросов этого раунда нет. Это не утверждение, что дальнейшая реализация не выявит новых дефектов.

Дальнейшая работа — уточнять ТЗ по новым требованиям и проверенным источникам, находить противоречия, прорабатывать все сценарии и критерии приёмки. Описанный порядок E01–E07 сохраняется как часть ТЗ; начинать его выполнение или предлагать переход к реализации не требуется и не допускается по D64.

Каждый законченный подэтап документации: результат в PROJECT_STATUS/плане, необходимые документальные проверки, локальный Git commit и проверка git status. Remote publication выполняется только по отдельному указанию. Код продукта, сборки, runtime-тесты и deployment исключены из нашей работы; исторические next steps не меняют эту границу.

Полная проверка документов и её ограничения — [FINAL_SPEC_REVIEW](docs/FINAL_SPEC_REVIEW.md).
