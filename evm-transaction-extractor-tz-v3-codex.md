# EVM Transaction Extractor — полное техническое задание v3

> DOC-FINAL-01 / D61–D63: чистовая доводка выполняется. Точные актуальные data/finality contracts — [CURRENT_SEMANTICS](docs/CURRENT_SEMANTICS.md), control/config/cutover — [CONTROL_WIRE_CONTRACT](docs/CONTROL_WIRE_CONTRACT.md). После commit подэтапа полный текст ниже будет синхронизирован и очищен в DOC-FINAL-02. Реализация не разрешена.

2026-11-05. Актуальная редакция по D16–D60. Только проектирование и документация; реализация, сборки и runtime-проверки не начаты. Исходный evm-transaction-extractor-tz-v2.md сохранён без изменений.

Этот файл содержит действующие требования, контракты capture/delivery/сообщений, конфигурацию, deployment и порядок разработки. Детальные документы в docs/ сохраняются как редактируемые источники разделов и source evidence; для чтения текущего ТЗ не требуется восстанавливать цепочку исторических поправок. При дальнейшем изменении контракта обновляются соответствующий источник и эта редакция. Приоритет при противоречии: решения пользователя → конкретные normative native/delivery/bootstrap contracts → общие пояснения; противоречие устраняется, а не скрывается ссылкой.

Все 41 находки аудита получили статус SPECIFIED: механизм описан и связан с будущим fixture/gate. Это не означает 41 выполненный runtime proof и не гарантирует отсутствие новых дефектов. История находок: [аудит](docs/DEEP_DESIGN_AUDIT_2026_11_05.md); текущий [реестр исправлений](docs/DESIGN_REPAIR_STATUS.md); [журнал решений](docs/DECISIONS.md).

## Граница поставки

Обязательный первый контур: **Ethereum Mainnet, BSC Mainnet, Polygon PoS Mainnet**. Встроенные native adapters собирают все включённые успешные/неуспешные транзакции, включая native system transactions, их применимые поля, calls/actions/receipt/logs/state changes, block metadata пустых блоков и lifecycle. Нормализованные данные доставляются в NATS JetStream. Приёмочный считыватель — тестовый инструмент, не промышленный downstream.

Только live после синхронизации: под native guard фиксируется H/hash, новый сбор начинается H+1. Pending/recovery obligations восстанавливаются независимо. История N–M, первоначальная sync-выгрузка и сканирование downtime gaps отложены. Targeted native replay начатого capture/изменившейся ветки допускается по recovery contract и не является загрузкой истории.

Обязательны transaction balance actions и before/after; самостоятельные изменения нативных балансов вне транзакций — optional D17. Native system tx BSC/Bor не становятся optional. Начальный snapshot всех адресов не требуется; отсутствие наблюдений не означает нулевой баланс. При выключенном outside-tx расширении нельзя обещать полную историю всех балансов или равенство after-last-tx и end-of-block.

Не входят: ABI/ERC-20/business decoding, mempool/симуляции, аналитическая БД, downstream consumer/dedup, production completeness verifier, HA/backup/multi-DC, post-ACK retention, performance SLO/hardware thresholds и production-методика. Base/Arbitrum и остальные будущие клиенты, Rust adapters, history/backfill, лицензия и публичная open-source упаковка отложены. Справочные типы этих профилей ниже не создают обязательных задач первого контура.

## Сквозной flow и ответственность

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

## Содержание

- [Семантика исходных данных](#семантика-исходных-данных)
- [Нативные балансы и причины изменений](#нативные-балансы-и-причины-изменений)
- [Контракт сообщений](#контракт-сообщений)
- [Реестр native типов](#реестр-native-типов)
- [Native capture и lifecycle recovery](#native-capture-и-lifecycle-recovery)
- [Durable delivery и смена назначения](#durable-delivery-и-смена-назначения)
- [Live YAML и команды](#live-yaml-и-команды)
- [Bootstrap и окружение](#bootstrap-и-окружение)
- [Порты и экспонирование](#порты-и-экспонирование)
- [Стек и границы сборок](#стек-и-границы-сборок)
- [REC сценарии](#rec-сценарии)
- [План доказательств и границы модулей](#план-доказательств-и-границы-модулей)
- [Задачи разработки E01–E07](#задачи-разработки-e01e07)


## Семантика исходных данных

### 4. Источник данных

Экстрактор получает данные через:

- нативные execution hooks;
- интерфейсы клиента для чтения транзакций и receipts;
- доступ к нативному состоянию и журналам его изменений;
- уведомления клиента об изменении канонической цепочки.

Local и remote JSON-RPC, WebSocket RPC и debug_trace* не используются экстрактором как рабочий источник транзакционных данных.

RPC разрешается для диагностики и независимой тестовой сверки. Использование RPC самим клиентом для L1 derivation или штатной синхронизации не является нарушением этого ограничения.

Каждая текущая сеть исполняется своим native executor; BSC/Bor не исполняются через обычный Ethereum executor.

### 5. Когда транзакция готова к публикации

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

### 6. Данные транзакции

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

### 7. Internal transactions

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

### 8. Ошибки и отменённые внутренние вызовы

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

### 9. SELFDESTRUCT и создание контрактов

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

### 10. Logs и state diff

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

### 31. Полнота нативных балансов

#### 31.1. Обязательная граница покрытия

Для каждой поддержанной сети обязательны все транзакции и их применимые balance actions/before/after в текущем live-профиле. Сбор изменений вне транзакций необязателен по D17; допустимо отказаться от него при усложнении кода. Далее проверки самостоятельных системных операций относятся только к включённому расширению.

Нативный баланс — канонический баланс нативного актива адреса по модели конкретной сети. Профиль фиксирует актив, минимальную единицу, способ чтения и представление в state. Нельзя предполагать, что все runtime хранят его в том же account.balance, что Ethereum. ERC-20 balances, wrapped native tokens, consensus-layer validator balances и внешние UTXO сами по себе в это определение не входят. Родные atomic-транзакции и их изменения нативного баланса адресов поддерживаемой сети остаются обязательными.

Транзакционные изменения берутся из AccountChange. Для протокольных операций вне транзакций используется SystemBalanceRecord. Нельзя повторно публиковать транзакционные изменения в системном потоке или считать value каждого CallFrame самостоятельным переводом.

#### 31.2. Гранулярность и rollback

Необходимо сохранять действия, вызвавшие изменения нативных балансов, и их причинную связь с транзакцией, внутренним вызовом или протокольной операцией. Итоговый before/after на границах транзакции или системной операции сохраняется дополнительно для сверки состояния; он не заменяет историю действий.

Если в одной успешной транзакции адрес получил 10 и отправил 10, обе причины изменения должны остаться в данных, несмотря на нулевой итоговый diff. Для одной завершённой операции сохраняется одна итоговая пара before/after адреса; отдельные действия внутри неё не должны теряться при свёртке. Транзакционная трассировка сохраняется полностью.

Изменение в успешно завершившемся дочернем вызове может быть отменено родительским revert: локальный outcome и итоговая применимость эффекта различаются. Отменённые действия остаются в трассировке с соответствующей семантикой и не участвуют в расчёте канонического баланса.

Действие не приравнивается автоматически к каждой временной записи native balance hook. Отображение gas debit/refund, fees, mint/burn и системных операций в действия определяется профилем клиента; нельзя вычислять переводы суммированием CallFrame.value. Идентичность действия, причинная ссылка, execution position, применимость и адресные effects определены в контракте сообщений ниже; compiled schema/validators — E02.

Сохранённые fee/nonce effects неуспешной транзакции учитываются по существующим требованиям. Системный поток не должен компенсировать ошибочно неполный транзакционный diff.

#### 31.3. Захват и классификация

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

#### 31.4. Проверка нормализации

На разработческих fixtures проверяются before/after, actions, fees, rollback и сохранение +10/−10 при нулевом итоге. Это не обязательный production block/balance completeness verifier. При известной ошибке получения/декодирования запись не считается успешно доставленной; никаких выводов о полноте диапазона по NATS_ACKED.

#### 31.5. Граница собранных наблюдений

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
| Timestamp | seconds:u64 + nanos:u32 < 1e9 + precision=`SECONDS | MILLISECONDS | NANOSECONDS`; сохраняется доступная source precision |
| NetworkIdentity | family=`EVM`, chain_id:UInt256, genesis_hash:Hash32; chain_key — только alias для маршрутизации |
| ProfileRef | network profile, client commit, adapter version, fork rules version, normalization version; зарегистрированный профиль |
| FieldAvailability | `VALUE(T) | NOT_APPLICABLE | UNAVAILABLE(reason_code)`; применяется только к полям с определённой семантикой отсутствия |

Отсутствующий scalar и корректный ноль различаются. Proto scalar с обязательным присутствием использует explicit presence и validator. Для `oneof` требуется ровно один вариант; неизвестные enum values, profile/type/schema versions не публикуются как нормализованные данные. Empty list означает, что источник вернул пустой применимый набор; известная ошибка получения не маскируется пустым списком. Независимый verifier полноты не требуется. `UNAVAILABLE` у обязательных transaction details означает ошибку обработки; неприменимость доказывается профилем.

Произвольные JSON, `Any`, map<string,bytes> не являются обходом типов. Bytes разрешены для hashes/addresses, calldata, code, logs data, source encoding и сериализованного транспортного payload. Каждое сетевое расширение имеет конкретную схему и version gate.

Основание: [Protobuf field presence](https://protobuf.dev/programming-guides/field_presence/). Protobuf по умолчанию не проверяет эти доменные ограничения; нужен validator E02.

### 2. Envelope и идентичность

`Envelope={schema_version, network, profile_ref, event_id, producer_id, run_id, source, capture_mode, payload}`.

`source={node_instance_id, writer_epoch, source_sequence}`. `capture_mode=LIVE|SYNC|BACKFILL`. Эти поля — provenance, не идентичность транзакции. `payload` — закрытый union из таблицы событий ниже. Версия wire schema не обязана совпадать с номером редакции Markdown v3.

`BlockRef={number:u64,hash:Hash32,parent_hash:Hash32,timestamp:Timestamp}`. `InclusionRef={network,block_hash,native_origin,native_position,transaction_hash}`. Для native body transaction native_position — индекс в body, native_origin=`BLOCK_BODY`. Для `ARBITRUM_CLASSIC_AVM` позиция включает native AVM log/result index, а source transaction_hash имеет kind=NATIVE_REQUEST_ID; optional Ethereum projection index хранится отдельно. Правила и ограничения — [Classic profile](docs/research/ARBITRUM_CLASSIC_CAPTURE.md). Нельзя идентифицировать включение одним tx hash либо считать Classic projection единственным inventory.

`ActivationRef={inclusion_ref,producer_id,run_id,activation_id}`. Для блока аналогично используется `BlockActivationRef={network,block_hash,producer_id,run_id,activation_id}`. `event_id` обозначает неизменяемое событие; повтор передачи сохраняет ID и исходные байты. Разные события lifecycle имеют разные event_id. event_id=(producer_id,run_id,event_sequence); persistent counters и publication plan определены в [протоколе .06](#rec-сценарии); вероятностная генерация нового ID на каждый retry запрещена.

### 3. TransactionRecord

| Поле | Состав и правило |
|---|---|
| inclusion, block_activation | Связь с конкретной веткой и активацией блока |
| native_payload | Ровно один вариант из [реестра типов](#реестр-native-типов); native type проверяется с сетью/fork |
| source_hash | Значение native клиента; hash_kind=`NATIVE_TRANSACTION | IMPORTED_OVERRIDE | NATIVE_REQUEST_ID`; Bor synthetic receipt имеет отдельный auxiliary origin |
| raw_encodings | Непустой список `{kind,bytes}`: `EVM_LEGACY_RLP | EIP2718_ENVELOPE | ARB_NATIVE_ENVELOPE | ARB_TRIE_VALUE | BOR_STATE_SYNC_ENVELOPE | CLASSIC_REQUEST_BYTES | CLASSIC_AVM_VALUE`; обязательный набор определяется вариантом |
| execution_projection | Typed effective sender/target/value/input и provenance=`ENCODED | NATIVE_DERIVED | NOT_APPLICABLE`; исходные native поля не перезаписываются |
| receipt | Typed outcome, fees/gas, committed logs и применимые network receipt details |
| calls | Упорядоченные execution frames, включая failed/reverted calls |
| actions | Причинные native balance actions, включая fee/refund/mint/burn и компенсирующие действия |
| execution_actions | Типизированные действия исполнения, не сводимые к balance transfer; обязательный SelfDestructAction при его native исполнении |
| state_changes | Итоговый transaction-boundary diff затронутых accounts/storage после rollback, включая реально сохранённые fees/nonce |
| execution_position | operation_index:u64 и execution BlockRef; при отличии от inclusion block связь задаёт профиль |
| capture_status | Применимость receipt/calls/actions/state_changes/network details; известная ошибка сбора даёт ERROR, независимой сверки состава блока нет |

`ExecutionProjection` — отдельный union `EvmMessage | NativeProtocolExecution`; последнее задаёт typed operation и адреса только если они имеют смысл. Execution sender нельзя заполнять нулём потому, что так отвечает универсальный accessor системной транзакции.

`Receipt`: native outcome=`Status(success:bool) | PreStatusStateRoot(Hash32)`; execution result=`SUCCESS | REVERT | FAILURE`; cumulative_gas_used и gas_used по применимости; effective_gas_price, blob_gas_used, blob_gas_price с явным присутствием. Отдельные optional Address20: attempted_create_address и created_contract (только успешное создание); итоговый existence находится в AccountChange. Logs, logs_bloom, source_receipt_encoding с encoding kind сохраняются отдельно от projection. Pre-Byzantium source root не превращается в RPC status: результат берётся из native execution. Field provenance и границы Bor/BSC — [NATIVE_CAPTURE_PROTOCOL](#native-capture-и-lifecycle-recovery). Network details — закрытые варианты; Base/Nitro относятся к отложенным профилям.

`BorReceiptDetails.source_log_scopes` — список `{transaction_log_index, scope=SYSTEM_PRE | TX_STATE_SYNC | SYSTEM_POST}` для source logs, атрибутируемых native instrumentation; порядок/bytes исходных logs не меняются. Native system log не выдаётся за действие StateSync tx. Полнота этой provenance проверяется SCOPE-01; отдельная публикация outside-tx событий остаётся optional. Field numbers и generated bindings — E02.

`Log={address,topics:Hash32[],data,transaction_log_index,block_log_index}`. Receipt содержит только committed logs. Если сохраняются reverted log attempts, это отдельные execution actions с effects_committed=false, не receipt logs.

`CallFrame={frame_id,parent_id?,execution_order,depth,engine,kind,context_address?,code_address?,msg_sender?,target?,value?,input,output,gas?,gas_used?,outcome,effects_committed,error_code?,revert_data?,created_address?}`. kind — закрытый набор CALL/CALLCODE/DELEGATECALL/STATICCALL/CREATE/CREATE2/STYLUS_HOST_CALL/NATIVE_PROTOCOL; runtime-specific дополнительные поля требуют registered variant. `execution_order` отражает источник; сортировать только по depth нельзя. Успешный child внутри reverted ancestor имеет effects_committed=false.

`BalanceAction={action_index,cause,engine,kind,from?,to?,amount,asset=NATIVE,effects_committed,rollback_scope?}`. kind: TRANSFER/MINT/BURN/FEE/REFUND/SELFDESTRUCT_TRANSFER; cause=`Frame(frame_id) | TransactionProtocol(registered_operation)`. Точный перечень network protocol operations — E02-05, до этого неизвестный cause не превращается в OTHER. Fee attribution сохраняет плательщика/получателя или burn, не предполагает обычную EVM-пару адресов для всех сетей. Gas refund units и native monetary refund различаются. +10 и −10 остаются двумя actions при нулевом diff.

`ExecutionAction={execution_action_id,execution_order,frame_id,payload}`: закрытый union, текущий обязательный вариант — `SelfDestructAction={contract_address,beneficiary,balance_at_entry,effects_committed,deletion_eligible_by_rules,deletion_committed}`. Все scalar имеют presence; addresses/amount соответствуют общим типам. `frame_id` ссылается на исполнявший opcode frame, а не на искусственный CALL. Native pseudo enter/exit для SELFDESTRUCT нормализуются в это действие и не создают второй transfer. `deletion_eligible_by_rules` описывает fork/creation eligibility, `deletion_committed` — сохранённый результат этого действия после rollback/finalization; при effects_committed=false deletion_committed=false. Конечное существование account проверяется отдельно через AccountChange, а не выводится только из opcode. Повторные/отменённые действия сохраняются со своим порядком, даже если сумма равна нулю. Денежная проекция использует `BalanceAction.cause=ExecutionAction(execution_action_id)` (добавленный вариант cause); execution action и его balance projection не суммируются как два изменения. Self-beneficiary/возможный burn следуют native fork rules, не универсальному правилу transfer. [EIP-6780](https://eips.ethereum.org/EIPS/eip-6780). Точные field numbers — E02; смысл полей фиксирован этим контрактом.

`AccountChange={address,existed_before,exists_after,balance_before,balance_after,nonce_before,nonce_after,code_before,code_after,storage_reset,storage_changes[]}`. `StorageChange={key:Hash32,before:Hash32,after:Hash32}`. Данные относятся к transaction boundary, не к последнему child frame. Code и storage reset семантика проверяются по fork, включая SELFDESTRUCT. Balance actions и итоговый diff — разные представления одного исполнения; их нельзя складывать друг с другом как независимые изменения.

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

Finality — typed evidence, а не свободная строка и не универсальный confirmations counter. Варианты: Ethereum consensus safe/finalized; BSC native finality evidence; Base L2 safe/finalized с L1 block reference; Arbitrum L1 inclusion/settlement evidence; Polygon Heimdall/checkpoint evidence. Эти стадии не объявляются эквивалентными. Конкретные структуры evidence и availability guards требуют E02 fixtures для pinned clients.

Поздняя finality передаётся StatusChanged. Отсутствующие обязательные execution details не разрешается выдавать как полную транзакцию с обещанием дослать. Если потребуется изменяемая non-finality metadata, до реализации вводится отдельный typed event/version с fixture; свободного patch JSON нет. Fork-specific L1/batch сведения, недоступные на данном этапе, имеют явно определённый pending статус, не нулевое выдуманное значение.

### 6. Транспорт и порядок

Полный Prepare/Publish/Release, immutable header contract и bounded serialization задаёт [DELIVERY_STATE_MACHINE](#durable-delivery-и-смена-назначения). Нормативная единица retry — сохранённый publication plan с destination и exact frame/header bytes, а не повторная сериализация record новой сборкой.

`TransportFrame={transport_version,network,event_id,payload_digest,publication_epoch,chain_sequence,payload}`.

payload — ровно `CompleteRecord{envelope_bytes}` либо `RecordPart{part_index,part_count,total_bytes,data}`. envelope_bytes — bytes конкретной versioned Envelope, прошедшей validator; это транспортное кодирование, не разрешение opaque native payload. Маленькая запись передаётся одним CompleteRecord. Большая — частями с индексами 0..count−1. Каждая часть содержит одинаковые event_id/digest/count/total_bytes. Смешивать complete и parts в одном retry события нельзя.

SHA-256 вычисляется по исходным сериализованным Envelope bytes. Они фиксируются один раз до передачи и повторяются побайтно. Part layout также фиксируется для retry. Digest проверяет транспорт; он не является универсальным tx hash или semantic event ID. [Protobuf serialization is not canonical](https://protobuf.dev/programming-guides/serialization-not-canonical/): повторная сериализация другим runtime/version не обязана дать те же bytes.

Лимит учитывает полную сериализованную TransportFrame и NATS headers, а не только data. D52: default `transport.max_frame_bytes=2097152` (2 MiB) задаётся YAML Gateway и валидируется до отправки; NATS server/stream настроены на этот предел, gRPC учитывает собственную обёртку. Большая нормализованная транзакция передаётся несколькими RecordPart. Изменение лимита относится к новым планам; сохранённые pending bytes не перекодируются. При несовместимом уменьшении лимита — явная ошибка конфигурации до публикации, pending сохраняются до восстановления достаточного лимита. [Подробности](#live-yaml-и-команды). Pending spool без TTL/прикладной квоты по D28. Post-ACK retention вне scope. При невозможности разместить запись — backpressure/явная ошибка и незавершённый блок; обрезать данные запрещено.

Gateway назначает chain_sequence в пределах publication_epoch своего store; все части имеют одну пару epoch/sequence. Epoch сохраняется при restart с диском и меняется при создании нового store. Publication plan и точные frame bytes сохраняются producer до первой публикации; после потери Gateway store replay использует прежнюю пару и bytes. Это порядок регистрации в одном epoch, не block chronology, не NATS stream sequence и не глобальный порядок через потерю диска. Части history/live могут чередоваться. Протокол Prepare/Publish/IngestAck/Release: [ROCKSDB_DELIVERY_PROTOCOL](#rec-сценарии).

Execution chronology задают block parent links, native transaction position, operation_index и action/frame order. Delivery chronology задают chain_sequence и transport metadata. `part_index` задаёт только сборку bytes. Нельзя сортировкой chain_sequence восстанавливать blockchain chronology.

NATS message ID должен стабильно различать `(network,event_id,complete-or-part-index)`. После PubAck всех сообщений/частей закрытого block batch Gateway sync фиксирует NATS_ACKED и подтверждает adapter batch_id/digest/plan identity. ACK одного event не разрешает очистить весь блок. NATS deduplication window не обеспечивает вечный exactly-once: контракт допускает повторы с тем же ID/bytes. ACK не подтверждает работу downstream consumers. Durable plan/WAL, повторный запуск, потеря состояния и block-batch IngestAck/Release определены в ROCKSDB_DELIVERY_PROTOCOL.

### 7. Маршрутизация

Для каждой сети отдельные streams/subjects/limits. Сохраняется рабочий namespace ТЗ: `evm.v3.<chain_key>.<event_family>.<event_kind>`, где event_family=`tx|block|balance.system`. Event kind задаётся закрытым реестром: tx applied/removed/status; block applied/removed/status; optional balance.system applied/removed/status. Transport version/schema version находятся внутри frame; изменение Markdown v3 само по себе не меняет subject.

Полная запись и её части направляются в один subject своего logical event; identity сети из frame проверяется против route. D48: stream и subject prefix задаются в YAML Gateway. D54: logical destination (account/stream/subject mapping) фиксируется в durable publication plan и ACK; изменение назначения допускается только после завершения pending адаптера и Gateway. Смена URL того же назначения допустима после проверки идентичности. Подтверждения разных назначений не образуют общий NATS_ACKED. Перенос pending не проектируется; точная идентификация назначения и handshake/drain checks остаются E03. Post-ACK retention не входит в гарантию сборщика.

### 8. Проверки для последующих этапов

| Fixture | Обязательный результат |
|---|---|
| Failed tx + успешный child в reverted parent | Gas/nonce сохранены по native правилам; child actions не committed |
| Компенсирующие transfers | Оба actions есть, net diff может быть 0 |
| DEFERRED D42: Base deposit | Нет искусственной подписи/nonce; mint и deposit receipt fields сохранены |
| BSC Parlia system | Фактический wire type/signature не потеряны; native Finalize capture проверен |
| DEFERRED D42: Nitro retryable / imported legacy | Native payload отделён от projection; HashOverride и разные encodings проверены |
| DEFERRED D42: Nitro EVM ↔ Stylus | Frame engines и native actions без потери меж-runtime причинности |
| DEFERRED D38/D42: Classic native result ↔ Ethereum projection | Все source results учтены; CL-01…08 проверяют archive, декодирование, observer и неизменность native исполнения |
| Bor StateSync / legacy synthetic | Body 0x7f и optional legacy source receipt не смешаны; fixtures POLY-L01…07 из исследования E00-15 |
| Пустой блок / ошибка нормализации | Metadata пустого блока передаётся; известная ошибка даёт ERROR, а не NATS_ACKED |
| Parts reorder/duplicates/corruption | Сборка по индексу, повторы идемпотентны, конфликт bytes/digest отклоняется |
| Live reorg во время публикации; history overlap DEFERRED D38 | Дубли разных runs допустимы; lifecycle references не смешиваются, ACK учитывает все части |
| Неизвестный тип/версия/fork | Явная ошибка нормализации, без фиктивного поддержанного payload |
| Optional balances выключены | Передаётся заявленный transaction scope; полнота всех балансов не заявляется |

Это acceptance matrix будущих E01/E02/E03/E04/E05, не отчёт о пройденных тестах. E02 фиксирует compiled field numbers/closed validators текущих профилей; E03 проверяет delivery и bounded collector memory, без performance SLO. Classic/Base/Nitro и historical legacy fixtures явно deferred D38/D42; текущий Bor StateSync обязателен. История/YAML history control не входят в active backlog.


## Реестр native типов

> D42 (2026-10-21): текущий этап — Ethereum Mainnet, BNB Smart Chain (BSC) Mainnet и Polygon PoS Mainnet. Base/Arbitrum и их адаптеры/fixtures отложены; исследования и типизированные расширения ниже сохранены справочно, не блокируют текущую версию. Исторические сценарии отложены D38. Действующие live-типы транзакций ETH/BSC/Polygon, включая native system types, остаются обязательными. У каждой сети собственные WAL и журнал Gateway.

> Актуальная граница D23–D25 (2026-10-16): live и history работают в разных pod/process; история запускается простой командой N–M. Пересечения и повторные загрузки допускают дубли. Дедупликация и независимая проверка полноты блока выполняются за пределами сборщика. Здесь проверяется доставка подготовленных сообщений: RECEIVED → WAL_SAVED → NATS_ACKED. BlockComplete/coverage report и общий координатор устранения overlap не требуются. Подробности: [LIVE_HISTORY_DELIVERY](docs/LIVE_HISTORY_DELIVERY.md).

E00-14, 2026-10-15. Проект нормативной модели для пяти сетей, основанный на закреплённых исходниках [CLIENT_MATRIX.md](docs/CLIENT_MATRIX.md). Это не результат исполнения fixtures и не подтверждение активности каждого типа в Mainnet. Контракт сообщений: [MESSAGE_CONTRACT.md](#контракт-сообщений).

### Правила профиля

Ключ варианта: `(network identity, execution profile, native type)`. Один byte type без сети и fork profile недостаточен. Профиль содержит commit клиента, версию адаптера, fork schedule и версию нормализации. Применимость определяется нативными правилами конкретного блока, включая timestamp/height/ArbOS version, а не текущими настройками head.

В общей записи нет обязательных для всех типов signature, account nonce, gas price или получателя. Поля располагаются в конкретном варианте `oneof`; отсутствующее по модели поле не подменяется нулём. Типизированная execution projection отделена от исходных полей. Неизвестный тип, неподдержанный fork или отсутствие обязательного поля дают явную ошибку обработки; успешная доставка такого блока не объявляется.

`Q` — UInt256; `A` — Address20; `H` — Hash32; `B` — bytes. `Target` — `Call(A) | Create`; это явное различие, не нулевой адрес. Все поля варианта обязательны, кроме явно отмеченных optional. `[]` — упорядоченная последовательность, в том числе пустая. Нативные более узкие целые дополнительно проверяются на исходную разрядность.

### Стандартные EVM-варианты

| ID / payload | Поля |
|---|---|
| `0x00 EvmLegacy` | nonce:u64, gas_price:Q, gas_limit:u64, target:Target, value:Q, input:B, signature:{v:Q,r:Q,s:Q}; replay_protection=`Unprotected | Eip155(chain_id:Q)` |
| `0x01 EvmAccessList` | chain_id:Q, nonce:u64, gas_price:Q, gas_limit:u64, target:Target, value:Q, input:B, access_list:AccessEntry[], signature:TypedSignature |
| `0x02 EvmDynamicFee` | chain_id:Q, nonce:u64, max_priority_fee_per_gas:Q, max_fee_per_gas:Q, gas_limit:u64, target:Target, value:Q, input:B, access_list:AccessEntry[], signature:TypedSignature |
| `0x03 EvmBlob` | поля EvmDynamicFee, но target только `Call`; дополнительно max_fee_per_blob_gas:Q, blob_versioned_hashes:H[] |
| `0x04 EvmSetCode` | поля EvmDynamicFee, но target только `Call`; дополнительно authorization_list:Authorization[] |

`AccessEntry={address:A, storage_keys:H[]}`. `TypedSignature={y_parity:0|1,r:Q,s:Q}` относится к внешней typed transaction. `AuthorizationSignature={y_parity:u8,r:Q,s:Q}` и `Authorization={chain_id:Q,address:A,nonce:u64,signature:AuthorizationSignature}` сохраняют исходный authorization tuple. Порядок и дубликаты source lists сохраняются. Нельзя отвергать целую включённую native транзакцию потому, что отдельный tuple имеет parity вне 0/1, неверную подпись, chain_id/nonce или неприменимую authority: native может пропустить tuple и продолжить обработку. Проверки исходной разрядности остаются обязательными; source tuple не заменяется результатом signature recovery. Валидность внешней транзакции и применимость tuple — разные проверки. [EIP-7702](https://eips.ethereum.org/EIPS/eip-7702), [выбранный Geth source](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/types/tx_setcode.go).

Blob sidecars не являются обязательной частью включённой execution transaction: сохраняются поля native body, blob hashes и применимый receipt; доступность blob data не подменяется обещанием полного DA-архива. Fixtures AUTH-01–04 определены в [реестре исправлений](docs/DESIGN_REPAIR_STATUS.md); runtime-проверки ещё не выполнены.

Это библиотека вариантов, а не разрешение всех пяти типов во всех сетях. ETH/BSC исходники определяют ID 0–4; Base/Nitro/Bor используют только варианты, разрешённые их собственными правилами в данном блоке. Fork validator обязателен. Наличие типа в исходнике само по себе не доказывает активацию.

Источники: [Geth types](https://github.com/ethereum/go-ethereum/blob/a579077007b98217c3e253a66e4b452ca0c32b96/core/types/transaction.go), [BSC types](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/types/transaction.go).

### Base

| ID / payload | Поля и применимость |
|---|---|
| `0x7e BaseDeposit` | source_hash:H, from:A, target:Target, mint:Q с native bound u128, value:Q, gas_limit:u64, is_system_transaction:bool, input:B |

Deposit не имеет обычной подписи и account nonce. `deposit_nonce` в receipt — отдельное optional поле, не основание синтезировать nonce исходной транзакции. `deposit_receipt_version` также optional и зависит от fork. `is_system_transaction` сохраняется из source и проверяется по fork, а не выводится из отсутствия подписи.

Типизированный `BaseReceiptExtension`: deposit_nonce?:u64, deposit_receipt_version?:u64. Типизированный `BaseFeeDetails` по применимому fork: l1_base_fee:Q, l1_fee_overhead?:Q, l1_base_fee_scalar:Q, l1_blob_base_fee?:Q, l1_blob_base_fee_scalar?:Q, operator_fee_scalar?:Q, operator_fee_constant?:Q, da_footprint_gas_scalar?:u16, tx_l1_cost?:Q. Отсутствие данных отличается от неприменимости. Коэффициенты сохраняются как native integers; RPC float `l1FeeScalar` не используется как точный источник.

Кодовые ветви будущих AA/Eip8130 (`0x79`) не считаются подтверждённым действующим Mainnet-контрактом. Новый fork требует отдельной версии реестра, полей, validator и fixtures до объявления покрытия.

Источники: [deposit](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/crates/common/consensus/src/transaction/deposit.rs), [deposit receipt](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/crates/common/consensus/src/receipts/deposit.rs), [native fee information](https://github.com/base/base/blob/e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832/crates/common/evm/src/l1block.rs). Конкретная nullable-семантика проверяется в E02 fixtures.

### Arbitrum Nitro

| ID / payload | Native поля |
|---|---|
| `0x64 ArbDeposit` | chain_id:Q, l1_request_id:H, from:A, to:A, value:Q |
| `0x65 ArbUnsigned` | chain_id:Q, from:A, nonce:u64, gas_fee_cap:Q, gas:u64, target:Target, value:Q, data:B |
| `0x66 ArbContract` | chain_id:Q, request_id:H, from:A, gas_fee_cap:Q, gas:u64, target:Target, value:Q, data:B |
| `0x68 ArbRetry` | chain_id:Q, nonce:u64, from:A, gas_fee_cap:Q, gas:u64, target:Target, value:Q, data:B, ticket_id:H, refund_to:A, max_refund:Q, submission_fee_refund:Q |
| `0x69 ArbSubmitRetryable` | chain_id:Q, request_id:H, from:A, l1_base_fee:Q, deposit_value:Q, gas_fee_cap:Q, gas:u64, retry_target:Target, retry_value:Q, beneficiary:A, max_submission_fee:Q, fee_refund_addr:A, retry_data:B |
| `0x6a ArbInternal` | chain_id:Q, data:B |
| `0x78 ArbLegacyImported` | legacy:EvmLegacy, hash_override:H, effective_gas_price:u64, l1_block_number:u64, sender_override?:A |

У `0x64–0x6a` нет native signature: нулевые V/R/S из универсальных accessor не становятся подписью. Nonce есть у Unsigned и Retry, но Retry пропускает nonce check; у Deposit/Contract/SubmitRetryable/Internal такого поля нет. Deposit/Internal также не содержат native gas limit/fee cap.

У Internal effective sender выводится как ArbOS address и помечается `DERIVED`. У SubmitRetryable исполнение обращается к ArbRetryableTx precompile с ABI calldata; это не `retry_target/retry_value/retry_data`. Сохраняются обе структуры: исходный вариант и execution projection с происхождением `NATIVE_DERIVED`. Обязательные сведения о взимании L1/L2 fees и retryable effects связываются с actions; более подробный состав `ArbitrumExecutionDetails` требует fixtures E02, не маскируется generic bytes.

Encoding различается: custom `MarshalBinary` даёт type+RLP, но pinned `UnmarshalBinary` их отвергает; native `DecodeRLP` разрешает arb parsing. Для `0x78` `Hash()` возвращает hash_override, trie encoding использует embedded legacy, а MarshalBinary — wrapper. Поэтому обязательны source hash и encoding kind; правило `keccak(raw)==hash` применяется только там, где подтверждено профилем.

Наличие `0x78` не решает extraction Arbitrum Classic. E00-15 определил отдельный native профиль ниже Nitro boundary 22 207 817, описанный далее. Его сборка, архив, observer и all-epoch decoders ещё не приняты runtime-проверками; историю от genesis нельзя объявлять полностью поддержанной.

Источники: [native structs](https://github.com/OffchainLabs/go-ethereum/blob/5eee8f546e7602b7deeef7909f291bbebc77e233/core/types/arb_types.go), [encoding and hash](https://github.com/OffchainLabs/go-ethereum/blob/5eee8f546e7602b7deeef7909f291bbebc77e233/core/types/transaction.go), [legacy wrapper](https://github.com/OffchainLabs/go-ethereum/blob/5eee8f546e7602b7deeef7909f291bbebc77e233/core/types/arbitrum_legacy_tx.go), [sender](https://github.com/OffchainLabs/go-ethereum/blob/5eee8f546e7602b7deeef7909f291bbebc77e233/core/types/arbitrum_signer.go).

### Arbitrum Classic: отдельный native origin

Исследование E00-15: [ARBITRUM_CLASSIC_CAPTURE.md](docs/research/ARBITRUM_CLASSIC_CAPTURE.md). Classic v1.4.7 `dd4595f894b52c73a293a0c4bce9411afb12da12`, ArbOS source pin `234cf670016d675095110cd944cb82fde9c460b8`. Это Go + C++ AVM, а не ещё один Nitro/Geth tracer. Source pin не заменяет machine-code identity конкретной исторической эпохи.

`ClassicNativeTransaction`: origin=`ARBITRUM_CLASSIC_AVM`, request_id:H, avm_log_index, native_result_tx_index, optional ethereum_projection_index; provenance={l1_sequence_number:Q,parent_request_id:H,index_in_parent:Q}; sender, block/timestamp metadata, native inbox kind, optional L2 subtype, native result enum, gas/fee data, typed payload. Точные типы индексов и gas/fee bounds сверяются с native decoder в E02, без усечения до u64.

| Известный payload-кандидат | Подтверждённые native поля |
|---|---|
| ClassicUnsignedTransaction | MaxGas:Q, GasPriceBid:Q, SequenceNum:Q, DestAddress:A, Payment:Q, Data:B |
| ClassicRetryable | Destination:A, Value:Q, Deposit:Q, MaxSubmissionCost:Q, CreditBack:A, Beneficiary:A, MaxGas:Q, GasPriceBid:Q, Data:B |

Signed/contract/compressed/deposit variants и native result enums требуют отдельного закрытого mapping в E02. Значения parser constants и источники перечислены в исследовании; это не обещание применимости всех control/simulation kinds к Mainnet transactions. Raw request bytes сохраняются дополнительно, но не заменяют типизированные поля. Неподдержанный decoder означает неполное покрытие, а не generic payload, принятый как полный.

Receipt source hash для Classic — native MessageID/request_id. Normalized hash kind=`NATIVE_REQUEST_ID`; raw kinds=`CLASSIC_REQUEST_BYTES | CLASSIC_AVM_VALUE`. Ethereum projection хранится отдельно и может отсутствовать. Native retryable projection создаёт dummy LegacyTx с нулевыми полями; эти значения не становятся исходными nonce/gas/signature.

Полный source inventory читается из persisted AVM TxResult/BlockInfo log range, с сохранением связи с Ethereum projection; отдельный runtime verifier полноты не вводится. Функция FilterEthTxResults способна исключать неподдающиеся конвертации результаты; нельзя считать projected body полным inventory. Каждый persisted результат явно классифицируется; control/batch input сам по себе не объявляется транзакцией. Unknown kind блокирует COMPLETE. Исследование показывает возможность фильтрации в коде; конкретные Mainnet примеры ещё требуют fixtures.

Native GetBlockResults проверяет BlockInfo и декодирует TxResult из префикса длиной BlockStats.TxCount; остальные AVM logs не становятся транзакциями. Принадлежность результату блока задаётся этим native range, а не только IncomingRequest.L2BlockNumber: source допускает расхождение для RevertCode. Исходное поле request сохраняется отдельно, без переопределения канонического BlockRef.

Для всех calls/actions/diffs предложен native observer на уровне C++ AVM с декодированием семантики конкретной ArbOS эпохи. Нельзя переписать исторический ArbOS для добавления debug instructions и считать это тем же исполнением: machine hash, gas, logs и sends должны совпадать с baseline. Полнота observer и доступность архива остаются gate E01/AP-06, а не закрытым результатом E00-15.

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

Источники: [state sync payload](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/core/types/tx_state_sync.go), [items](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/core/types/state_data.go), [legacy receipt](https://github.com/0xPolygon/bor/blob/9c445ef603e71469e1c176d8e57e7cf94864ade7/core/types/bor_receipt.go).

### Capture engines и граница готовности

`EngineKind = EVM | STYLUS | CLASSIC_AVM | NATIVE_PROTOCOL`. Engine задаётся на frame/action: одна транзакция Nitro может переходить между EVM и Stylus; Classic исполняет EVM-семантику внутри AVM. Go hooks применяются в Geth/BSC/Nitro/Bor; Base требует Rust inspector + native executor; Classic — Go native reader + C++ observer. Canonical notification не заменяет execution capture. Для native actions без EVM frame сохраняется typed cause, а не выдуманный CALL.

E00-14 фиксирует карту вариантов и правила их представления; E00-15 дополняет её Classic origin и optional legacy Bor receipt. Компилируемые proto, точные registered network execution extensions, all-epoch decoders и fork validators — E02; native runtime-доказательства — E01/E04/E06. Эти зависимости запрещают объявлять полный протокол и всю историю поддержанными по одному source research.


## Native capture и lifecycle recovery

2026-11-05 · DOC-REPAIR-03 · D57/D58. Нормативное уточнение [архитектуры](docs/COLLECTION_ARCHITECTURE.md), [модели изменений](#нативные-балансы-и-причины-изменений) и раннего описания capture в [RocksDB protocol](#rec-сценарии). Это проект native patches; наличие подходящих upstream API не доказывает работу будущей сборки. E01/E04/E05 остаются обязательными gates.

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

`observation_id=(producer_id,run_id,observation_sequence)` относится к native наблюдению, а не к transport batch. Observation counter выделяется durable; отсутствие публичных событий у rejected/side-branch observation не создаёт дыр в delivery sequence. Последний выделяется отдельно по [DELIVERY_STATE_MACHINE](#durable-delivery-и-смена-назначения).

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

[Geth state_processor](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/state_processor.go) — источник makeReceipt; публичные поля задаёт [MESSAGE_CONTRACT](#контракт-сообщений).

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

2026-11-05. D26–D33, D52, D54, D57–D59. Этот документ уточняет и заменяет неоднозначные переходы [ROCKSDB_DELIVERY_PROTOCOL](#rec-сценарии). Только технический проект; реализация/crash fixtures не выполнены. Native admission и source retention — отдельный контракт; сюда поступают durable capture obligations, затем immutable Envelope bytes.

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
3. Producer фиксирует drain_id и batch_highwater, затем отправляет SealGeneration(binding_generation,admission_generation,drain_id,highwater). Gateway fencing barrier проверяет отсутствие незавершённых операций <=highwater и совпадение retired_prefix. Sync SEALED_GENERATION запрещает новые Prepare этой admission generation; повтор возвращает тот же SealAck.
4. Producer sync сохраняет DRAINED с SealAck и завершается. Crash до этого продолжает DRAIN; новый LIVE не включается автоматически. Оба owners сохраняют identity/lifecycle, terminal prefix и seal evidence, очищают временные delivery payload/receipts. «Оба журнала очищены» не означает удалить DB/ключи.
5. Для смены destination новый descriptor имеет новую generation; обе конфигурации указывают old/new binding и drain_id. Fenced handshake пишет Gateway SWITCH_PREPARED, producer sync новую binding, Gateway подтверждает SWITCH_COMMITTED. Повтор каждого шага идемпотентен; несовпадение generation блокирует capture. Команда LIVE выполняет этот handshake, затем Resume до снятия DRAINED и фиксации новой H. Для возврата в LIVE без смены назначения также требуется явный restart с mode=live и Resume после завершённого seal.
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


## Live YAML и команды

> Нормативные effective native configs, mounts/UID, trust bootstrap, peer role/network admission и startup — [DEPLOYMENT_BOOTSTRAP_CONTRACT](#bootstrap-и-окружение). Для поставляемого окружения credentials/TLS обязательны по этому профилю, даже если общий YAML допускает иные явно выбранные transport modes.

> D52: операционная готовность/синхронизация наблюдается по логам; управление штатными средствами Docker/Kubernetes, без отдельного API и обязательной детализации probes/shutdown таймаутов. Native граница H+1 и сохранение pending остаются. Default размера frame — 2 MiB, правила ниже.

> D51: штатные native configs можно заменить целиком и дополнить аргументами. Окружение выполняет отдельный init-шаг, первый старт создаёт необходимые native файлы, рестарт сохраняет state/ключи. Исходники `/config` read-only; изменяемые native файлы находятся в `/data/node`. Это подготовка окружения, не новая команда CLI сборщика. Точные filenames/init/mount mappings остаются в [deployment-задачах](docs/NODE_DEPLOYMENT_SPEC.md).

> D50: [таблица endpoints и экспонирования](#порты-и-экспонирование) закреплена по поручению пользователя. P2P overrides задаются native конфигурацией/окружением, не новыми произвольными ключами YAML сборщика. Gateway каждой сети использует внутренний service DNS и порт 50051, NATS — внутренний 4222. Значения настраиваемы; P2P NodePort не затрагивает приложение и не открывает его API наружу.

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

Объекты TLS: `mode: disabled` запрещает дополнительные поля; `server` требует `ca_file`; `mutual` требует `ca_file`, `cert_file`, `key_file`. Проверка имени сервера обязательна при TLS. Для NATS disabled соответствует `nats://`, остальные режимы — `tls://`. Server-only TLS для адаптер↔Gateway не разрешён. UDS защищается правами файловой системы. Противоречивые режимы — ошибка до запуска.

Структура всего документа проверяется целиком. Обязательные поля, локальные файлы и credentials проверяются только для запускаемой роли. Live не требует чтения NATS credentials; отсутствующая секция другой роли допустима. Секций ClickHouse, history, coverage и startup-уведомлений нет.

### Размер сообщения D52

Gateway применяет `transport.max_frame_bytes` при формировании publication plan и определяет part layout. До Prepare адаптер хранит неизменные Envelope bytes; локальные фрагменты хранения не задают сетевое разбиение. Адаптер сериализует и сохраняет точные frames по полученному plan, Gateway проверяет их перед публикацией; второй независимый лимит разбиения в live YAML не вводится. Если полный record не помещается с envelope/headers, он разбивается на RecordPart, каждый в пределах лимита. Никакой размер транзакции не оправдывает усечение данных.

В поставляемой конфигурации NATS `max_payload` задаётся явно `2097152`; stream MaxMsgSize также `2097152`. Полный размер frame и headers проверяется до отправки; gRPC send/receive пределы должны вмещать размер соответствующего RPC сообщения с его обёрткой, а не только bytes frame. RPC не должен объединять неограниченное число frames в одно сообщение. Проверка фактического сериализованного размера обязательна; ограничения NATS и gRPC должны быть согласованы с override лимита.

Pending publication plan/bytes неизменны при restart и retry. Изменённый лимит применяется к новым планам; если сохранённые frames больше действующего лимита либо server/stream их не допускает, публикация останавливается с явной ошибкой конфигурации. Pending не удаляются и не разбиваются повторно; нужно восстановить достаточный лимит. Горячая перезагрузка не добавляется.

Штатный NATS default — 1 MB по [документации](https://docs.nats.io/running-a-nats-service/configuration); он не гарантирует размещение полной нормализованной транзакции с calls/logs/state changes. Поэтому 2 MiB — предел сообщения, не обещание «любая транзакция одним сообщением».

### Смена назначения NATS — D54

Точный нормативный порядок D54/D57/D59: [DELIVERY_STATE_MACHINE](#durable-delivery-и-смена-назначения). Binding задаётся окружением до первого capture и сохраняется обоими owners; local READY без Prepare уже имеет destination. Drain и switch проходят durable generation barrier, а не проверку пустоты RAM.

`stream` и `subject_prefix` проверяются вместе с network identity и доступным NATS account при online handshake. Это конфигурация существующего назначения; Gateway не создаёт streams и не меняет их административно. Точные правила идентификации account/назначения и их проверки при reconnect требуется завершить в E03 до реализации этого протокола; совпадение URL или имени stream само по себе недостаточно.

Publication plan и ACK относятся к одному сохранённому логическому назначению. При наличии незавершённой доставки у адаптера или Gateway смена account/stream/subject mapping отклоняется до публикации; pending и исходные настройки назначения сохраняются. Смена адреса или credentials, по-прежнему ведущих в тот же account/stream/subjects, не является переносом данных, но требует проверки назначения. Неопределённость этой проверки не разрешает объединять ACK.

Для перенастройки назначения остановить новые live-наблюдения, завершить pending и обмен подтверждениями в прежнем назначении, проверить сохранённые состояния обоих owners, затем изменить конфигурацию и перезапустить контур. Только пустой Gateway или очередь RAM не доказывают завершение producer pending; при недоступности необходимого состояния переход не подтверждается. После перезапуска новые наблюдения начинаются с новой H+1 по D39. Если прежнее назначение недоступно, данные сохраняются, а смена блокируется; принудительный перенос pending в текущий scope не входит. Post-ACK retention/потеря NATS store остаются вне гарантии сборщика по D30–D31.

### Native-профили

| network.profile | native.kind | Выбранная реализация |
|---|---|---|
| ethereum-mainnet | geth | Go/Geth: native hooks/state journal, canonical notifications |
| bsc-mainnet | bsc | Go/BSC: tx execution и Parlia Finalize |
| polygon-pos-mainnet | bor | Go/Bor: tx execution, StateSync/Finalize; supporting Heimdall |

Сборки и toolchains закрепляются по [CLIENT_MATRIX](docs/CLIENT_MATRIX.md) и [COMPONENT_STACK](#стек-и-границы-сборок). YAML не выбирает произвольный executable/version и не добавляет новый тип блокчейна без адаптера. Исторические Classic/replay профили сейчас не предлагаются. Эта таблица использует ранее собранную документацию, не утверждает совместимость по результатам runtime-тестов.

**Общий интерфейс, native параметры.** Аргументы запуска, datadir, consensus/rollup/Heimdall соединения и JWT задаются штатной конфигурацией native клиента и окружением. Они не дублируются в YAML адаптера. Общий параметр подключения YAML является частью нашей native интеграции; команды Gateway/config check остаются отдельными. Native capture не заменяется RPC.

Встроенный адаптер сверяет network.profile/native.kind с эффективной конфигурацией запущенного клиента, используя результат его штатного parser. Несоответствие сети или сборки даёт ошибку до capture; адаптер не меняет native настройки. Datadir определяется из этой эффективной конфигурации. Отдельный config check проверяет YAML, но не обещает сверку с ещё не запущенной нодой.

Для каждого профиля при реализации потребуются шаблоны штатного запуска с подключённым YAML адаптера и проверки настоящим клиентом. Новое исследование версий и сборки в этом шаге не выполнялись.

### Состояние и запуск

1. Проверить CLI/YAML, пути, типы, профиль и эффективную native конфигурацию до открытия writable состояния. Ошибка не должна частично применять настройки.
2. Эксклюзивно открыть состояние owner. Native datadir, live_dir и gateway_dir не могут совпадать или быть вложены друг в друга; symlinks разрешаются перед сравнением. Второй writer получает ошибку.
3. Сверить сохранённые network/profile, роль, wire schema/build identity. Несовместимый WAL не удалять и не переинициализировать молча. Сеть нельзя сменить, сохранив прежний каталог.
4. До native recovery установить hooks/decision bridge в RECOVERY_ONLY; разрешить сохранённые ADMITTED/EXEC_READY/markers и lifecycle по NATIVE_CAPTURE_PROTOCOL. Независимо восстановить pending по [DELIVERY_STATE_MACHINE](#durable-delivery-и-смена-назначения), сохраняя IDs/bytes/parts. Новый safe live capture не ждёт NATS, но не обходит незавершённые native obligations.
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


## Bootstrap и окружение

2026-11-05 · DOC-REPAIR-04 · D43–D52/D57–D59. Нормативная детализация [состава нод](docs/NODE_DEPLOYMENT_SPEC.md), [портов](#порты-и-экспонирование) и [live YAML](#live-yaml-и-команды). Здесь описаны будущие init/runtime contracts; Compose/Kubernetes файлы, секреты, образы и процессы ещё не создавались.

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

Native parser определяет precedence TOML/YAML/args; preflight проверяет **effective** значения после этого precedence. Overrides не могут обойти chain identity, hash pin provider, ordinary Bor import и ownership ограничения [NATIVE_CAPTURE_PROTOCOL](#native-capture-и-lifecycle-recovery). Неизвестные upstream options проверяются самим pinned parser. Полный rendered config digest и redacted effective settings входят в manifest.

Capture baseline Geth/BSC задаёт `--state.scheme hash --gcmode full`; Bor TOML — верхнеуровневый quoted key `"state.scheme"="hash"`, `gcmode="full"`, `syncmode="full"`. `history.state` — настройка path backend, она не заменяет durable pins. Сам hash scheme без retention patches тоже недостаточен. У supporting Polygon L1 нет capture pins, его профиль остаётся отдельным.

State scheme задаётся уже при создании native DB/genesis и проверяется в записанном store до ENV_READY: runtime override не конвертирует ранее созданный path store в hash. В BSC пустой default init выбирает path, поэтому явный init flag обязателен. [BSC init](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/cmd/geth/chaincmd.go), [ParseStateScheme](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/rawdb/accessors_trie.go).

Точные Bor overrides: `--pipeline.enable-import-src=false`, `--witness.parallelstatelessimport=false`, `--witness.syncwithwitnesses=false`, `--relay.enable-preconfs=false`, `--accept-preconf-tx=false`, `--sequencer.enabled=false`; по D60 дополнительно `--parallelevm.enable=false --parallelevm.enforce=false`. Соответствующие проверенные TOML keys: `[pipeline] enable-import-src`, `[witness] parallel-stateless-import`, `[relay] enable-preconfs`, `[jsonrpc] accept-preconf-tx`, `[sequencer] enabled`. Для остальных используется проверенный CLI, неизвестные TOML аналоги не придумываются. [Bor flags](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/internal/cli/server/flags.go), [config](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/internal/cli/server/config.go).

### Trust bootstrap и NATS permissions

Окружение создаёт отдельную private CA либо принимает операторскую CA. Private CA key хранится у bootstrap/operator, не монтируется в runtime. Server certificate SAN соответствует реальному service DNS/endpoint; client identity содержит deployment/network/role. Gateway проверяет CA/expiry/EKU и allowlist adapter identity для своей сети; одного доверия общей CA недостаточно. Cross-network/role certificate отвергается до handshake/state mutation. TLS hostname verification нельзя отключать. UDS, если явно выбран, проверяет filesystem ownership/peer identity и не разрешает чужую сеть через общий socket.

У каждой Geth/Prysm пары свой 32-byte JWT secret в hex; Polygon L1 не использует основной Ethereum JWT. Gateway получает только свои network-limited NATS credentials; adapter их не получает. Runtime secrets read-only с правами минимального owner. Rotation готовит новый secret generation, атомарно устанавливает files и рестартует затронутые процессы; CA rotation допускает ограниченный trust overlap. Смена NATS credentials всё равно проходит проверку прежнего account/binding. Рабочие secrets сейчас не генерируются.

NATS environment bootstrap и runtime Gateway используют разные credentials. Bootstrap создаёт только configured streams, сверяет существующие; compatible UPDATE допускается только для явно заданной config revision. Никаких автоматических DELETE/PURGE/reset при несовпадении. Runtime: publish в закрытый subject namespace своей сети; request `$JS.API.INFO`, `$JS.API.STREAM.INFO.<stream>`, `$SYS.REQ.USER.INFO`; subscribe только в собственный reply inbox prefix. CREATE/UPDATE/DELETE/PURGE/consumer admin runtime не получает. Environment обязан обеспечить UserInfo responder/permissions и проверяемую account identity; невозможность проверки блокирует публикацию, не заменяется сравнением URL. [NATS account API](https://docs.nats.io/reference/jetstream/api/account), [stream API](https://docs.nats.io/reference/jetstream/api/stream), [UserInfo](https://github.com/nats-io/nats.docs/blob/master/release_notes/whats_new_210.md).

Bootstrap выпускает общий immutable DestinationBinding, который оба owners сохраняют до capture. Expected account/stream/subjects/metadata/limits проверяются Gateway online. NATS server max_payload и stream max_msg_size должны пропускать фактические frame+headers; default проекта 2 MiB, plan size immutable. Отсутствующий/чужой stream даёт CONFIG_BLOCKED; bootstrap созданного окружения не является разрешением уничтожить прежнюю историю. Drain/cutover/Resume — только по [DELIVERY_STATE_MACHINE](#durable-delivery-и-смена-назначения).

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

> Точные native mappings, listener scopes и bootstrap — [DEPLOYMENT_BOOTSTRAP_CONTRACT](#bootstrap-и-окружение). D58 обновил Heimdall до v0.13.2, порты сохранены. CometBFT RPC 26657 baseline слушает loopback внутри Heimdall. По D52 отдельная probe/shutdown спецификация не вводится, обязательные состояния отражаются в logs.

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
| Ethereum consensus | Prysm 7.3.0, кандидат D47; image digest не закреплён | Отдельный компонент основной Ethereum-ноды; layout контейнеров — DEP-03 |
| Polygon L1 источник | Отдельный Geth + Prysm, D44 | Собственная Ethereum-нода Polygon без нашего Ethereum-адаптера, со своими процессами/datadirs; основная Ethereum-нода не используется |
| BSC native adapter | Go внутри BSC v1.7.8 | Обычные tx и Parlia Finalize capture; отдельный client build |
| Отложено D42: Base native adapter | Rust 1.96.0, base/base v1.5.0 + pinned base/reth | Native executor + включённый Inspector, per-tx results, canonical ExEx |
| Отложено D42: Arbitrum Nitro adapter | Go в Nitro v3.12.1 с его собственным Geth; native Rust/cgo/WASM build dependencies | ArbOS/Stylus/native special paths; отдельная сборка и fixtures |
| Отложено D42: Arbitrum Classic adapter | Go + C++ AVM, Classic v1.4.7 candidate | Native result reader + consensus-neutral observer candidate; отдельный history runtime той же сети |
| Polygon adapter | Go внутри Bor v2.10.3, Heimdall-v2 v0.13.2 | Native tx и StateSync/Finalize, отдельный optional legacy scope; последовательный профиль D60 |
| Canonical Coordinator | Библиотечный модуль адаптера/контура соответствующей сети | Отдельный общий сервис для всех сетей не вводится. Каждый producer/run владеет своим lifecycle state; единого владельца для устранения пересечений нет; см. STATE_OWNERSHIP |
| Gateway/publisher | Go 1.27.2, gRPC-Go 1.84.0, nats.go 1.54.0; один экземпляр роли publisher на сеть | Единый исходный код/binary для всех сетей, отдельный процесс и конфиг каждой сети; NATS SDK только здесь |
| Wire schema | Protobuf proto3 с explicit presence и closed oneof | Одна versioned schema, native validators, transaction/block metadata/lifecycle/parts из MESSAGE_CONTRACT |
| Генерация Go | protoc 36.2 + protobuf-go/protoc-gen-go 1.36.12 | protoc-gen-go-grpc из согласованного source release grpc-go; точный module tag/commit и lock фиксируются E01/E02, не подменяются версией server SDK |
| Отложено D42: Rust transport/codec | tonic 0.14.6 + tonic-prost/tonic-prost-build 0.14.6; prost/prost-build 0.14.4 | Общая proto schema; встройка в Base Cargo graph проверяется E01/E02, upstream lock клиента не обновляется автоматически |
| Adapter → Gateway | gRPC streaming; локальный UDS при совместном размещении, TCP+mTLS при сетевом соединении | Оба конца проверяют schema/profile/размер/целостность; production topology определяет endpoint, не семантику данных |
| WAL/publication journal | RocksDB 10.4.2; Go grocksdb v1.10.2; Rust bindings отложены D42 | Sync WriteBatch, WAL enabled, отдельный owner; [нормативный автомат](#durable-delivery-и-смена-назначения). BlobDB optional, backup исключён D27 |
| Приёмочный harness | Go + тот же protobuf decoder + JetStream client | Только проверка опубликованных сообщений против fixtures; не промышленный downstream |
| Локальный запуск | Docker Compose: все execution/consensus/supporting-компоненты трёх нод, адаптеры, WAL/Gateway и NATS | Полная компоновка и конфигурация проекта по D43; [детализация](docs/NODE_DEPLOYMENT_SPEC.md) |
| Production | Kubernetes: все компоненты нод, адаптеры, независимые workload/volume/config контуры сетей и NATS | D43: готовим весь технический комплект; NATS chart/server candidates и [исследование](docs/research/NATS_DEPLOYMENT_STACK.md) сохраняются; production-методика отдельно |

Native pins и расхождения toolchains подробно сохранены в [CLIENT_MATRIX.md](docs/CLIENT_MATRIX.md). Classic runtime ещё не доказан — [исследование](docs/research/ARBITRUM_CLASSIC_CAPTURE.md). Новый baseline Gateway не изменяет compiler versions native клиентов.

### Почему SDK не встраивается во все native clients

Pinned gRPC-Go 1.84.0 требует Go 1.25.0; nats.go 1.54.0 — Go 1.26.0. protobuf-go 1.36.12 требует Go 1.23. Поэтому импорт нового NATS SDK в Geth/BSC candidate с Go 1.25 создаёт лишнее требование смены toolchain. Адаптер знает gRPC/Protobuf, Gateway — NATS; процессная граница уже принята D19. Генерируемые Go bindings всё равно проверяются в каждом client module отдельно.

Источники: [grpc-go go.mod](https://github.com/grpc/grpc-go/blob/v1.84.0/go.mod), [nats.go go.mod](https://github.com/nats-io/nats.go/blob/v1.54.0/go.mod), [protobuf-go go.mod](https://github.com/protocolbuffers/protobuf-go/blob/v1.36.12/go.mod), [Go distributions](https://go.dev/dl/?mode=json). Go 1.27.2 — кандидат общего сервиса на дату проверки, не утверждение об установленном компиляторе проекта.

Отложено D42: у tonic 0.14.6 workspace MSRV 1.88, у prost 0.14.4 — 1.85; это ниже source toolchain Base 1.96.0. Cargo graph проверяется только после возобновления Rust-профиля, не prerequisite E01–E07 текущих трёх Go клиентов.

Источники: [tonic workspace](https://github.com/grpc/grpc-rust/blob/tonic-v0.14.6/Cargo.toml), [tonic-prost](https://github.com/grpc/grpc-rust/blob/tonic-v0.14.6/tonic-prost/Cargo.toml), [tonic-prost-build](https://github.com/grpc/grpc-rust/blob/tonic-v0.14.6/tonic-prost-build/Cargo.toml), [prost](https://github.com/tokio-rs/prost/blob/v0.14.4/Cargo.toml).

### Версионирование и проверка

- Исходники native clients остаются на отдельных закреплённых commits; общий транспортный модуль не требует слияния их несовместимых Go/Cargo dependencies в один build graph.
- Теги библиотек выше — кандидаты на дату проверки. Перед воспроизводимой сборкой E01 записывает commit/checksum, compiler, generator versions, dependency lock и image digest. В конфигурации поставки не используются плавающие `latest`.
- Версии protoc, Go/Rust generators, runtimes и wire schema — разные величины. Они явно перечисляются в build manifest, не выводятся друг из друга.
- Текущие Go fixtures проверяют поля/presence/oneof; cross-language Rust fixtures отложены D42. Protobuf bytes независимых сериализаций не обязаны совпадать; retries сохраняют исходные bytes/digest события.
- NATS server version, Helm chart version и appVersion фиксируются отдельно. Наличие Helm chart не выбирает автоматически cluster sizing, storage class или политику потери диска.
- Конкретный Linux base image, CPU architectures и поддерживаемые Kubernetes/Compose versions закрепляются после build smoke tests; не заявляется поддержка каждой платформы из-за наличия Dockerfile.
- YAML/restart определены LIVE_CONFIG_SPEC; parser/implementation E06. Hot reload, metrics и численные performance thresholds не являются открытыми обязательными задачами. Собственный orchestration/operator не добавляется.

Релизы/модули для проверки: [gRPC-Go 1.84.0](https://github.com/grpc/grpc-go/releases/tag/v1.84.0), [nats.go 1.54.0](https://github.com/nats-io/nats.go/releases/tag/v1.54.0), [protoc 36.2](https://github.com/protocolbuffers/protobuf/releases/tag/v36.2), [protobuf-go 1.36.12](https://github.com/protocolbuffers/protobuf-go/releases/tag/v1.36.12), [grpc generator module](https://github.com/grpc/grpc-go/blob/v1.84.0/cmd/protoc-gen-go-grpc/go.mod).


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
| REC-T13 | Producer volume потерян | Новый run с явной потерей прежних локальных obligations; старый Gateway state не уничтожается автоматически. YAML не восстанавливает cursor/payload. Ручной history диапазон DEFERRED D38, гарантия полной потери диска не добавляется |
| REC-T14 | Незакрытый ранее начатый capture / reorg после ACK cleanup и restart | восстановление конкретного batch/activation references либо ERROR при отсутствии state; нет сканирования всего пропущенного периода. Live продолжает текущие наблюдения по D37; отсутствие прежних данных не маркируется успешной доставкой |
| REC-T15 | Неверный digest, schema, ACK или лимит ниже pending part | явная ошибка, неизменные pending bytes сохранены |
| REC-T16 | Live drain/Seal/Resume и потеря ReleaseAck; history CloseRun DEFERRED | Durable DRAIN/DRAINED, admission generations, ordered Release/frontiers; нет новых admissions в drain, поздний old RPC не воскрешает state, pending без ACK не удаляется |
| REC-T17 | Часть batch подтверждена; изменены account/stream/subjects; restart или потеря Gateway store | несовместимая смена отвергнута до публикации, исходные bytes/plan сохранены; ACK разных назначений не закрывают batch. Смена адреса того же назначения проверяется отдельно; изменение назначения допускается после подтверждённого drain обоих owners |

Fixtures выполняются для текущих Go journals и Gateway/NATS в E01/E03/E05/E06 по IMPLEMENTATION_GATES; Rust/history variants DEFERRED D38/D42. Данные и expected имеют независимое происхождение. Это tests разработки, не production completeness verifier. Исторический .06 не закрывает ни один runtime gate; актуальный проект — NATIVE_CAPTURE_PROTOCOL/DELIVERY_STATE_MACHINE.


## План доказательств и границы модулей

2026-11-05 · DOC-REPAIR-05. Уточнение [DEVELOPMENT_PLAN](#задачи-разработки-e01e07), последовательность D40 **E01 → E02 → E03 → E04 → E05 → E06 → E07 сохраняется**. Все перечисленные ниже code/build/runtime tasks имеют статус TODO до отдельного разрешения на реализацию. SPECIFIED в [реестре исправлений](docs/DESIGN_REPAIR_STATUS.md) означает только готовый проверяемый проект.

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

Переход к следующему этапу — после evidence всех обязательных подзадач ETH/BSC/POLY, обновления PROJECT_STATUS и commit. Optional/deferred задачи не используются ни как ложный PASS, ни как блокер текущего gate. Реализация не начинается без последующего указания пользователя.

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


## Готовность и следующее действие

Все 41 проектные находки специфицированы; DEP-04 и DOC-CONSOLIDATE завершены как документация. Runtime E01–E07 не выполнялся. Известные build/native integration/artifact риски перечислены в gates, включая полный Heimdall genesis payload proof, dependency/image locks, pin/marker coverage и power-loss tests. Исследование исходников не заменяет эти результаты.

Следующее действие после отдельного указания на реализацию: E01-01 — pinned builds, artifacts и минимальный native harness трёх сетей, затем E01-02 observation/recovery seams. При новой развилке поведения задать вопрос до зависимых изменений. Без такого указания остаёмся в проектировании и документации.
