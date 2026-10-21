# Стек компонентов: проектный baseline E00-03.03

> D42 (2026-10-21): текущий этап — Ethereum Mainnet, BNB Smart Chain (BSC) Mainnet и Polygon PoS Mainnet. Base/Arbitrum и их адаптеры/fixtures отложены; исследования и типизированные расширения ниже сохранены справочно, не блокируют текущую версию. Исторические сценарии отложены D38. Действующие live-типы транзакций ETH/BSC/Polygon, включая native system types, остаются обязательными. У каждой сети собственные WAL и журнал Gateway.

> D26–D31 (2026-10-16): [текущие требования доставки и восстановления](DELIVERY_RECOVERY.md). Restart с сохранённым диском автоматический; после потери диска история задаётся заново N–M. WAL копится без TTL/прикладной квоты до подтверждения NATS всех сообщений/частей блока. После ACK payload и временные статусы очищаются; остаётся минимальное рабочее состояние для restart. Сейчас один NATS; HA/backup и дальнейшая судьба подтверждённых данных вне scope. Backend принят D33: RocksDB. E00-03.06 завершён как проект: [pins](research/ROCKSDB_STACK.md), [протокол и REC-T01–16](ROCKSDB_DELIVERY_PROTOCOL.md).

> Актуальная граница D23–D25 (2026-10-16): live и history работают в разных pod/process; история запускается простой командой N–M. Пересечения и повторные загрузки допускают дубли. Дедупликация и независимая проверка полноты блока выполняются за пределами сборщика. Здесь проверяется доставка подготовленных сообщений: RECEIVED → WAL_SAVED → NATS_ACKED. BlockComplete/coverage report и общий координатор устранения overlap не требуются. Подробности: [LIVE_HISTORY_DELIVERY](LIVE_HISTORY_DELIVERY.md).

2026-10-15. Технический выбор для последующей реализации; версии проверены по upstream manifests/releases, но совместная сборка и runtime не проверены. D21/D22 принимают Compose + Kubernetes и NATS внутри обеих сред. Эти ответы не утверждают HA, replicas, retention и storage backend.

## Компоненты и интеграция

| Компонент | Baseline | Реализация / граница |
|---|---|---|
| Ethereum native adapter | Go внутри Geth v1.17.8 | Native hooks/state journal + canonical notifications; отдельный client build |
| Ethereum consensus | Prysm, выбран D44; версия/образ ещё не закреплены | Отдельный компонент основной Ethereum-ноды; layout контейнеров — DEP-03 |
| Polygon L1 источник | Отдельный Geth + Prysm, D44 | Собственная Ethereum-нода Polygon без нашего Ethereum-адаптера, со своими процессами/datadirs; основная Ethereum-нода не используется |
| BSC native adapter | Go внутри BSC v1.7.8 | Обычные tx и Parlia Finalize capture; отдельный client build |
| Отложено D42: Base native adapter | Rust 1.96.0, base/base v1.5.0 + pinned base/reth | Native executor + включённый Inspector, per-tx results, canonical ExEx |
| Отложено D42: Arbitrum Nitro adapter | Go в Nitro v3.12.1 с его собственным Geth; native Rust/cgo/WASM build dependencies | ArbOS/Stylus/native special paths; отдельная сборка и fixtures |
| Отложено D42: Arbitrum Classic adapter | Go + C++ AVM, Classic v1.4.7 candidate | Native result reader + consensus-neutral observer candidate; отдельный history runtime той же сети |
| Polygon adapter | Go внутри Bor v2.10.2, Heimdall-v2 v0.12.2 | Native tx и StateSync/Finalize, отдельный optional legacy scope |
| Canonical Coordinator | Библиотечный модуль адаптера/контура соответствующей сети | Отдельный общий сервис для всех сетей не вводится. Каждый producer/run владеет своим lifecycle state; единого владельца для устранения пересечений нет; см. STATE_OWNERSHIP |
| Gateway/publisher | Go 1.27.2, gRPC-Go 1.84.0, nats.go 1.54.0; один экземпляр роли publisher на сеть | Единый исходный код/binary для всех сетей, отдельный процесс и конфиг каждой сети; NATS SDK только здесь |
| Wire schema | Protobuf proto3 с explicit presence и closed oneof | Одна versioned schema, native validators, transaction/block metadata/lifecycle/parts из MESSAGE_CONTRACT |
| Генерация Go | protoc 36.2 + protobuf-go/protoc-gen-go 1.36.12 | protoc-gen-go-grpc из согласованного source release grpc-go; точный module tag/commit и lock фиксируются E01/E02, не подменяются версией server SDK |
| Отложено D42: Rust transport/codec | tonic 0.14.6 + tonic-prost/tonic-prost-build 0.14.6; prost/prost-build 0.14.4 | Общая proto schema; встройка в Base Cargo graph проверяется E01/E02, upstream lock клиента не обновляется автоматически |
| Adapter → Gateway | gRPC streaming; локальный UDS при совместном размещении, TCP+mTLS при сетевом соединении | Оба конца проверяют schema/profile/размер/целостность; production topology определяет endpoint, не семантику данных |
| WAL/publication journal | RocksDB 10.4.2; Go grocksdb v1.10.2; Rust rocksdb 0.24.0 / librocksdb-sys 0.17.3+10.4.2 | Sync WriteBatch, WAL enabled, отдельный каталог owner; [pins/API/build gates](research/ROCKSDB_STACK.md), [protocol](ROCKSDB_DELIVERY_PROTOCOL.md). BlobDB optional, backup исключён D27 |
| Приёмочный harness | Go + тот же protobuf decoder + JetStream client | Только проверка опубликованных сообщений против fixtures; не промышленный downstream |
| Локальный запуск | Docker Compose: все execution/consensus/supporting-компоненты трёх нод, адаптеры, WAL/Gateway и NATS | Полная компоновка и конфигурация проекта по D43; [детализация](NODE_DEPLOYMENT_SPEC.md) |
| Production | Kubernetes: все компоненты нод, адаптеры, независимые workload/volume/config контуры сетей и NATS | D43: готовим весь технический комплект; NATS chart/server candidates и [исследование](research/NATS_DEPLOYMENT_STACK.md) сохраняются; production-методика отдельно |

Native pins и расхождения toolchains подробно сохранены в [CLIENT_MATRIX.md](CLIENT_MATRIX.md). Classic runtime ещё не доказан — [исследование](research/ARBITRUM_CLASSIC_CAPTURE.md). Новый baseline Gateway не изменяет compiler versions native клиентов.

## Почему SDK не встраивается во все native clients

Pinned gRPC-Go 1.84.0 требует Go 1.25.0; nats.go 1.54.0 — Go 1.26.0. protobuf-go 1.36.12 требует Go 1.23. Поэтому импорт нового NATS SDK в Geth/BSC candidate с Go 1.25 создаёт лишнее требование смены toolchain. Адаптер знает gRPC/Protobuf, Gateway — NATS; процессная граница уже принята D19. Генерируемые Go bindings всё равно проверяются в каждом client module отдельно.

Источники: [grpc-go go.mod](https://github.com/grpc/grpc-go/blob/v1.84.0/go.mod), [nats.go go.mod](https://github.com/nats-io/nats.go/blob/v1.54.0/go.mod), [protobuf-go go.mod](https://github.com/protocolbuffers/protobuf-go/blob/v1.36.12/go.mod), [Go distributions](https://go.dev/dl/?mode=json). Go 1.27.2 — кандидат общего сервиса на дату проверки, не утверждение об установленном компиляторе проекта.

У tonic 0.14.6 workspace MSRV 1.88, у prost 0.14.4 — 1.85; это ниже source toolchain Base 1.96.0. Этого достаточно для предварительного выбора, но не для доказательства Cargo dependency compatibility. Точный lock/dependency graph проверяется в E01; при конфликте меняется транспортный crate pin, а не молча весь upstream runtime.

Источники: [tonic workspace](https://github.com/grpc/grpc-rust/blob/tonic-v0.14.6/Cargo.toml), [tonic-prost](https://github.com/grpc/grpc-rust/blob/tonic-v0.14.6/tonic-prost/Cargo.toml), [tonic-prost-build](https://github.com/grpc/grpc-rust/blob/tonic-v0.14.6/tonic-prost-build/Cargo.toml), [prost](https://github.com/tokio-rs/prost/blob/v0.14.4/Cargo.toml).

## Версионирование и проверка

- Исходники native clients остаются на отдельных закреплённых commits; общий транспортный модуль не требует слияния их несовместимых Go/Cargo dependencies в один build graph.
- Теги библиотек выше — кандидаты на дату проверки. Перед воспроизводимой сборкой E01 записывает commit/checksum, compiler, generator versions, dependency lock и image digest. В конфигурации поставки не используются плавающие `latest`.
- Версии protoc, Go/Rust generators, runtimes и wire schema — разные величины. Они явно перечисляются в build manifest, не выводятся друг из друга.
- Go и Rust fixtures проверяют одинаковую семантику полей/presence/oneof; protobuf bytes не обязаны быть одинаковыми после независимой сериализации. Для retries сохраняются исходные bytes/digest конкретного события.
- NATS server version, Helm chart version и appVersion фиксируются отдельно. Наличие Helm chart не выбирает автоматически cluster sizing, storage class или политику потери диска.
- Конкретный Linux base image, CPU architectures и поддерживаемые Kubernetes/Compose versions закрепляются после build smoke tests; не заявляется поддержка каждой платформы из-за наличия Dockerfile.
- YAML schema/reload и их библиотеки остаются E00-07/09; метрики и численные пороги — E07. Не добавляем orchestration/operator как обязательный собственный runtime-сервис.

Релизы/модули для проверки: [gRPC-Go 1.84.0](https://github.com/grpc/grpc-go/releases/tag/v1.84.0), [nats.go 1.54.0](https://github.com/nats-io/nats.go/releases/tag/v1.54.0), [protoc 36.2](https://github.com/protocolbuffers/protobuf/releases/tag/v36.2), [protobuf-go 1.36.12](https://github.com/protocolbuffers/protobuf-go/releases/tag/v1.36.12), [grpc generator module](https://github.com/grpc/grpc-go/blob/v1.84.0/cmd/protoc-gen-go-grpc/go.mod).

## Проверяемый результат подэтапа

Для каждого компонента определён язык/runtime, integration boundary и version candidate либо явная внешняя зависимость. Это закрывает .03 как проектный baseline, но не build/runtime gate. Проверки E01/E02: сборка каждого адаптера с bindings, совместимость Rust graph, gRPC transfer large record, независимый Gateway/NATS SDK build, одинаковая валидация wire fields и packaging с закреплёнными версиями.

.04 задаёт владельцев состояния; .06 закрепляет RocksDB/pins и протокол доставки/очистки; .07 завершает [архитектурную сверку](E00_03_ARCHITECTURE_REVIEW.md). Совместные сборки и runtime tests остаются E01/E03. Следующий шаг ТЗ — CFG/JOB-01, не реализация.
