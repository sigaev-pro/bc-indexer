# CFG-S04 — конфигурация live/Gateway

> Нормативные effective native configs, mounts/UID, trust bootstrap, peer role/network admission и startup — [DEPLOYMENT_BOOTSTRAP_CONTRACT](DEPLOYMENT_BOOTSTRAP_CONTRACT.md). Для поставляемого окружения credentials/TLS обязательны по этому профилю, даже если общий YAML допускает иные явно выбранные transport modes.

> D52: операционная готовность/синхронизация наблюдается по логам; управление штатными средствами Docker/Kubernetes, без отдельного API и обязательной детализации probes/shutdown таймаутов. Native граница H+1 и сохранение pending остаются. Default размера frame — 2 MiB, правила ниже.

> D51: штатные native configs можно заменить целиком и дополнить аргументами. Окружение выполняет отдельный init-шаг, первый старт создаёт необходимые native файлы, рестарт сохраняет state/ключи. Исходники `/config` read-only; изменяемые native файлы находятся в `/data/node`. Это подготовка окружения, не новая команда CLI сборщика. Точные filenames/init/mount mappings заданы в [DEPLOYMENT_BOOTSTRAP_CONTRACT](DEPLOYMENT_BOOTSTRAP_CONTRACT.md); их runtime-проверки ещё не выполнены.

> D50: [таблица endpoints и экспонирования](NETWORK_PORTS.md) закреплена по поручению пользователя. P2P overrides задаются native конфигурацией/окружением, не новыми произвольными ключами YAML сборщика. Gateway каждой сети использует внутренний service DNS и порт 50051, NATS — внутренний 4222. Значения настраиваемы; P2P NodePort не затрагивает приложение и не открывает его API наружу.

> D49: defaults deployment — Gateway `50051/TCP`, WAL `/data/wal`, журнал Gateway `/data/journal`, configs `/config` и secrets `/run/secrets` только для чтения. Процессы имеют собственные volumes; пути/порты переопределяемы. RPC/Engine API/Gateway/NATS не публикуются наружу по умолчанию. Это значения шаблонов окружения, не отмена обязательных полей схемы YAML. [Полный контракт defaults](NODE_DEPLOYMENT_SPEC.md).

> D48: каждая сеть имеет собственный YAML сборщика и отдельный YAML Gateway. Штатные native configs и дополнительные аргументы передаются окружением, не включаются в схему сборщика. Образы, порты/P2P, каталоги, ресурсы и размещение доступны через Compose overrides/Kubernetes values. Секреты отдельными файлами; изменения применяются перезапуском компонента. Проект предоставляет defaults для трёх сетей; значения закреплены в [NODE_DEPLOYMENT_SPEC](NODE_DEPLOYMENT_SPEC.md), native flags/init — в [DEPLOYMENT_BOOTSTRAP_CONTRACT](DEPLOYMENT_BOOTSTRAP_CONTRACT.md). Создание файлов окружения и runtime-проверка остаются результатами описанных задач E01/E06.

> D46: endpoints используют service DNS, configs отделены от секретов, JWT у Geth/Prysm пар различаются. Каждый компонент — StatefulSet replicas=1 со своими Service/PVC, в Compose — сервис с named volumes. Готовность публикации не блокирует синхронизацию/capture и запись в WAL. Кандидаты версий и native init/config/flags заданы в [DEPLOYMENT_BOOTSTRAP_CONTRACT](DEPLOYMENT_BOOTSTRAP_CONTRACT.md); build/runtime proof не выполнен. Отдельные probes/shutdown таймауты не требуются по D52.

> D45: каждый самостоятельный компонент — отдельный Compose service и Kubernetes workload/Pod; адаптер остаётся встроенным в native клиент. Базовые связи adapter→Gateway идут по конфигурируемым сетевым endpoints (TCP/gRPC+mTLS), без обязательного общего хоста/UDS. Поддержка UDS в схеме не является требованием совместного размещения. Карта компонентов — [NODE_DEPLOYMENT_SPEC](NODE_DEPLOYMENT_SPEC.md).

> D43: проект компонует и настраивает полный комплект нод для Compose/Kubernetes, включая supporting clients. «Запускает окружение» описывает механизм запуска, а не внешнюю ответственность. Native configs, связи и YAML адаптера входят в наш технический комплект: [NODE_DEPLOYMENT_SPEC](NODE_DEPLOYMENT_SPEC.md).

По D42 текущие сети — Ethereum, BSC и Polygon PoS. Base/Arbitrum отложены; их YAML-профили текущая поставка отвергает как неподдерживаемые. Исследования сохраняются справочно.

2026-10-21. Технический проект для текущего scope D38. Заменяет активную часть [предыдущего draft](CONFIG_CLI_DRAFT.md). Команды и поля — проектные интерфейсы, ещё не реализованы. История и загрузка пропусков не входят в этот документ.

## Команды и исполнение

| Команда | Назначение |
|---|---|
| Штатная команда native клиента с параметром `--extractor-config network.yml` | Запускается окружением; встроенный адаптер получает общий YAML. Имя параметра — проектный интерфейс нашей сборки, не существующий upstream флаг |
| Та же native команда с `--extractor-mode=drain` | D57: только восстановление сохранённых обязательств и доставка; нет нового live boundary/capture, exit после durable DRAINED |
| `extractor gateway --config gateway.yml` | Gateway одной сети, доставка в NATS; отдельный файл этого контура |
| `extractor config check --config network.yml --role live` | Проверка YAML и локальных предпосылок выбранной роли: live либо gateway |

По D39 ноду запускает окружение: Compose/Kubernetes либо штатная команда клиента. Общий CLI не выбирает и не запускает native executable. Hooks/Inspector работают внутри закреплённой native сборки, адаптер получает YAML при её запуске. Подключение hooks к произвольной уже работающей upstream ноде не обещается. Сигналы и lifecycle ноды обслуживает её штатный процесс и окружение.

CLI не скачивает клиенты, не создаёт pods и не вызывает API Kubernetes/Docker. Окружение запускает независимые процессы каждой сети. Добавление сети не меняет настройки других сетей. Native supporting clients запускаются окружением, не загрузчиком. Команд history/resume/apply и параметров диапазона блоков нет.

## Общая схема YAML v1

D78: общее разрешение относительных путей применяется к `storage.live_dir` и `storage.gateway_dir`; база — каталог выбранного YAML, независимо от cwd процесса. Например, для `/srv/extractor/config/eth.yml` значение `../wal/eth` означает `/srv/extractor/wal/eth` при запуске из любого рабочего каталога. Относительный аргумент `--config` сначала выбирает сам YAML относительно cwd; дальнейшая база полей — каталог этого файла. Явное field-specific ограничение имеет приоритет: `finality.polygon.sources_file` требует абсолютный путь даже при существующем файле рядом с YAML. Разрешение путей не создаёт каталоги или DB; collector/Gateway и config check используют одно правило, а абсолютные storage paths сохраняют своё назначение.

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

## Размер сообщения D52

Gateway применяет `transport.max_frame_bytes` при формировании publication plan и определяет part layout для каждого RecordFragment. До Prepare адаптер хранит неизменные типизированные RecordFragment bytes (<=65536 bytes по версии формата D73), включая дробление крупных полей до сериализации. Это логическое кодирование независимо от NATS limit, а не локальное разбиение монолитной Envelope. Адаптер сохраняет точные frames по plan; Gateway проверяет их до публикации. Второй YAML limit не вводится. Если отдельный fragment с wrapper/headers не помещается, он передаётся FragmentPart. Никакой размер транзакции не оправдывает усечение данных.

В поставляемой конфигурации NATS `max_payload` задаётся явно `2097152`; stream MaxMsgSize также `2097152`. Полный размер frame и headers проверяется до отправки; gRPC send/receive пределы должны вмещать размер соответствующего RPC сообщения с его обёрткой, а не только bytes frame. RPC не должен объединять неограниченное число frames в одно сообщение. Проверка фактического сериализованного размера обязательна; ограничения NATS и gRPC должны быть согласованы с override лимита.

Pending publication plan/bytes неизменны при restart и retry. Изменённый лимит применяется к новым планам; если сохранённые frames больше действующего лимита либо server/stream их не допускает, публикация останавливается с явной ошибкой конфигурации. Pending не удаляются и не разбиваются повторно; нужно восстановить достаточный лимит. Горячая перезагрузка не добавляется.

Штатный NATS default — 1 MB по [документации](https://docs.nats.io/running-a-nats-service/configuration); он не гарантирует размещение полной нормализованной транзакции с calls/logs/state changes. Поэтому 2 MiB — предел сообщения, не обещание «любая транзакция одним сообщением».

## Смена назначения NATS — D54

Точный нормативный порядок D54/D57/D59: [DELIVERY_STATE_MACHINE](DELIVERY_STATE_MACHINE.md). Binding задаётся окружением до первого capture и сохраняется обоими owners; local READY без Prepare уже имеет destination. Drain и switch проходят durable generation barrier, а не проверку пустоты RAM.

`stream` и `subject_prefix` должны совпасть с desired DestinationBinding. Gateway проверяет точное account поле UserInfo, stream/subjects/limits и descriptor digest по [CONTROL_WIRE_CONTRACT](CONTROL_WIRE_CONTRACT.md); URL не identity. Stream создаёт environment bootstrap, runtime Gateway не выполняет административный create/update.

Publication plan и ACK относятся к одному сохранённому логическому назначению. При наличии незавершённой доставки у адаптера или Gateway смена account/stream/subject mapping отклоняется до публикации; pending и исходные настройки назначения сохраняются. Смена адреса или credentials, по-прежнему ведущих в тот же account/stream/subjects, не является переносом данных, но требует проверки назначения. Неопределённость этой проверки не разрешает объединять ACK.

Для cutover выполнить старый binding drain до DRAINED; заменить binding_file обеих ролей на desired descriptor и согласовать Gateway stream/prefix. Старые binding/seal/drain_id читаются из store, дополнительных YAML keys нет. Gateway с desired mismatch входит в AWAIT_SWITCH; native explicit LIVE завершает persisted Switch/Resume до новой H. Полная последовательность — CONTROL_WIRE_CONTRACT. Пустой Gateway/RAM не доказывает завершение native/producer obligations. При недоступном старом назначении pending сохраняются, cutover блокируется; forced migration отсутствует. Post-ACK retention/потеря NATS store вне гарантии D30–D31.

## Native-профили

| network.profile | native.kind | Выбранная реализация |
|---|---|---|
| ethereum-mainnet | geth | Go/Geth: native hooks/state journal, canonical notifications |
| bsc-mainnet | bsc | Go/BSC: tx execution и Parlia Finalize |
| polygon-pos-mainnet | bor | Go/Bor: tx execution, StateSync/Finalize; supporting Heimdall |

Текущие source/toolchain candidates заданы в [DEPLOYMENT_BOOTSTRAP_CONTRACT](DEPLOYMENT_BOOTSTRAP_CONTRACT.md) и [COMPONENT_STACK](COMPONENT_STACK.md); CLIENT_MATRIX сохраняет исторические исследования. YAML не выбирает произвольный executable/version и не добавляет новый тип блокчейна без адаптера. Исторические Classic/replay профили сейчас не предлагаются. Эта таблица использует ранее собранную документацию, не утверждает совместимость по результатам runtime-тестов.

**Общий интерфейс, native параметры.** Аргументы запуска, datadir, consensus/rollup/Heimdall соединения и JWT задаются штатной конфигурацией native клиента и окружением. Они не дублируются в YAML адаптера. Общий параметр подключения YAML является частью нашей native интеграции; команды Gateway/config check остаются отдельными. Native capture не заменяется RPC.

Встроенный адаптер сверяет network.profile/native.kind с эффективной конфигурацией запущенного клиента, используя результат его штатного parser. Несоответствие сети или сборки даёт ошибку до capture; адаптер не меняет native настройки. Datadir определяется из этой эффективной конфигурации. Отдельный config check проверяет YAML, но не обещает сверку с ещё не запущенной нодой.

Для каждого профиля при реализации потребуются шаблоны штатного запуска с подключённым YAML адаптера и проверки настоящим клиентом. Новое исследование версий и сборки в этом шаге не выполнялись.

## Состояние и запуск

1. Проверить CLI/YAML, пути, типы, профиль и эффективную native конфигурацию до открытия writable состояния. Ошибка не должна частично применять настройки.
2. Эксклюзивно открыть состояние owner. Native datadir, live_dir и gateway_dir не могут совпадать или быть вложены друг в друга; symlinks разрешаются перед сравнением. Второй writer получает ошибку.
3. Сверить сохранённые network/profile, роль, wire schema/build identity. Несовместимый WAL не удалять и не переинициализировать молча. Сеть нельзя сменить, сохранив прежний каталог.
4. До native recovery установить hooks/decision bridge в RECOVERY_ONLY; разрешить сохранённые ADMITTED/EXEC_READY/markers и lifecycle по NATIVE_CAPTURE_PROTOCOL. Независимо восстановить pending по [DELIVERY_STATE_MACHINE](DELIVERY_STATE_MACHINE.md), сохраняя IDs/bytes/parts. Новый safe live capture не ждёт NATS, но не обходит незавершённые native obligations.
5. По D39 после синхронизации ноды зафиксировать текущую каноническую голову H (высоту и hash); начать новый live-сбор с H+1. Начальная синхронизация не выгружает прошлые блоки. Фиксация H согласована с native подпиской так, чтобы события после границы не потерялись. При рестарте для новых наблюдений фиксируется новая H, а pending WAL передаётся независимо. Это начальная граница, не постоянный фильтр по высоте: последующие reorg/finality обрабатываются native lifecycle bridge, в том числе относительно сохранённых активаций.
6. Получать native события, нормализовать, сохранять в WAL, передавать через Gateway до NATS PubAck. Не обходить диапазон старого checkpoint→head. Восстановление конкретного незакрытого capture, текущие reorg/finality сохраняют существующий контракт.

Не вводятся TTL, квота pending или удаление при недоступности NATS. Физическая ошибка записи явная; WAL_SAVED не объявляется без durable записи. Готовность источника и готовность публикации — разные состояния.

## Валидация и остановка

`--extractor-mode=live|drain` — именованный проектный параметр native сборки, не произвольный override YAML. Новый store по умолчанию LIVE. Сохранённый незавершённый DRAIN после crash возобновляется в DRAIN; явный live до его завершения отвергается. После DRAINED возврат в LIVE требует explicit mode=live, seal, применимого switch и Resume новой admission generation. Этот режим не существует в upstream без нашей интеграции.

D57: fatal локального WAL/normalization останавливает весь native процесс соответствующей сети; Gateway journal fatal останавливает Gateway, не все сборщики. Transient transport outage допускает продолжение safe local capture по D28. D58 разрешает VM-safe yield вне hooks для bounded snapshot/spooling; unsupported pruning/backend/import modes отвергаются preflight.

Upgrade preflight разделяет build ID и store/plan/wire/native profile capabilities. Совместимые версии повторяют старые bytes. Несовместимый wire/plan upgrade — после drain старой сборкой. Несовместимый persistent store format не становится допустимым после drain: lifecycle/identity ещё существуют; без поддержанной read compatibility такой переход/rollback запрещён до записи, auto-migration нет.

Ошибка содержит код, путь поля и причину, без секретов и полного дампа native argv. `config check` не создаёт DB/stream/run, не запускает ноду, не публикует данные и не обещает доступность сети. Online handshake выполняется при запуске выбранной роли.

D77: NATS stream metadata extractor.network_digest/extractor.destination_id вычисляются из DestinationBinding; новых YAML overrides для них нет. Локальный config check проверяет descriptor и форматы, но не объявляет remote metadata сверенными. Gateway проверяет StreamInfo online по фазам CONTROL_WIRE_CONTRACT; missing/malformed/mismatch блокируют публикацию с CONFIG_BLOCKED, не меняют destination/pending и не запускают автоматическое исправление stream.

Проектные exit codes: 0 — успешный check/штатное завершение; 2 — CLI/YAML/native config; 3 — занятое или несовместимое состояние; 4 — неподдержанная сборка/несовместимый источник; 5 — невосстановимая runtime/дисковая ошибка. Исходная native ошибка сохраняется в диагностике. Недоступность Gateway/NATS вызывает retry, не успешное завершение.

SIGINT/SIGTERM прекращает новые чтения и закрывает состояние, сохраняя durable pending. Gateway CLI возвращает 130/143; native процесс сохраняет штатную обработку сигналов и exit codes, адаптер использует его shutdown hook. Остановка не ждёт бесконечно NATS PubAck. STAGING восстанавливается по протоколу; неподтверждённый payload не удаляется. SIGKILL восстанавливается следующим запуском как crash.

Будущие проверки: одинаковые команды всех профилей; неверные kind/chain; неизвестные/дублирующиеся ключи; TLS mismatch; недоступные credentials; конфликты native args; общий/занятый datadir; pending при остановке и NATS outage; отсутствие стартовой истории; изоляция сетей. Это требования к будущим тестам, не проведённые проверки.

D40 исключает требования производительности и production-методику из ТЗ. Техническое разбиение payload и bounded-memory обработка сохраняют контракт протокола; это не latency/TPS/RAM SLO. У каждой сети отдельные live_dir и gateway_dir с независимыми RocksDB writers; общая multi-network DB запрещена. Версии YAML-библиотек и совместная сборка закрепляются на этапе реализации, без изменения native toolchains по умолчанию.

## Результат CFG-S04

CFG-S04/CFG-S05 завершены как документация с уточнениями D39: нода запускается окружением, live начинается с H+1 после синхронизации. CFG-R4 снят. E00-04 закрыт D40 в границе функционального ТЗ, без performance требований и production-методики. Runtime-проверки и реализация не выполнялись.
