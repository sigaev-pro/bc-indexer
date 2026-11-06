# Bootstrap, конфигурация и владение окружением

D83: перечисленные ниже pins — исходная исследованная база, не утверждение о latest stable. Отдельный scripts/refresh-versions по [TESTING_CONTRACT](TESTING_CONTRACT.md) проверяет и обновляет весь ComponentRegistry до точных stable pins через candidate lock и обязательный gate. Floating latest запрещён; incompatible update явно блокируется, существующий lock сохраняется. Здесь версии фактически не обновлялись, скрипт не реализован.

D82: приведённые ниже mainnet artifacts/команды — baseline трёх mainnet контуров. Для каждого testnet обязательны отдельные проверенные ProfileManifest/genesis/fork/bootstrap/supporting configs по [TESTING_CONTRACT](TESTING_CONTRACT.md); mainnet defaults не наследуются. Polygon Amoy использует собственную L1 Sepolia пару. ENV_READY и credentials привязаны к конкретному profile/network digest; отсутствующий testnet bootstrap блокирует запуск.

Polygon bootstrap также создаёт persistent supporting deployment IDs и read-only `/etc/extractor/polygon-sources.json` по CURRENT_SEMANTICS. Файл связывает роли/адреса/chain identities с deployment provenance; Geth RPC не выдаёт эти IDs и не доказывает роль supporting service. Identity-файлы находятся в существующих native volumes, новый volume не нужен.

D63 добавляет только control evidence внутри Bor adapter: доступ к собственному polygon-l1-geth HTTP RPC для hash-pinned finalized RootChain read. Новый сервис/volume/ETH tx extractor не добавляется. Строгие YAML keys — LIVE_CONFIG_SPEC; finality/GC — CURRENT_SEMANTICS. Bootstrap сохраняет проверенные RootChain address/child interval и supporting L1 identity вместе с конфигурацией; отсутствие online L1 задерживает GC, не готовую Polygon delivery.

2026-11-05 · DOC-REPAIR-04 · D43–D52/D57–D59. Нормативная детализация [состава нод](NODE_DEPLOYMENT_SPEC.md), [портов](NETWORK_PORTS.md) и [live YAML](LIVE_CONFIG_SPEC.md). Здесь описаны будущие init/runtime contracts; Compose/Kubernetes файлы, секреты, образы и процессы ещё не создавались.

## Source baseline и артефакты

Geth 1.17.9 `9dffe6e976cbbb87a84c5fd395448f0950290def`; BSC 1.7.8 `cdb7548b5baacfdae92f9f63437d6456411665f3`; Bor 2.10.3 `a16f123d9b4f41a552a3a98d93a0340430838576`; Prysm 7.3.0 `d01132449756e217d7f6c460e780174c34139b41` для обеих независимых пар. Остальные версии — [принятые кандидаты](research/DEPLOYMENT_VERSION_CANDIDATES.md). Source pin не заменяет patch commit/image digest/build proof.

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

## Environment init и частичный запуск

Окружение владеет одноразовым init service/job каждой роли. Runtime не вызывает Docker/Kubernetes API и не создаёт соседние процессы. Автомат:

`VALIDATE_INPUTS → LOCK_OWNER → PREPARE → NATIVE_INIT → INSTALL_CONFIG → VERIFY → ENV_READY`.

Manifest: init_schema, deployment_id, owner_role, network/genesis identity, native source/patch commit, input config digest/generation, bootstrap artifact identity, phase и effective paths. Phase markers и ENV_READY fsync сохраняются вместе с необходимым directory sync. Это marker окружения, не обещание атомарности произвольной native DB.

PREPARE создаёт staging внутри того же writable volume. Init работает в staging home, проверяет результат, затем публикует завершённые paths атомарным rename внутри volume; mountpoint не переименовывается. Native keys генерирует штатный init, только для действительно нового home. После crash init сверяет phase/manifest/существующие ключи и продолжает безопасный шаг. Не перегенерировать ключи при retry, не удалять чужой/nonempty datadir, не считать один факт существования каталога ENV_READY. Native-opened state с утраченным marker проходит identity reconciliation; автоматического повторного init/reset нет.

Замена config не копирует весь home: allowlist config/app/client files, проверка parser/effective values, generation digest, atomic file installation до startup. Genesis/key/state paths не входят в произвольную config replacement. Неизвестный incompatible state/config вызывает явную ошибку. No hot reload; изменения применяются restart соответствующего owner.

## Пути, права и volume ownership

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

## Native commands и effective config

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

Native parser определяет precedence TOML/YAML/args; preflight проверяет **effective** значения после этого precedence. Overrides не могут обойти chain identity, hash pin provider, ordinary Bor import и ownership ограничения [NATIVE_CAPTURE_PROTOCOL](NATIVE_CAPTURE_PROTOCOL.md). Неизвестные upstream options проверяются самим pinned parser. Полный rendered config digest и redacted effective settings входят в manifest.

Capture baseline Geth/BSC задаёт `--state.scheme hash --gcmode full`; Bor TOML — верхнеуровневый quoted key `"state.scheme"="hash"`, `gcmode="full"`, `syncmode="full"`. `history.state` — настройка path backend, она не заменяет durable pins. Сам hash scheme без retention patches тоже недостаточен. У supporting Polygon L1 нет capture pins, его профиль остаётся отдельным.

State scheme задаётся уже при создании native DB/genesis и проверяется в записанном store до ENV_READY: runtime override не конвертирует ранее созданный path store в hash. В BSC пустой default init выбирает path, поэтому явный init flag обязателен. [BSC init](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/cmd/geth/chaincmd.go), [ParseStateScheme](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/rawdb/accessors_trie.go).

Точные Bor overrides: `--pipeline.enable-import-src=false`, `--witness.parallelstatelessimport=false`, `--witness.syncwithwitnesses=false`, `--relay.enable-preconfs=false`, `--accept-preconf-tx=false`, `--sequencer.enabled=false`; по D60 дополнительно `--parallelevm.enable=false --parallelevm.enforce=false`. Соответствующие проверенные TOML keys: `[pipeline] enable-import-src`, `[witness] parallel-stateless-import`, `[relay] enable-preconfs`, `[jsonrpc] accept-preconf-tx`, `[sequencer] enabled`. Для остальных используется проверенный CLI, неизвестные TOML аналоги не придумываются. [Bor flags](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/internal/cli/server/flags.go), [config](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/internal/cli/server/config.go).

## Trust bootstrap и NATS permissions

Окружение создаёт отдельную private CA либо принимает операторскую CA. Private CA key хранится у bootstrap/operator, не монтируется в runtime. Server certificate SAN соответствует реальному service DNS/endpoint; client identity содержит deployment/network/role. Gateway проверяет CA/expiry/EKU и allowlist adapter identity для своей сети; одного доверия общей CA недостаточно. Cross-network/role certificate отвергается до handshake/state mutation. TLS hostname verification нельзя отключать. UDS, если явно выбран, проверяет filesystem ownership/peer identity и не разрешает чужую сеть через общий socket.

У каждой Geth/Prysm пары свой 32-byte JWT secret в hex; Polygon L1 не использует основной Ethereum JWT. Gateway получает только свои network-limited NATS credentials; adapter их не получает. Runtime secrets read-only с правами минимального owner. Rotation готовит новый secret generation, атомарно устанавливает files и рестартует затронутые процессы; CA rotation допускает ограниченный trust overlap. Смена NATS credentials всё равно проходит проверку прежнего account/binding. Рабочие secrets сейчас не генерируются.

NATS environment bootstrap и runtime Gateway используют разные credentials. Bootstrap создаёт только configured streams, сверяет существующие; compatible UPDATE допускается только для явно заданной config revision. Никаких автоматических DELETE/PURGE/reset при несовпадении. Runtime: publish в закрытый subject namespace своей сети; request `$JS.API.INFO`, `$JS.API.STREAM.INFO.<stream>`, `$SYS.REQ.USER.INFO`; subscribe только в собственный reply inbox prefix. CREATE/UPDATE/DELETE/PURGE/consumer admin runtime не получает. Environment обязан обеспечить UserInfo responder/permissions и проверяемую account identity; невозможность проверки блокирует публикацию, не заменяется сравнением URL. [NATS account API](https://docs.nats.io/reference/jetstream/api/account), [stream API](https://docs.nats.io/reference/jetstream/api/stream), [UserInfo](https://github.com/nats-io/nats.docs/blob/master/release_notes/whats_new_210.md).

Bootstrap выпускает общий immutable DestinationBinding, который оба owners сохраняют до capture. Expected account/stream/subjects/metadata/limits проверяются Gateway online. NATS server max_payload и stream max_msg_size должны пропускать фактические frame+headers; default проекта 2 MiB, plan size immutable. Отсутствующий/чужой stream даёт CONFIG_BLOCKED; bootstrap созданного окружения не является разрешением уничтожить прежнюю историю. Drain/cutover/Resume — только по [DELIVERY_STATE_MACHINE](DELIVERY_STATE_MACHINE.md).

D77: bootstrap вычисляет `extractor.network_digest` и `extractor.destination_id` из проверенного DestinationBinding в точных форматах [CONTROL_WIRE_CONTRACT](CONTROL_WIRE_CONTRACT.md); отдельные редактируемые значения этих полей в YAML collector/Gateway не добавляются. При первоначальном CREATE configured stream оба ключа входят в тот же StreamConfig. После CREATE/compatible UPDATE требуется read-back StreamInfo и проверка identity/subjects/limits перед успехом bootstrap. Если CREATE столкнулся с уже существующим stream или после crash повторяется init, используется ветка проверки существующего stream, не безусловный UPDATE.

У существующего stream оба ключа сначала проверяются. Missing/malformed/mismatch → CONFIG_BLOCKED с диагностикой; автоматическое добавление отсутствующей identity, перезапись чужого destination/network, DELETE/PURGE/reset/recreate запрещены даже для пустого stream. Compatible UPDATE по явно заданной config revision сохраняет две проверенные identity-метки и все посторонние metadata key/value из текущей конфигурации; серверная ошибка/limit не разрешают выбросить посторонние поля ради успеха. Такие конфигурационные изменения выполняются по штатному порядку владения окружением, не runtime Gateway. Повтор bootstrap не удаляет metadata, которые не перечислены в extractor descriptor.

При cutover новый stream заранее создаётся/проверяется с метками той же network и destination_id. Изменение binding generation или subject prefix не требует менять metadata generation/digest: таких обязательных ключей нет. Existing initialized binding с исчезнувшим stream остаётся ошибкой по AC-OPS-06-02; первоначальное создание нового целевого stream не выдаётся за восстановление старой истории. Runtime credentials по-прежнему не получают CREATE/UPDATE/DELETE/PURGE.

В NATS v2.15.1 `b2fd3efc7456b11b06a54877ad6f6ccfb911edb4` JetStream создаёт default internal system account при его отсутствии, USER.INFO service автоматически импортируется в обычные accounts. Gateway не нужны system credentials или wildcard `$SYS.>`: сравнивается поле `account` ответа (не display `account_name`). Bootstrap проверяет фактический responder/import/ACL своей конфигурации. [JetStream source](https://github.com/nats-io/nats-server/blob/b2fd3efc7456b11b06a54877ad6f6ccfb911edb4/server/jetstream.go), [UserInfo routing/response](https://github.com/nats-io/nats-server/blob/b2fd3efc7456b11b06a54877ad6f6ccfb911edb4/server/events.go).

## Startup, restart и наблюдаемость

Два независимых автомата:

- Native: ENV_VALIDATED → NATIVE_OPEN → RECOVERY_ONLY → SYNCING → BOUNDARY_H_COMMITTED → CAPTURING.
- Delivery: STORE_VALIDATED → LOCAL_RECOVERY → CONNECTING → DESTINATION_VERIFIED → REPLAY/PUBLISH.

Transient Gateway/NATS outage не блокирует safe local native capture. READY replay не ждёт нового live sync; recovery незакрытого native capture может ждать source inputs. Wrong local store/profile или local fatal прекращает native процесс; Gateway WAL fatal прекращает Gateway, безопасное накопление adapter WAL регулируется D28/D57. Permanent destination mismatch блокирует publication без переписывания pending. Ни один network owner не ждёт прогресса другой сети.

Bor и Heimdall запускаются после собственной config/identity проверки, а не по циклической зависимости health-ready друг от друга. Их native retry/sync зависимости сохраняются; отсутствие source readiness не означает live capture готов. Init/config/transport readiness не заменяет native sync или boundary H.

Replacement: stop → подтвердить завершение старого writer/освобождение volume → start. StatefulSet replicas=1 и force-delete Pod сами по себе не доказывают отсутствие старого writer. Compose исключает второй проект/процесс на том же volume; Kubernetes использует соответствующую стратегию обновления и storage attach fencing. Нет concurrent rolling writers. При недоказанном завершении старого owner новый не стартует. Удаление WAL/state для ускорения restart запрещено.

Готовность и остановка видны в logs по D52, без отдельного API/probe/metrics продукта. Обязательные control events: CONFIG_VALIDATED, INIT_PHASE, INIT_READY, STORE_OPENED, RECOVERY_STARTED/COMPLETED, SOURCE_SYNCING, LIVE_BOUNDARY, CAPTURE_STARTED, TRANSPORT_RETRY, DESTINATION_VERIFIED, BATCH_DURABLE, DELIVERY_CONFIRMED, DRAIN_STARTED/COMPLETED, STOPPED, FATAL. Control outcomes не исчезают при отключении debug. Fields: timestamp, event_code, process_instance, network, role, build/config generation, producer/run; применимые H/hash, batch/plan/binding/drain identities и error class. Secrets, полные credentials/argv не логируются. STOPPED после graceful завершения не обещается при SIGKILL; следующий startup обязан показать recovery.

## Будущие проверки

BOOT-01 — interrupted init в каждой phase, сохранение keys/state, wrong genesis/owner, отсутствующий ENV_READY. CONFIG-01 — native parser/precedence всех overrides, mounted config replacement, mandatory profile reject. MOUNT-01 — 14 owner volumes, одинаковый backing volume под разными paths, UID/permissions, read-only inputs. TRUST-01 — wrong SAN/CA/role/network, rotation и разные JWT пары. NATS-01 — минимальные runtime/bootstrap permissions, UserInfo identity, wrong destination, полный frame с headers. START-01 — 11 services с Gateway/NATS outage, Bor/Heimdall взаимный retry без startup deadlock. REPLACE-01 — задержанный старый writer и новый Pod. LOG-01 — полный обязательный lifecycle по logs без debug/секретов. ARTIFACT-01 — hashes всех materialized artifacts, native init/parse и совместный запуск pinned builds.

Эти критерии закрывают проектные OPS-02…12; E01/E06/E07 не завершены. Image/toolchain/ABI locks, genesis payload proof и runtime smoke tests остаются явными gates, а не скрытыми предположениями о работающей поставке.
