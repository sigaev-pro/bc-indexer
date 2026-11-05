# IR-01: адрес попытки и успешного CREATE/CREATE2

2026-11-05. Source review закреплённых Geth/BSC/Bor для DOC-IR01-03-CLOSE-01. Проверены исходники; сборки, исполнения и runtime fixtures не запускались. Наличие fork-specific ветки в коде не доказывает её активацию на Mainnet. Исследование не меняет pinned версии и scope live-only.

## Вывод для ТЗ

Точная модель [CURRENT_SEMANTICS](../CURRENT_SEMANTICS.md#block-calls-и-account-changes) обоснована: **attempted_create_address, локальный успех создания и конечное существование account — разные факты**. Старый перечень CallFrame, называющий created_address просто адресом, который уже удалось определить, нужно привести к этой модели. Новое продуктовое решение не требуется.

| Ситуация | Правильное представление |
|---|---|
| Native определил адрес A, constructor завершился REVERT/FAILURE | attempted_create_address=A; created_address отсутствует |
| Native создание локально успешно | created_address=A; отсутствие дальнейшего rollback ещё не доказано |
| Локально успешный child CREATE позже отменён родителем | created_address=A сохраняет факт локального успеха, effects_committed=false; конечное состояние читается через AccountChange |
| Ошибка случилась до определения адреса/входа в соответствующую native creation boundary | Не выдумывать адрес или дополнительный child frame |
| Адрес уже определён, но API возвращает нулевой address при раннем отказе | Нулевой return value не стирает адрес, ранее наблюдённый на native boundary; он не является ни признаком успешного создания, ни универсальным признаком неизвестного адреса |

Вывод следует из разделённых стадий определения адреса, execution outcome и snapshot rollback в закреплённых источниках ниже. Названия публичных полей — решение проекта, а не имена полей upstream API. `engine`/`kind`/`profile_ref` остаются действующей моделью; отдельные runtime/native_type в CallFrame этим исследованием не вводятся.

## Проверенные первоисточники

| Клиент | Проверенный файл и SHA-256 |
|---|---|
| Geth `9dffe6e976cbbb87a84c5fd395448f0950290def` | [evm.go](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/vm/evm.go): `42016043f3376a3d8fca94693a0641f775af4010b750310b762a847c1fbbd1a8` |
| Geth, тот же pin | [instructions.go](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/vm/instructions.go): `06027536b7f673b6faae36e76c7789a12cdced522b7fe14e55912e67546ee0d4` |
| BSC `cdb7548b5baacfdae92f9f63437d6456411665f3` | [evm.go](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/vm/evm.go): `811462fe710d3fe8506df36f935f5d5191bfd64534633b12805050b8b84026a3` |
| BSC, тот же pin | [instructions.go](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/vm/instructions.go): `306933c393a6637a8b33fe94b6f56dfc1a1064a43b2bba23fb3d4661a1707d39` |
| Bor `a16f123d9b4f41a552a3a98d93a0340430838576` | [evm.go](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/vm/evm.go): `a87014dad231d161e6b262cbe3c67eaa06c44067dc73b8430e6240c2b18a35ff` |
| Bor, тот же pin | [instructions.go](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/vm/instructions.go): `a423cc463aa13e9c07b35954b469bab4bd820596e385e95f54ed0e2f533156e9` |

## Адрес определяется раньше результата создания

Внешние EVM.Create/Create2 вычисляют адрес до вызова общего create. В Geth это строки 707–719; внутренние opCreate/opCreate2 также вычисляют адрес до evm.create. В BSC — строки 721–734, в Bor — 704–717. Поэтому наличие вычисленного адреса не доказывает успешное исполнение init code. [Geth wrappers](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/vm/evm.go#L707-L719), [Geth opcodes](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/vm/instructions.go#L635-L709), [BSC](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/vm/evm.go#L721-L734), [Bor](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/vm/evm.go#L704-L717).

Общий create передаёт вычисленный address в captureBegin. При ранних depth/balance/nonce ошибках либо collision он может вернуть common.Address{}, хотя адрес уже известен на входе. В Geth проверка вынесена в createFramePreCheck и зависит от fork path; в BSC/Bor расположена внутри create. Источник attempted address — наблюдённая native операция с её provenance; чтения только итогового return tuple недостаточно. [Geth](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/vm/evm.go#L506-L603), [BSC](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/vm/evm.go#L566-L622), [Bor](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/vm/evm.go#L558-L614).

Обратное тоже важно: не каждая ошибка CREATE достигает этой boundary. Например, BSC/Bor opCreate/opCreate2 при readOnly возвращают ErrWriteProtection раньше EVM.Create/Create2. В Geth opcode есть отдельный account-creation charge до общего create. Эти пути не разрешают изготовить фиктивный успешный child; capture должен различать реально наблюдённые границы, доступный адрес и outcome. [BSC opCreate](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/vm/instructions.go#L660-L679), [BSC opCreate2](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/vm/instructions.go#L703-L720), [Bor opCreate](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/vm/instructions.go#L728-L748), [Bor opCreate2](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/vm/instructions.go#L773-L792), [Geth](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/vm/instructions.go#L635-L656).

## Constructor failure и rollback родителя

После initNewContract общий create откатывает snapshot при применимой ошибке и может вернуть исходный address вместе с err. Следовательно, ненулевой возвращённый адрес также не является достаточным условием created_address. Opcode отдельно определяет результат на стеке по native outcome. В исходниках сохранена историческая pre-Homestead особенность ErrCodeStoreOutOfGas; нельзя формулировать универсальное правило всех fork только как «любой Go err означает одно и то же». Применяются native rules конкретного поддержанного профиля, без возвращения исторического backfill в scope. [Geth create completion](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/vm/evm.go#L638-L652), [BSC](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/vm/evm.go#L658-L667), [Bor](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/vm/evm.go#L649-L658).

Внешний Call делает собственный snapshot перед исполнением и RevertToSnapshot при ошибке. Успешное вложенное создание ещё находится в области этого snapshot: локальный успех не защищает его изменения от последующего revert родителя. Отсюда следует необходимость сохранять одновременно локальный outcome/created_address и итоговый effects_committed/final account state. [Geth parent Call](https://github.com/ethereum/go-ethereum/blob/9dffe6e976cbbb87a84c5fd395448f0950290def/core/vm/evm.go#L283-L356), [BSC parent Call](https://github.com/bnb-chain/bsc/blob/cdb7548b5baacfdae92f9f63437d6456411665f3/core/vm/evm.go#L227-L325), [Bor parent Call](https://github.com/0xPolygon/bor/blob/a16f123d9b4f41a552a3a98d93a0340430838576/core/vm/evm.go#L312-L391).

## Минимальная документальная правка и проверка

Ранний перечень CallFrame должен ссылаться на точные имена [MESSAGE_CONTRACT](../MESSAGE_CONTRACT.md#3-transactionrecord): frame_id, execution_order, engine, kind, caller/context/code/msg_sender/target, attempted_create_address и created_address. Для адреса попытки сохранить условие «когда native определил адрес»; для created_address — локальный успешный исход по native rules. Конечное существование остаётся в AccountChange. Исходный v2 сохраняется без правок.

Для D81 добавлен отдельный AC-DAT-04-03 к общему покрытию AC-DAT-04-01: constructor REVERT/FAILURE, успешное создание с ancestor revert, сохранённый успех и ранний отказ. Ожидание для раннего отказа зависит от того, достигнуты ли native address/creation boundaries; нулевой returned address сам по себе не доказывает отсутствие attempted address. Независимый expected должен проверять source boundaries и outcome, а не зеркалировать normalizer. Этот перечень уточняет уже принятую семантику, не объявляет выполненные fixtures.

Исследование отвечает только на IR-01. Оно не доказывает покрытие каждого caller, отсутствие alias/race, корректность будущего normalizer или состояние runtime-приёмки. Последние остаются NOT_RUN по действующему ТЗ.
