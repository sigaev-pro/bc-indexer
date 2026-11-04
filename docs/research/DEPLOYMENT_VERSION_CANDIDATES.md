# Принятые кандидаты версий — D47

2026-10-21. Пользователь принял набор после проверки официальных release notes и manifests. Это документальная проверка, без сборок и запуска. Таблица определяет текущие версии вместо прежних кандидатов Geth 1.17.8/Bor 2.10.2; старые исследования сохраняют свои исходные pins.

| Компонент | Принятая версия | Источник и основание |
|---|---|---|
| Geth, основной Ethereum и отдельный Polygon L1 | 1.17.9 | [Release](https://github.com/ethereum/go-ethereum/releases/tag/v1.17.9): исправления повреждения БД при потере питания и конкурентного чтения состояния. Изменён EVM opcode dispatch; native hooks требуют повторной сверки. Указанная активация Amsterdam относится к Hoodi, не к Mainnet. |
| Prysm, обе независимые пары | 7.3.0 | [Release](https://github.com/OffchainLabs/prysm/releases/tag/v7.3.0): исправления с рекомендацией обновления mainnet beacon operators. [go.mod](https://raw.githubusercontent.com/OffchainLabs/prysm/v7.3.0/go.mod): Go 1.26.5. Внутренняя зависимость от go-ethereum не определяет версию отдельного Geth-процесса. |
| BSC Mainnet | 1.7.8 | [Release](https://github.com/bnb-chain/bsc/releases/tag/v1.7.8): сохранён прежний кандидат Mainnet/Testnet. [1.8.1](https://github.com/bnb-chain/bsc/releases/tag/v1.8.1) адресован Chapel testnet operators; это не основание автоматически менять Mainnet baseline и не утверждение о невозможности его запуска в Mainnet. |
| Bor | 2.10.3 | [Release](https://github.com/0xPolygon/bor/releases/tag/v2.10.3): security/stability, исправления balance reads при selfdestruct в BlockSTM и обработки Heimdall timeout. [go.mod](https://raw.githubusercontent.com/0xPolygon/bor/v2.10.3/go.mod): Go 1.26.8. Native capture audit обновить. |
| Heimdall-v2 | 0.12.2 | [Release](https://github.com/0xPolygon/heimdall-v2/releases/tag/v0.12.2): прежний кандидат, Go 1.26.8. Публикация DockerHub images удалена upstream; registry/digest либо собственная сборка требуют отдельного выбора. |
| NATS server | 2.15.1 | [Release](https://github.com/nats-io/nats-server/releases/tag/v2.15.1). Версии server, Go SDK и Helm chart различаются; D47 не выбирает новый chart. |
| RocksDB | 10.4.2 | [Release](https://github.com/facebook/rocksdb/releases/tag/v10.4.2). Прежний кандидат сохранён; совместимость binding не проверялась сборкой. |
| gRPC-Go | 1.84.0 | [go.mod](https://raw.githubusercontent.com/grpc/grpc-go/v1.84.0/go.mod): Go >= 1.25.0. |
| nats.go | 1.54.0 | [go.mod](https://raw.githubusercontent.com/nats-io/nats.go/v1.54.0/go.mod): Go >= 1.26.0. |
| protoc | 36.2 | [Release](https://github.com/protocolbuffers/protobuf/releases/tag/v36.2). |
| protobuf-go | 1.36.12 | [Release](https://github.com/protocolbuffers/protobuf-go/releases/tag/v1.36.12). |

## Границы проверки и продолжение

- Geth/Prysm, Bor/Heimdall и NATS/SDK не проходили совместную сборку или runtime-проверку. Версии не являются доказанной совместимой поставкой.
- Старые commit hashes в CLIENT_MATRIX относятся к старому source audit. Полные source pins новых релизов, собственные adapter patch commits и immutable image digests ещё закрепить; старые hashes нельзя переименовать под новые tags.
- Go 1.27.2 для Gateway остаётся прежним кандидатом. Native toolchains проверяются по каждому клиенту отдельно; новый Geth toolchain здесь не подтверждён. Внутренние библиотеки клиентов не принудительно выравниваются с Gateway.
- grocksdb 1.10.2 остаётся прежним baseline из [ROCKSDB_STACK](ROCKSDB_STACK.md), без новой проверки binding в этом исследовании. Полный dependency lock, включая protoc-gen-go-grpc, не закрыт.
- После D49–D52 порты/P2P/defaults и init-модель выбраны; отдельная probe/shutdown спецификация снята. Следующий документальный шаг DEP-03.2b.2b.3b: точные native flags/config keys/init/mount mappings, source pins и delta audit; затем DEP-04. Это актуализация задач, не повторная upstream-проверка кандидатов D47. Изменение принятых кандидатов требует согласования.
- Сборки, функциональные тесты и deployment выполняются лишь после отдельного разрешения на реализацию; сейчас сохраняются их задачи, не результаты.
