# EVM Transaction Extractor — техническое задание v3, редакция Codex

> D36 (2026-10-21): основной сборщик только live. История — отдельный сервис на узле native ноды, явная команда N–M. Default три часа/start_block/apply-triggered history отменены. Единый интерфейс без зависимости от Kubernetes; подробности — [CONFIG_CONTRACT](docs/CONFIG_CONTRACT.md).

> D26–D31 (2026-10-16): [текущие требования доставки и восстановления](docs/DELIVERY_RECOVERY.md). Restart с сохранённым диском автоматический; после потери диска история задаётся заново N–M. WAL копится без TTL/прикладной квоты до подтверждения NATS всех сообщений/частей блока. После ACK payload и временные статусы очищаются; остаётся минимальное рабочее состояние для restart. Сейчас один NATS; HA/backup и дальнейшая судьба подтверждённых данных вне scope. Backend принят D33: RocksDB. E00-03.06 завершён как проект: [pins](docs/research/ROCKSDB_STACK.md), [протокол и REC-T01–16](docs/ROCKSDB_DELIVERY_PROTOCOL.md).

Актуализация 2026-10-16: D23–D25. Live/history — разные pods, команда N–M без согласования пересечений. Дедупликация и независимая проверка полноты блоков вне scope. Граница — получение native данных → WAL → NATS PubAck. [Текущий контракт доставки](docs/LIVE_HISTORY_DELIVERY.md) заменяет прежние coverage/overlap требования.

Дата редакции: 15 октября 2026 года.
Основа: evm-transaction-extractor-tz-v2.md от 7 октября 2026 года.
Статус: отдельная рабочая редакция; исходный файл v2 не изменяется.

Форма поставки: open-source система сбора, нормализации и публикации данных блокчейнов в кластер NATS.IO. Конечный пользователь выбирает поддерживаемые блокчейны и параметры загрузки через YAML (.yml) файлы. Лицензия и публичный repository ещё не выбраны.

Действующее уточнение D17: главный обязательный результат — все транзакции и их полные детали. Изменения нативных балансов вне транзакций — необязательное расширение; их можно не реализовывать, если они усложняют код. Все требования к SystemBalance* ниже действуют только при включённом расширении; оно не блокирует транзакционную поставку. Полная история всех изменений баланса при отключённом расширении не заявляется.

Имена новых сообщений и схема v3 ниже — предлагаемая конкретизация этого требования. Открытые решения и необходимые проверки перечислены в разделе 32; совместимость конкретных клиентов этой редакцией не подтверждается.

## 1. Цель

Разработать экстрактор, который получает данные из нативных клиентов и публикует в NATS JetStream все включённые в блоки транзакции со всеми применимыми вызовами, результатами и изменениями состояния. Для всех поддерживаемых семейств применяется единый строго типизированный контракт; необязательное расширение добавляет изменения балансов вне транзакций.

Действующая граница D16: унифицированные данные доставлены в кластер NATS.IO. Дальнейшее хранение, передача и обработка не входят в этот этап и не являются условиями приёмки. Сейчас завершается архитектурное ТЗ; реализация ещё не разрешена. Базовый транспорт — NATS JetStream; параметры надёжности и формат больших сообщений требуют фиксации.

Поддерживаемые семейства:

Первый контур подтверждён пользователем: Ethereum Mainnet, BSC Mainnet, Base Mainnet, Arbitrum One и Polygon PoS. Общая модель и точки нативного захвата проверяются на всех пяти сетях с начала разработки. Рекомендуемые клиенты/commits закреплены исследованием E00-13 в CLIENT_MATRIX; runtime-проверка и сборки ещё не выполнены. Base использует конкретный исследованный base/base + pinned base/reth profile. Avalanche и ZKsync остаются последующими направлениями, без включения в первую поставку.

1. Ethereum / Geth.
2. BSC и совместимые форки Geth.
3. Reth и производные, включая Base и OP Stack на соответствующих клиентах.
4. Arbitrum Nitro.
5. ZKsync Era и совместимые ZK Stack сети с EraVM.
6. Avalanche C-Chain.
7. Polygon PoS: клиент и historical profile фиксируются до реализации; совместимость с обычным Geth не предполагается.

Единицы полезных данных:

- Запись блока: идентичность сети и блока, header/parent metadata, нативный порядок, каноничность и готовность данных. Блок сохраняется также при отсутствии транзакций; проектирование транспортного представления block metadata — обязательная задача E02.

- TransactionRecord: одна включённая в цепочку транзакция, её receipt, logs, полное дерево внутренних вызовов и итоговый state diff, включая изменения нативных балансов.
- SystemBalanceRecord: одна нативная протокольная операция вне транзакции, изменившая нативные балансы, с итоговыми before/after по затронутым адресам и привязкой к блоку и порядку исполнения.

Приняты D17/D18: обычная транзакция — одно сообщение, крупная — несколько типизированных транспортных частей. Дополнительные block metadata и reorg/finality сообщения обязательны; SystemBalance* допустимы при реализации необязательного расширения. Промежуточные VM events не публикуются как готовые данные.

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
- хронология network → block → transaction inclusion → calls/logs/actions/state changes, включая пустые блоки; самостоятельные системные действия входят только в optional расширение;
- пользовательская YAML-конфигурация состава поддерживаемых сетей и параметров загрузки.

В выгрузку входят успешные и неуспешные транзакции, присутствующие в канонической цепочке. Не включаются mempool, симуляции, отклонённые кандидаты и транзакции блоков, которые ещё не получили необходимое каноническое подтверждение.

Операции протокола, действительно представленные транзакциями в родной сети, выгружаются независимо от того, кто их инициировал. Изменения вне транзакций — например, Ethereum withdrawals, награды блока и самостоятельные системные операции — не превращаются в искусственные транзакции.

При реализации расширения изменения нативных балансов вне транзакций публикуются как SystemBalanceRecord. Родная системная транзакция всегда относится к обязательному TransactionRecord, а не к optional потоку.

Результат — все транзакции согласованного диапазона и live с полными деталями. Полный журнал системного code/nonce/storage и весь consensus layer не входят. Покрытие балансов вне транзакций явно декларируется отдельно (§31).

## 3. Архитектура

Pipeline:

1. Native Node исполняет транзакции и протокольные операции.
2. Node Adapter собирает все транзакционные данные внутри нативного execution path; самостоятельные системные балансы — только при включённом расширении.
3. Adapter сохраняет завершённые транзакционные, block metadata и lifecycle записи и связывает их с включением и нативным порядком. При наличии расширения тем же путём проходят SystemBalanceRecord.
4. Adapter передаёт запись в Ingest Gateway через gRPC.
5. Gateway проверяет, сохраняет и публикует её в NATS JetStream.
6. Gateway завершает доставку события после подтверждения публикации JetStream; при согласованном разбиении — после подтверждения всех частей.
7. Приёмочные проверки считывают сообщения из NATS и сверяют с native fixtures. Это тест контракта, не компонент дальнейшей обработки.

Основные компоненты:

| Компонент | Ответственность |
|---|---|
| Native Node | Нативное исполнение и определение состояния цепочки |
| Adapter | Транзакция, внутренние вызовы, receipt, logs, state diff и протокольные изменения нативных балансов |
| Canonical Coordinator | Каноничность включений, reorg, finality и checkpoints |
| Adapter WAL | Восстановление и повторная доставка |
| Ingest Gateway | Валидация транспорта, учёт публикации, PubAck; blockchain dedup отсутствует |
| NATS JetStream | Конечная граница этапа: подтверждённая публикация унифицированных событий |

Canonical Coordinator может быть частью адаптера. Gateway обеспечивает единого издателя для потока конкретной сети.

Adapter собирает и нормализует данные; Gateway валидирует и публикует их в NATS. Между ними — versioned protobuf/gRPC contract.

WAL и publication journal — внутреннее состояние надёжной доставки до NATS, а не отдельный продукт хранения данных.

Каждая сеть имеет отдельные native node/adapter, WAL, publication journal и Gateway/publisher. Принято D19: общий кластер NATS, раздельные streams/subjects и лимиты. Нет межсетевых вызовов или ожидания прогресса соседней сети; изменение, сбой или restart одной не требует действий в остальных. Изоляция нагрузки проверяется; отказ общего NATS остаётся общим инфраструктурным событием.

Нормализация выполняется до публикации: адаптер переводит нативные значения в общую модель без потери семантики; Gateway проверяет схему, идентичности, размер и целостность. Он не исполняет EVM повторно. Сетевые отличия сохраняются в typed extensions.

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

Полная финальность не является обязательным условием первоначальной публикации: она отражается отдельным статусом.

SystemBalanceApplied публикуется после завершения родной операции, учёта rollback, подтверждения каноничности соответствующего включения и надёжного сохранения полной записи. Сам вызов balance hook не является основанием для публикации.

## 6. Данные транзакции

Полнота означает полный состав транзакций каждого обработанного блока: hash, index, родной type и число записей сверяются с клиентом. Включаются успешные, неуспешные и родные системные транзакции; internal calls остаются деталями родительской записи. Неизвестный тип или отсутствие обязательного поля не допускают молчаливого пропуска и объявления блока полным. Неприменимое поле отличается от неизвестного/потерянного значения. Mempool и невключённые кандидаты не входят в прежнюю границу.

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
| BlockApplied | Native metadata блока, включая пустой; не доказательство полноты |
| BlockRemoved / BlockStatusChanged | Отмена активации блока и typed finality evidence |
| SystemBalanceApplied | Полная запись протокольного изменения нативных балансов |
| SystemBalanceRemoved | Ранее опубликованная активация протокольного изменения отменена |
| SystemBalanceStatusChanged | Изменились статусы финальности протокольного изменения |

Прикладных событий начала, окончания и промежуточных этапов обработки блока нет.

Это ограничение касается промежуточных событий исполнения. Типизированные block metadata сообщения, включая пустые блоки, обязательны по D17; логическая схема определена в [MESSAGE_CONTRACT.md](docs/MESSAGE_CONTRACT.md), proto и fixtures завершаются в E02.

RecordPart используется только для передачи больших записей. Он не описывает процесс исполнения.

При реализации optional расширения протокольные изменения балансов вне транзакций передаются как SystemBalanceApplied/Removed/StatusChanged. Это не искусственные транзакции. Системный code/storage целиком отдельно не публикуется.

## 12. Идентификация, повторы и порядок

Идентификатор сети задаётся chain_id и genesis_hash; chain_key — удобное уникальное имя для subjects.

Идентификатор включения:

network + block_hash + transaction kind + transaction position + transaction_hash.

Для нестандартных транзакций используется native_position, если обычный EVM transaction index неприменим.

Transaction hash недостаточно: одна транзакция может после reorg оказаться в другом блоке.

Каждая публикация TransactionApplied получает activation_id в пределах producer/run. Разные live/history runs могут публиковать одно native включение с разными activation/event IDs; дедупликации по включению нет. TransactionRemoved и TransactionStatusChanged ссылаются на эту активацию. Повторное возвращение того же блока в каноническую цепочку создаёт новую активацию.

event_id идентифицирует конкретное событие доставки. Повторная отправка из WAL сохраняет event_id и исходные байты. Новое изменение жизненного цикла получает новый event_id.

source_sequence монотонен внутри writer_epoch. После потери состояния writer новая эпоха получает новый идентификатор.

Gateway присваивает chain_sequence в пределах publication_epoch своего store. Mapping event_id → publication_epoch/chain_sequence и точные frame bytes сохраняются Gateway и producer до публикации. При потере Gateway store pending события восстанавливаются из producer plan; новые события получают новый epoch.

Порядок chain_sequence отражает назначение публикаций, а не block number, физический NATS stream sequence или завершение доставки всех частей. Исторические записи могут публиковаться одновременно с live-данными; части разных событий могут чередоваться.

Для SystemBalanceRecord идентификатор включения: network + block_hash + operation_index. Он не содержит искусственного transaction_hash. Каждая новая каноническая активация получает activation_id; повторы доставки сохраняют его, event_id и исходные байты. Remove/status ссылаются на конкретную активацию.

ExecutionPosition.operation_index задаёт общий порядок транзакций и протокольных операций внутри блока. Порядок определяется нативным исполнением до фильтрации публикуемых записей; пропуски индексов допустимы и сами по себе не означают потерю данных. LIVE/SYNC/BACKFILL обязаны воспроизводить одинаковую нумерацию по закреплённому профилю клиента. Публикация из параллельных диапазонов не заменяет этот порядок.

В сетях с отложенным исполнением execution_block_hash/number задают блок фактического применения эффектов, если он отличается от блока исходного включения. Профиль сети фиксирует связь inclusion, execution и settlement, правила отмены и сверки; эта связь сохраняется в native metadata; независимая проверка полноты не добавляется.

## 13. Protobuf-протокол

Действующий логический контракт: [docs/MESSAGE_CONTRACT.md](docs/MESSAGE_CONTRACT.md). Он определяет Envelope, transaction/block metadata события, lifecycle, явное присутствие полей и транспорт CompleteRecord | RecordPart. Компилируемые proto, field numbers, bindings и validators создаются в E02; архитектурный документ не выдаётся за проверенную реализацию.

Прежняя заготовка сохранена в [архиве](docs/archive/PROTO_V3_PRE_E00_14.md), исключена из действующих требований. Обязательные для всех транзакций nonce/signature/gas и generic Any не используются.

## 14. Typed extensions

[Реестр native типов](docs/TRANSACTION_TYPE_REGISTRY.md) задаёт concrete payload variants и применимость полей Ethereum, BSC, Base, Nitro и Bor. Common record содержит closed oneof native payload; сетевые receipt/execution/finality данные получают зарегистрированные типизированные варианты. Неизвестные версии, types и forks запрещено молча пропускать.

Исходные native поля, derived execution projection, encoding kind и source hash сохраняются раздельно. Нельзя универсально выводить transaction hash из raw bytes: imported Arbitrum legacy имеет HashOverride и отдельное trie encoding. BSC ParliaSystem — классификация, а не новый wire type. Bor StateSync 0x7f — body transaction; legacy synthetic receipt имеет отдельный optional auxiliary профиль E00-15.

E00-15 описал Classic/legacy origins и ограничения. Точные execution/finality extensions, all-epoch Classic decoders и fork fixtures остаются задачами E01/E02/E04/E06. Будущие сети и старая история не считаются поддержанными из-за наличия общей Envelope.

## 15. Большие записи

Принято D18: транзакция, помещающаяся в согласованный лимит, публикуется одним сообщением; превышающая лимит — несколькими частями. Конкретный транспортный union, кодирование, лимиты и поля ниже уточняются в E02, само разрешение разбиения уже принято.

Порядок передачи:

1. Сформировать полную Envelope.
2. Один раз сериализовать её в protobuf.
3. Рассчитать SHA-256 исходных сериализованных байтов.
4. Если полная запись превышает лимит, разбить сериализованные байты на RecordPart; иначе передать полную запись одним сообщением.
5. Для частей использовать общий event_id, digest, total_size и part_count; part_index определяет позицию. Для целой записи сохраняется та же логическая идентичность.
6. Собрать и проверить полную запись на принимающей стороне.

Маленькая запись передаётся как TransportFrame.CompleteRecord; большая — как TransportFrame.RecordPart. Part layout и исходные Envelope bytes фиксируются для retry. SHA-256 относится к этим bytes, а не к повторной сериализации. Правила и проверки — MESSAGE_CONTRACT §6; proto создаётся в E02.

part_index начинается с нуля. Размер каждой сериализованной части, включая заголовки, должен укладываться в действующий лимит NATS max_payload; предел gRPC настраивается согласованно.

PrepareBatch возвращает publication_epoch/chain_sequence; producer сохраняет plan и точные frame bytes sync до PublishBatch. Gateway не меняет эти bytes при retry. Сама Envelope не изменяется.

В предлагаемом формате событие полно только при наличии всех частей и совпадении размера и SHA-256. Части не являются отдельными транзакциями. Сборку проверяет тест контракта; промышленная дальнейшая обработка не проектируется.

Запрещены молчаливое усечение input/output, удаление call frames, сокращение storage diff и публикация неполной записи как полной.

Дедупликационный идентификатор части в NATS: event_id + part_index.

## 16. Транспорт, WAL и ACK

На одном сервере предпочтителен gRPC через Unix Domain Socket. Между серверами — gRPC/TCP с mTLS.

Execution hooks выполняют сбор данных в ограниченные буферы. Сетевой I/O вынесен в отдельную задачу.

Adapter WAL хранит:

- полные исходные Envelope;
- event_id и digest;
- writer_epoch и sequence;
- канонические активации транзакций и протокольных изменений балансов, порядок исполнения и checkpoints;
- состояние подтверждения доставки.

Gateway надёжно хранит mapping event_id → publication_epoch/chain_sequence и состояние публикации; producer держит durable plan до ACK cleanup.

Подтверждение:

1. Adapter надёжно сохраняет событие.
2. Gateway получает и проверяет CompleteRecord либо все RecordPart.
3. Gateway резервирует publication plan; producer сохраняет plan/frame bytes, затем передаёт PublishBatch. Gateway сохраняет CLOSED перед отправкой.
4. Gateway публикует CompleteRecord либо части в JetStream.
5. JetStream возвращает PubAck для полной записи либо каждой части.
6. После PubAck всех сообщений/частей закрытого batch блока Gateway sync фиксирует NATS_ACKED и отправляет IngestAck(batch_id,digest,plan identity).
7. Adapter sync фиксирует ACKED_CLEANUP/release_outbox и очищает payload batch; после release handshake удаляются временные ACK metadata. Подробности — ROCKSDB_DELIVERY_PROTOCOL.

IngestAck означает подтверждённую публикацию всех подготовленных сообщений и частей закрытого batch блока в JetStream. Это конец доставки текущего этапа; не доказательство полноты исходного блока и не подтверждение downstream обработки. Дальнейшее хранение после PubAck вне scope.

Приёмка проверяет delivery ACK, типизацию, корректность нормализации выбранных fixtures и сборку parts. Независимый production verifier состава/хеша блока не требуется.

Модель доставки — at-least-once. Повторы имеют стабильные идентификаторы; физическая единственность сообщения не обещается. Окно дедупликации JetStream не является бессрочной гарантией. Дальнейшая обработка за пределами этапа.

При недоступности NATS сбор продолжается в RocksDB без TTL и прикладной квоты. RAM/in-flight ограничены; ожидающие данные остаются на диске. Физическая ошибка записи/исчерпание диска не даёт права объявлять WAL_SAVED или продвигать capture checkpoint; сообщается ERROR без удаления pending данных. Backpressure RAM/движка не подменяется политикой удаления по возрасту/размеру.

## 17. NATS subjects

| Subject | Payload |
|---|---|
| evm.v3.<chain>.tx.applied | Полная TransactionApplied в TransportFrame |
| evm.v3.<chain>.tx.removed | TransactionRemoved в TransportFrame |
| evm.v3.<chain>.tx.status | TransactionStatusChanged в TransportFrame |
| evm.v3.<chain>.balance.system.applied | SystemBalanceApplied в TransportFrame |
| evm.v3.<chain>.balance.system.removed | SystemBalanceRemoved в TransportFrame |
| evm.v3.<chain>.balance.system.status | SystemBalanceStatusChanged в TransportFrame |
| evm.v3.<chain>.block.applied | BlockApplied в TransportFrame |
| evm.v3.<chain>.block.removed | BlockRemoved в TransportFrame |
| evm.v3.<chain>.block.status | BlockStatusChanged в TransportFrame |

Примеры:

- evm.v3.ethereum-mainnet.tx.applied
- evm.v3.bsc-mainnet.tx.applied
- evm.v3.base-mainnet.tx.applied
- evm.v3.arbitrum-one.tx.applied
- evm.v3.polygon-pos.tx.applied

Рабочая схема — отдельный JetStream stream для subjects каждой сети, storage=file. Доставка подтверждается publish PubAck; consumer AckPolicy не является подтверждением публикации. Replicas=1 по D30; streams/credentials создаёт deployment, Gateway проверяет subjects/size и не управляет downstream retention.

Перечисленные subjects описывают transaction/block metadata/lifecycle и optional system события. Отсутствие транзакционных сообщений не является доказательством отсутствия блока: пустой блок публикует metadata, входящую в его локальный delivery batch.

Один активный publisher сети отвечает за назначение chain_sequence и fencing; parts могут чередоваться, поэтому физический порядок NATS не подменяет lifecycle references и blockchain chronology. Другие сети от этого publisher не зависят.

Ограничения размера, идентичность, WAL, ACK и at-least-once распространяются на все согласованные типы записей. Chunking и namespace v3 уточняются в финальном контракте; миграция внешних систем не входит в этап.

## 18. Reorg и finality

При reorg:

1. Определить старые включения транзакций и протокольных изменений балансов в отменяемой ветке.
2. Для ранее опубликованных включений выдать TransactionRemoved или SystemBalanceRemoved.
3. Отменять старую ветку от вершины вниз; внутри блока — по убыванию общего operation_index с учётом правил deferred execution профиля сети.
4. Выдать TransactionApplied и SystemBalanceApplied для новой ветки по возрастанию высоты и общего порядка исполнения.
5. Сохранить ссылки на активацию своего producer/run; parts могут чередоваться. Пересечение backfill/live и повторные наблюдения допустимы.
6. Выдать native BlockRemoved/BlockApplied observations своего producer. Проверка полноты и объединение повторных наблюдений принимающей стороной вне scope.

Транзакция, переехавшая в другой блок, получает новое включение и activation_id. Повторное возвращение того же включения после удаления также получает новую активацию.

Receipt status не меняется при reorg: меняется каноничность включения.

Finality хранится набором типизированных native evidence/stages, не произвольных строк. Детальные варианты и version gates закрепляются в E02 по MESSAGE_CONTRACT. Примеры:

| Семейство | Native stages |
|---|---|
| Ethereum | included, safe, finalized |
| BSC | included, native fast-finality stages |
| OP Stack / Base | unsafe, safe, finalized |
| Nitro | included и подтверждения L1/родной finality модели |
| ZKsync | included, committed, proven, executed, settlement_finalized |
| Avalanche | accepted и применимое подтверждение результатов settlement |

Названия и mapping закрепляются профилем сети. Число confirmations не подменяет native finality.

TransactionStatusChanged и SystemBalanceStatusChanged содержат полный актуальный набор стадий для activation_id. Возможные регрессии отражаются reached=false; нельзя предполагать монотонность промежуточных L1 статусов.

Для L2 завершение native стадии на текущей L1 ветке и финальность соответствующего L1 блока различаются. Evidence включает domain, block/batch number и hash, когда применимо.

## 19. Историческая выгрузка

### 19.1. Управление глубиной истории через YAML

D23: простая команда «загрузи блоки N…M» запускает отдельный history pod/job, в Compose — отдельный сервис/процесс. Диапазон включительный, N ≤ M. Пример проектного CLI: `extractor history --config network.yml --from N --to M`. Постоянный scheduler, UI и управление пересечениями не требуются.

Начальная история автоматически не загружается (D36). Live получает текущие данные; нужный диапазон N–M пользователь загружает отдельным history-сервисом на узле ноды. Настройки live не запускают историю.

### 19.2. Способы исполнения истории

History использует родной executor и необходимые historical state/inputs. Native snapshots/архивы проверяются по профилям CLIENT_MATRIX; RPC trace extractor не вводится. Запрещён общий writable datadir двух процессов. Classic — отдельный native runtime той же Arbitrum One.

Повтор диапазона и пересечение с live допускают повторные сообщения. Загрузчик не сверяет уже выгруженные диапазоны и не исключает дубли. Каждый запуск имеет run_id; retries сообщения сохраняют event_id/bytes.

Native отсутствие данных и ошибка нормализации дают явную ошибку, не скрытое сокращение N–M. Завершение — каждый запрошенный блок получен, локальный batch сохранён в WAL и все его сообщения/parts подтверждены NATS. Независимая проверка состава/хеша блока не выполняется. Ресурсы history ограничиваются отдельно; отсутствие влияния на live проверяется нагрузочными тестами.

Подробности стадий/учёта: [LIVE_HISTORY_DELIVERY.md](docs/LIVE_HISTORY_DELIVERY.md).

## 20. Ethereum / Geth

Source audit E00-13: рекомендуемый кандидат Geth v1.17.8, commit `a579077007b98217c3e253a66e4b452ca0c32b96`; Go directive 1.25.0, patch toolchain и consensus client ещё фиксируются. Pins и доказательства — [CLIENT_MATRIX](docs/CLIENT_MATRIX.md). Это проверка исходников, не выполненный runtime-тест. В этом релизе включённый tracer отключает parallel BAL path, что учитывается в benchmark.

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

Для заданного диапазона предлагается отдельный последовательный native replay от доступного parent state. Full sync с capture может подготовить полную историю, но не является обязательным объёмом экспорта при старте extractor. Snap sync не доказывает наличие предшествующих execution traces/state. Источники и ограничения — [исследование Ethereum/BSC](docs/research/ETHEREUM_BSC_CLIENTS.md).

Нужны корректные исторические receipts, authorization transactions, delegated code, precompiles и все применимые hard forks.

Балансовые hooks не являются готовым итоговым diff: адаптер учитывает rollback и границы транзакции. Самостоятельные операции вне транзакций, включая withdrawals/исторические награды, проверяются только при включённом optional расширении. Полнота транзакционных действий обязательна; наличие callback само по себе не доказывает её.

## 21. BSC и другие форки Geth

Source audit E00-13: кандидат BSC v1.7.8, commit `cdb7548b5baacfdae92f9f63437d6456411665f3`, Go directive 1.25.0. Релиз v1.8.0-alpha, возвращавшийся latest API, не рекомендован авторами для production. Parlia Finalize выполняет системные транзакции отдельно от обычного цикла и вызывает tracing hooks; оба пути обязательны. Geth/BSC имеют одинаковый Go module path, поэтому сетевые зависимости собираются отдельно. [Проверенные исходники](docs/research/ETHEREUM_BSC_CLIENTS.md).

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

### 21.1. Polygon PoS — самостоятельный профиль Bor/Heimdall

Source audit E00-13: Bor v2.10.2 / `9c445ef603e71469e1c176d8e57e7cf94864ade7` и Heimdall-v2 v0.12.2 / `ced0710a3f6e269b8d5e996eb7d2b740fd9f80c5`. Сборщик на Go в Bor; Heimdall — companion этой же сети. Effective Go version уточняется при сборке из-за различия manifest/release notes. [Проверенные источники](docs/research/ARBITRUM_POLYGON_CLIENTS.md).

Обязательны обычный execution и отдельный StateSync/Finalize path. После Madhugiri native StateSyncTx 0x7f входит последней в body; до перехода отдельный synthetic Bor receipt с derived hash не является body transaction. [E00-15 Polygon profile](docs/research/POLYGON_LEGACY_CAPTURE.md) определяет optional BorLegacyReceiptRecord, отдельный inventory и source-receipt-only scope. Он не доказывает полный capture system actions; отсутствие receipt не означает отсутствие действий. Optional публикация не отключает обязательное native исполнение state sync. Pinned Mainnet boundary — 80 084 800; fixtures проверяют обе стороны перехода.

Tracer исключает parallel V2 execution; capture overhead измеряется. Известное в коде попадание block-alloc upgrades в state-sync trace window требует проверки атрибуции, а не автоматического включения этих изменений в транзакционный diff. Для historical replay нужны native state/body и доступные исторические Heimdall inputs; современный snapshot сам этого не доказывает.

## 22. Reth / Base / OP Stack

Source audit E00-13 уточняет Base: кандидат base/base v1.5.0, commit `e0fe7fa0bef1dbeb27d1ccf05df0f8f9fa545832`, Rust 1.96.0, dependency base/reth `7134be3b51cf9d9504f988bff3266a23085f0bd9`. Нативные Base executor и receipt builder обязательны. Это source-verified рекомендация, а не принятая сборка. [Base profile](docs/research/BASE_CLIENT.md).

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

Для pinned Base необходимо включить Inspector в реальный node factory: default NoOpInspector/inspect=false не даёт capture. Per-tx result/state снимается до native commit, receipt — через родной builder. Не смешивать planned upgrades/тип в исходниках с активным Mainnet fork; отдельная карта типов и дат обязательна.

## 23. Arbitrum Nitro

Source audit E00-13: Nitro v3.12.1, commit `70fa99a6e711f09eedd96a907290c3df0f3bcf7d`, собственный go-ethereum `5eee8f546e7602b7deeef7909f291bbebc77e233`. Go адаптер, сборка включает Rust/cgo/C++/WASM. Live tracer в проверенном appendBlock вызывает повторное исполнение; нагрузку измерить. [Nitro/Classic profile](docs/research/ARBITRUM_POLYGON_CLIENTS.md).

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

Pinned Nitro boundary — 22 207 817. [Classic profile E00-15](docs/research/ARBITRUM_CLASSIC_CAPTURE.md): v1.4.7 / `dd4595f894b52c73a293a0c4bce9411afb12da12`, Go native reader + C++ AVM observer. Inventory строится по исходным persisted TxResult/BlockInfo, а не только по фильтрованной Ethereum projection. State/actions требуют наблюдения за исторической машиной без изменения bytecode/hash/gas/logs/sends; готовый trace недостаточен. Архив, observer и all-epoch decoders ещё требуют runtime-приёмки. Полная история остаётся требованием; этот gap нельзя скрыть обрезанием N–M. Classic и Nitro — профили одной Arbitrum One, со своим runtime и общим контуром публикации сети, без зависимости от Ethereum extractor.

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

Служебный ключ: network + producer/run + block number/hash. Один блок может встречаться в нескольких runs; это не ошибка.

Стадии: RECEIVED → WAL_SAVED → NATS_ACKED; ERROR сохраняет стадию и причину. WAL_SAVED выставляется после durable записи сообщений и закрытого локального списка references. NATS_ACKED — после PubAck всех этих сообщений/частей. Read/replay cursor не является delivery ACK.

Хранятся native identity/parent, mode/run, подготовленные event/part references, WAL offsets, cursor команды, ACK/error states. Gateway имеет свой publication journal сети. Выбран RocksDB 10.4.2: grocksdb v1.10.2, Rust rocksdb 0.24.0 / sys 0.17.3+10.4.2. Атомарность, sync, cleanup и восстановление определены в [ROCKSDB_DELIVERY_PROTOCOL](docs/ROCKSDB_DELIVERY_PROTOCOL.md); сборки ещё не выполнены. Общий writable файл между pods не используется.

Это учёт доставки, не индекс балансов, не dedup registry и не доказанное coverage. UI/API просмотра сейчас не проектируются. Полная модель: [STATE_OWNERSHIP.md](docs/STATE_OWNERSHIP.md), [LIVE_HISTORY_DELIVERY.md](docs/LIVE_HISTORY_DELIVERY.md).

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
- жизненный цикл activation_id для обоих видов записей;
- обязательность пар balance_before/after, уникальность адресов системной операции и соответствие execution_position сетевому профилю;
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
- служебный прогресс получения/WAL/PubAck по каждому producer/run;
- размер call trees и transaction records;
- количество протокольных операций и изменённых нативных балансов;
- служебный прогресс получения/WAL/PubAck по каждому producer/run;
- validation failures.

Числовые performance limits закрепляются до приёмки по итогам benchmark на согласованном hardware и fixtures; неподтверждённые универсальные TPS не используются.

## 28. Структура кодовой базы

| Каталог | Содержание |
|---|---|
| protocol/proto | Общая схема и typed extensions |
| gateway | gRPC, durable publication journal, NATS publisher |
| adapters/go-core | Calls, state/balance normalization, protobuf, WAL, transport |
| adapters/geth | Geth integration |
| adapters/bsc | BSC integration и compatibility shims |
| adapters/nitro | Nitro/ArbOS integration |
| adapters/avalanche | Исторический Coreth и современный C-Chain path |
| adapters/rust-core | Общие Rust protocol/WAL/transport components |
| adapters/reth | Inspector и ExEx integration |
| adapters/zksync | EraVM/MultiVM integration |
| backfill | Планирование диапазонов и native replay workers |
| fixtures | Эталонные транзакции и сценарии |
| docs | Клиенты, версии, границы поддержки и инструкции эксплуатации |

Общий код отделяется от client-specific integration. Для каждого клиента поддерживается минимальный набор изменений, закреплённый upstream commit и проверка совместимости обновлений.

## 29. Проверка и критерии приёмки

По D17 сценарии самостоятельных системных изменений обязательны только для включённого расширения. Транзакционные сценарии проверяют корректность native capture на fixtures разработки. По D25 production verifier полноты блока/балансов не входит в сборщик.

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
21. Протокольное начисление и списание вне транзакций; withdrawals и исторические награды в применимых сетях.
22. Блок без транзакций с протокольным изменением баланса.
23. Один адрес изменяется транзакцией и системной операцией в одном блоке; итоговый баланс и порядок корректны.
24. Комиссии и native protocol transactions не учитываются дважды.
25. Системное изменение отменено родным rollback и не попало в выгрузку.
26. Reorg/reinclusion системных операций и повтор доставки после сбоя.
27. Проверка actions и before/after на известных native fixtures, включая компенсирующие изменения.
28. Нулевой суммарный эффект по адресу за блок при ненулевых изменениях в разных операциях: обе записи сохранены.
29. LIVE/SYNC/BACKFILL дают одинаковые позиции и содержимое операций на одинаковой ветке.
30. Граница исторического hard fork, меняющего протокольные изменения балансов.

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
- прикладной поток содержит транзакционные события и события протокольных изменений нативных балансов с их жизненным циклом;
- все транзакции, включая failed/native system types, и их применимые эффекты представлены; изменения вне транзакций проверяются только для заявленного расширения;
- тесты нормализации actions и before/after на согласованных fixtures пройдены;
- служебный прогресс получения/WAL/PubAck по каждому producer/run;
- benchmark и ограничения покрытия документированы.
- унифицированные сообщения всех пяти сетей подтверждены JetStream и сверены тестовым считывателем с native fixtures;
- частичная запись связанных таблиц, повтор INSERT и reorg не дают ложного статуса готовности;
- согласованные native metadata, каноничность и порядок представлены в контракте;
- для подготовленных сообщений каждого полученного блока, включая пустые блоки, учтены WAL и PubAck;
- конфигурация пользователя включает/исключает поддерживаемые сети и задаёт глубину загрузки без изменения исходников;

## 30. Порядок реализации и результаты разработки

Порядок:

1. Уточнить варианты и клиентов Ethereum, BSC, Base, Arbitrum и Polygon; проверить минимальный нативный захват каждой сети, включая протокольные изменения балансов.
2. На примерах всех пяти сетей определить общую схему v3, transport/WAL, Gateway и сетевые extensions.
3. Реализовать общую доставку до NATS JetStream и адаптеры первого контура; для каждой сети проверить содержимое сообщений, reorg, finality и повторную публикацию после сбоя.
4. Выполнить native historical replay тестового диапазона каждой сети и проверить нормализацию fixtures и подтверждения доставки.
5. Принять первый контур только по результатам всех пяти сетей. Детальные задачи и стек фиксируются в docs/DEVELOPMENT_PLAN.md.
6. После первого контура — дополнительные сети и клиенты, включая Avalanche C-Chain и ZKsync Era, в отдельно согласованном порядке.

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
- служебный прогресс получения/WAL/PubAck по каждому producer/run;
- каталог поддержанных протокольных операций и воспроизводимые тесты нормализации.

Основной результат — полные строго типизированные транзакционные данные в NATS.IO, block metadata и lifecycle. Разбиение крупных транзакций принято; детали wire format уточняются. Последующая обработка не входит.

## 31. Полнота нативных балансов

### 31.1. Обязательная граница покрытия

Для каждой поддержанной сети обязательны все транзакции и их применимые balance actions/before/after в выбранном диапазоне и live. Сбор изменений вне транзакций необязателен по D17; допустимо отказаться от него при усложнении кода. Далее проверки самостоятельных системных операций относятся только к включённому расширению.

Нативный баланс — канонический баланс нативного актива адреса по модели конкретной сети. Профиль фиксирует актив, минимальную единицу, способ чтения и представление в state. Нельзя предполагать, что все runtime хранят его в том же account.balance, что Ethereum. ERC-20 balances, wrapped native tokens, consensus-layer validator balances и внешние UTXO сами по себе в это определение не входят. Родные atomic-транзакции и их изменения нативного баланса адресов поддерживаемой сети остаются обязательными.

Транзакционные изменения берутся из AccountChange. Для протокольных операций вне транзакций используется SystemBalanceRecord. Нельзя повторно публиковать транзакционные изменения в системном потоке или считать value каждого CallFrame самостоятельным переводом.

### 31.2. Гранулярность и rollback

Необходимо сохранять действия, вызвавшие изменения нативных балансов, и их причинную связь с транзакцией, внутренним вызовом или протокольной операцией. Итоговый before/after на границах транзакции или системной операции сохраняется дополнительно для сверки состояния; он не заменяет историю действий.

Если в одной успешной транзакции адрес получил 10 и отправил 10, обе причины изменения должны остаться в данных, несмотря на нулевой итоговый diff. Для одной завершённой операции сохраняется одна итоговая пара before/after адреса; отдельные действия внутри неё не должны теряться при свёртке. Транзакционная трассировка сохраняется полностью.

Изменение в успешно завершившемся дочернем вызове может быть отменено родительским revert: локальный outcome и итоговая применимость эффекта различаются. Отменённые действия остаются в трассировке с соответствующей семантикой и не участвуют в расчёте канонического баланса.

Действие не приравнивается автоматически к каждой временной записи native balance hook. Отображение gas debit/refund, fees, mint/burn и системных операций в действия определяется профилем клиента; нельзя вычислять переводы суммированием CallFrame.value. До утверждения протокола необходимо определить идентичность действия, причинную ссылку, execution position, применимость и адресные balance effects. Схема раздела 13 пока не содержит окончательной модели этих действий и должна быть уточнена в E02.

Сохранённые fee/nonce effects неуспешной транзакции учитываются по существующим требованиям. Системный поток не должен компенсировать ошибочно неполный транзакционный diff.

### 31.3. Захват и классификация

Каждый адаптер проверяет все транзакционные execution/state paths, включая родные системные транзакции и поддержанные исторические версии. Самостоятельные системные balance paths исследуются для optional расширения и не блокируют основную поставку.

При реализации optional расширения для каждой поддерживаемой системной операции профиль задаёт:

- operation_type и typed metadata причины;
- границы начала, завершения и rollback;
- привязку к блоку, порядок operation_index и native identifiers, если они существуют;
- источник balance_before/after и каноничности;
- live, sync и historical capture paths;
- правила finality, reinclusion и отложенного исполнения, если применимо.

Изменения code/storage, необходимые для нативного расчёта баланса, могут читаться и учитываться адаптером; это не расширяет публичный контракт до полного системного state diff. Нельзя приписывать изменение случайной соседней транзакции для удобства формата.

Неизвестный транзакционный тип/путь даёт явную ошибку обработки блока. Неизвестная операция необязательного системного расширения не блокирует транзакции при отключённом расширении. Успешная доставка не объявляется доказательством полноты.

### 31.4. Проверка нормализации

На разработческих fixtures проверяются before/after, actions, fees, rollback и сохранение +10/−10 при нулевом итоге. Это не обязательный production block/balance completeness verifier. При известной ошибке получения/декодирования запись не считается успешно доставленной; никаких выводов о полноте диапазона по NATS_ACKED.

### 31.5. Граница собранных наблюдений

История диапазона не задаёт начальных балансов ненаблюдавшихся адресов. Доступное исходное состояние необходимо нативному исполнению; его наличие проверяется отдельно.

По решению D10 полный начальный balance snapshot всех адресов не входит в обязательный старт. Собираются действия выбранного периода и live с родными before/after затронутых адресов. Для адреса без наблюдений в покрытом диапазоне абсолютный баланс может оставаться неизвестным; отсутствие строки не кодируется как ноль. Это не ограничивает корректное родное исполнение, которому необходимо доступное входное состояние.

Публикуемые наблюдения содержат точные before/after и границы исполнения. Вычисление представлений после NATS не проектируется.

Отсутствие наблюдений об адресе не кодируется как нулевой баланс. Конец блока отличается от конца последней транзакции, если после неё есть системные действия.

## 32. Открытые решения и следующие необходимые шаги

E00-03.01–07 завершены как архитектурные документы: [итоговая сверка](docs/E00_03_ARCHITECTURE_REVIEW.md). Это не завершение всего ТЗ. Следующий раунд — E00-07/E00-09: .yml, reload, автоматическая стартовая история и её связь с N–M. Отдельно остаются критерии приёмки E00-04, процесс публикации/лицензия E00-05/08 и финальный пакет E00-11. Реализация требует отдельного указания.

По D17 обязательны все транзакции и их применимые детали; вне-транзакционные балансовые изменения optional. E00-13 закрепил source pins, E00-14 — реестр типов и логический контракт. Следующие детали требуют проверки или решения; проект схемы не означает готовые proto, реализацию или runtime-приёмку.

| Приоритет | Что необходимо сделать | Проверяемый результат |
|---|---|---|
| До реализации | Для Ethereum, BSC, Base, Arbitrum и Polygon зафиксировать точную сеть, клиент/commit, диапазон, hardware и наличие historical state | Матрица поддержки первого контура из пяти сетей; отдельные доказательства capture каждого клиента |
| До реализации | Проверить все пути изменения нативных балансов выбранного клиента | Карта hooks/journal/system operations, список пробелов и нужных изменений клиента |
| До реализации | Утвердить гранулярность и нумерацию нативных операций | Fixtures со смешанным порядком tx/system, одинаковые позиции при replay |
| Выполнено в ТЗ | Native lifecycle bridge каждого producer/run, RocksDB и Gateway | Владельцы состояния, sync/READY/ACK cleanup, writer ownership и REC-T01–16 описаны; overlap разрешён без дедупликации |
| До реализации | Определить публикацию metadata, появляющихся после первой записи | Правила ожидания или typed updates для batch/settlement; запрет молчаливой неполноты |
| До реализации доставки | Реализовать принятый протокол .06 | WAL без TTL/квоты, cleanup после ACK, один NATS; REC-T01–16 и размеры transport/config проверяются в E03 |
| До финализации доставки | Завершить принятое разбиение D18 | Точный размер, typed transport union, IDs частей, digest, ACK и тестовый валидатор |
| До финализации доставки | Завершить строгий контракт D17 | Общие tx/block metadata/lifecycle типы, optional SystemBalance, registered extensions и валидаторы |
| До приёмки | Уточнить большие записи и постоянные ошибки | Ограничение памяти, дисковый spool, политика остановки и исправления без пропусков |
| До приёмки | Завершить протокол и валидаторы | Форматы IDs, registered extensions, правила неизвестных типов, compiled bindings, совместимость обновлений |
| До приёмки | Зафиксировать fixtures и численные пороги | Корректность нормализации, crash/reorg/retry и benchmark; независимый production oracle полноты вне scope |

E00-15 завершён как исследование исторических профилей, E00-03 — как архитектурный блок. Будущие нативные прототипы всех пяти клиентов, включая Classic POC, относятся к E01 после завершения согласования ТЗ и отдельного разрешения на код: ordinary/failed tx, native system types, fee effects, повторная доставка и reorg. Их результатом должны стать доказательства capture и карта historical/system paths; один Geth-прототип не заменяет эту проверку. Сейчас эти работы не выполняются.

Для остальных клиентов оценка сроков выполняется после аналогичной проверки точек захвата и доступности исторического состояния. Происхождение от Geth и наличие balance hooks не заменяют этой проверки.

## 33. Конечная граница: NATS.IO

D16 заменяет прежнюю границу поставки. Система собирает исходные данные, нормализует их и публикует в кластер NATS.IO. После подтверждённой публикации ответственность данного этапа заканчивается. Промышленная обработка опубликованных событий не разрабатывается.

Проверка результата: native fixture → нормализованная запись → NATS → тестовое считывание. Сверяются обязательные поля, сохранность действий, ошибки/revert, extensions, lifecycle, идентичности повторов и целостность согласованного формата. PubAck сам по себе не доказывает корректности содержимого.

## 34. Хронология в сообщениях

Сохраняются network, block hash/parent/height, native transaction position и порядок действий; producer/run и event ID описывают получение/передачу. Порядок поступления live/history не равен blockchain chronology, повторные включения допустимы.

Block metadata, включая пустой блок, и native reorg/finality observations сохраняются. Metadata не выдаётся за собственную проверку полноты. BlockComplete/CoverageUpdated исключены D25; стадии WAL/PubAck — внутренний учёт.

Дедупликация, вычисление/проверка хешей принимающей стороной и дальнейшее хранение не проектируются.

## 35. Open-source и пользовательская YAML-конфигурация

Проект разрабатывается как open-source продукт для самостоятельного развёртывания. Поставка включает исходники, воспроизводимую сборку, инструкции запуска, примеры .yml, схему сообщений и матрицу поддержанных сетей/диапазонов. Лицензия и публичный repository выбираются до публикации.

По D21/D22 первая поставка включает готовые конфигурации Docker Compose для разработки/локального запуска и Kubernetes для production. NATS.IO разворачивается внутри каждой из этих сред; внешний готовый production-кластер не является обязательным предусловием. Технический baseline — официальный NATS Helm chart с проектными values в Kubernetes и официальный server image с volume в Compose, JetStream включён явно. Replicas=1 по D30, recovery определён в .06, post-ACK retention вне scope. Resource sizing проверяется при реализации и не означает HA.

Стек/версии-кандидаты общих компонентов: [COMPONENT_STACK](docs/COMPONENT_STACK.md); logical ownership lifecycle, IDs, journals и ACK: [STATE_OWNERSHIP](docs/STATE_OWNERSHIP.md). Native clients не переводятся на общий Go/Rust toolchain ради Gateway SDK. Эти документы не являются отчётом о сборке или развёртывании.

Конечный пользователь через YAML (.yml) выбирает поддержанные блокчейны и параметры каждого независимого сборщика: network identity, профиль клиента, локальные подключения, роли live/history и назначение NATS; диапазон истории задаётся отдельной командой. Схема и defaults утверждаются в CFG-02.

Выбор сети из готовых адаптеров не требует изменения кода. YAML не заменяет разработку адаптера для неподдержанного runtime. Неизвестные сети, несовместимые профили и невалидные параметры должны выдавать понятную ошибку с указанием проблемного поля.

По D36 управление едино для всех сетей и независимо от Kubernetes/Docker API: основной сервис только live, отдельный history-сервис на том же узле получает явные N–M. Start_block/reload/apply-triggered история исключены; остальные настройки применяются рестартом соответствующего процесса. Другие сети, Gateway и NATS не переконфигурируются. Точная schema/CLI и граница live restart — [CONFIG_CONTRACT](docs/CONFIG_CONTRACT.md).

Публичные примеры не содержат рабочих секретов или персональных путей; способ передачи credentials выбирается в рамках конфигурационного контракта. Публикация repository/release не выполняется самой записью этого требования.
