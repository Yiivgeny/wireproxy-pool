# wireproxy-pool

[English](README.md) | **Русский**

`wireproxy-pool` — локальный SOCKS5 TCP CONNECT proxy с active/passive пулом независимых AmneziaWG 3 backend'ов. Каждый конфиг запускается напрямую через официальный `github.com/amnezia-vpn/amneziawg-go/v3` как отдельные userspace TUN, netstack и device. Общий SOCKS5 listener выбирает backend выше уровня VPN. Поддерживаются также конфиги WireGuard и AmneziaWG 1/2.

Module path: `github.com/yiivgeny/wireproxy-pool`.

## Сборка

Нужен Go 1.26 или новее. Зависимость `github.com/amnezia-vpn/amneziawg-go/v3` закреплена на официальном теге `v3.1.20260828`. Соседние checkout и локальные `replace` не требуются.

Сборка:

```sh
go build -o wireproxy-pool ./cmd/wireproxy-pool
```

Реальные `configs/*.conf` исключены из Git. Файл `configs/example.conf.example` содержит только безопасные placeholders; скопируйте его в `.conf` и заполните локально.

## Конфигурация AmneziaWG 3

Принимается один `[Interface]` и один или несколько `[Peer]`; имена полей и секций нечувствительны к регистру. Неизвестные поля и секции приводят к ошибке, чтобы параметры VPN не игнорировались. Секции прокси из `wireproxy-awg` и импорт через `WGConfig` не поддерживаются: listener и health-check задаются аргументами `wireproxy-pool`.

Кроме стандартных `PrivateKey`, `Address`, `DNS`, `MTU`, `ListenPort` и peer-полей поддерживаются:

| Секция | Поля | Формат |
| --- | --- | --- |
| Interface | `Jc`, `Jmin`, `Jmax`, `S1`–`S4` | Целые неотрицательные числа |
| Interface | `H1`–`H4` | Число или диапазон `100-199`; диапазоны не пересекаются |
| Interface | `I1`–`I5` | Строки custom signature packets |
| Interface | `HeaderProtectionKey` | Ключ из серверного конфига: 32 байта в base64 |
| Interface | `ContentPaddingAddition` | Число или диапазон |
| Interface | `RekeyAfterTime`, `RekeyTimeout`, `RejectAfterTime`, `KeepaliveTimeout` | Секунды: число или диапазон |
| Interface | `MaxHandshakeAttempts` | Число или диапазон |
| Interface | `RandomTrailers`, `DisableCookies` | `on`/`off` или `true`/`false` (AWG 3.1) |
| Peer | `PersistentKeepalive` | Секунды: число или диапазон, например `22-30` |

Для защиты заголовков AWG 3 укажите одинаковый `HeaderProtectionKey` на клиенте и сервере; каждое из `S1`–`S4` должно быть не меньше 12. Ключ и согласованные с сервером параметры нельзя получить простым обновлением клиентской библиотеки: используйте экспортированный сервером конфиг. Без `HeaderProtectionKey` сохраняется совместимость с прежними конфигами. Подробности параметров: [официальная документация AmneziaWG](https://github.com/amnezia-vpn/amneziawg-go/blob/v3.1.20260828/README.md#configuration).

Значение `$NAME` читается из переменной окружения; `$$` в начале экранирует `$`. Повторные `Address`, `DNS` и `AllowedIPs` объединяются. По умолчанию MTU равен 1420; если `AllowedIPs` отсутствует, используются `0.0.0.0/0` и `::/0`. `Address` обязателен. Некорректные конфиги завершают запуск без вывода ключей и значений полей.

Парсер ограничивает `Jc` диапазоном 0–128, проверяет `0 <= Jmin <= Jmax <= 65507` и размеры signature packets, чтобы ошибочные значения не приводили к panic или чрезмерным выделениям памяти внутри VPN engine.

Инициализация TUN обходит [upstream-ошибку первого пакета при ненулевом S4](https://github.com/amnezia-vpn/amneziawg-go/issues/168): автоматический `EventUp` подавляется, а первое чтение после применения конфигурации возвращает ноль пакетов, чтобы device обновил transport padding до передачи данных.

## Запуск

Два явных файла:

```sh
./wireproxy-pool configs/awg1.conf configs/awg2.conf
```

Одна quoted-маска, раскрываемая самим процессом:

```sh
./wireproxy-pool 'configs/*.conf'
```

Комбинация файлов и нескольких масок:

```sh
./wireproxy-pool configs/awg1.conf 'configs/fallback-*.conf' '/etc/wireproxy/extra-?.conf'
```

Параметры должны идти до первого пути:

```sh
./wireproxy-pool \
  --socks-listen 127.0.0.1:1080 \
  --metrics-listen 127.0.0.1:9090 \
  --log-level debug \
  --health-target one.one.one.one:443 \
  --health-interval 15s \
  --health-timeout 5s \
  --connect-timeout 10s \
  --failure-threshold 3 \
  --success-threshold 2 \
  'configs/*.conf'
```

Каждая маска обязана найти хотя бы один обычный файл. Некорректная маска, отсутствующий файл и пустой итоговый набор завершают запуск с понятной ошибкой. Аргументы сохраняют заданный порядок, совпадения одной маски сортируются, а дубликаты удаляются по абсолютному пути с сохранением первого вхождения. Поэтому первый итоговый конфиг — primary.

### supervisord

Для постоянного запуска можно использовать Homebrew `supervisord`. Конфигурация конкретной машины в локальном каталоге `supervisor/` не входит в репозиторий. Задайте рабочим каталогом корень проекта и команду `go run ./cmd/wireproxy-pool configs/*.conf`, указав полный путь к Go. Маску `configs/*.conf` раскрывает сам `wireproxy-pool`.

```sh
supervisorctl status wireproxy-pool
supervisorctl restart wireproxy-pool
```

## Endpoints

- SOCKS5: `127.0.0.1:1080` по умолчанию. Поддерживается TCP CONNECT без аутентификации; BIND и UDP ASSOCIATE запрещены.
- Prometheus: `http://127.0.0.1:9090/metrics` по умолчанию.

Оба listener'а по умолчанию привязаны только к loopback. При публикации SOCKS5 на внешний интерфейс добавьте внешний контроль доступа: встроенной аутентификации сейчас нет.

### Debug-лог соединений

По умолчанию используется `--log-level info`. На уровне `debug` после успешного SOCKS CONNECT записываются клиент, запрошенный destination, фактически выбранный resolved endpoint и backend:

```text
DEBUG SOCKS CONNECT client="127.0.0.1:54321" requested_endpoint="example.com:443" selected_endpoint="203.0.113.10:443" backend="awg2-53034a35"
```

Запись отражает backend, реально установивший соединение после возможного fallback. На уровне `info` такие строки не выводятся.

## Health и failover

Health-check проверяет полезную связность каждого конкретного backend: при hostname выполняется resolution через resolver backend'а, затем через тот же userspace netstack устанавливается TCP-соединение с `--health-target`. Это не проверка только факта handshake.

Семантика доступности:

1. До первой проверки backend unavailable. Начальная проверка выполняется перед запуском listener'ов; первый успех сразу делает backend available, первый провал оставляет его unavailable.
2. Available backend становится unavailable после `--failure-threshold` последовательных провалов health-check (по умолчанию 3).
3. Unavailable backend восстанавливается после `--success-threshold` последовательных успехов (по умолчанию 2). Эти пороги защищают от flapping.
4. Active backend — первый available backend в стабильном порядке конфигов. После восстановления primary снова получает новые соединения.
5. Ошибки пользовательского destination, включая connection refused и NXDOMAIN, не изменяют health backend'а. Они учитываются только для текущего запроса и могут вызвать попытку через следующий available backend.

Для SOCKS-запроса resolution и dial первой попытки привязаны к одному выбранному backend. При fallback исходное hostname резолвится заново через следующий backend. `--connect-timeout` применяется отдельно к каждой backend-попытке; `--health-timeout` — отдельно к probe каждого backend.

Если в VPN-конфиге задан `DNS`, lookup выполняется через DNS соответствующего netstack. Если `DNS` отсутствует, используется системный resolver; TCP dial всё равно проходит через выбранный userspace netstack. Для полностью tunnel-local DNS укажите DNS-сервер в каждом конфиге.

Failover относится только к новым соединениям и попыткам установления. Уже открытые TCP-сессии не мигрируют между backend'ами.

## Метрики

Все per-backend gauges имеют устойчивые labels `backend` и `config`. `backend` строится из имени файла и короткого hash абсолютного пути; содержимое конфигов и ключи не читаются для построения labels.

- `wireproxy_backend_up` — текущая устойчивая оценка доступности, `0` или `1`.
- `wireproxy_backend_active` — backend сейчас первый available, `0` или `1`.
- `wireproxy_backend_last_check_timestamp_seconds` — время последней завершённой проверки.
- `wireproxy_backend_last_check_success` — результат последней проверки, `0` или `1`.
- `wireproxy_backend_last_check_duration_seconds` — длительность последней проверки.
- `wireproxy_backend_consecutive_failures` — серия провалов health-check.
- `wireproxy_backend_failovers_total{from_backend,to_backend}` — переходы между backend'ами внутри request attempts.
- `wireproxy_backend_dial_errors_total{backend,stage}` — ошибки стадий `resolve`, `connect` и `no_backend`.

Private/public/preshared keys и содержимое конфигов не выводятся в логах или метриках. Label `config` содержит только абсолютный путь к файлу.

## Завершение и проверки

SIGINT/SIGTERM останавливает health goroutines, закрывает SOCKS listener и активные client connections, выполняет graceful shutdown metrics HTTP server и затем закрывает все успешно созданные devices. При частичной ошибке старта уже созданные backend'ы также закрываются.

Локальные проверки не требуют внешней сети. Интеграционные тесты поднимают пары userspace устройств на loopback UDP и проверяют handshake, DNS и TCP echo для WireGuard, AWG 2, AWG 3 с защитой заголовков и content padding, а также AWG 3.1 с `RandomTrailers` и `DisableCookies`:

```sh
go test ./...
go test -race ./...
go vet ./...
```
