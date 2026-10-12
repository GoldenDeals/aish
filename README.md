# aish-core

Полностью модульный и расширяемый LLM-харнесс на Go.

Ядро ничего не знает про LLM, тулы, UI или историю. Оно даёт только две вещи: **шину событий** и **жизненный
цикл модулей**. Всё остальное — провайдер модели, вызов тулов, подтверждения, интерфейс, хранение диалога —
отдельные модули, которые можно заменить, убрать или дописать свои.

## Как это устроено

- **Модуль** — обычный Go-пакет: реализует `Init` / `Start` / `Stop` и регистрируется в `init()`.
  Модули не импортируют друг друга и общаются только через шину.
- **Шина** — события с транзакциями: каждое внешнее событие обрабатывается атомарно (все последствия
  применились или ни одно), с приоритетами, перехватом (`bus.Stop`) и стримами — например, токенов от LLM.
- **Сборка** — как xcaddy для caddy: `cmd/build` собирает бинарник из того набора модулей, который вы выбрали.
- **Конфиг** — один YAML, по блоку на модуль.

## Быстрый старт

```sh
go run ./cmd/build \
	-with github.com/you/llm@v1.2.0 \
	-with github.com/you/ui=../ui \
	-o aish
./aish # конфиг — .agents/aish.yaml, другой путь: -config
```

```go
// Сокращённо; полный модуль с Start/Stop и импортами — в docs/modules.md.
package hello

func init() { module.Register("hello", &hello{}) }

func (h *hello) Init(b bus.Bus, cfg config.Config) error {
	bus.Async(b, event.ByName("hello"), func(_ context.Context, ev event.Event[string]) {
		fmt.Println(ev.Data())
	})
	return nil
}
```

## Документация

- [docs/modules.md](docs/modules.md) — как написать модуль, жизненный цикл, конфиг, логи, сборка.
- [docs/bus.md](docs/bus.md) — шина событий: транзакции, sync/async-обработчики, перехват, стримы.
- [docs/examples/config.yaml](docs/examples/config.yaml) — пример конфига: провайдеры, токены, системный промпт.
- [docs/tui.md](docs/tui.md) — терминальный UI: элементы, z-порядок, ввод, inline и alt screen.

Публичный API — `pkg/` (`module`, `bus`, `tx`, `event`, `config`); `internal/` модули не импортируют.
Общие контракты событий между модулями — `contracts/` (`llm`: системный промпт, сообщение пользователя, ответ;
`tool`: объявление тула, вызов и результат;
`tui`: экран и элементы, `tui/renderer`: блоки контента от ядра рендереров, `tui/caret`: где кончается текст разговора, `session`: ID сессии).

## Модули

- [modules/llm/anthropic](modules/llm/anthropic) — провайдер Claude: собирает контекст сессии и отвечает на `user.message`, вызывая тулы.
- [modules/llm/openai](modules/llm/openai) — провайдер OpenAI (Responses API): отвечает на `user.message`, вызывая тулы.
- [modules/tools/bash](modules/tools/bash) — тул `bash`: команда в рабочей директории, stdout и stderr вместе. Без подтверждения.
- [modules/tools/read](modules/tools/read) — тул `read`: строки файла с номерами, по 2000 за раз.
- [modules/tools/write](modules/tools/write) — тул `write`: создать файл (с директориями) или заменить его целиком.
- [modules/tools/edit](modules/tools/edit) — тул `edit`: заменить точный фрагмент файла, один или все.
- [modules/tools/mcp](modules/tools/mcp) — тулы MCP-серверов (stdio и streamable HTTP) как `mcp__<сервер>__<тул>`; секреты для `env` и `headers` — из команды, например `pass`.
- [modules/context/system_prompt](modules/context/system_prompt) — системный промпт: из конфига (`text`), из файла (`file`) или встроенный по умолчанию.
- [modules/context/append](modules/context/append) — дополнение к системному промпту: из конфига (`text`) или из файла (`file`).
- [modules/context/agents.md](modules/context/agents.md) — AGENTS.md и CLAUDE.md в системный промпт; где искать — glob'ы `patterns`, по умолчанию корень, `.agents`, `.claude` и они же в `~`.
- [modules/context/skills](modules/context/skills) — список скиллов (имя, описание, путь к SKILL.md) в системный промпт; где искать — glob'ы `patterns`.
- [modules/context/skills/tool](modules/context/skills/tool) — тул `skill`: по имени скилла отдаёт его SKILL.md и директорию; скиллы ищет так же, как context.skills.
- [modules/log](modules/log) — логи: включает slog и пишет его в файл; модули логируют сами.
- [modules/frontend/tui](modules/frontend/tui) — компоновщик терминала: раскладывает элементы других модулей по z, сам ничего не рисует.
- [modules/frontend/tui/fullscreen](modules/frontend/tui/fullscreen) — разговор на весь экран со скроллом.
- [modules/frontend/tui/inputline](modules/frontend/tui/inputline) — строка ввода сообщений.
- [modules/frontend/tui/thinking](modules/frontend/tui/thinking) — курсор модели: идёт за текстом ответа, после 1 с тишины — `⠋ thinking…`.
- [modules/frontend/tui/renderer](modules/frontend/tui/renderer) — ядро рендереров: разбирает контент по правилам, которые регистрируют рендереры, побеждает самое специфичное.
- [modules/frontend/tui/renderer/markdown](modules/frontend/tui/renderer/markdown) — правила markdown для ответов модели.
- [modules/frontend/tui/renderer/codeblock](modules/frontend/tui/renderer/codeblock) — ```` ```lang ```` с подсветкой chroma.
- [modules/frontend/tui/renderer/mermaid](modules/frontend/tui/renderer/mermaid) — диаграммы mermaid рамками в терминале.
- [modules/frontend/tui/renderer/math](modules/frontend/tui/renderer/math) — формулы LaTeX в Unicode.
- [modules/frontend/tui/renderer/toolcall](modules/frontend/tui/renderer/toolcall) — вызовы тулов: `● имя(аргумент)` и начало вывода.
