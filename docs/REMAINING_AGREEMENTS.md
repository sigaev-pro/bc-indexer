# Состояние согласований после D63

2026-11-05. Live-only Ethereum/BSC/Polygon до NATS. D16–D63 повторно не открываются. [Реестр исправлений](DESIGN_REPAIR_STATUS.md) и [traceability/gates](IMPLEMENTATION_GATES.md) фиксируют проект каждого из 41 пунктов. Source inspection и документальная сверка не являются runtime proof. Неотвеченных продуктовых вопросов текущего раунда нет; E01–E07 ещё TODO и требуют отдельного разрешения на реализацию.

## Последний список вопросов закрыт

1. **Готовность:** по логам; native H+1 и независимость сетей остаются. Отдельная probe-спецификация/детектор зависания не требуются сейчас.
2. **Управление:** стандартные Docker Compose/Kubernetes запуск, остановка и restart. Отдельный API/управляющий сервис и собственные shutdown таймауты не детализируются. Сохранение pending и crash recovery остаются.
3. **Сообщение:** default `transport.max_frame_bytes=2097152` в YAML Gateway, NATS max_payload/stream MaxMsgSize согласованы на 2 MiB. Большие записи разбиваются. Pending frames не перекодируются; несовместимое уменьшение лимита — явная ошибка до публикации.
4. **Документы:** локального Git достаточно, remote/дополнительный backup сейчас не требуется. E00-05 закрыт как процесс; публикация не разрешена.
5. **Дальнейший результат:** D53/D56 — проектирование, аудит и исправление документов. DOC-CONSOLIDATE завершён: [единое полное ТЗ v3](../evm-transaction-extractor-tz-v3-codex.md); реализация запрещена без последующего указания.
6. **Смена назначения:** D54 — NATS account/stream/subject mapping меняются только после завершения pending адаптера и Gateway; другой адрес того же назначения допустим после проверки. Перенос pending не проектируется.

## Закрытые решения D62–D63

- BSC: early VotePool и header-attested finality публикуются раздельно; GC только по header-derived Source.
- Комиссии: фактические gas purchase/return/recipient credits сохраняются отдельно; receipt summary не добавляется к ним повторно.
- Polygon: recovery-state GC после checkpoint, проверенного в finalized состоянии собственного supporting Ethereum L1. Публикация tx/status не ждёт этого GC.
- Точные IDs, wire/control/cutover, supporting identities и 63 критерия приёмки определены; открытых вопросов текущего раунда нет.

## Проработанные механизмы и оставшиеся доказательства

Нормативные механизмы — [native capture](NATIVE_CAPTURE_PROTOCOL.md), [delivery](DELIVERY_STATE_MACHINE.md), [bootstrap](DEPLOYMENT_BOOTSTRAP_CONTRACT.md). Порядок разработки и независимые fixtures/evidence — IMPLEMENTATION_GATES. Исторический аудит сохраняет исходные находки; текущий статус берётся из DESIGN_REPAIR_STATUS.

- **DEP-03.2b.2b.3b:** init/config/paths/UID/mounts определены; generated файлы и native parser/runtime checks — E01/E06.
- **Pins/артефакты:** exact native source refs закреплены, Heimdall обновлён D58. Toolchain/dependency/image locks и полный payload proof mainnet genesis — E01/E06, не выдуманные результаты документации.
- **Capture/delivery:** admission/pins/decision markers, owned snapshots, Prepare/Release/fencing, drain/cutover/Resume спроектированы. Power-loss/native completeness доказательства — E01/E03/E05.
- **Typed lifecycle:** контракт source/actions/revisions/terminal Removed и GC определён. Proto field numbers/компиляция/валидаторы и native outcomes — E02/E04/E05.
- **DEP-04 / E00-11:** документальная сверка, критерии и единый root MD завершены. Принятие документов не запускает E01; неизвестных ответов пользователя сейчас нет.

Закрытые развилки D57–D60: local fatal останавливает ноду; отдельный drain; несовместимый upgrade после drain при читаемом оставшемся store, автоматических migrations нет; native retention и safe-point yield; Heimdall v0.13.2; Bor ordinary import без BlockSTM; ACKed history не мигрирует. Новый вопрос нужен при новой развилке поведения/ослаблении guarantee, а не для каждого технического шага.

## Не возвращать в текущие согласования

История/догрузка пропусков, Base/Arbitrum, ClickHouse/downstream, production HA/мульти-ДЦ, production-методика тестирования, требования производительности, лицензия и публичная open-source поставка остаются отложенными либо вне scope. Optional изменения балансов вне транзакций не становятся обязательным gate. Выбранные RocksDB, независимые WAL/Gateway, порты, клиентские кандидаты, layout и init-модель повторного подтверждения не требуют.
