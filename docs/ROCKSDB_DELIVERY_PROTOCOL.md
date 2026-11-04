# RocksDB: запись, доставка и восстановление — E00-03.06

> D38 (2026-10-21): загрузка истории и пропусков отложена. History N–M, replay диапазонов и history recovery ниже — будущий контекст, не требования текущего этапа. Сейчас только live-сбор, единая типизированная нормализация и доставка в NATS. Восстановление pending WAL и live reorg/finality сохраняются.

2026-10-16. Проектный контракт по D26–D33, не реализованный код. Native clients, gRPC/protobuf и NATS остаются в ранее принятой топологии. Только сообщения, доставка и минимальное рабочее состояние; никакой downstream БД, проверки полноты native блока или устранения overlap.

## Стек и локальное состояние

Pins и source evidence: [ROCKSDB_STACK](research/ROCKSDB_STACK.md). Обычный RocksDB; integrated BlobDB выключен в baseline. Один default column family с префиксами, один serialized writer на каталог. WAL включён, sync=true для всех изменений, влияющих на recoverability, подтверждения или удаление. Sync WriteBatch — атомарный commit; отдельный Flush memtable перед каждой отправкой не нужен. Внутренние WAL/SST обслуживает RocksDB; приложение не удаляет их по NATS ACK.

Каждый producer имеет отдельный persistent каталог; Gateway сети — свой. Kubernetes: один writer, сохраняемый volume, без совместного writable каталога и без параллельного rolling replacement. Compose: named volume. Shared filesystem с несколькими writers не поддерживается. RocksDB lock предотвращает второй opener одного каталога; это не distributed lease. Замена writer разрешена после подтверждённой остановки старого процесса и передачи volume. Split-brain/автоматический multi-node failover не обещаются.

RocksDB work вынесен из consensus-critical callback и async event loop в ограниченный worker. Очереди RAM и сетевые запросы ограничены; ожидающие сообщения лежат на диске. Замедление RocksDB/ошибка диска не превращается в успешный capture. Отсутствие квоты WAL не означает отсутствие физических write stalls.

## Identity и ключи

- producer_id и run_id генерируются один раз при создании состояния и сохраняются sync; restart с диском использует те же значения. Повтор команды и запуск после потери producer-диска создают новые ID.
- local batch_seq — последовательность delivery batch данного run; не высота блока. Reorg/status тоже получают новые batch_seq. batch_id=(producer_id,run_id,batch_seq).
- event_id=(producer_id,run_id,event_sequence): последовательность резервируется durable вместе с подготовкой. Исходные Envelope bytes и digest неизменны; транспортные parts/layout фиксируются позднее по publication plan Gateway (D52). Неиспользованные sequence допустимы, повторное использование запрещено.
- Prefixes producer: identity/job; capture_checkpoint; batch/<seq>/manifest,state; message/<seq>/<event>/<part>; publication_plan/<seq>; ack_cleanup/<seq>; release_outbox/<seq>. Manifest содержит block hash/parent/height и список фактически подготовленных references, не независимый native inventory.
- Native lifecycle bridge дополнительно владеет минимальным lifecycle_state: references наблюдаемых активаций, counter и branch/status checkpoint. Изменения этого состояния фиксируются вместе с READY соответствующего observation batch. ACK cleanup не удаляет references, ещё необходимые для корректного Removed/Status/reinclusion; native профиль задаёт момент их безопасного удаления или воспроизводимый способ восстановления. Это рабочее состояние, не payload или архив доставленных блоков. Точные network finality/late-status правила проверяются в E02/E05; нельзя считать payload WAL единственным источником lifecycle identity.
- Prefixes Gateway: instance_epoch/counter; active_session; batch/<id>/plan,frames,ackset; release_frontier/<run>. Payload Gateway временный, producer остаётся источником повторной отправки до IngestAck.

## 1. Capture и durable READY

1. Перед приёмом новых событий создать или открыть состояние owner. History хранит явные N, M и run_id; live — identity/capture checkpoint и pending состояние. D36 отменяет default три часа/start_block и auto-history при старте. Повтор отправки pending после restart сохраняется. D37: поиск и догрузка новых блоков за время остановки live вне scope; восстанавливаются pending записи и конкретные ранее начатые batch, не весь разрыв до head.
2. Получить native данные, нормализовать, сохранить STAGING, неизменяемые Envelope bytes и references bounded sync WriteBatch пакетами. Фрагментация локального хранения не является transport part layout: его определяет Gateway на Prepare по D52. RECEIVED не является durable гарантийным состоянием. Для восстановления конкретного незакрытого capture необходим durable replay descriptor; при crash до его записи механизм обнаружения наблюдения пока не описан. Это открытый технический пробел, а не разрешение пропускать событие. До закрытия REC-T01 требуется определить и проверить точку durable intent и её согласование с native callback без сканирования разрыва до head; точные native механизмы — E01/E03.
3. После успешного сохранения всех фактически подготовленных записей одним sync WriteBatch поставить READY/closed marker и продвинуть capture_checkpoint. Это WAL_SAVED. Для пустого блока batch содержит metadata; для lifecycle события используется такой же delivery protocol.
4. Из STAGING отправка запрещена. После crash восстановить native capture конкретного незакрытого STAGING по сохранённой block identity/replay descriptor, не сканируя разрыв до head; IDs новой попытки не переиспользуют старые sequence. Публикации из незакрытого batch не было.
5. Capture checkpoint — позиция native источника плюс branch identity, не максимальная увиденная высота. При restart native bridge проверяет актуальность ветки и восстанавливает соответствующие lifecycle observations. Native import и RocksDB не образуют общую транзакцию: незавершённый ранее начатый capture восстанавливается повторным native чтением/replay конкретной записи. D37 исключает автоматическое сканирование/восполнение отсутствующего периода live. Если required state недоступен — явная ERROR, без пропуска до текущей головы.

READY сохраняется независимо от состояния NATS. Новые блоки продолжают поступать в новые batch, пока физически возможна запись; нет TTL, прикладной квоты или удаления pending по возрасту.

## 2. Регистрация publication plan

Сохраняется ранее выбранный chain_sequence Gateway. Чтобы потеря его журнала не меняла bytes уже опубликованного события, mapping перед публикацией сохраняется также у producer.

1. Producer посылает PrepareBatch с batch_id, manifest digest и описанием неизменных событий: IDs, digests, размером Envelope bytes и видом события для маршрутизации. Это служебный gRPC обмен, не публичное NATS событие; точная wire schema определяется в E02/E03.
2. Gateway атомарно резервирует последовательности и plan с publication_epoch, logical destination и part layout по своему max_frame_bytes (D52). Plan учитывает полный размер frames/headers, а не только data. Epoch создаётся при создании нового Gateway store; при restart с диском сохраняется. chain_sequence уникален только в пределах epoch, не является blockchain order.
3. Producer сериализует TransportFrame с полученным plan, сохраняет точные frame bytes пакетами и sync закрывает plan с batch. Только после этого разрешён PublishBatch(plan,frames). Незакрытый plan после crash строится заново, исходный READY batch остаётся. Потеря ответа Prepare до producer commit не могла вызвать публикацию; reservation можно повторно получить или создать заново после потери Gateway store.
4. Gateway принимает только совпадающие identity/digest/layout; один event_id с другими исходными bytes — ERROR. Сохраняет plan и frames пакетами, а затем CLOSED marker sync. Только CLOSED batch допускается к публикации. Не нужно держать блок целиком в RAM.
5. После потери Gateway-диска producer повторно передаёт свой durable plan и frames. Gateway принимает старый publication_epoch как replay identity только после проверки того же logical destination; для новых событий выделяет новый epoch. Контракт сохраняет bytes/ID и не требует вечного registry доставленных событий. Потеря журнала Gateway не разрешает перенаправить pending в другое назначение.

TransportFrame дополняется publication_epoch. Пара (publication_epoch,chain_sequence) — локальный порядок регистрации Gateway, не сортировка всех событий сети навсегда. Точные сериализованные frame bytes фиксированы producer до первой публикации; новая версия Gateway не пересериализует pending frame. Sequence не участвует в вычислении event_id. NATS message ID — стабильное кодирование network/event_id/part_index (или complete).

## 3. PubAck и IngestAck

Streams/credentials создаются deployment-конфигурацией Kubernetes/Compose, не динамически загрузчиком. Gateway при старте проверяет network subjects, доступность JetStream и допустимый frame size; ошибка конфигурации не удаляет pending данные. Топология: один NATS, file stream, replicas=1. Publisher имеет права публикации и получения PubAck; административное изменение stream не требуется. Post-ACK retention/consumers не входят в гарантию сборщика; loader не ждёт consumers и не управляет их данными.

Gateway публикует CLOSED frames с ограничением in-flight RAM. Каждый успешный PubAck учитывается по конкретному message/part reference. Нет ACK — запись остаётся pending; timeout, disconnect и потеря ответа приводят к retry с capped exponential delay/jitter, без лимита числа попыток. Ошибка schema/permissions/размера — явная ERROR с сохранением данных; её нельзя лечить усечением или изменением layout. После исправления настройки retry возобновляется.

ACK bitmap разрешено сохранять пакетами: потеря последней недолговечной отметки вызывает повтор, но не потерю данных. Каждый ACK относится к message/part и logical destination durable plan; подтверждения из разных account/stream/subject mapping не суммируются. После всех PubAck в этом назначении нужен sync commit NATS_ACKED с manifest digest и plan identity; только после него Gateway отправляет IngestAck(batch_id,digest,plan identity). Запись NATS_ACKED допускает удаление Gateway payload; минимальный ACK receipt сохраняется до release.

Producer принимает только ACK всего своего закрытого batch с совпадающими references/digest/plan. gRPC receipt, ACK части и сетевой write success не являются ACK блока. Никакого consumer ACK или block completeness verifier здесь нет.

D54: несовместимая смена назначения при pending у любого owner отклоняется до публикации с сохранением данных. До перенастройки останавливаются новые наблюдения и завершается доставка в прежнее назначение; необходима сверка durable состояния адаптера и Gateway, включая восстановление после потери ответа. URL/credentials могут меняться при сохранении того же назначения и проверке этой идентичности. Точные идентификаторы account/stream, startup/reconnect checks и подтверждение отсутствия pending — технический остаток E03, а не выполненная реализация. Перенос pending не входит в текущий scope. [Конфигурационный контракт](LIVE_CONFIG_SPEC.md).

## 4. Очистка без архива доставленных блоков

1. Producer sync фиксирует ACKED_CLEANUP и release_outbox; после этого payload можно удалять. Маленький batch можно удалить вместе с этой записью одним WriteBatch. Крупный — bounded пакетами с durable cleanup cursor; crash возобновляет уборку, не публикацию уже ACKed batch.
2. Не удалять manifest/cleanup marker до удаления всех его payload/plan keys. Финальный sync batch удаляет временные статусы и обновляет рабочий delivered frontier; release_outbox сохраняется до ответа Gateway. Out-of-order ACK не разрешает перескочить pending batch: frontier — только непрерывный завершённый префикс, кратковременные completion markers для дыр очищаются при его продвижении.
3. Producer отправляет Release(batch_id,digest). Gateway после прекращения in-flight работы этого batch удаляет payload/ACK mapping и sync фиксирует released frontier/временные markers для дыр. Возвращает ReleaseAck. Producer удаляет release_outbox sync. Повтор Release безопасен. Если Gateway store потерян и записи нет, Release возвращает успех без публикации.
4. На reconnect сначала drain Release outbox, затем повтор pending публикаций. Один активный session token на producer/run; старый stream отзывается, старые queued операции не выполняются после session switch. Release и publish данного batch сериализуются. Для конечного history после закрытия всех batch выполняется CloseRun: закрыть session, удалить run-local служебные frontier. Финальный локальный job marker DONE допускается до удаления пользователем volume; это одна запись задания, не архив блоков.
5. Если producer-диск потерян, старое состояние не восстанавливается автоматически: новый run_id и ручной N–M по D27. Старый Gateway run нельзя бесконечно переоткрывать; оператор может явно AbandonRun через административную операцию только после остановки прежнего writer. Она удаляет лишь уже NATS_ACKED служебные receipts; CLOSED pending Gateway batch сначала допубликовываются, STAGING требует полного источника/повторного диапазона и явной диагностики. Отказ от backup не даёт права удалять неподтверждённый CLOSED payload по таймеру. Административная операция не требует UI/постоянного management API.

Небольшое рабочее состояние frontiers/session/release_outbox не является долговременным журналом доставленных блоков. Не хранить payload после ACK ради аудита. Удаление RocksDB логическое; фактическое освобождение SST места зависит от compaction. Приложение не удаляет SST/WAL вручную.

## Сценарии приёмки будущей реализации

| ID | Точка/ошибка | Проверяемый результат |
|---|---|---|
| REC-T01 | Crash до STAGING или внутри него | capture_checkpoint не продвинут, из незакрытого batch ничего не опубликовано; native replay конкретного capture. Для crash до durable descriptor механизм пока НЕ ОПРЕДЕЛЁН: требуется устранить это окно и проверить границу на native fixtures; сценарий не считается закрытым заявлением о replay |
| REC-T02 | Crash после READY, до Prepare | исходные bytes/ID восстановлены, публикация продолжается |
| REC-T03 | Потерян Prepare reply / Gateway store до producer plan commit | повтор reservation; прежний неподтверждённый plan ещё ничего не публиковал |
| REC-T04 | Gateway store потерян после публикации части | durable producer plan восстанавливает те же frame bytes/epoch/sequence; повторы допустимы |
| REC-T05 | Crash Gateway после PubAck, до записи bitmap | повтор тех же сообщений, IngestAck только после всех ACK |
| REC-T06 | Потерян IngestAck | Gateway повторяет receipt или сообщения; producer payload сохранён |
| REC-T07 | Crash внутри producer cleanup/Release | ACKED marker предотвращает потерю pending; уборка/release идемпотентны |
| REC-T08 | Поздний блок ACKed раньше раннего | ранний payload сохранён, frontier не перескакивает дыру |
| REC-T09 | Пустой блок / multipart | metadata доставляется; ACK части не завершает batch |
| REC-T10 | NATS offline; затем reconnect | накопление без прикладной квоты, bounded RAM, retry/drain; новые блоки продолжают capture при доступном диске |
| REC-T11 | Disk-full / sync error / повреждение БД | явная ERROR, нет ложного READY/ACK и silent skip; automatic repair с отбрасыванием данных запрещён |
| REC-T12 | Второй writer / старый stream после reconnect | второй opener/старый session отвергнут; только подтверждённое завершение writer позволяет замену |
| REC-T13 | Producer volume потерян | новый run, ручной диапазон; нет ложного обещания восстановить прежний cursor из YAML |
| REC-T14 | Незакрытый ранее начатый capture / reorg после ACK cleanup и restart | восстановление конкретного batch/activation references либо ERROR при отсутствии state; нет сканирования всего пропущенного периода. Live продолжает текущие наблюдения по D37; отсутствие прежних данных не маркируется успешной доставкой |
| REC-T15 | Неверный digest, schema, ACK или лимит ниже pending part | явная ошибка, неизменные pending bytes сохранены |
| REC-T16 | Final history CloseRun / потеря ReleaseAck / abandon старого run | служебные receipts очищаются без архива, pending не удаляются без NATS ACK |
| REC-T17 | Часть batch подтверждена; изменены account/stream/subjects; restart или потеря Gateway store | несовместимая смена отвергнута до публикации, исходные bytes/plan сохранены; ACK разных назначений не закрывают batch. Смена адреса того же назначения проверяется отдельно; изменение назначения допускается после подтверждённого drain обоих owners |

Fixtures выполняются для Go/Rust journal и интеграции с Gateway/NATS в E01/E03/E05/E06. Данные blockchain берутся из выбранных fixtures; это тест доставки, не production verifier полноты. .06 закрывает проектные решения, не выполнение этих тестов. Следующий документальный шаг — .07 после проверки и commit.
