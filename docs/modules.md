# Модули и сборка

Модуль — Go-пакет, который регистрируется сам, в `init()`. Бинарник собирает `cmd/build`: генерирует `main.go`
с `import _ "<модуль>"` для каждого выбранного модуля и собирает его — как xcaddy для caddy. При запуске каждый
модуль получает шину и свою секцию конфига; общаются модули только через шину ([bus.md](bus.md)).

## Интерфейс

```go
import "github.com/GoldenDeals/aish-core/pkg/module"

type Module interface {
	Init(b bus.Bus, cfg config.Config) error // подписки
	Start(ctx context.Context) error         // начать работу
	Stop(ctx context.Context) error          // освободить ресурсы
}

module.Register(name string, m Module) // из init(); name — уникальный, он же ключ секции конфига
```

`config.Config` (`pkg/config`) — секция конфига модуля: `Map() map[string]any`.

## Пример

```go
package hello

import (
	"context"
	"fmt"

	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/module"
)

type hello struct {
	b        bus.Bus
	greeting string
}

func init() { module.Register("hello", &hello{}) }

func (h *hello) Init(b bus.Bus, cfg config.Config) error {
	h.b, h.greeting = b, "hello from the bus"
	if g, ok := cfg.Map()["greeting"].(string); ok {
		h.greeting = g
	}
	bus.Async(b, event.ByName("hello"), func(_ context.Context, ev event.Event[string]) {
		fmt.Println(ev.Data())
	})
	return nil
}

func (h *hello) Start(ctx context.Context) error {
	return h.b.Dispatch(ctx, event.New("hello", h.greeting))
}

func (h *hello) Stop(context.Context) error {
	fmt.Println("bye")
	return nil
}
```

## Жизненный цикл

1. **`init()`** — `module.Register`. Пустое имя, nil-модуль или повтор имени — паника при старте бинарника.
2. **`Init`** — всем модулям по алфавиту имён. Здесь подписываются на шину и читают конфиг.
   `Dispatch` здесь не вызывать: модули дальше по алфавиту ещё не подписаны.
3. **`Start`** — всем в том же порядке. Можно отправлять события. `Start` должен вернуться быстро:
   долгую работу — в горутины на переданном ctx, он заканчивается при остановке.
4. **SIGINT / SIGTERM** — `Stop` всем стартовавшим в обратном порядке; на всех вместе — 10 секунд (ctx).

Ошибки (в сообщении есть имя модуля, бинарник завершается с кодом 1):
- ошибка `Init` — никто не стартует;
- ошибка `Start` у модуля — уже стартовавшие останавливаются в обратном порядке;
- ошибки `Stop` собираются вместе, остальные модули всё равно останавливаются.

Паники модулей не перехватываются — процесс падает.

Порядок по алфавиту важен для шины: при равном priority sync-обработчики вызываются в порядке подписки,
то есть в алфавитном порядке модулей. Не полагайся на это — давай перехватчикам уникальные priority.

## Конфиг

Конфиг — один YAML-файл, путь передаётся флагом: `./aish -config aish.yaml`. Без флага конфига нет.
Каждый модуль — свой верхнеуровневый блок с ключом, равным имени модуля из `module.Register`:

```yaml
hello:
  greeting: hi from yaml
history:
  limit: 100
  storage:
    path: /var/lib/aish/history
```

- `cfg.Map()` — содержимое блока модуля: `map[string]any`, вложенные блоки — тоже `map[string]any`,
  списки — `[]any`, числа — `int` / `float64`.
- Нет блока (или он пустой, `hello:`) — пустая секция, не nil. Читай значения с умолчанием, как в примере.
- Блок, имя которого не совпадает ни с одним модулем, — ошибка при запуске: так ловятся опечатки.
- Блок — не словарь (`hello: hi`), повтор ключа, битый YAML — ошибка при запуске.

## Сборка

```sh
go run ./cmd/build \
	-with github.com/you/llm@v1.2.0 \
	-with github.com/you/ui=../ui \
	-with github.com/GoldenDeals/aish-core/modules/history \
	-o aish \
	-- -trimpath -ldflags="-s -w"
./aish
```

| Флаг | Значение |
|---|---|
| `-with path` | модуль последней версии |
| `-with path@version` | модуль конкретной версии (`go get path@version`) |
| `-with path=local/dir` | модуль из локальной директории (replace) |
| `-o file` | куда положить бинарник, по умолчанию `aish` |
| `-core dir` | локальная копия aish-core; по умолчанию — репозиторий, из которого запущен сборщик |
| `-- флаги` | всё после `--` уходит в `go build` как есть: `-ldflags`, `-gcflags`, `-tags`, `-trimpath`, `-race`… |

- Модули внутри этого репозитория (`.../aish-core/modules/...`) передаются без версии — они берутся из `-core`.
- Нужен хотя бы один `-with`.
- Сборка идёт во временной директории; при ошибке она остаётся, путь печатается — там сгенерированные
  `main.go` и `go.mod`.
- Свой `-o` после `--` не сработает: флаги из `--` ставятся раньше `-o` сборщика.
- `GOOS` / `GOARCH` / `GOFLAGS` из окружения работают как обычно.
- Шаблон генерируемого `main.go` — `cmd/build/main.go.tmpl`.
- Ядро пока подключается только из локальной копии (replace): опубликованных версий aish-core ещё нет.
