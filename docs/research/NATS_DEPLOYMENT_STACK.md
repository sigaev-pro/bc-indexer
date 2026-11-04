# NATS: стек развёртывания

Проверено 2026-10-15. E00-03.03, исследование первичных источников; deployments не создавались и не запускались. D21/D22: в поставку входят Docker Compose для локального запуска и Kubernetes для production, с NATS внутри каждой среды.

## Подтверждённые факты

- Официальный репозиторий NATS публикует Helm 3 chart `nats/nats`; chart рендерит StatefulSet и поддерживает JetStream PVC. Это готовая основа для проектных values. [Репозиторий](https://github.com/nats-io/k8s), [values закреплённого chart](https://github.com/nats-io/k8s/blob/f8d578086c4c44d011a5ff5a5285749e8f05f301/helm/charts/nats/values.yaml).
- Проверенный кандидат: chart release `nats-2.15.1`, опубликован 2026-10-13, commit `f8d578086c4c44d011a5ff5a5285749e8f05f301`. В его `Chart.yaml` независимо проверены `version: 2.15.1` и `appVersion: 2.15.1`. Совпадение номеров в этом релизе не является правилом совместимости. [Release](https://github.com/nats-io/k8s/releases/tag/nats-2.15.1), [Chart.yaml](https://github.com/nats-io/k8s/blob/f8d578086c4c44d011a5ff5a5285749e8f05f301/helm/charts/nats/Chart.yaml).
- Отдельно проверен server release `v2.15.1`, опубликован 2026-10-09. Chart задаёт image `nats:2.15.1-alpine` через `container.image.repository/tag`. `container.image.digest` перекрывает tag, а `fullImageName` перекрывает остальные части имени. [Server release](https://github.com/nats-io/nats-server/releases/tag/v2.15.1), [image values](https://github.com/nats-io/k8s/blob/f8d578086c4c44d011a5ff5a5285749e8f05f301/helm/charts/nats/values.yaml).
- В chart JetStream **по умолчанию выключен**: `config.jetstream.enabled: false`. Для файлового хранения предусмотрены `config.jetstream.fileStore.enabled`, `.dir`, `.pvc.enabled`, `.pvc.size`, `.pvc.storageClassName`, `.maxSize`. Значения chart по умолчанию не являются согласованными параметрами нашего production. [values](https://github.com/nats-io/k8s/blob/f8d578086c4c44d011a5ff5a5285749e8f05f301/helm/charts/nats/values.yaml).
- `nats-io/nats-docker` — источник официальных server images; Compose может использовать тот же server image без отдельной реализации NATS. Native server поддерживает `--jetstream`/`--js` и `--store_dir`; конфигурационный файл поддерживает `store_dir`. [Официальные images](https://github.com/nats-io/nats-docker), [параметры server v2.15.1](https://github.com/nats-io/nats-server/blob/v2.15.1/server/opts.go).
- Старый `nats-io/nats-operator` помечен deprecated и архивирован. Его нельзя путать с NACK/JetStream Controller, который управляет объектами JetStream и является отдельным компонентом. [Старый operator](https://github.com/nats-io/nats-operator), [NACK chart](https://github.com/nats-io/k8s/tree/f8d578086c4c44d011a5ff5a5285749e8f05f301/helm/charts/nack).

## Предложение для проекта

| Среда | Способ поставки | Что фиксируем |
|---|---|---|
| Kubernetes | Официальный `nats/nats` Helm chart + проектные values | Версию chart, server image digest, JetStream enabled, файловое хранилище через PVC |
| Docker Compose | Официальный `nats` image + project-owned конфигурационный файл + named volume | Ту же server version/digest, JetStream enabled, явный `store_dir` на volume |

Рекомендуется Helm как основа Kubernetes-поставки, без копирования всего StatefulSet в собственные raw manifests и без deprecated operator. NACK не добавлять автоматически: способ создания streams/accounts относится к будущему решению о владельце конфигурации и доступах. Эта рекомендация не выбирает bootstrap-механику.

Release lock должен отдельно хранить chart version и digest всех реально используемых images, включая включённые chart sidecars; тег `latest` не использовать. Кандидат `2.15.1` ещё не прошёл проектные интеграционные проверки. Registry digest/архитектуры не проверялись — закрепить после выбора платформ и проверки доступности image, до приёмки поставки.

JetStream file storage в локальной среде сохраняется в volume при пересоздании контейнера; это само по себе не обещание восстановления после удаления volume или отказа диска. Kubernetes PVC также не заменяет согласованную модель восстановления.

## Открытые границы

Уточнение после D30–D31/D52: один NATS instance и одна stream replica; post-ACK retention, HA/backup и гарантии пережить потерю NATS вне scope. Default полного frame с headers — 2 MiB, server/stream limits согласованы в LIVE_CONFIG_SPEC. Storage class/размер и точные deployment artifacts остаются технической детализацией; chart defaults не принимаются молча. Server replicas и stream replicas — разные параметры. Это обновление проектного статуса, не новая проверка upstream версий.

Не выбраны версии Kubernetes/Helm/Compose в матрице поддержки: проверить совместимость с выбранным chart при подготовке deployment artifacts. Не выполнены Helm render/lint, Compose config, запуск контейнеров, PubAck/persistence/failover tests. Текущий результат — документированный кандидат стека, не runtime-приёмка.
