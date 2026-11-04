# CFG-S04 — конфигурация live/Gateway

> D52: операционная готовность/синхронизация наблюдается по логам; управление штатными средствами Docker/Kubernetes, без отдельного API и обязательной детализации probes/shutdown таймаутов. Native граница H+1 и сохранение pending остаются. Default размера frame — 2 MiB, правила ниже.

> D51: штатные native configs можно заменить целиком и дополнить аргументами. Окружение выполняет отдельный init-шаг, первый старт создаёт необходимые native файлы, рестарт сохраняет state/ключи. Исходники `/config` read-only; изменяемые native файлы находятся в `/data/node`. Это подготовка окружения, не новая команда CLI сборщика. Точные filenames/init/mount mappings остаются в [deployment-задачах](NODE_DEPLOYMENT_SPEC.md).

> D50: [таблица endpoints и экспонирования](NETWORK_PORTS.md) закреплена по поручению пользователя. P2P overrides задаются native конфигурацией/окружением, не новыми произвольными ключами YAML сборщика. Gateway каждой сети использует внутренний service DNS и порт 50051, NATS — внутренний 4222. Значения настраиваемы; P2P NodePort не затрагивает приложение и не открывает его API наружу.

> D49: defaults deployment — Gateway `50051/TCP`, WAL `/data/wal`, журнал Gateway `/data/journal`, configs `/config` и secrets `/run/secrets` только для чтения. Процессы имеют собственные volumes; пути/порты переопределяемы. RPC/Engine API/Gateway/NATS не публикуются наружу по умолчанию. Это значения шаблонов окружения, не отмена обязательных полей схемы YAML. [Полный контракт defaults](NODE_DEPLOYMENT_SPEC.md).

> D48: каждая сеть имеет собственный YAML сборщика и отдельный YAML Gateway. Штатные native configs и дополнительные аргументы передаются окружением, не включаются в схему сборщика. Образы, порты/P2P, каталоги, ресурсы и размещение доступны через Compose overrides/Kubernetes values. Секреты отдельными файлами; изменения применяются перезапуском компонента. Проект предоставляет defaults для трёх сетей; конкретные значения ещё уточняются в [NODE_DEPLOYMENT_SPEC](NODE_DEPLOYMENT_SPEC.md).

> D46: endpoints используют service DNS, configs отделены от секретов, JWT у Geth/Prysm пар различаются. Каждый компонент — StatefulSet replicas=1 со своими Service/PVC, в Compose — сервис с named volumes. Готовность публикации не блокирует синхронизацию/capture и запись в WAL. Версии и точные native/probe-параметры ещё не утверждены; [deployment-контракт](NODE_DEPLOYMENT_SPEC.md).

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

## Размер сообщения D52

Gateway применяет `transport.max_frame_bytes` при формировании publication plan и определяет part layout. До Prepare адаптер хранит неизменные Envelope bytes; локальные фрагменты хранения не задают сетевое разбиение. Адаптер сериализует и сохраняет точные frames по полученному plan, Gateway проверяет их перед публикацией; второй независимый лимит разбиения в live YAML не вводится. Если полный record не помещается с envelope/headers, он разбивается на RecordPart, каждый в пределах лимита. Никакой размер транзакции не оправдывает усечение данных.

В поставляемой конфигурации NATS `max_payload` задаётся явно `2097152`; stream MaxMsgSize также `2097152`. Полный размер frame и headers проверяется до отправки; gRPC send/receive пределы должны вмещать размер соответствующего RPC сообщения с его обёрткой, а не только bytes frame. RPC не должен объединять неограниченное число frames в одно сообщение. Проверка фактического сериализованного размера обязательна; ограничения NATS и gRPC должны быть согласованы с override лимита.

Pending publication plan/bytes неизменны при restart и retry. Изменённый лимит применяется к новым планам; если сохранённые frames больше действующего лимита либо server/stream их не допускает, публикация останавливается с явной ошибкой конфигурации. Pending не удаляются и не разбиваются повторно; нужно восстановить достаточный лимит. Горячая перезагрузка не добавляется.

Штатный NATS default — 1 MB по [документации](https://docs.nats.io/running-a-nats-service/configuration); он не гарантирует размещение полной нормализованной транзакции с calls/logs/state changes. Поэтому 2 MiB — предел сообщения, не обещание «любая транзакция одним сообщением».

## Смена назначения NATS — D54

Точный нормативный порядок D54/D57/D59: [DELIVERY_STATE_MACHINE](DELIVERY_STATE_MACHINE.md). Binding задаётся окружением до первого capture и сохраняется обоими owners; local READY без Prepare уже имеет destination. Drain и switch проходят durable generation barrier, а не проверку пустоты RAM.

`stream` и `subject_prefix` проверяются вместе с network identity и доступным NATS account при online handshake. Это конфигурация существующего назначения; Gateway не создаёт streams и не меняет их административно. Точные правила идентификации account/назначения и их проверки при reconnect требуется завершить в E03 до реализации этого протокола; совпадение URL или имени stream само по себе недостаточно.

Publication plan и ACK относятся к одному сохранённому логическому назначению. При наличии незавершённой доставки у адаптера или Gateway смена account/stream/subject mapping отклоняется до публикации; pending и исходные настройки назначения сохраняются. Смена адреса или credentials, по-прежнему ведущих в тот же account/stream/subjects, не является переносом данных, но требует проверки назначения. Неопределённость этой проверки не разрешает объединять ACK.

Для перенастройки назначения остановить новые live-наблюдения, завершить pending и обмен подтверждениями в прежнем назначении, проверить сохранённые состояния обоих owners, затем изменить конфигурацию и перезапустить контур. Только пустой Gateway или очередь RAM не доказывают завершение producer pending; при недоступности необходимого состояния переход не подтверждается. После перезапуска новые наблюдения начинаются с новой H+1 по D39. Если прежнее назначение недоступно, данные сохраняются, а смена блокируется; принудительный перенос pending в текущий scope не входит. Post-ACK retention/потеря NATS store остаются вне гарантии сборщика по D30–D31.

## Native-профили

| network.profile | native.kind | Выбранная реализация |
|---|---|---|
| ethereum-mainnet | geth | Go/Geth: native hooks/state journal, canonical notifications |
| bsc-mainnet | bsc | Go/BSC: tx execution и Parlia Finalize |
| polygon-pos-mainnet | bor | Go/Bor: tx execution, StateSync/Finalize; supporting Heimdall |

Сборки и toolchains закрепляются по [CLIENT_MATRIX](CLIENT_MATRIX.md) и [COMPONENT_STACK](COMPONENT_STACK.md). YAML не выбирает произвольный executable/version и не добавляет новый тип блокчейна без адаптера. Исторические Classic/replay профили сейчас не предлагаются. Эта таблица использует ранее собранную документацию, не утверждает совместимость по результатам runtime-тестов.

**Общий интерфейс, native параметры.** Аргументы запуска, datadir, consensus/rollup/Heimdall соединения и JWT задаются штатной конфигурацией native клиента и окружением. Они не дублируются в YAML адаптера. Общий параметр подключения YAML является частью нашей native интеграции; команды Gateway/config check остаются отдельными. Native capture не заменяется RPC.

Встроенный адаптер сверяет network.profile/native.kind с эффективной конфигурацией запущенного клиента, используя результат его штатного parser. Несоответствие сети или сборки даёт ошибку до capture; адаптер не меняет native настройки. Datadir определяется из этой эффективной конфигурации. Отдельный config check проверяет YAML, но не обещает сверку с ещё не запущенной нодой.

Для каждого профиля при реализации потребуются шаблоны штатного запуска с подключённым YAML адаптера и проверки настоящим клиентом. Новое исследование версий и сборки в этом шаге не выполнялись.

## Состояние и запуск

1. Проверить CLI/YAML, пути, типы, профиль и эффективную native конфигурацию до открытия writable состояния. Ошибка не должна частично применять настройки.
2. Эксклюзивно открыть состояние owner. Native datadir, live_dir и gateway_dir не могут совпадать или быть вложены друг в друга; symlinks разрешаются перед сравнением. Второй writer получает ошибку.
3. Сверить сохранённые network/profile, роль, wire schema/build identity. Несовместимый WAL не удалять и не переинициализировать молча. Сеть нельзя сменить, сохранив прежний каталог.
4. Восстановить pending delivery по [RocksDB-протоколу](ROCKSDB_DELIVERY_PROTOCOL.md), сохраняя IDs, bytes и parts. Приём новых live данных в WAL не должен ждать доступности NATS.
5. По D39 после синхронизации ноды зафиксировать текущую каноническую голову H (высоту и hash); начать новый live-сбор с H+1. Начальная синхронизация не выгружает прошлые блоки. Фиксация H согласована с native подпиской так, чтобы события после границы не потерялись. При рестарте для новых наблюдений фиксируется новая H, а pending WAL передаётся независимо. Это начальная граница, не постоянный фильтр по высоте: последующие reorg/finality обрабатываются native lifecycle bridge, в том числе относительно сохранённых активаций.
6. Получать native события, нормализовать, сохранять в WAL, передавать через Gateway до NATS PubAck. Не обходить диапазон старого checkpoint→head. Восстановление конкретного незакрытого capture, текущие reorg/finality сохраняют существующий контракт.

Не вводятся TTL, квота pending или удаление при недоступности NATS. Физическая ошибка записи явная; WAL_SAVED не объявляется без durable записи. Готовность источника и готовность публикации — разные состояния.

## Валидация и остановка

`--extractor-mode=live|drain` — именованный проектный параметр native сборки, не произвольный override YAML. Новый store по умолчанию LIVE. Сохранённый незавершённый DRAIN после crash возобновляется в DRAIN; явный live до его завершения отвергается. После DRAINED возврат в LIVE требует явно указанного mode=live и завершённого seal/switch handshake. Этот режим не существует в upstream без нашей интеграции.

D57: fatal локального WAL/normalization останавливает весь native процесс соответствующей сети; Gateway journal fatal останавливает Gateway, не все сборщики. Transient transport outage допускает продолжение safe local capture по D28. D58 разрешает VM-safe yield вне hooks для bounded snapshot/spooling; unsupported pruning/backend/import modes отвергаются preflight.

Upgrade preflight разделяет build ID и store/plan/wire/native profile capabilities. Совместимые версии повторяют старые bytes. Несовместимый wire/plan upgrade — после drain старой сборкой. Несовместимый persistent store format не становится допустимым после drain: lifecycle/identity ещё существуют; без поддержанной read compatibility такой переход/rollback запрещён до записи, auto-migration нет.

Ошибка содержит код, путь поля и причину, без секретов и полного дампа native argv. `config check` не создаёт DB/stream/run, не запускает ноду, не публикует данные и не обещает доступность сети. Online handshake выполняется при запуске выбранной роли.

Проектные exit codes: 0 — успешный check/штатное завершение; 2 — CLI/YAML/native config; 3 — занятое или несовместимое состояние; 4 — неподдержанная сборка/несовместимый источник; 5 — невосстановимая runtime/дисковая ошибка. Исходная native ошибка сохраняется в диагностике. Недоступность Gateway/NATS вызывает retry, не успешное завершение.

SIGINT/SIGTERM прекращает новые чтения и закрывает состояние, сохраняя durable pending. Gateway CLI возвращает 130/143; native процесс сохраняет штатную обработку сигналов и exit codes, адаптер использует его shutdown hook. Остановка не ждёт бесконечно NATS PubAck. STAGING восстанавливается по протоколу; неподтверждённый payload не удаляется. SIGKILL восстанавливается следующим запуском как crash.

Будущие проверки: одинаковые команды всех профилей; неверные kind/chain; неизвестные/дублирующиеся ключи; TLS mismatch; недоступные credentials; конфликты native args; общий/занятый datadir; pending при остановке и NATS outage; отсутствие стартовой истории; изоляция сетей. Это требования к будущим тестам, не проведённые проверки.

D40 исключает требования производительности и production-методику из ТЗ. Техническое разбиение payload и bounded-memory обработка сохраняют контракт протокола; это не latency/TPS/RAM SLO. У каждой сети отдельные live_dir и gateway_dir с независимыми RocksDB writers; общая multi-network DB запрещена. Версии YAML-библиотек и совместная сборка закрепляются на этапе реализации, без изменения native toolchains по умолчанию.

## Результат CFG-S04

CFG-S04/CFG-S05 завершены как документация с уточнениями D39: нода запускается окружением, live начинается с H+1 после синхронизации. CFG-R4 снят. E00-04 закрыт D40 в границе функционального ТЗ, без performance требований и production-методики. Runtime-проверки и реализация не выполнялись.
