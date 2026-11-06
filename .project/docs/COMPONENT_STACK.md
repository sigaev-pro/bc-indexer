# Стек компонентов текущей поставки

D82: правила семейства применяются отдельно к каждому mainnet/testnet профилю из [TESTING_CONTRACT](TESTING_CONTRACT.md). Active fork/types, native/supporting compatibility и identity закрепляются ProfileManifest; mainnet source evidence не доказывает testnet runtime. Указанные версии остаются кандидатами.

D58–D63: Heimdall 0.13.2; hash state profiles; Bor без BlockSTM/ускоренных import/preconfirmation paths. Точные source refs — [bootstrap contract](DEPLOYMENT_BOOTSTRAP_CONTRACT.md). Immutable image/dependency locks и совместные runtime proofs — будущие E01/E06.

Тематическое представление [единого ТЗ v3](../evm-transaction-extractor-tz-v3-codex.md); изменения нормативного содержания синхронизируются с root в том же commit.

## Компоненты и интеграция

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
| WAL/publication journal | RocksDB 10.4.2; Go grocksdb v1.10.2 | Sync WriteBatch, WAL enabled, отдельный owner; [нормативный автомат](DELIVERY_STATE_MACHINE.md). BlobDB optional, backup исключён D27 |
| Приёмочный harness | Go + тот же protobuf decoder + JetStream client | Только проверка опубликованных сообщений против fixtures; не промышленный downstream |
| Локальный запуск | Docker Compose: все execution/consensus/supporting-компоненты трёх нод, адаптеры, WAL/Gateway и NATS | Полная компоновка и конфигурация проекта по D43; [детализация](NODE_DEPLOYMENT_SPEC.md) |
| Production | Kubernetes: все компоненты нод, адаптеры, независимые workload/volume/config контуры сетей и NATS | D43: готовим весь технический комплект; NATS chart/server candidates и [исследование](research/NATS_DEPLOYMENT_STACK.md) сохраняются; production-методика отдельно |

Native pins и расхождения toolchains подробно сохранены в [CLIENT_MATRIX.md](CLIENT_MATRIX.md). Новый baseline Gateway не изменяет compiler versions native клиентов.

## Почему SDK не встраивается во все native clients

Pinned gRPC-Go 1.84.0 требует Go 1.25.0; nats.go 1.54.0 — Go 1.26.0. protobuf-go 1.36.12 требует Go 1.23. Поэтому импорт нового NATS SDK в Geth/BSC candidate с Go 1.25 создаёт лишнее требование смены toolchain. Адаптер знает gRPC/Protobuf, Gateway — NATS; процессная граница уже принята D19. Генерируемые Go bindings всё равно проверяются в каждом client module отдельно.

Источники: [grpc-go go.mod](https://github.com/grpc/grpc-go/blob/v1.84.0/go.mod), [nats.go go.mod](https://github.com/nats-io/nats.go/blob/v1.54.0/go.mod), [protobuf-go go.mod](https://github.com/protocolbuffers/protobuf-go/blob/v1.36.12/go.mod), [Go distributions](https://go.dev/dl/?mode=json). Go 1.27.2 — кандидат общего сервиса на дату проверки, не утверждение об установленном компиляторе проекта.



## Версионирование и проверка

- Исходники native clients остаются на отдельных закреплённых commits; общий транспортный модуль не требует слияния их несовместимых Go/Cargo dependencies в один build graph.
- Теги библиотек выше — кандидаты на дату проверки. Перед воспроизводимой сборкой E01 записывает commit/checksum, compiler, generator versions, dependency lock и image digest. В конфигурации поставки не используются плавающие `latest`.
- Версии protoc, Go generators, runtimes и wire schema — разные величины. Они явно перечисляются в build manifest, не выводятся друг из друга.
- Go fixtures проверяют поля/presence/oneof. Protobuf bytes независимых сериализаций не обязаны совпадать; retries сохраняют исходные bytes/digest события.
- NATS server version, Helm chart version и appVersion фиксируются отдельно. Наличие Helm chart не выбирает автоматически cluster sizing, storage class или политику потери диска.
- Конкретный Linux base image, CPU architectures и поддерживаемые Kubernetes/Compose versions закрепляются после build smoke tests; не заявляется поддержка каждой платформы из-за наличия Dockerfile.
- YAML/restart определены LIVE_CONFIG_SPEC; parser/implementation E06. Hot reload, metrics и численные performance thresholds не являются открытыми обязательными задачами. Собственный orchestration/operator не добавляется.

Релизы/модули для проверки: [gRPC-Go 1.84.0](https://github.com/grpc/grpc-go/releases/tag/v1.84.0), [nats.go 1.54.0](https://github.com/nats-io/nats.go/releases/tag/v1.54.0), [protoc 36.2](https://github.com/protocolbuffers/protobuf/releases/tag/v36.2), [protobuf-go 1.36.12](https://github.com/protocolbuffers/protobuf-go/releases/tag/v1.36.12), [grpc generator module](https://github.com/grpc/grpc-go/blob/v1.84.0/cmd/protoc-gen-go-grpc/go.mod).

## Проверяемый результат подэтапа

Для каждого компонента определён язык/runtime, boundary и version candidate. Это проект, не build/runtime gate. Проверки E01/E02/E03: отдельные builds ETH/BSC/Bor, native seams, bindings, large records, Gateway/NATS и validators. Независимые expected results/evidence — IMPLEMENTATION_GATES. Rust graph отложен.

.04/.06/.07 — исторические архитектурные подэтапы. Текущие normative contracts: NATIVE_CAPTURE_PROTOCOL, DELIVERY_STATE_MACHINE, DEPLOYMENT_BOOTSTRAP_CONTRACT и IMPLEMENTATION_GATES. Следующая задача документации определяется PROJECT_STATUS; CFG live описан, JOB/history deferred. D64 навсегда ограничивает нашу работу идеальным ТЗ.
