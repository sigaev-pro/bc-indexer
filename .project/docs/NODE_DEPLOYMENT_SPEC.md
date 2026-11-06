# Полный комплект нод для Compose и Kubernetes

D82: топология ниже описывает baseline трёх mainnet контуров (11 services / 14 stores), не полный семипрофильный стенд. Каждый выбранный testnet создаёт независимый контур своего семейства, включая supporting clients; NATS может быть общим при изолированных streams/credentials. Профили и test orchestration — [TESTING_CONTRACT](TESTING_CONTRACT.md).

11 основных сервисов, 14 persistent volumes; три сети независимы. Bootstrap/identity/config permissions — [DEPLOYMENT_BOOTSTRAP_CONTRACT](DEPLOYMENT_BOOTSTRAP_CONTRACT.md), Polygon D63 observer/sources binding — [CURRENT_SEMANTICS](CURRENT_SEMANTICS.md). Документация готова, runtime/deployment не выполнялись.

Тематическое представление [единого ТЗ v3](../evm-transaction-extractor-tz-v3-codex.md); изменения нормативного содержания синхронизируются с root в том же commit.

## Состав и ответственность

| Контур | Что проект должен скомпоновать и настроить | Принятое решение и граница доказательства |
|---|---|---|
| Ethereum | Geth с native-адаптером + Prysm (D44), их соединение и credentials, собственные native datadirs, RocksDB WAL адаптера, Gateway и его журнал | Prysm 7.3.0 и отдельные контейнеры/Pods приняты; native конфигурация задана bootstrap-контрактом, build/image/runtime evidence ещё нет |
| BSC | BSC-клиент с native-адаптером и штатным консенсусом, native datadir, собственные WAL/Gateway/журнал | Конфигурация/flags/init закреплены bootstrap-контрактом; build/image/runtime evidence ещё нет |
| Polygon PoS | Bor с native-адаптером + Heimdall-v2, отдельные Geth/Prysm для Polygon L1 по D44, необходимые соединения, собственные native datadirs, WAL/Gateway/журнал | Кандидаты версий, отдельные workloads и native конфигурация приняты; build/image/runtime evidence ещё нет |
| NATS | Один ранее принятый NATS JetStream внутри Compose/Kubernetes, persistent storage, подключение независимых Gateway, раздельные streams/subjects | Конфигурация identity/ACL/metadata/bootstrap задана; runtime evidence ещё нет, HA/DC-топология вне scope |

Текущие native candidates заданы в [DEPLOYMENT_BOOTSTRAP_CONTRACT](DEPLOYMENT_BOOTSTRAP_CONTRACT.md), стек — [COMPONENT_STACK](COMPONENT_STACK.md); CLIENT_MATRIX хранит исторические исследования. Кандидат версии не означает доказанную совместимость сборки. Добавление validator duties/staking не требуется для сбора данных и не предполагается автоматически.

## Обязательное описание для обеих сред

D44: L1-нода Polygon — самостоятельный Geth/Prysm комплект внутри Polygon-контура, без нашего Ethereum-адаптера. Она не публикует Ethereum-транзакции в NATS и не использует WAL/Gateway Ethereum-контура. Datadirs, credentials и процессы отделены от основной Ethereum-ноды. Общий выбор ПО не означает общий экземпляр или общее состояние. Остановка основного Ethereum-контура не должна останавливать источник L1 для Polygon.

D47/D58: приняты кандидаты Geth 1.17.9/Prysm 7.3.0 для обеих пар, BSC 1.7.8, Bor 2.10.3/Heimdall-v2 0.13.2. Совместимость и image digests ещё не подтверждены; [весь набор и источники](research/DEPLOYMENT_VERSION_CANDIDATES.md). D45 выбирает отдельный Compose service и Kubernetes workload/Pod для каждого самостоятельного компонента. Совместное размещение компонентов в контейнере/Pod не требуется.

### Карта размещения D45

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

Компоненты связываются конфигурируемыми сетевыми endpoints: service DNS в Kubernetes/Compose либо заданные адреса. Связи не предполагают localhost или общий хост. Для adapter→Gateway применяется ранее принятый TCP/gRPC с mTLS; native связи используют штатную аутентификацию, в том числе Engine API/JWT для каждой отдельной Geth/Prysm пары. NATS остаётся общим транспортом. Точные адреса, порты и передача secrets заданы NETWORK_PORTS и DEPLOYMENT_BOOTSTRAP_CONTRACT.

Каждый компонент можно размещать и перезапускать отдельно. Это не устраняет зависимость Geth от Prysm или Bor от Heimdall/L1: их недоступность отражается в готовности соответствующей ноды. Остальные сетевые контуры не переконфигурируются и не ожидают её прогресса; pending WAL сохраняется независимо.

- Полный список services/workloads, их роли, образы/сборки и закреплённые версии. Сборки execution-клиентов содержат наш адаптер, остальные компоненты подключаются согласованными штатными интерфейсами.
- Штатные native configs, YAML адаптера/Gateway и способ передачи их процессам. Проект готовит согласованный комплект: пользователь не должен самостоятельно угадывать недостающие связи компонентов.
- Сетевые адреса/порты между компонентами, аутентификация, JWT/TLS/credentials и способ их предоставления без включения рабочих секретов в примеры.
- Persistent volumes/datadirs отдельно для native клиентов и RocksDB каждого owner. Общая writable DB сетей запрещена; нет второго writer одного каталога при рестарте/замене процесса.
- Условия готовности компонентов и порядок подключения, а не только факт старта контейнера. Live активируется с H+1 после синхронизации по D39. Pending WAL не ждёт синхронизации для своей повторной доставки, если компоненты доставки могут работать.
- Независимый запуск, остановка и изменение конфигурации контура каждой сети. Native dependencies внутри сети допускаются; общего координатора прогресса и зависимости от extractor другой сети нет. Общий NATS сохраняет согласованную роль транспорта.
- Штатная остановка, сохранение томов и восстановление pending по существующему RocksDB-протоколу. Автоматическое удаление данных и загрузка исторических пропусков не добавляются.

Docker Compose используется для локального/dev запуска, Kubernetes — для production-размещения. Один и тот же функциональный контракт применяется к обеим средам. Сборщик не вызывает их API для создания/управления процессами: окружение исполняет подготовленные проектом конфигурации. Принятая компоновка workloads/Service/PVC и bootstrap заданы ниже и в DEPLOYMENT_BOOTSTRAP_CONTRACT; manifests/templates и runtime evidence не создавались в этой документальной работе.

## Настройки размещения и связей D46

**Хранение.** StatefulSet каждого компонента владеет собственными PVC. В Compose используются собственные named volumes. Для execution-клиента с адаптером native datadir и RocksDB WAL размещаются раздельно; publication journal принадлежит отдельному Gateway. Количество PVC определяется набором этих отдельных хранилищ, а не правилом «ровно один том на сеть». Единственный writer сохраняется при обновлении/рестарте; replicas=1 не заменяет соблюдение этого правила. Перенос зависит от возможностей StorageClass/тома и не означает автоматический failover.

**Соединения.** Конфиги содержат service DNS и порты, не IP конкретного контейнера/Pod. Смена IP не требует правки клиентов. Основные связи:

| Отправитель/клиент | Получатель | Назначение |
|---|---|---|
| eth-prysm | eth-geth | Engine API своей Ethereum-ноды |
| polygon-l1-prysm | polygon-l1-geth | Engine API отдельной L1-ноды Polygon |
| polygon-heimdall | polygon-l1-geth и polygon-bor | Штатные L1/Bor подключения |
| polygon-bor | polygon-l1-geth:8545 | Finality observer: hash-pinned finalized L1 checkpoint reads; собственная supporting L1 Polygon, не основной eth-geth |
| polygon-bor | polygon-heimdall | Штатный интерфейс Bor/Heimdall |
| Адаптер внутри eth-geth / bsc-node / polygon-bor | eth-gateway / bsc-gateway / polygon-gateway соответственно | gRPC с mTLS |
| Каждый Gateway | nats | Публикация и получение PubAck |

Таблица не заменяет version-specific native настройки: точные методы, порты и дополнительные обязательные связи сверяются при закреплении версий. Внутренние service DNS не заменяют native P2P соединения; публикация P2P-портов также должна войти в полную конфигурацию.

**Конфиги и секреты.** Обычные параметры — отдельные YAML и штатные конфиги клиентов. JWT/TLS-ключи/NATS credentials передаются отдельными секретами или файлами. JWT основной Geth/Prysm пары не используется парой Polygon L1. Каждый процесс получает только нужные ему файлы. Конкретные mounts/пути описываются в шаблонах; генерация рабочих значений не выполняется на этапе ТЗ.

**Готовность.** Ноды начинают синхронизацию независимо от Gateway/NATS. Готовность к native capture наступает после синхронизации по сетевому профилю; фиксируются H/hash, новые live-блоки собираются с H+1. Pending WAL передаётся независимо от новой live-границы. Отказ Gateway/NATS не означает остановку готового capture, пока доступна durable запись. Native зависимости внутри сети сохраняются, но нет глобального барьера readiness между сетями. Временная недоступность downstream сама по себе не является причиной перезапускать исправный producer через liveness probe. D52: операционное наблюдение по логам и штатное управление окружением; отдельная детализация probes/shutdown не требуется.

## Гибкая конфигурация D48

Один сетевой контур получает собственный YAML сборщика и отдельный YAML Gateway. Конфиги Ethereum, BSC и Polygon не объединяются в обязательный общий файл; обновление настроек сети не требует перезапуска других сетей. У общего NATS собственный конфиг сервера, отличный от настроек подключения Gateway.

| Уровень | Что настраивается | Способ передачи |
|---|---|---|
| Встроенный сборщик одной сети | Сеть, WAL, endpoint своего Gateway, TLS, параметры доставки | Типизированный YAML через проектный `--extractor-config` |
| Gateway одной сети | NATS, subjects/streams, журнал, входящее соединение и TLS | Собственный YAML через `--config` |
| Native клиенты | Все поддерживаемые выбранной версией штатные настройки, дополнительные аргументы | Родные файлы конфигурации и аргументы запуска; без проектного allowlist и копирования всего upstream schema в YAML сборщика |
| Окружение | Версии/образы, порты, P2P-адреса, каталоги, ресурсы, размещение | Compose overrides либо Kubernetes values; приложение не читает API оркестратора |
| Секреты | JWT, TLS-ключи/сертификаты, NATS credentials | Отдельные файлы, ссылки в конфигурации; отдельный JWT у каждой Geth/Prysm пары |

Комплект должен содержать готовые согласованные defaults для всех семи профилей трёх семейств и обеих сред. Пользователь меняет только нужные настройки. Произвольный выбор upstream параметров/образов не означает доказанную совместимость произвольной сборки с native адаптером; проверенные кандидаты D47 и обязательные интерфейсы сохраняются.

Изменение применяется перезапуском затронутого компонента. Watcher/hot reload не требуются. Сохранённые WAL/журнал обслуживаются по принятому протоколу; restart не добавляет загрузку истории или пропусков. Изменение native конфигурации не выполняется командой сборщика: процесс запускает окружение. Типизированный YAML сборщика/Gateway продолжает запрещать неизвестные ключи; свобода native аргументов относится к отдельному уровню.

Общие defaults портов и каталогов приняты D49 ниже. Таблица P2P/native endpoints закреплена D50 в NETWORK_PORTS; flags/mount mappings определены в DEPLOYMENT_BOOTSTRAP_CONTRACT; native parser/runtime verification остаётся E01/E06. Здесь описано ТЗ, не готовые deploy-файлы.

## Defaults портов, доступа и каталогов D49

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

P2P-ключ/идентичность сохраняется в persistent native state либо связанном с ним постоянном расположении, соответствующем конфигурации клиента. Обычный перезапуск не генерирует новую идентичность. Точные native файлы/flags заданы в DEPLOYMENT_BOOTSTRAP_CONTRACT; их runtime-проверка ещё не выполнена, upstream формат ключей не меняется.

## Подготовка конфигов и native файлов D51

Комплект содержит готовые штатные конфиги выбранных клиентов. Пользователь может заменить файл целиком и задать дополнительные native аргументы по D48. Исходные файлы монтируются в `/config` только для чтения; файлы, которые native клиент должен изменять, располагаются в его собственном `/data/node`. Секреты остаются отдельными файлами согласно D49.

Перед запуском native процесса окружение выполняет отдельный init-шаг: на первом старте создаёт необходимые каталоги и native файлы. Повторный запуск сохраняет существующее состояние цепочки и ключи, не выполняет разрушительную повторную инициализацию и не генерирует новую P2P-идентичность. Сборщик не управляет этим шагом и не запускает ноду.

Механика init для Compose/Kubernetes, точные native команды, filenames и mount mappings определены в [DEPLOYMENT_BOOTSTRAP_CONTRACT](DEPLOYMENT_BOOTSTRAP_CONTRACT.md); DEP-03.2b.2b.3b завершён документально. Обновляемые пользовательские настройки отделены от сохраняемых native state/ключей: применение нового конфига при рестарте не должно затирать состояние. D51 не выбирает молча универсальное копирование всех файлов `/config` поверх существующего datadir. Файлы реального deployment сейчас не создаются.

## Сохраняемые границы (D38–D42)

История и пропуски отложены D38; performance требования и production-методика исключены D40; лицензия и публичная поставка отложены D41; Base/Arbitrum отложены D42. Эти решения не отменяют описанный здесь полный технический комплект трёх нод. Функциональные проверки конфигурации/совместимости относятся к будущей реализации, не объявляются уже выполненными.

## Задачи пункта 2 E00-11

- [x] DEP-01 Зафиксировать полный deployment scope по D43 и ответственность проекта.
- [x] DEP-02 D44: выбран Prysm; Polygon использует собственный отдельный Ethereum L1-комплект Geth/Prysm. Не зависит от основной Ethereum-ноды/extractor. Подробное размещение и версии — DEP-03.
- [x] DEP-03 SPECIFIED: процессы/config/init/trust/ownership/startup описаны в DEPLOYMENT_BOOTSTRAP_CONTRACT; runtime E01/E06.
- [x] DEP-03.1 D45: каждый компонент — отдельный service/container и workload/Pod; карта 11 основных сервисов выше.
- [x] DEP-03.2 SPECIFIED: кандидаты/source pins и workload/config/endpoint/volume contract закреплены; совместимость сборок/image digests доказывается E01/E06.
- [x] DEP-03.2a D46: приняты StatefulSet/Service/PVC, named volumes, service DNS и разделение configs/secrets.
- [x] DEP-03.2b SPECIFIED: D47/D58/D60 и точные native interfaces; build/runtime TODO.
- [x] DEP-03.2b.1 D47: кандидаты версий согласованы; совместная сборка/runtime не проверены, source inspection выполнен; image digests/build/runtime proofs остаются E01/E06.
- [x] DEP-03.2b.2 SPECIFIED: значения/defaults и override constraints определены для обеих сред; operator-specific addresses/secrets задаются при deployment.
- [x] DEP-03.2b.2a D48: независимые YAML сборщика/Gateway каждой сети, штатные native configs/args, Compose overrides/Kubernetes values, отдельные secrets и restart компонента.
- [x] DEP-03.2b.2b SPECIFIED: defaults/flags/ports/mounts/source pins определены; image digests — будущий build artifact.
- [x] DEP-03.2b.2b.1 D49: внутренние Geth/Gateway порты, только P2P наружу, независимые внешние адреса/порты, пути контейнеров и сохранение P2P-идентичности.
- [x] DEP-03.2b.2b.2 D50: [таблица P2P/native endpoints](NETWORK_PORTS.md), Compose port mappings и Kubernetes NodePort; LoadBalancer опционален. Решение принято агентом по поручению пользователя.
- [x] DEP-03.2b.2b.3 SPECIFIED: native flags/config/mount mappings и оставшиеся artifact gates записаны.
- [x] DEP-03.2b.2b.3a D51: готовые заменяемые native configs, init-шаг окружения, создание недостающих native файлов при первом старте, сохранение state/ключей при рестарте, read-only `/config` и writable файлы в `/data/node`.
- [x] DEP-03.2b.2b.3b SPECIFIED: DEPLOYMENT_BOOTSTRAP_CONTRACT, включая partial init, config generations, hash scheme при BSC init, mainnet genesis gate, role/network trust и source pins.
- [x] DEP-03.3 D46/D52: независимость готовности и pending сохранена; операционная готовность по логам, управление средствами окружения. Детальная probe/shutdown спецификация не требуется.
- [x] DEP-03.3a D46: принята независимая готовность и продолжение capture в WAL при недоступности Gateway/NATS.
- [x] DEP-03.3b D52: синхронизация/готовность/ошибки видны в логах; native адаптер сообщает зафиксированную границу H/hash и старт H+1. Стандартное управление Docker/Kubernetes. Детализация probe-команд и собственных shutdown таймаутов снята пользователем; требования сохранности pending не отменены.
- [x] DEP-04 SPECIFIED: YAML/native modes, binding/drain/Resume, 11 services/14 volumes, независимость и NATS boundary сверены; BOOT/CONFIG/MOUNT/TRUST/NATS/START/REPLACE/LOG/ARTIFACT criteria и evidence/stage mapping в IMPLEMENTATION_GATES. Runtime не выполнен.

Документальная детализация DEP-01…04 завершена; текущие runtime/artifact gates — E01/E06/E07 и IMPLEMENTATION_GATES. Консолидация root MD отслеживается отдельно в PROJECT_STATUS. Решения повторно не выбирать; архитектурный документ не разрешает реализацию, сборку или deployment.
