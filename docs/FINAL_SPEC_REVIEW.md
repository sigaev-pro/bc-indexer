# Финальная сверка ТЗ — DOC-FINAL-02

2026-11-05. Область: проектирование и документация ETH/BSC/POLY live extractor до NATS. [Единый нормативный документ](../evm-transaction-extractor-tz-v3-codex.md); исходный v2 неизменен. Реализация, сборки, runtime, deployment и push не выполнялись.

## Документальный результат

- 41/41 находки исходного аудита имеют статус SPECIFIED и mapping на fixture/этап в [IMPLEMENTATION_GATES](IMPLEMENTATION_GATES.md). Это число относится к аудиту, не ко всему продукту.
- [ACCEPTANCE_REQUIREMENTS](ACCEPTANCE_REQUIREMENTS.md): 63 уникальных требования — scope 6, capture 10, данные 10, lifecycle 8, delivery 14, эксплуатация 10, разработка 5. Все runtime verdict NOT_RUN.
- Приняты D62: два источника BSC finality и полная детализация фактических комиссионных движений. Принят D63: Polygon recovery-state GC по checkpoint, проверенному в finalized состоянии собственной supporting Ethereum L1. Неотвеченных пользовательских вопросов текущего раунда нет.
- Root v3 содержит текущие данные, native capture, durable delivery, ID/wire/control, YAML, полный состав нод/bootstrap/порты/стек, REC, gates, E01–E07 и приёмку. Отложенные сети/исторические режимы не являются текущими обязательными payload или задачами реализации. Старые проектные варианты помечены как исторические.

## Проверенные сквозные сценарии документации

| Сценарий | Определённый контракт / граница |
|---|---|
| Первый запуск и live boundary | Проверенный bootstrap и native readiness → durable H → новые live H+1; sync-история не экспортируется |
| Обычная/failed/system tx, пустой блок | Pinned native paths; обязательные source encodings/receipt/calls/actions/state; metadata пустого блока |
| Большая транзакция и давление памяти | Owned chunks, безопасная VM pause вне hooks; bounded buffers, без disk/network операций в hooks |
| Crash до/после admission, execution и native commit | Persistent state/input pin, capsules/dependency log, EXEC_READY и native decision marker; targeted recovery принятого obligation |
| Reorg, reinclusion и поздний status | Activation/revision/decision identity, Removed для durable Applied независимо от NATS ACK; новая activation после reinclusion |
| Финальность и очистка | ETH actual setter, BSC header Source, Polygon L1-finalized checkpoint; подтверждённая regression ниже durable anchor — fatal; source outage задерживает GC |
| ACK/retry/store loss | Immutable READY/SEALED bytes; full manifest/layout validation; IMPORT_SEALED без новой reservation; durable ACK/outbox/Release |
| Drain/cutover/restart | Завершение обеих pending очередей; desired binding не активируется сам; сохранённый intent, MAINTENANCE recovery lost reply, новый LIVE admission после Resume |
| Обновление версии или лимита | Pending bytes не меняются; incompatible wire upgrade после old-build drain; drain сам не мигрирует persistent store |
| Ошибка диска/нормализации | Явная fatal ошибка owner; отсутствие успешного ACK/skip; никакого repair с отбрасыванием obligations |
| Три сети и supporting-компоненты | Независимые stores/processes; 11 services / 14 volumes; own Polygon L1 routing и bootstrap deployment IDs, без фиктивной RPC-аттестации |

## Проверки документов

Проверены 55 Markdown-файлов, 420 локальных links (в том числе 24 anchors), совпадение 13 включённых нормативных разделов root с тематическими файлами, уникальность 63 REQ ID и покрытие 41 audit ID, структура таблиц, отсутствие устаревших active next steps, git diff --check. Ошибок этих проверок нет. SHA-256 v2: `ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634`.

Source inspection закреплённых клиентов использован для точных контрактов, включая BSC milliseconds, фактические setter paths, Bor milestone recovery и checkpoint commitment. Это не runtime proof и не независимая проверка консенсуса.

## Что остаётся при реализации

E01–E07 полностью впереди: независимые builds/toolchain locks, native pin/admission/ownership/decision proofs, compiled schema/validators, durable delivery, полный corpus трёх сетей, crash/reorg/GC matrix, generated окружения и end-to-end приёмка. Immutable image digests, ABI/toolchain compatibility, полный bootstrap genesis payload и power-loss durability нельзя объявлять проверенными по тексту ТЗ.

После отдельного указания пользователя первая задача — E01-01, затем E01-02 отдельно для ETH/BSC/POLY. Отрицательный proof останавливает зависимый этап и возвращает проектное противоречие на уточнение. Завершение документации не является разрешением реализации или обещанием отсутствия будущих дефектов.
