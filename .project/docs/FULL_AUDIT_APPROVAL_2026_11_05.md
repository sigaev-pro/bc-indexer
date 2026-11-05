# Полный аудит и пакет согласования — 2026-11-05

DOC-FULL-AUDIT-01 / D71. Проверенная редакция: `61afb7a`. [Краткое ТЗ](../../TZ_DECISION_MAKER.md), [подробное ТЗ](../../TZ_EXECUTOR.md), нормативные тематические документы, решения и словарь проверены до границы D70: предусмотренные данные в стандартизированном формате, подтверждённо доставленные в NATS JetStream. D64: исключительно документация, без реализации.

**Текущий статус D79: все 14 находок F01–F14 согласованы и исправлены в документации (DOC_FIXED). Ожидающих согласования предложений нет.** Доказательства исходных дефектов и номера строк ниже относятся к указанному audit commit. Архивные исследования и прежние решения не считаются конкурирующими действующими требованиями. DOC_FIXED не означает runtime PASS.

**Согласование завершено:** D73–D78 закрыли F11/F03/F12/F13/F01, D79 утвердил весь оставшийся пакет.

## Результат

Подтверждены **14 групп замечаний**: 10 ранее известных перепроверены; 4 новых — F11–F14. Это реестр подтверждённых находок, не обещание отсутствия любых неизвестных ошибок. F08/F09 — опасные для чтения формулировки, но более точные разделы уже задают безопасное поведение; они не объявляются доказанными дефектами самого протокола.

Все 14 групп исправлены в рамках D64/D70; расширение результата за NATS и реализация не добавлены.

| Приоритет | Что он означает в этом отчёте |
|---|---|
| P1 | Нельзя считать контракт данных/приёмки однозначно завершённым без решения |
| P2 | Существенная несогласованность правил, зависимостей или проверок |
| P3 | Редактура либо точность происхождения доказательств; наблюдаемый runtime-сбой не установлен |

## Реестр и конкретные исправления

### F01 · P1 · Относительный путь разрешён контрактом и запрещён AC

**DOC_FIXED / D78:** из отрицательного AC-OPS-01-01 убран общий запрет relative paths. AC-OPS-01-04 проверяет storage.live_dir/storage.gateway_dir относительно каталога одного YAML при разных cwd; AC-OPS-01-05 отдельно отклоняет относительный finality.polygon.sources_file. LIVE_CONFIG_SPEC уточняет базу путей, field-specific исключение и отсутствие создания каталогов/DB при check. Mapping E06-07.yaml и counts обоих ТЗ синхронизированы; проверки документации — PROJECT_STATUS, runtime NOT_RUN.

**Доказательство:** `TZ_EXECUTOR.md:1222` разрешает relative paths относительно YAML; `:1973` / AC-OPS-01-01 требует field error для варианта `relative path`. `:1810` требует проверять все перечисленные варианты. Специальное absolute-only поле Polygon sources_file задано отдельно.

**Контрпример:** `storage.live_dir: ./wal` одновременно должен пройти и провалить config check.

**Принятое изменение D78:** убрать общий relative-path вариант из отрицательного AC-OPS-01-01. Добавить отдельные AC: допустимый относительный storage path разрешается относительно директории YAML, независимо от cwd; относительный `finality.polygon.sources_file` отклоняется как нарушение конкретного absolute-only ограничения. Никаких writable store/stream при config check.

**Затрагивает:** REQ-OPS-01; LIVE_CONFIG_SPEC, ACCEPTANCE_CASES, TASK_DECOMPOSITION; owner `E06-07.yaml`. При добавлении AC не переиспользовать существующие IDs и обновить число сценариев во всех представлениях.

### F02 · P2 · Batch назван пакетом блока, хотя выделяется на native decision

**DOC_FIXED / D79: один public native decision — один полный batch, multi-block reorg/finality-only и no-events checkpoint определены в MESSAGE_CONTRACT/NATIVE_CAPTURE_PROTOCOL/DELIVERY_STATE_MACHINE. Удалены block-batch ограничения; добавлены AC-CAP-08-02 и AC-DLV-02-04…05.**

**Доказательство:** `TZ_EXECUTOR.md:560`, `:605` против `:858`, `:864`, `:940`, `:1070`. Native decision может быть много-блочным reorg либо finality-only событием; автомат атомарно связывает decision с batch_id.

**Контрпример:** перестройку двух старых блоков на два новых один исполнитель делит на четыре block batches, другой — на один decision batch. Границы ACK и recovery становятся разными.

**Принятая формулировка D79:** «Native decision с публичными событиями порождает один delivery batch, содержащий полный набор его semantic descriptors. Decision без публичных событий не выделяет batch. Batch может содержать события нескольких блоков либо только статусы. Большие списки и payload хранятся фрагментами; лимит WriteBatch не делит semantic decision на другие delivery batches». Заменить неточные block-batch формулировки, сохранив атомарность closing marker.

**Приёмка:** crash внутри построения много-блочного reorg; повтор marker даёт прежний batch_id и состав событий. Finality-only создаёт один соответствующий batch, empty decision — только decision checkpoint. REQ-CAP-08/DLV-02; `E02-01.semantic_descriptors`, `E05-02.reorg`; дополнительные AC назначаются точному owner в соответствующем этапе.

### F03 · P1 · Не завершены общий порядок действий и публичная область отката

**DOC_FIXED / D74:** пользователь принял модель ниже. В CURRENT_SEMANTICS/MESSAGE_CONTRACT закреплены единый execution_order и допустимая пара opcode/projection, независимые body/protocol/effect indices, RollbackScope tree/ref/presence, scope 0 против root execution, reuse native IDs, ancestor rollback и сохранённые fees/nonce/authorization. Native capture фиксирует chronology и scope identity до worker; recovery не удваивает логические операции. Добавлены AC-DAT-04-02, AC-DAT-05-03…07, AC-DAT-06-03…04, E02/E04 task mapping. Оба ТЗ и нормативные зеркала синхронизированы; проверки — PROJECT_STATUS. Runtime NOT_RUN.

**Доказательство:** `TZ_EXECUTOR.md:539`, `:543`, `:603`, `:696`, `:702`, `:880`, `:1068`. У frames, execution actions и balance actions есть execution_order, но нет единого правила сравнения этих списков и совпадающих значений. `rollback_scope?` не имеет публичного типа и объекта ссылки. Тип/область frame/action IDs уже определены — их отсутствие не заявляется.

**Контрпример:** GAS_PURCHASE, вход root frame, SELFDESTRUCT и его денежная проекция получают общий либо три независимых счётчика. Оба варианта сохраняют порядок внутри списков, но дают разные сравнения между ними. Native snapshot ID может переиспользоваться; без нормализованной области ссылки неоднозначны.

**Принятая модель — D74:**

1. `execution_order` — один checked u64 счётчик наблюдаемых операций в пределах транзакции, начиная с 1. Field chunks, техническое сохранение и транспортные retries его не увеличивают. CallFrame получает позицию входа; отдельная native операция — следующую позицию.
2. SELFDESTRUCT ExecutionAction и его денежная проекция имеют одинаковый execution_order как два представления одной операции. Такое совпадение допустимо только по явной cause-ссылке; оно не означает два денежных движения. Финальный burn — отдельная поздняя операция с собственным order и related action refs.
3. `action_index` и `execution_action_id` остаются собственными tx-local идентификаторами; `effect_index` задаёт порядок эффектов внутри одной BalanceAction. Идентификатор не подменяет общий execution_order.
4. Для текущих body-транзакций `execution_position.operation_index` равен native body position; `TransactionProtocol.operation_index` — отдельный tx-local номер протокольной операции. Области сравнения этих полей описываются явно. Для включаемого позднее outside-tx профиля порядок задаётся его extension contract до признания профиля supported.
5. Публикуется список `RollbackScope{scope_id:u64,parent_scope_id?:u64,owner_frame_id?:u64,rolled_back:bool}`. Scope 0 — транзакционная граница; остальные уникальные tx-local IDs соответствуют наблюдённым native snapshot/revert scopes, а не копируют потенциально повторно используемый native ID. Ссылка BalanceAction.rollback_scope указывает на существующую область; техническое восстановление journal не создаёт новых денежных actions. Отсутствие native scope не выдумывается ради failed frame без эффектов.
6. Транзакционные fees/nonce и пережившие root revert authorization changes остаются в соответствующей неотменённой области. Reverted ancestor делает вложенные действия неcommitted; public scopes не заменяют итоговый effects_committed.

**Приёмка:** разные scopes при повторном native snapshot ID, nested success→ancestor revert, fees/authorization при root revert, zero-balance SELFDESTRUCT и денежная проекция, стабильный порядок после capture recovery. REQ-DAT-04/05/06/08; `E02-05.actions_model`, `E04-01.calls`, `E04-03.balance_actions`. Принятые правила включены в ТЗ; исполнение сценариев не выполнялось.

### F04 · P2 · Evidence не связывает все требуемые результаты приёмки

**DOC_FIXED / D79: CaseRegistry/CaseResult/RunManifest v2 связывают AC/network/variant/task/fixture и индивидуальные outcomes. Applicability отделена от verdict, исключения требуют rules, NOT_RUN не выдумывает actual outputs. AC-DEV-04-02 и AC-DEV-05-02…03; gates проверяют полный реестр.**

**Доказательство:** `TZ_EXECUTOR.md:1810` требует отдельный результат `(AC ID, network, variant)` и NOT_APPLICABLE/NOT_ENABLED. Обязательный manifest `:2088–2095` не содержит AC/variant identity; его enum содержит только PASS/FAIL/NOT_RUN/DEFERRED. `:2315` обещает связь с AC.

**Принятое изменение D79:** раздельно определить applicability и execution verdict. `CaseResult{ac_id,network,variant_id,task_id,fixture_id,fixture_version,applicability,verdict?,run_manifest_ref?,reason?}`. Applicability=`APPLICABLE|NOT_APPLICABLE|NOT_ENABLED|DEFERRED`; verdict=`NOT_RUN|PASS|FAIL` только для APPLICABLE. PASS/FAIL требуют ссылку на полный run evidence; NOT_RUN не требует вымышленных actual output/timestamps. Исключения требуют обоснования и правила профиля/решения. Manifest содержит точный список case identities; gate проверяет полный реестр обязательных variants, а не количество файлов manifest.

**Приёмка:** два варианта одной AC с разными исходами; отсутствие одного результата не превращается в PASS; отключённый optional не блокирует обязательный gate и не маскирует обязательный native путь. REQ-DEV-04/05; `E01-01.evidence`, `E07-09.requirement_verdict`.

### F05 · P2 · Два предписанных набора child task IDs

**DOC_FIXED / D79: TASK_DECOMPOSITION — единственный каталог 68 child IDs. Старые shorthand-предписания заменены exact E01 IDs; supporting outputs и сетевые slots сохранены. Нормативные ссылки проверены по каталогу.**

**Доказательство:** `TZ_EXECUTOR.md:2056–2058` требует `.build.ETH/BSC/POLY/.supporting/.artifacts` и `.ownership/.pin/.decision`; итоговый каталог `:2315–2326` использует `.builds/.evidence/.harness/.pins/.inputs/.owned_capture/.decision_bridge/...` и отдельные сетевые slots.

**Принятое изменение D79:** единственным реестром child IDs назначить текущий TASK_DECOMPOSITION. В IMPLEMENTATION_GATES заменить старое предписание точным перечнем этих IDs; supporting artifacts оставить обязательным выходом `.builds/.harness`, а сетевые варианты — applicability slots. Не создавать второй параллельный набор задач. Сохранить parent IDs и 68 существующих child IDs, если отдельное согласованное исправление не требует новой задачи.

**Приёмка:** каждое нормативное упоминание дочерней задачи разрешается в один каталог; нет альтернативных сокращённых предписаний. REQ-DEV-01/03.

### F06 · P2 · Карта доступа не содержит обязательную связь Bor→L1

**DOC_FIXED / D79: добавлен polygon-bor→polygon-l1-geth:8545 в обе карты связей. Endpoint/TLS override требует той же supporting identity/role; основной eth-geth не замена. AC-OPS-07-02…03 назначены E06-13.network.**

**Доказательство:** observer внутри Bor читает supporting L1 по `TZ_EXECUTOR.md:765–771`, `:1243`; таблицы `:1383–1390`, `:1613–1621` показывают только Heimdall как клиента L1 RPC.

**Принятое изменение D79:** добавить `polygon-bor → polygon-l1-geth:8545` для hash-pinned finalized checkpoint reads. При override использовать согласованный endpoint/TLS; доступ остаётся внутренним. В network/config fixtures проверить корректную связь и запрет случайного направления к основному Ethereum extractor.

**Следствие:** без строки исполнитель схемы доступа может закрыть обязательный канал и остановить продвижение GC. Это не утверждение о существующем отказе Compose. REQ-LIF-07/OPS-07; `E05-01.polygon_observer`, `E06-13.network`.

### F07 · P2 · Краткое ТЗ не раскрывает весь обязательный state diff

**DOC_FIXED / D79: краткое ТЗ явно включает balance/nonce/code bytes/storage/existence до/после tx и отделяет сохранённый diff от попыток. Snapshot и глобальное outside-tx состояние не добавлены.**

**Доказательство:** таблица `TZ_DECISION_MAKER.md:19–33` не объясняет nonce/code/storage/account existence, обязательные в `TZ_EXECUTOR.md:545`, `:698`. Исходные события упомянуты в кратком документе, поэтому полное отсутствие logs не заявляется.

**Принятая строка краткой таблицы D79:** «Изменения состояния аккаунтов — баланс, nonce, код, storage и существование до/после транзакции; изменения кода передаются с его bytes. Это итоговое сохранённое состояние, отдельно от отменённых попыток». Уточнить, что полное системное состояние вне tx и начальный snapshot этим не обещаются.

**Приёмка:** обязательные категории подробного контракта представлены в кратком без добавления функций. REQ-DAT-07; поведение не меняется.

### F08 · P3 · Формулировка срока хранения ACK допускает неправильное прочтение

**DOC_FIXED / D79: оба текста сохраняют полный IngestAck/release_outbox до проверенного ReleaseAck и атомарного producer retired_prefix; payload cleanup их не удаляет. Протокол RecoveryRelease и существующая приёмка сохранены.**

**Доказательство:** `TZ_EXECUTOR.md:969`, `:1154` говорят «до cleanup», но таблица Release, RecoveryRelease и AC-DLV-06-04 требуют пережить удаление payload.

**Принятая замена D79:** «Полный IngestAck и release_outbox сохраняются до проверенного ReleaseAck и атомарного продвижения producer retired_prefix. Payload cleanup их не удаляет». Уточнить оба места; автомат не менять.

**Приёмка:** crash после удаления payload и потеря Gateway store позволяют RecoveryRelease по сохранённому ACK. REQ-DLV-06/07; `E03-05.recovery_matrix`.

### F09 · P3 · Правило immutable bytes не различает Envelope и frame

**DOC_FIXED / D79: immutable READY относится к event identity/RecordFragment bytes; SEALED дополнительно фиксирует reservation/layout/headers/frames. Только существующий pre-SEALED recovery может менять неопубликованный transport plan.**

**Доказательство:** `TZ_EXECUTOR.md:967` запрещает менять «любые bytes» под event_id, тогда как `:977`, `:1924` допускают новый неопубликованный plan после потери Gateway до SEALED.

**Принятая замена D79:** «После READY неизменны логический payload/его зафиксированное кодирование и event identity; после SEALED — также reservation, layout, headers и frames. Перепланирование допустимо только в явно указанном pre-SEALED recovery». При принятии F11 термин payload encoding должен относиться к новому record container, не возвращать ограничение одного Envelope.

**Приёмка:** old READY/new pre-SEALED plan допустим; изменение SEALED bytes запрещено. REQ-DLV-03/04.

### F10 · P3 · В текущем тексте остались старые scope и статусы

**DOC_FIXED / D79: из активного контракта удалены Classic/history и ничем не используемый Rust/tonic source list; принятые config/init/flags отделены от NOT_RUN evidence. Исторические материалы сохранены с явным статусом.**

**Доказательство:** Classic/AVM и live/history описаны как действующие на `TZ_EXECUTOR.md:558`, `:579`, `:601`, хотя они deferred. `:1195–1197`, `:1330–1332` говорят о неутверждённых настройках, уже заданных поздними таблицами. Отдельный список Rust/tonic источников не имеет текущей роли.

**Принятое изменение D79:** убрать эти предписания из текущего нормативного текста либо вынести в явно ненормативную историческую справку. Разделить «параметр принят» и «runtime-проверка не выполнена». Архивные документы не переписывать так, будто прежних решений не было.

**Приёмка:** поиск current-only текста не возвращает обязательный Classic/history/Rust путь или уже закрытый вопрос как незакрытый. D38/D42/D64/D70 сохраняются.

### F11 · P1 · Разбиение после сериализации не решает предел Protobuf

**DOC_FIXED / D73:** выбран вариант A. В [MESSAGE_CONTRACT](MESSAGE_CONTRACT.md) закреплены closed schema-validated TypedItem, BlobRef/BlobChunk, header/seal, exact tags/presence/order/digests и предел 65536 bytes на RecordFragment / 16384 bytes на BlobChunk.data. В [CONTROL_WIRE_CONTRACT](CONTROL_WIRE_CONTRACT.md) — v2 manifests/reservation/transport/header/control, fragment identity в MsgID/RPC/cursors и exact layout. Producer READY сохраняет отдельные bounded fragments; SEALED сохраняет transport bytes; полный ACK включает все части всех фрагментов. Логический registry по-прежнему закрепляется артефактами E02-02.schemas; монолитный Envelope wire удалён. Добавлены AC-DLV-04-05…12, mapping E02/E03, обновлены оба ТЗ и тематические зеркала. Проверки документации и неизменности v2 фиксируются в PROJECT_STATUS; runtime всех сценариев NOT_RUN.

**Доказательство:** `TZ_EXECUTOR.md:595–599`, `:1020–1026`, `:1111–1126`, `:1257` задаёт один Envelope на логическую запись, его immutable bytes и нарезку этих bytes на RecordPart. Итоговый event не ограничен размером технического chunk. При этом [официальный предел сериализованного Protobuf message — менее 2 GiB](https://protobuf.dev/programming-guides/proto-limits/#total-size-of-the-message). [Рекомендация для больших наборов](https://protobuf.dev/programming-guides/techniques/#large-data-sets) — последовательность отдельных сообщений.

**Контрпример:** нормализованный trace/список actions разрастается до Envelope ≥2 GiB. Все NATS frames могут быть маленькими и иметь правильные digests, но сборка обратно в одно protobuf-сообщение не удовлетворяет стандартному пределу. Disk spool решает RAM, а не допустимость единого encoded message. Наличие конкретной такой mainnet-транзакции не заявляется: ТЗ не задаёт и не доказывает верхнюю границу, исключающую этот случай.

**Принятый вариант A — D73:** логическая запись состоит из последовательности ограниченных **типизированных protobuf-сообщений до сериализации**, связанных event_id и фиксированным logical manifest. Большие bytes и коллекции разбиваются на уровне схемы; одно protobuf-сообщение не содержит неограниченный список/поле. Полные данные, closed variants, exact retry bytes и общий контроль целостности сохраняются.

**Конкретная рамка изменения wire contract:**

- `RecordHeader` фиксирует event identity, network/profile/schema, kind и descriptor логической записи.
- `TypedItem` — закрытые варианты items текущей схемы: frames/actions/effects/account changes/storage entries/logs/native payload items/evidence. Неограниченные nested lists становятся связанными items; крупные byte fields — typed BlobRef. Any/JSON map не вводятся.
- `BlobChunk` передаёт ограниченный фрагмент конкретного typed byte field с идентичностью, offset/length и проверкой целостности. Крупный отдельный item также не обходит ограничение.
- `RecordSeal` фиксирует количество элементов/bytes и digest детерминированной последовательности. Manifest chunks и closing marker не требуют монолитного списка в RAM или protobuf.
- Transport plan фиксирует каждый сериализованный фрагмент и его publication identity. Приёмочный декодер читает стандартные protobuf-сообщения по одному, не собирая гигантский Envelope.
- Точный registry/tag/layout/digest contract должен быть синхронно переписан во всех нормативных разделах перед объявлением F11 закрытым. Одной фразы «поддерживается streaming» недостаточно.

**Альтернатива B:** явно ограничить logical record размером ниже 2 GiB и останавливать ноду с ошибкой до публикации при превышении. Это сужает обещание обработки больших полных записей и требует отдельного принятия пользователем; молча выбрать этот вариант нельзя.

**Приёмка предлагаемого A:** логический event больше 2 GiB и одиночное поле больше лимита фрагмента; каждый protobuf-message допустим по размеру; порядок/дубли/corruption проверяются; рабочая память ограничена; immutable replay и all-parts ACK сохраняются. Это предписанный будущий fixture, не запускаемый сейчас тест. REQ-DAT-01/DLV-04/DLV-05/CAP-06; `E02-02.schemas`, `E02-02.codec`, `E03-03.publisher`, приёмка E07. Новые AC нужны, прежние IDs не переиспользовать.

### F12 · P1 · Не задан точный публичный receipt encoding registry

**DOC_FIXED / D75–D76:** в MESSAGE_CONTRACT закреплены SourceReceiptEncoding, enum values 1/2/3, native_type presence, consensus/storage origin applicability, typed prefix без outer RLP wrapper, exact native sources, общий Receipt отдельно от consensus bytes и closed network details. Native MarshalBinary — эталон; пользователь отдельно разрешил потоковую byte-equivalent адаптацию из native Receipt в spool, чтобы не нарушать F11 bounded memory. Optional raw Bor storage bytes не перекодируются; историческое имя registry явно заменено. Проверены pinned receipt.go Geth/BSC/Bor и Bor rawdb/bor_receipt.go. Добавлены AC-DAT-02-02…05, AC-DAT-09-03…04, E02/E04 mapping; оба ТЗ и нормативные зеркала синхронизированы. Проверки документации — PROJECT_STATUS; runtime NOT_RUN.

**Доказательство:** `TZ_EXECUTOR.md:533` требует `source_receipt_encoding` с encoding kind и closed ETH/BSC/Polygon details; конкретного enum кодирования нет. Bor source_log_scopes определены на `:535`; эквивалентная точная таблица применимости receipt details для остальных профилей отсутствует.

Это не косметика: pinned native clients имеют разные представления receipt. Consensus `MarshalBinary` и `ReceiptForStorage.EncodeRLP` не эквивалентны; storage-представление, например, не содержит Bloom. Проверены исходники [Geth](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/types/receipt.go), [BSC](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/types/receipt.go) и [Bor](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/types/receipt.go).

**Контрпример:** два исполнителя сохраняют consensus bytes и storage RLP соответственно под неопределённым `encoding kind`. Оба считают байты native, но один стандартный decoder не может интерпретировать их по фиксированному контракту.

**Принятый выбор D75, уточнение D76:** для обязательных body tx использовать native consensus encoding: `LEGACY_CONSENSUS_RLP` либо `TYPED_CONSENSUS_ENVELOPE` с исходным receipt type byte. Эталон — штатный pinned native codec, вывод допускает bounded byte-equivalent writer без реконструкции из наших нормализованных полей. Typed network details: ETH/BSC — явно зафиксированное отсутствие дополнительных полей первого профиля, Bor — определённые source_log_scopes; обязательные общие поля остаются в Receipt. Optional legacy Bor storage receipt имеет отдельный `BOR_LEGACY_STORAGE_RLP` и не выдаётся за consensus body receipt. Новые native extras требуют явного registry/profile update, не opaque map.

**Приёмка:** golden native bytes для legacy/typed/BSC system/Bor StateSync; storage RLP не принимается как consensus; zero/absence и исходные logs не изменяются; большой receipt не требует полного дополнительного буфера. [BSC receipt codec](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/types/receipt.go). REQ-DAT-01/02/09; `E02-10.closed_profiles`, `E04-01.receipts`. Контракт принят; native fixture/streaming writer не реализованы и не запускались.

### F13 · P2 · Проверка stream metadata обязательна, но её схема отсутствует

**DOC_FIXED / D77:** CONTROL_WIRE_CONTRACT задаёт два exact key/value, hex formats, выбор active/desired binding по фазе и CONFIG_BLOCKED при missing/malformed/mismatch. Bootstrap включает метки в CREATE/read-back, проверяет existing stream, не выполняет auto-adoption/retag и сохраняет чужие metadata при разрешённом compatible UPDATE. Runtime без admin authority. Новое target проверяется до SwitchPrepared/первого CommitSwitch; committed control retries сохраняют idempotency при NATS outage. Добавлены AC-DLV-09-05…08 и AC-OPS-06-04…06, task mapping; оба ТЗ и нормативные зеркала синхронизированы. Pinned NATS StreamConfig metadata map проверена по исходнику; runtime NOT_RUN, документальные проверки в PROJECT_STATUS.

**Доказательство:** `TZ_EXECUTOR.md:1001`, `:1545`, REQ-DLV-09 и AC-DLV-09-03 требуют сверять metadata с binding и блокировать mismatch. CONTROL_WIRE_CONTRACT определяет JSON DestinationBinding, но ни имена stream metadata keys, ни ожидаемые значения для сравнения не задаёт. E06-13.nats_bootstrap должен создать/проверить эти же неизвестные поля.

**Контрпример:** bootstrap пишет только network identity, Gateway ждёт descriptor digest; оба следуют слову metadata, но штатный startup оказывается CONFIG_BLOCKED. Или каждый проверяет произвольный неполный набор и считает чужую конфигурацию подходящей.

**Принятый минимальный контракт D77:** принадлежащие проекту stream metadata keys `extractor.network_digest` (64 lowercase hex по существующей network_digest формуле) и `extractor.destination_id` (Id16 lowercase hex). Оба обязательны и точно сравниваются с желаемым/сохранённым descriptor по фазе протокола. Missing/mismatch → CONFIG_BLOCKED; автоматический reset/purge запрещён. Посторонние metadata не используются как доказательство binding и не удаляются bootstrap. Account/stream/subject prefix, поколения и descriptor_digest проверяются отдельно по уже принятому control protocol. Переход в новый stream проверяет те же keys до активации назначения; смена одного prefix не создаёт скрытой необходимости менять stream metadata generation.

Если требуется хранить generation/binding digest именно в stream metadata, необходимо отдельно определить порядок их обновления при cutover и совместимость с другим producer. Такой расширенный вариант здесь не принят автоматически.

**Приёмка:** correct/missing/wrong metadata; изменение URL при прежней identity; новый stream до и после cutover; отсутствие административных UPDATE у runtime Gateway. REQ-DLV-09/OPS-06; `E03-03.destination`, `E06-13.nats_bootstrap`.

### F14 · P3 · Ссылка доказательства Geth types ведёт на прежний source pin

**DOC_FIXED / D79: ссылка Geth transaction types заменена на 9dffe6e976cbbb87a84c5fd395448f0950290def. Повторно прочитан pinned transaction.go: constants 0x00–0x04 подтверждены; fork activation/build не заявлены.**

**Доказательство:** `TZ_EXECUTOR.md:656` ссылается на `a579077007b98217c3e253a66e4b452ca0c32b96`, а текущий capture/bootstrap baseline закреплён на `9dffe6e976cbbb87a84c5fd395448f0950290def`. CLIENT_MATRIX явно объявляет прежнюю таблицу исторической.

Проверенный [выбранный source transaction.go](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/types/transaction.go) также содержит типы 0x00–0x04. Ошибка текущего списка типов не установлена; проблема — происхождение его доказательства.

**Принятое изменение D79:** заменить ссылку в действующем реестре/root на выбранный source; исторические ссылки оставить в исторических исследованиях. Не выдавать это за новую проверку сборки или fork activation.

**Приёмка:** текущие native source claims указывают на выбранный baseline либо явно называют другой pin как исторический. REQ-CAP-01/OPS-10.

## Итоговая сквозная проверка D79

Все 14 замечаний F01–F14 согласованы и исправлены в документации. Исходные доказательства/номера строк выше относятся к audit commit 61afb7a; отметки DOC_FIXED описывают результат правок. Прежние предложения не являются ожидающими ответа вопросами.

| Область | Итог |
|---|---|
| Scope/live/recovery | D64/D70 сохранены: только ТЗ; три mainnet, live H+1, без выгрузки простоя/production consumer |
| Capture/decision | Owned snapshots, static/dynamic pins и child-root/marker protocol сохранены; один native decision имеет один batch |
| Данные | Полные calls/actions/account diff; общий order/scopes; bounded typed fragments и exact native receipt registry |
| Lifecycle/finality | Removed/reinclusion/status, BSC evidence и Polygon L1 checkpoint GC не подменяются transport ACK |
| Delivery/cleanup | READY/SEALED границы точны; полный batch ACK, IngestAck до ReleaseAck, loss/retry/fencing и cutover сохранены |
| Окружение | Relative YAML paths и absolute-only Polygon field согласованы; Bor→supporting L1 включён; NATS identity metadata точны |
| Приёмка/задачи | 63 REQ / 195 AC / 68 child tasks / 7 gates; каждый AC назначен, applicability/variant evidence определены |
| Документы | Два адресных ТЗ и 15 нормативных зеркал синхронизированы; старый scope явно исторический |

Проверены REQ/AC/task mapping и stages, неизвестные IDs/dependencies и циклы, Markdown links/anchors/tables, совпадение зеркал и тела TZ_EXECUTOR с v3, git diff --check и неизменность исходного v2. Точные результаты последнего прогона — PROJECT_STATUS и FINAL_SPEC_REVIEW.

Точечный source review: предел Protobuf, receipt codecs pinned Geth/BSC/Bor, NATS metadata и текущие Geth transaction type constants. Это не сборка, не запуск нод, не crash test и не обновление всех зависимостей на последние релизы.

DOC_FIXED означает устранение документального дефекта. Runtime остаётся NOT_RUN. Закрытие этого реестра не является доказательством отсутствия любого будущего контрпримера, наличия реализации или production-готовности. Открытых решений по согласованному пакету нет; повторный вопрос требуется только при новом существенном контрпримере/изменении требований.
