# Порты и экспонирование — D50

> Точные native mappings, listener scopes и bootstrap — [DEPLOYMENT_BOOTSTRAP_CONTRACT](DEPLOYMENT_BOOTSTRAP_CONTRACT.md). D58 обновил Heimdall до v0.13.2, порты сохранены. CometBFT RPC 26657 baseline слушает loopback внутри Heimdall. По D52 отдельная probe/shutdown спецификация не вводится, обязательные состояния отражаются в logs.

2026-10-21. DEP-03.2b.2b.2. Пользователь поручил выбрать удобный вариант самостоятельно. Только ТЗ; конфигурации не запускались. Все значения переопределяются по D48. Неизменны границы D49: наружу только P2P, внутренние API отдельно, собственная идентичность каждого клиента.

## P2P по экземплярам

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

## Docker Compose

- Отдельная bridge-сеть каждого блокчейн-контура; внутри Polygon находятся его собственные L1 Geth/Prysm. Gateway дополнительно подключается к транспортной сети NATS. Имена сервисов разрешаются через DNS соответствующей сети.
- Только P2P публикуется на хост как `порт:тот_же_порт`, TCP/UDP отдельными mappings. Все компоненты можно запустить на одном хосте без взаимных конфликтов из таблицы.
- RPC, Engine API, Gateway и NATS не получают host port mappings по умолчанию. Опциональный диагностический override привязывает выбранный порт к 127.0.0.1; его host port выбирается без конфликтов.
- Конфигурация native listener должна принимать нужные соединения внутри контейнерной сети; upstream localhost-only default не используется для межконтейнерных связей. Доступные APIs ограничиваются нужными связями, JWT/mTLS сохраняются.

## Kubernetes

- Для внутренних endpoints — ClusterIP Services; для стабильной идентичности StatefulSet предусматривается governing headless Service. P2P каждого native экземпляра получает отдельный NodePort Service, не смешанный с API ports.
- P2P Service использует явные nodePort из таблицы, именованные TCP/UDP ports и `externalTrafficPolicy: Cluster`. Это позволяет принимать соединения на выбранном достижимом узле независимо от размещения единственного Pod. Сохранение исходного IP пира при таком маршруте не обещается.
- Advertised address — явно заданный достижимый IP узла либо внешний адрес с корректным TCP/UDP forwarding. Нужны сетевой маршрут и разрешённые порты. При потере этого адреса его обновляет оператор; автоматический failover публичного endpoint и HA не входят в текущий ТЗ.
- LoadBalancer доступен как override при наличии подходящего контроллера и поддержки нужных TCP/UDP ports. HTTP Ingress, hostNetwork и hostPort для P2P по умолчанию не используются. NodePort не требует cloud LoadBalancer, но сам по себе не создаёт публичный IP или NAT-маршрут.
- Для диагностики API можно использовать локальный port-forward на 127.0.0.1 без постоянной публичной публикации. Только P2P Service получает внешнюю доступность.

## Внутренние интерфейсы

Все порты ниже TCP, доступны только нужным компонентам внутри окружения. Наличие строки не означает обязательное включение всех RPC namespaces.

| Получатель | Порт | Клиент / назначение |
|---|---|---|
| eth-geth | 8551 | eth-prysm → Engine API с JWT основной пары |
| polygon-l1-geth | 8551 | polygon-l1-prysm → Engine API с JWT Polygon L1 |
| polygon-l1-geth | 8545 | polygon-heimdall → Ethereum JSON-RPC |
| polygon-l1-geth | 8545 | polygon-bor → hash-pinned finalized checkpoint reads собственной supporting L1; внутренний JSON-RPC |
| polygon-bor | 8545 | polygon-heimdall → Bor JSON-RPC |
| polygon-heimdall | 1317 | polygon-bor → Heimdall REST |
| polygon-heimdall | 26657 | Внутренняя диагностика CometBFT / будущая проверка синхронизации |
| eth-prysm, polygon-l1-prysm | 3500 | Внутренний Beacon REST / будущая проверка синхронизации |
| eth-geth, bsc-node | 8545 | Внутренняя диагностика JSON-RPC при включении; не источник native capture |
| eth-gateway, bsc-gateway, polygon-gateway | 50051 | Только свой адаптер → gRPC/mTLS |
| nats | 4222 | Gateway → NATS с принятыми credentials/TLS настройками |

Bor observer получает supporting L1 endpoint/role identity из finality.polygon.sources_file. Default — polygon-l1-geth:8545; при согласованном endpoint/TLS override проверяются те же bootstrap identity и роль POLYGON_L1_EXECUTION по CURRENT_SEMANTICS. Основной eth-geth не становится допустимой заменой только из-за той же chain_id. Связь остаётся внутренней в Compose/Kubernetes.

WebSocket, validator RPC, metrics/admin, NATS monitoring/cluster ports и дополнительные Heimdall gRPC endpoints не публикуются автоматически. Дополнительные interfaces требуют явного изменения карты доступа; обязательные текущие связи определены таблицей. Staking/validator duties не добавляются.

## Проверенные источники и оставшаяся работа

- [Geth CLI](https://geth.ethereum.org/docs/fundamentals/command-line-options): настраиваемые P2P/discovery, HTTP RPC/Engine API, NAT и JWT.
- [BSC v1.7.8 flags](https://github.com/bnb-chain/bsc/blob/v1.7.8/cmd/utils/flags.go): P2P и RPC параметры native клиента.
- [Prysm v7.3.0 shared flags](https://github.com/OffchainLabs/prysm/blob/v7.3.0/cmd/flags.go): отдельные p2p-tcp-port, p2p-quic-port, p2p-udp-port; [сетевые интерфейсы](https://docs.prylabs.network/docs/prysm-usage/p2p-host-ip).
- [Bor v2.10.3 config](https://github.com/0xPolygon/bor/blob/v2.10.3/builder/files/config.toml): JSON-RPC и Heimdall REST; [Heimdall v0.13.2 config](https://github.com/0xPolygon/heimdall-v2/blob/03090c94e7eaef344cd16efd6c968431c4041847/packaging/templates/config/mainnet/config.toml): P2P external_address и CometBFT RPC.
- [Kubernetes Service](https://kubernetes.io/docs/concepts/services-networking/service/): NodePort range, TCP/UDP Services, ClusterIP и LoadBalancer.
- [NATS configuration](https://docs.nats.io/running-a-nats-service/configuration): внутренний клиентский listener.

Источники подтверждают интерфейсы; они не являются результатом запуска нашей конфигурации. Документальная привязка flags/config/init/mounts завершена в DEPLOYMENT_BOOTSTRAP_CONTRACT; parse/init/runtime tests остаются E01/E06. Готовность по logs и штатному управлению окружением соответствует D52.
