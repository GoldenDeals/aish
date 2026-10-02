# План: сетевая песочница

Сетевой контроль для shell под aish: ядро заворачивает HTTP/HTTPS-трафик shell'а в локальный
слушатель aish, тот расшифровывает его, проверяет сетевыми правилами Cedar
(`Action::"connect"`) и подставляет в запросы секреты, которых нет ни у модели, ни в окружении
shell'а.

План вынесен из `tasks/` (бывшие задачи 38–41) — это отдельная ветка развития, а не очередная
правка ядра: она требует root для установки, ставит свой CA в системное хранилище доверия и
добавляет MITM-прокси, то есть заметно расширяет поверхность атаки. Браться за неё стоит после
стабилизации ядра (экран, разбиение `Proxy`, интерфейс shell — задачи 50, 26, 22, 14).

## Этапы

| этап | бывшая задача | что даёт | зависит от |
|---|---|---|---|
| 1 | 38-2.26-Harden-network-setup | `aish harden`/`unharden`: группа, CA, правило nftables, `/etc/aish` | — |
| 2 | 39-2.27-Shell-under-net-gid | shell через `sg <группа>`, слушатель с `SO_ORIGINAL_DST` и SNI | 1 |
| 3 | 40-2.28-Network-policy-l7 | MITM TLS, разбор HTTP/1.1, `Action::"connect"` в Cedar (opt-in) | 2, 37-1K (закрыта) |
| 4 | 41-2.29-Inject-secrets-in-requests | `~/.config/aish/net-secrets.yaml`: подстановка заголовков | 3 |

Тексты этапов ниже — исходные задачи как есть. Внутри них номера 38, 39, 40, 41 означают этапы
1–4; ссылки `файл:строка` и списки «кто ещё правит этот файл» писались на 2026-10-02 и к началу
работы устареют. Чтобы взять этап в работу, заведи его заново задачей через скилл
`create-parallel-task` (новый номер, свежие ссылки на код), опираясь на текст отсюда.

Связи с открытыми задачами:

- **14-1B-Shell-agnostic-interface** меняет `Proxy.Run` на `Shell.Command`. Этап 2 вставляет туда
  `netguard.Wrap(st, bash, args)` перед `exec.Command`; кто придёт вторым, сохраняет обёртку под
  GID — она от shell не зависит.
- **22-2.6-Split-proxy** переставит `internal/proxy`; места правок этапов 2–4 в `proxy.go`
  (`Run`, `cmd.Env`) после неё надо искать заново.

## Этап 1. `aish harden` / `aish unharden`: группа, CA и правило netfilter для сетевого фильтра

*Бывшая задача `38-2.26-Harden-network-setup`.*

**Приоритет:** средний — без этой команды сетевой фильтр (39, 40, 41) некуда ставить; сама по себе
команда ничего не включает в рантайме, только готовит систему.
**Файлы:** `internal/netguard/netguard.go` (новый: пакет, `State`, `Load`, `Status`, `Active`),
`internal/netguard/ca.go` (новый: генерация CA), `internal/netguard/setup.go` (новый: шаги
`harden`/`unharden` как данные + исполнение), `internal/netguard/netguard_test.go`,
`internal/netguard/setup_test.go` (новые), `cmd/aish/harden.go` (новый: `hardenCmd`,
`unhardenCmd`, текст WARNING), `cmd/aish/harden_test.go` (новый), `cmd/aish/main.go`
(doc-комментарий и `usage` — две строки после строки `aish skills`; `switch` в `run` — два `case`
после `case "skills"`, строка 90 — туда же 37-1K ставит `case "policy"`: при rebase оставить все три),
`cmd/aish/status.go` (одна строка `row("network", …)` сразу
после `row("policy", …)`, строка 97), `README.md` (новый раздел «Сетевой фильтр» между «Политики»
и «Настройки», строка 270), `CLAUDE.md` (строка `internal/netguard` в таблице пакетов после
строки `internal/policy`; пункт про `/etc/aish` в «Конфигурация и пути»).
**Зависит от:** —
**Параллельно:** да. `cmd/aish/main.go` правят также 24-2.14, 29-2.19, 34-2.24, 37-1K — здесь
ровно две строки `usage`, две строки doc-комментария и два `case`, место названо точно.
`cmd/aish/status.go` правят 29-2.19 (строка `journal`), 33-2.23, 35-2.25 — здесь одна строка
после `policy`. Если 24-2.14 уже в `origin/master` — добавить `harden` и `unharden` в её
`UserCommands`. С 37-1K (Cedar) пересечений нет: `internal/policy` здесь не трогается, поэтому
задачу можно вести параллельно с ней; нужен строгий порядок — поставьте
`**Зависит от:** 37-1K-Cedar-policy-engine`.

### Проблема

Политика (`internal/policy`) видит только argv команды до её запуска: `curl "$URL"` — это
литерал `$URL`, а `python -c`, скрипт проекта или `git push` вообще не называют хост. Сетевого
контроля в aish нет никакого.

Выбранный способ его получить (обсуждён отдельно, альтернативы отвергнуты ниже) — редирект на
уровне ядра: shell под aish запускается с real+effective GID выделенной группы, а правило
netfilter заворачивает TCP 80/443 от процессов с этим GID на локальный порт, где слушает aish.
Env-переменные `HTTPS_PROXY` отвергнуты: их снимает любой процесс. cgroup v2 отвергнут: на
systemd-машине поддерево `user@<uid>.service` делегировано пользователю (проверено:
`/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service` принадлежит пользователю), процесс
выходит из-под правила одной записью в `cgroup.procs`. eBPF — те же права и та же cgroup, но
тяжёлая зависимость.

Настройка этого требует root ровно один раз: группа, CA, правило, персистентность. Это и есть
`aish harden`; `aish unharden` снимает всё обратно.

### Предложение

#### Пакет `internal/netguard`

Пакет про «ядро заворачивает трафик shell'а в aish». Здесь — только раскладка и установка;
слушатель и политики добавят задачи 39 и 40 своими файлами.

`netguard.go`:
```go
// State is what `aish harden` left on the machine; it is the single source
// of truth for the runtime, which must not guess the group or the port.
type State struct {
	Group   string `json:"group"`
	GID     int    `json:"gid"`
	UID     int    `json:"uid"`      // the user the rule matches
	Port    int    `json:"port"`
	CACert  string `json:"ca_cert"`
	CAKey   string `json:"ca_key"`
	NftFile string `json:"nft_file"`
	Anchor  string `json:"anchor"`   // the copy of the cert in the trust store
	Created string `json:"created"`
}

const StatePath = "/etc/aish/netguard.json"

func Load() (*State, error)                 // nil, nil if the file is absent
func (s *State) Check() []string            // what is broken: no group, no rule, no cert, uid mismatch
func Active() (*State, bool)                // Load + len(Check()) == 0 + s.UID == os.Getuid()
```
`Check` проверяет: группа существует и её gid совпадает, текущий uid совпадает с `State.UID`,
файлы CA на месте, `nft list table inet aish` отвечает успехом. Ошибки — человеческим текстом,
они печатаются в `aish status` и в предупреждении при старте.

`ca.go`: `GenerateCA(host string) (certPEM, keyPEM []byte, err error)` — ECDSA P-256, 10 лет,
`IsCA`, `MaxPathLen: 0`, `KeyUsage: CertSign|CRLSign`, CN `aish local inspection CA (<host>)`.
Только `crypto/*` из стандартной библиотеки, новых зависимостей нет.

`setup.go`: план как список шагов, чтобы `--dry-run`, вывод и `unharden` были одним кодом.
```go
type Step struct {
	Name  string                 // "group aish", "nft rule", …
	Check func(*Plan) (done bool, err error)  // already in place?
	Do    func(*Plan) error
	Undo  func(*Plan) error
}
type Plan struct {
	User  string; UID, GID int; Group string; Port int
	Dir   string // /etc/aish, overridable in tests
	state *State
}
func NewPlan(user string, port int) (*Plan, error)
func (p *Plan) Steps() []Step
func (p *Plan) Apply(w io.Writer, undo bool) error
```
Шаги, в порядке применения (откат — в обратном):

1. **group** — `groupadd -r <group>` (по умолчанию `aish`), затем `gpasswd -a <user> <group>`.
   Откат: `gpasswd -d`, затем `groupdel`, если в группе никого не осталось.
2. **ca** — каталог `/etc/aish` 0755 root:root; `ca.key` 0640 **root:<group>**, `ca.crt` 0644.
   Ключ читает прокси (пользователь — член группы) и shell под этим gid: это осознанный компромисс,
   он назван в WARNING.
3. **trust** — скопировать `ca.crt` в хранилище доверия и обновить его. Определять по наличию
   каталога/команды: `/etc/ca-certificates/trust-source/anchors` + `update-ca-trust` (Arch, Fedora),
   `/usr/local/share/ca-certificates` + `update-ca-certificates` (Debian, Ubuntu). Ни того, ни
   другого — шаг падает с текстом «положите <путь> в доверенные сами».
4. **nft** — записать `/etc/aish/aish.nft`:
   ```
   table inet aish {
   	chain output {
   		type nat hook output priority dstnat; policy accept;
   		meta skuid <uid> meta skgid <gid> ip daddr != 127.0.0.0/8 tcp dport { 80, 443 } redirect to :<port>
   		meta skuid <uid> meta skgid <gid> ip6 daddr != ::1 tcp dport { 80, 443 } redirect to :<port>
   	}
   }
   ```
   Точный синтаксис **проверить на живой машине** (`nft -c -f <файл>` требует netlink и под
   обычным пользователем не работает — в задаче он не проверен): сначала `nft -c -f`, и только
   при успехе `nft -f`. Таблица своя (`inet aish`) — чужой ruleset не трогаем ни при установке,
   ни при откате; откат — `nft delete table inet aish` и удаление файла.
5. **persist** — `/etc/systemd/system/aish-netguard.service`: oneshot, `RemainAfterExit=yes`,
   `ExecStart=/usr/bin/nft -f /etc/aish/aish.nft`, `ExecStop=/usr/bin/nft delete table inet aish`,
   `WantedBy=multi-user.target`; `systemctl daemon-reload && systemctl enable --now`. Нет
   `systemctl` — не ошибка: напечатать, как загружать правило самому.
6. **state** — записать `State` в `/etc/aish/netguard.json` 0644 (его читает прокси под
   пользователем).

Каждый шаг идемпотентен: `Check` возвращает `done` — печатается `already` и шаг пропускается.

#### `cmd/aish/harden.go`

`aish harden [--yes] [--dry-run] [--port N] [--group NAME] [--user NAME]`,
`aish unharden [--yes] [--dry-run]`, `aish harden --check`.

- Не root (`os.Geteuid() != 0`): напечатать WARNING и план, затем `run it as: sudo aish harden …`
  и выйти с кодом 1. Сам `sudo` не вызывать: пользователь должен видеть, что он запускает.
- `--user` по умолчанию `$SUDO_USER`, иначе владелец терминала; без него под sudo группа
  досталась бы root.
- Порт по умолчанию 7383; занят — не ошибка установки, это проверит рантайм.
- WARNING печатается **до** подтверждения, одним блоком, и каждый пункт — отдельной строкой:
  - приватный ключ CA ляжет в `/etc/aish/ca.key` с доступом для группы `<group>`, а сертификат —
    в системное хранилище доверия: любой процесс, который может прочитать ключ (любой член
    группы, включая агента и всё, что он запустит), выпустит сертификат на любой домен, и ему
    поверит любая программа этой машины, у всех пользователей;
  - весь HTTP/HTTPS-трафик shell'а под aish будет расшифрован и прочитан процессом aish —
    заголовки, URL, тела; ваши пароли и токены тоже;
  - трафик, который вы набираете сами, фильтруется наравне с трафиком агента;
  - правило переживает перезагрузку; пока aish не слушает порт, соединения из shell с этим GID
    будут отвергаться (fail-closed) — снимается `aish unharden`;
  - это не песочница: процесс с вашим uid уходит из-под правила через `sg`/`newgrp` в любую
    другую вашу группу (проверено), через `systemd-run --user`, `docker`, `incus`. Запрет на эти
    команды держите в политике; harden защищает от утечки и прямых попыток, а не от обхода.
- Затем план (`what will be done`), затем `continue? [y/N]` (пропускается при `--yes`;
  `--dry-run` печатает план и выходит).
- `aish harden --check` печатает `Status`: `off`, `on (group aish, port 7383)` или
  `broken: <причина>; <причина>` и выходит с 0/1.

`cmd/aish/status.go`: строка сразу после `policy`:
`row("network", "off")` / `row("network", "redirect :7383 → group aish")` /
`row("network", "broken: …")`.

Отвергнуто:
- Делать `harden` под sudo изнутри aish — команда меняет систему, пользователь должен набрать
  `sudo` сам; к тому же это запрос к ядру от имени агента, если он доберётся до неё.
- Класть CA в `~/.config/aish` 0600 — ключ всё равно доступен агенту (тот же uid), но тогда
  правило и ключ живут в разных местах; одна раскладка в `/etc/aish` проще для `unharden`.
- `iptables` вместо `nft` — на новых системах `iptables` это обёртка над nft, своя таблица
  в nft удаляется начисто.

### Границы

- Слушатель, запуск shell под GID, MITM, сетевые политики, подстановка секретов — задачи 39,
  40, 41. Здесь ничего в рантайме не меняется: `internal/proxy` не трогать.
- `internal/config/config.go` не трогать: группа и порт живут в `/etc/aish/netguard.json`,
  чтобы конфиг пользователя не мог разойтись с правилом ядра.
- Не трогать `internal/policy`.
- `/etc/aish/ca-bundle.crt` (бандл для программ со своим хранилищем) не делать — его собирает
  в `$AISH_RUN` задача 40, чтобы он не устаревал.

### Документация

`README.md` — новый раздел `## Сетевой фильтр` между «Политики» и «Настройки» (строка 270):
зачем (модель не должна ходить куда попало и видеть секреты), что делает `aish harden` по шагам,
полный список side-effect'ов тем же текстом, что печатает команда, `aish unharden`, и абзац
«чего это не даёт» (побег через `sg`/`newgrp`/`systemd-run`/`docker`, DNS и не-80/443 порты,
один uid — один домен доверия). Подразделы про рантайм допишут задачи 39-41, место под ними
не занимать.

`CLAUDE.md` — строка таблицы пакетов после `internal/policy`: «`internal/netguard` | редирект
трафика shell'а в aish: раскладка `/etc/aish`, `aish harden`/`unharden` (группа, CA, правило
nftables)»; в «Конфигурация и пути» — пункт `/etc/aish/` (`netguard.json` — состояние, `ca.crt`,
`ca.key` 0640 root:aish, `aish.nft`).

### Критерий готовности

- `netguard_test.go`: `Load` на отсутствующем файле — `nil, nil`; `Check` на подставном `Plan.Dir`
  во временном каталоге называет недостающее; `Active` возвращает `false` при чужом `UID`.
- `setup_test.go`: `Steps()` с подменённым `Dir` и подставными командами (поле с `func(name
  string, args ...string) error` в `Plan`, в тестах — запись вызовов) — применение печатает все
  шаги по порядку, повторное применение печатает `already` и ничего не зовёт, `Apply(undo)` зовёт
  шаги в обратном порядке; рендер `aish.nft` сравнивается с эталонной строкой.
- `ca.go`: сгенерированный сертификат разбирается `x509.ParseCertificate`, `IsCA == true`,
  ключ подходит к сертификату.
- `harden_test.go`: без root — код 1, в выводе есть WARNING и строка `sudo aish harden`;
  `--dry-run` ничего не зовёт.
- `gofmt -l .` пуст, `go build ./... && go vet ./... && go test ./...`.
- Руками (нужна машина, которую не жалко): `sudo ./aish harden --dry-run`, затем
  `sudo ./aish harden --yes`; `nft list table inet aish` показывает правило; `id -nG` после
  перелогина содержит `aish`; `./aish harden --check` печатает `on`; `./aish status` — строку
  `network`; `sudo ./aish unharden --yes`; `nft list table inet aish` отвечает `No such file`,
  `/etc/aish` удалён, сертификат из хранилища доверия исчез.

## Этап 2. Shell под GID сетевой группы и локальный слушатель, куда ядро заворачивает его трафик

*Бывшая задача `39-2.27-Shell-under-net-gid`.*

**Приоритет:** средний — это рантайм-половина сетевого фильтра: без неё `aish harden` ставит
правило, в которое никто не попадает и никто не слушает.
**Файлы:** `internal/netguard/shell.go` (новый: `Wrap`), `internal/netguard/shell_test.go`
(новый), `internal/netguard/listen.go` (новый: `Listener`, `Hold`, `Conn`, `Decide`),
`internal/netguard/origdst_linux.go` (новый: `origDst`, `//go:build linux`),
`internal/netguard/origdst_other.go` (новый: заглушка, `//go:build !linux`),
`internal/netguard/sni.go` (новый: разбор SNI из ClientHello), `internal/netguard/sni_test.go`,
`internal/netguard/listen_test.go` (новые), `internal/proxy/proxy.go` (`Run`: ~8 строк между
`bash, err := bashPath(cfg.Shell)` (строка 186) и `cmd := exec.Command(...)` (строка 190) —
старт слушателя и `netguard.Wrap`), `internal/proxy/netguard_test.go` (новый),
`README.md` (раздел «Сетевой фильтр» из 38 — подраздел «Как это работает»), `CLAUDE.md`
(новый пункт в «Что легко сломать» после пункта «Сессию держит один aish»).
**Зависит от:** 38-2.26-Harden-network-setup — отсюда берутся `netguard.State`, `Active()`,
`/etc/aish/netguard.json` и порт.
**Параллельно:** да. `internal/proxy/proxy.go` правят также 24-2.14 (`makeRunDir`, строки
249-273) и 34-2.24 (`Run`, рядом с `p.sess.Lock()`, строка 150) — здесь другое место, строки
186-199. Задача 40 (MITM и сетевые политики) ждёт эту: она заполняет `Decide` и подменяет TLS.
С 37-1K (Cedar) пересечений нет: `internal/policy` здесь не трогается.

### Проблема

`aish harden` (38) создаёт группу и правило netfilter «TCP 80/443 от процессов с uid=<user> и
gid=<group> завернуть на 127.0.0.1:<порт>». Но bash под aish запускается с обычным GID
пользователя (`internal/proxy/proxy.go:190-198`), под правило не попадает, и на порт никто не
слушает. Нужны обе стороны: shell с нужным GID и слушатель, умеющий узнать исходный адрес
завёрнутого соединения.

### Предложение

#### Запуск shell под GID группы

Сменить GID может только привилегированный процесс, но в системе уже есть setuid-helper:
`sg GROUP -c '…'` (shadow-utils). Проверено на Arch: пользователь-член группы получает
`real=effective=951`, пароль не спрашивается, окружение сохраняется, а вернуть прежний GID
нельзя — он остаётся только в supplementary (`setgid` на supplementary-группу не разрешён).

`shell.go`:
```go
// Wrap returns the argv that starts bash with the GID of the netguard
// group, so that the kernel rule matches it and everything it spawns.
// sg is setuid-root; aish itself needs no privileges.
func Wrap(st *State, bash string, args []string) (string, []string)
```
Возвращает `"sg", []string{st.Group, "-c", "exec " + quoted}`, где `quoted` — `bash` и `args`,
пропущенные через POSIX-квотирование (`'…'` с заменой `'` на `'\''`; своя `quote`, 6 строк).
`exec` обязателен: `sg` запускает строку через login-shell пользователя, без `exec` останется
лишний процесс между aish и bash. `st == nil` — вернуть `bash, args` как есть.

В `internal/proxy/proxy.go`, в `Run`, после `bashPath`:
```go
st, on := netguard.Active()
if on {
	if err := p.netguard(st); err != nil {   // в internal/proxy/netguard.go? нет: 5 строк здесь
		return 1, err
	}
}
name, argv := netguard.Wrap(st, bash, []string{"--rcfile", filepath.Join(run, "rc"), "-i"})
cmd := exec.Command(name, argv...)
```
`cmd.Env` (строка 191) не трогать. Слушатель останавливается `defer`, рядом с `defer ptmx.Close()`.

**Если слушатель не поднялся — aish не стартует** (`return 1, err` с текстом
`network filter: %w; run 'aish unharden' or free the port`). Молча запустить shell без фильтра
нельзя: пользователь считает, что фильтр включён, а правило ядра в это время обрывает трафик.

#### Слушатель

`listen.go`:
```go
// Listener accepts connections the kernel redirected from the shell and
// gives each one its original destination.
type Listener struct {
	Decide func(*Conn) (allow bool, reason string) // nil: allow everything (task 40 fills it)
	Log    func(format string, a ...any)
}

// Conn is one redirected connection.
type Conn struct {
	net.Conn
	DstIP   netip.AddrPort // where it was really going
	SNI     string         // TLS: the server name; "" for plain TCP
	Peeked  []byte         // bytes read while sniffing, to be replayed upstream
}

// Hold serves until ctx is done, holding a per-user lock: one listener per
// user filters every aish shell, and the next one takes over when it exits.
func Hold(ctx context.Context, st *State, l *Listener) error
```
- `Hold` берёт `flock` на `$XDG_RUNTIME_DIR/aish-netguard.lock` (нет переменной —
  `/tmp/aish-netguard-<uid>.lock`, 0600). Взял — слушает `127.0.0.1:<port>` и `[::1]:<port>`.
  Не взял — лок держит другой aish того же пользователя: это нормально (его слушатель
  обслуживает все shell'ы), ждать в цикле `time.Ticker` 2 с и попытаться снова, пока `ctx` жив.
  Так выход «главного» aish не оставляет остальные shell'ы без сети навсегда.
  Проверять, что порт действительно занят aish, не нужно: `Hold` без лока просто не слушает, а
  если порт занят чужим процессом — `net.Listen` под локом вернёт ошибку, и aish не стартует.
- На каждое соединение: `origDst(conn)` → `DstIP`. Если `DstIP` указывает на сам слушатель
  (петля) — закрыть.
- Порт 443: прочитать ClientHello и достать SNI (`sni.go`, разбор вручную: `crypto/tls` имя
  сервера без терминирования не отдаёт). Не TLS или имени нет — `SNI` пустой. Прочитанные байты
  кладутся в `Peeked`. Таймаут на чтение ClientHello — 5 с.
- `Decide` → `false`: закрыть; для порта 80 перед закрытием отдать
  `HTTP/1.1 403 Forbidden` с телом `aish: <reason>`, для 443 — просто закрыть (клиент увидит
  обрыв; осмысленный alert отдаст задача 40, когда появится TLS).
- `Decide` → `true`: соединиться с `DstIP` (`net.Dialer`, таймаут 10 с), отправить `Peeked`,
  дальше двусторонний `io.Copy` с закрытием обеих сторон по EOF. Никакой расшифровки: TLS здесь
  проходит насквозь.
- `Log` — в тестах, в прокси пока не подключать: печать в терминал посреди работы shell'а
  испортит экран.

`origdst_linux.go`:
```go
const soOriginalDst = 80 // SO_ORIGINAL_DST, not in the syscall package
func origDst(c *net.TCPConn) (netip.AddrPort, error)
```
Через `c.SyscallConn()` + `syscall.GetsockoptIPv6Mreq(fd, syscall.IPPROTO_IP, soOriginalDst)` для
IPv4 (`sockaddr_in` лежит в первых 8 байтах `Multiaddr`, порт — big-endian) и
`syscall.IPPROTO_IPV6`/`soOriginalDst` для IPv6. Только `syscall` из стандартной библиотеки:
`golang.org/x/sys` сейчас indirect-зависимость, делать её прямой — значит править `go.mod`,
а это конфликт у всех. `origdst_other.go` возвращает ошибку `not supported on this platform`.

Отвергнуто:
- `syscall.SysProcAttr.Credential` для смены GID в потомке — смена gid требует CAP_SETGID,
  у aish его нет; setgid-бит на бинаре aish — лишняя привилегия и ломается при пересборке.
- Слушатель в каждом экземпляре aish со своим портом — порт зашит в правило ядра, он один.
- Прозрачный режим через TPROXY — он для `prerouting`, локально порождённый трафик им не
  ловится.

### Границы

- Расшифровки TLS, HTTP-разбора, политик и секретов здесь нет: `Decide` остаётся `nil`, всё
  проходит насквозь. Это задачи 40 и 41.
- `cmd.Env` в `proxy.go` не трогать — переменные доверия к CA добавляет задача 40.
- `internal/policy`, `internal/config`, `cmd/aish` не трогать.
- Не логировать соединения в журнал сессии: что из них видит модель — решает задача 40.
- Не пытаться чинить побег через `sg`/`newgrp`/`systemd-run`: это назвал WARNING в 38.

### Документация

`README.md`, раздел «Сетевой фильтр» (создан задачей 38) — подраздел «Как это работает» в конце
раздела: bash запускается через `sg`, правило ядра заворачивает его 80/443 на локальный порт
aish, один слушатель на пользователя обслуживает все shell'ы, без слушателя aish не стартует.

`CLAUDE.md`, «Что легко сломать», новый пункт после «Сессию держит один aish»:
«**Сетевой фильтр держит один aish на пользователя.** Порт зашит в правило ядра
(`/etc/aish/netguard.json`), поэтому слушатель — синглтон под `flock`; остальные экземпляры
ждут лока и подхватывают его, когда владелец вышел. Shell запускается через `sg <группа>`, и
его GID уже не сменить — это и есть граница. Если слушатель не поднялся, aish обязан упасть:
правило ядра всё равно завернёт трафик, и shell останется без сети, думая, что всё в порядке».

### Критерий готовности

- `shell_test.go`: `Wrap` на пути с пробелом и апострофом даёт строку, которую `sh -c` разбирает
  обратно в исходный argv (проверить запуском `/bin/sh -c` с `printf '%s\n'`); `Wrap(nil, …)`
  возвращает аргументы без изменений.
- `sni_test.go`: SNI достаётся из реального ClientHello, записанного `tls.Client` в `net.Pipe`
  (хост `example.org`); на не-TLS байтах (`GET / HTTP/1.1`) — пусто и без ошибки; на обрезанном
  ClientHello — пусто, без паники.
- `listen_test.go` (без netfilter: `origDst` подменяется полем-функцией в `Listener`): соединение
  с `Decide == nil` проходит насквозь к тестовому серверу и переносит байты в обе стороны;
  `Decide` с `false` на порту 80 отдаёт `403` и закрывает; два `Hold` подряд на один лок-файл —
  второй не слушает, а после отмены первого забирает лок (с запасом по времени тикера).
- `internal/proxy/netguard_test.go`: при `netguard.Active() == false` (нет `/etc/aish`) `Run`
  собирает `exec.Command` с прежним argv — проверяется через `Wrap(nil, …)`, без запуска PTY.
- `gofmt -l .` пуст, `go build ./... && go vet ./... && go test ./...`.
- Руками, на машине с применённым `aish harden`: `./aish`, внутри `id -gn` печатает группу
  фильтра; `curl -sS http://example.com` работает (проходит насквозь); `ss -tlnp` показывает
  слушателя; второй `./aish` в другом терминале стартует и тоже ходит в сеть; `sudo nft list
  table inet aish` с `counter` показывает ненулевые пакеты.

## Этап 3. Расшифровка HTTPS в слушателе и сетевые правила в Cedar: `Action::"connect"`

*Бывшая задача `40-2.28-Network-policy-l7`.*

**Приоритет:** средний — ради этого и делались 38 и 39: политика должна видеть метод, хост и
путь запроса, а не argv команды, которая его отправила.
**Файлы:** `internal/netguard/mitm.go` (новый: терминирование TLS сертификатом, выпущенным CA,
кэш листовых сертификатов), `internal/netguard/http.go` (новый: разбор HTTP/1.1, `Request`,
решение, проброс вверх), `internal/netguard/bundle.go` (новый: бандл «системные корни + CA aish»
в `$AISH_RUN`), `internal/netguard/mitm_test.go`, `internal/netguard/http_test.go` (новые),
`internal/netguard/listen.go` (из 39: вызвать `mitm` вместо сквозного `io.Copy`, когда порт 443
и TLS; ~10 строк), `internal/policy/net.go` (новый: `NetInput`, `NetChecker`, `(*Engine).CheckNet`,
`(*Engine).HasNet`, cedar-сторона), `internal/policy/net_test.go` (новый),
`internal/policy/schema.cedarschema` (из 37: сущности `Domain`, `Host`, действие `connect`),
`examples/policy/default.cedar` (из 37: блок сетевых правил в конце),
`internal/proxy/netguard.go` (новый: `Listener.Decide` → `policy.NetInput`, атрибуция по
`p.asking`), `internal/proxy/proxy.go` (`Run`: две строки — присвоить `Decide` и добавить
переменные доверия в `cmd.Env`, строка 191), `cmd/aish/netwarn.go` (новый: предупреждение при
старте), `cmd/aish/main.go` (`shell`: одна строка вызова после проверки `AISH_SOCK`, строка 117),
`README.md` (раздел «Сетевой фильтр» — подраздел «Сетевые политики»), `CLAUDE.md` (новый пункт в
«Что легко сломать» после пункта про сетевой фильтр из 39).
**Зависит от:** 39-2.27-Shell-under-net-gid (слушатель, `Conn`, `Decide`) и
37-1K-Cedar-policy-engine (движок, схема, `cedarChecker`, `combine`).
**Параллельно:** да, но с оглядкой: 31-2.21 тоже правит `internal/policy/policy.go` и
`cache.go` после 37 — здесь `policy.go` не трогается вовсе, всё новое в `net.go`; при
rebase сохранить обе стороны в `schema.cedarschema`. Сигнатура `policy.Cache.Engine` меняется в
31-2.21 (`Engine(ctx, dir, rules)`) и 35-2.25 (список каталогов) — кто второй, тот правит вызов в
`internal/proxy/netguard.go`.

### Проблема

После 38 и 39 трафик shell'а приходит в aish, но тот видит только IP и SNI и пропускает всё
насквозь: ни метода, ни пути, ни заголовков. Политика по-прежнему судит по argv, то есть
`curl "$URL"` для неё — литерал `$URL`.

### Предложение

#### Терминирование TLS

`mitm.go`:
```go
// CA signs the leaf certificates shown to the shell; the real certificate
// of the upstream is verified separately, so a bad one is never papered over.
type CA struct{ cert *x509.Certificate; key crypto.Signer; mu sync.Mutex; leaves map[string]*tls.Certificate }
func LoadCA(st *State) (*CA, error)
func (ca *CA) leaf(name string) (*tls.Certificate, error)  // кэш по имени, срок 30 дней
func (ca *CA) Server(c net.Conn, name string) *tls.Conn    // ALPN только "http/1.1"
```
- `LoadCA` читает `st.CACert`/`st.CAKey`; нет прав на ключ — внятная ошибка «добавьте себя в
  группу <group> и перелогиньтесь».
- Листовой сертификат: CN и SAN из SNI; SNI пустой — SAN из IP назначения.
- ALPN ограничен `http/1.1`: HTTP/2 здесь не разбирается, а молча проксировать h2 как поток
  значило бы не видеть запросы. Клиент, который умеет только h2 без ALPN-согласования, получит
  http/1.1 — это нормально.
- Вверх — `tls.Dial` с обычной системной проверкой и `ServerName` из SNI. Ошибка проверки —
  закрыть соединение и отдать клиенту `502` (внутри нашего TLS); подменять доверие нельзя,
  иначе фильтр ослабляет безопасность вместо усиления.
- Клиент не доверяет нашему CA или пинит сертификат — рукопожатие падает, соединение
  закрывается. Это ожидаемо и описано в README.

`bundle.go`: `WriteBundle(runDir string, st *State) (string, error)` — конкатенация системного
бандла (первый существующий из `/etc/ssl/certs/ca-certificates.crt`,
`/etc/pki/tls/certs/ca-bundle.crt`, `/etc/ssl/cert.pem`) и `ca.crt` в `$AISH_RUN/ca-bundle.crt`
(0644). Нужен программам со своим хранилищем; собирается на каждый старт, поэтому не устаревает.
В `cmd.Env` (`proxy.go:191`) добавляются, только когда фильтр активен:
`REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `AWS_CA_BUNDLE`, `GIT_SSL_CAINFO` — путь к бандлу;
`NODE_EXTRA_CA_CERTS` — путь к `ca.crt`. `SSL_CERT_FILE` **не ставить**: он заменяет набор
корней целиком, а системный уже содержит CA aish после `aish harden`.

#### HTTP

`http.go`: цикл keep-alive поверх расшифрованного (или обычного, порт 80) соединения.
```go
type Request struct {
	Method, Scheme, Host, Path, Query string
	Port    int
	Headers []string // имена, в нижнем регистре, без значений
	Agent   bool     // пришёл, пока работала команда агента
}
```
- `http.ReadRequest` → `Request` → `Decide` → при `allow` отправить запрос вверх
  (`req.Write(up)`), прочитать `http.ReadResponse`, отдать вниз, повторить, пока соединение живо.
- Deny: `403 Forbidden`, тело `aish: <reason>\n`, заголовок `Connection: close`, закрыть.
- `Upgrade:`/`CONNECT`: решение принимается по заголовкам, дальше байты идут насквозь
  (`io.Copy` в обе стороны) — WebSocket не разбирается.
- Таймауты: 30 с на чтение заголовков запроса, дальше без дедлайнов (долгие скачивания).
- Значения заголовков наружу из `netguard` не отдаются никогда: в `Request` только имена.

#### Cedar: действие `connect`

`internal/policy/net.go`:
```go
// NetInput is one HTTP request seen by the network filter.
type NetInput struct {
	Method, Scheme, Host, Path, Query string
	Port    int      `json:"port"`
	Headers []string `json:"headers"`
	Agent   bool     `json:"agent"`
	Model   string   `json:"model,omitempty"`
}

// NetChecker is a Checker that also judges network requests. A checker
// without it simply has no say about them.
type NetChecker interface{ CheckNet(context.Context, NetInput) (Decision, error); HasNet() bool }

func (e *Engine) CheckNet(ctx context.Context, in NetInput) (Decision, error) // combine по всем NetChecker
func (e *Engine) HasNet() bool                                               // хоть один упомянул connect
```
Запрос: principal `Model::"<Model>"`, action `Action::"connect"`, resource `Host::"<host>"`,
родители `Domain::"api.github.com"` → `Domain::"github.com"` → `Domain::"com"` — чтобы правило
писалось как `resource in Domain::"github.com"` и покрывало поддомены. Context:
`{method, scheme, host, path, query, port: Long, headers: Set<String>, agent: Bool}`.

Схема (`schema.cedarschema`, дописать к тому, что дала 37):
```
entity Domain in [Domain];
entity Host in [Domain];
action connect appliesTo {
  principal: [Model], resource: [Host],
  context: { method: String, scheme: String, host: String, path: String, query: String,
             port: Long, headers: Set<String>, agent: Bool }
};
```

**Сетевые правила — opt-in.** Cedar default-deny: политика, в которой про сеть ничего не
сказано, запретила бы весь трафик машины. Поэтому `HasNet()`: `cedarChecker` при загрузке
обходит `ps.Map()` и смотрит у каждой политики область действия (`(*xast.Policy)(p.AST())`,
как в 37 при валидации) — упомянут ли `Action::"connect"` (`ScopeTypeEq`, `ScopeTypeIn`,
`ScopeTypeInSet`). Не упомянут ни у кого — `Listener.Decide` не ставится вовсе, трафик идёт
насквозь без расшифровки. Упомянут — работает обычная семантика Cedar, включая deny при
отсутствии `permit`. Проверять текст файла поиском подстроки нельзя: комментарий включил бы
фильтрацию и оставил машину без сети.

`ask` для сети не поддерживается: на том конце ждёт сокет чужой программы. `@ask`-правило,
сработавшее на `connect`, трактуется как deny с припиской `(ask is not available for network
requests)`.

#### Склейка в прокси

`internal/proxy/netguard.go`: метод `(*Proxy) netDecide(c *netguard.Request) (bool, string)` —
берёт движок из `p.policies.Engine(ctx, cfg.PolicyDir)` (та же `policy.Cache`, что у агента:
правки политик подхватываются без перезапуска), заполняет `NetInput`, `Model` — `p.model`,
`Agent` — `p.asking` под `p.mu`. Ошибка движка — **deny** с текстом ошибки (fail-closed, как в
37). В `Run` слушателю присваивается `Decide` только если `pol.HasNet()`.

#### Предупреждение при старте

`cmd/aish/netwarn.go`: `warnNetPolicy(w io.Writer, cfg config.Config)` — загружает политики
(`policy.Load`); если `HasNet()` и `netguard.Active()` вернул `false`, печатает в stderr:
```
aish: network policies are present but not enforced: <причина из State.Check или "harden is not set up">
      run: sudo aish harden
```
Вызов — одной строкой в `shell()` (`cmd/aish/main.go:117`), после проверки `AISH_SOCK`. Ошибку
загрузки политик здесь глотать (её покажет первый же запрос агента).

Отвергнуто:
- Передавать политике тело запроса — решение принимается до отправки, тело стримится; хэш тела
  ничего не даёт правилу.
- Список хостов «не расшифровывать» — пока их нет, нужны они будут, когда найдётся клиент с
  пиннингом; тогда это отдельный ключ.
- HTTP/2 внутри MITM — отдельная задача, если понадобится; ALPN-ограничение честно её заменяет.

### Границы

- Подстановка секретов и маскирование — задача 41, здесь заголовки только читаются по именам.
- `internal/policy/policy.go`, `checker.go`, `cedar.go`, `argv.go` не трогать: всё сетевое
  живёт в `net.go` (доступ к внутренностям `cedarChecker` есть — тот же пакет).
- `internal/agent` не трогать: сетевое решение принимает прокси, агент о нём не знает.
- Соединения в журнал сессии не писать.
- `internal/config` не трогать.

### Документация

`README.md`, раздел «Сетевой фильтр» — подраздел «Сетевые политики» после «Как это работает»:
что видно политике (action `connect`, `Host`/`Domain`, список полей context), пример правила,
правило opt-in (нет ни одного `connect` — фильтр не вмешивается, есть хоть одно — работает
default-deny Cedar и нужен явный `permit`), почему нет `ask`, что h2 понижается до http/1.1 и
клиент с пиннингом сертификата просто не соединится.

`examples/policy/default.cedar` — блок в конце, с комментарием «раскомментируйте, чтобы включить
сетевые правила»… нет: закомментированный Cedar не проверяется схемой и устареет. Добавить
рабочий блок:
```
permit(principal, action == Action::"connect", resource);

@reason("the agent may not reach anything but the allowed hosts")
forbid(principal, action == Action::"connect", resource)
when { context.agent }
unless { resource in [Domain::"github.com", Domain::"githubusercontent.com"] };
```
и в README предупредить, что этот блок включает фильтрацию (если `aish harden` не сделан —
старт печатает предупреждение).

`CLAUDE.md`, «Что легко сломать», новый пункт после пункта про сетевой фильтр из 39:
«**Сетевые правила включаются сами по себе.** `Engine.HasNet()` — единственный выключатель:
появилось правило с `Action::"connect"` — фильтр начинает решать, и Cedar default-deny
относится и к сети. Определять наличие правил по тексту файла нельзя, только по AST. Ошибка
движка на сетевом запросе — deny, как и на вызове инструмента».

### Критерий готовности

- `mitm_test.go`: `tls.Client` с CA из тестового `State` в корнях соединяется через `Server`,
  получает сертификат с нужным SAN, кэш отдаёт тот же объект на второй запрос; при неверном
  сертификате вверх (`httptest.NewTLSServer`, чей CA не в корнях) клиент получает `502`.
- `http_test.go`: `GET` с заголовками проходит к тестовому серверу, `Request.Headers` содержит
  имена в нижнем регистре и не содержит значений; `Decide == false` даёт `403` с причиной;
  keep-alive: два запроса в одном соединении оба проходят через `Decide`.
- `net_test.go`: политика без `connect` — `HasNet() == false`; с `permit(… action ==
  Action::"connect" …)` — `true`; комментарий со словом `connect` — `false`; `resource in
  Domain::"github.com"` срабатывает для `api.github.com` и не срабатывает для
  `github.com.evil.tld`; `context.agent` различает запросы; `@ask` на `connect` даёт deny с
  припиской; ошибка вычисления — deny.
- `gofmt -l .` пуст, `go build ./... && go vet ./... && go test ./...`; тесты 37 на
  `examples/policy/default.cedar` проходят с добавленным блоком.
- Руками, на машине с `aish harden`: с политикой из примера `curl -sS
  https://api.github.com/meta` работает, `curl -sS https://example.com` отдаёт `403` с текстом
  причины, в логе ничего не падает; убрать правила `connect` — оба запроса проходят; убрать
  `harden` — старт aish печатает предупреждение.

## Этап 4. Подстановка секретов в исходящие запросы: токен есть у aish, но не у модели

*Бывшая задача `41-2.29-Inject-secrets-in-requests`.*

**Приоритет:** низкий — последний шаг сетевого фильтра и самый узкий по пользе: прячет значение
токена от модели, но не отнимает у неё возможность этим токеном воспользоваться.
**Файлы:** `internal/netguard/secrets.go` (новый: `Secrets`, `LoadSecrets`, `Rule`, `Inject`),
`internal/netguard/secrets_test.go` (новый), `internal/netguard/http.go` (из 40: три строки —
вызвать `Inject` между решением и отправкой запроса вверх), `internal/proxy/netguard.go` (из 40:
загрузка секретов один раз при старте, передача слушателю, вычёркивание использованных
переменных из окружения shell), `internal/proxy/proxy.go` (`Run`: `cmd.Env` — вычеркнуть имена
переменных, названных в `net-secrets.yaml`, рядом со строками переменных доверия из 40,
строка 191), `README.md` (раздел «Сетевой фильтр» — подраздел «Подстановка секретов»),
`CLAUDE.md` (строка `~/.config/aish/net-secrets.yaml` в «Конфигурация и пути» после строки про
`mcp.yaml`).
**Зависит от:** 40-2.28-Network-policy-l7 — нужен разбор HTTP и `Action::"connect"`.
**Параллельно:** да.

### Проблема

Чтобы агент сходил в API, токен сейчас приходится положить в окружение shell'а или в команду —
и он немедленно оказывается и в журнале сессии, и в контексте модели (`env`, `cat .env`,
эхо команды). После 40 весь HTTPS-трафик shell'а и так идёт через aish: он может дописывать
заголовок авторизации сам, а в окружении shell'а токена не будет вовсе.

Чего это **не** даёт, и это надо сказать прямо в документации: агент по-прежнему может
отправить любой запрос, который правило `connect` разрешает, и aish приложит к нему токен —
классический confused deputy. Польза здесь — не «агент не может», а «агент не знает значения и
не может унести его в другое место»; ограничивает же его только сетевая политика. Плюс тот же
uid: файл с секретами агент прочитает, если доберётся до него, — поэтому рекомендуемый способ
задать значение не литерал, а команда менеджера паролей.

### Предложение

#### `~/.config/aish/net-secrets.yaml`

```yaml
- host: api.github.com          # точное имя или *.example.com
  path: /repos/                 # необязательно: префикс пути
  methods: [GET, POST]          # необязательно: по умолчанию любой
  override: false               # по умолчанию: не трогать заголовок, если клиент прислал свой
  headers:
    Authorization: "Bearer ${GITHUB_TOKEN}"
- host: api.internal.corp
  headers:
    X-Api-Key: { command: "pass show corp/api" }
```
`secrets.go`:
```go
type Rule struct {
	Host     string            `yaml:"host"`
	Path     string            `yaml:"path"`
	Methods  []string          `yaml:"methods"`
	Override bool              `yaml:"override"`
	Headers  map[string]Value  `yaml:"headers"`
}

// Value is a literal, ${ENV} or the output of a command, resolved once at
// load: the request path must not fork a process.
type Value struct{ Literal, Env, Command string }

type Secrets struct {
	rules []Rule
	envs  []string // names used as ${ENV}: the shell must not see them
	vals  []string // resolved values, for callers that must never log them
}

func LoadSecrets(path string, getenv func(string) string) (*Secrets, error)
func (s *Secrets) Env() []string                       // envs
func (s *Secrets) Inject(r *Request, h http.Header) int // how many headers were set
```
- Файла нет — `nil, nil`, всё молчит.
- Права строже 0600 (доступен группе или всем) — ошибка `net-secrets.yaml: must be 0600`.
  Секреты, лежащие с групповым доступом, — именно то, от чего задача защищает.
- `${VAR}` разрешается из окружения **процесса прокси** один раз при загрузке; переменной нет —
  ошибка с именем. `command:` исполняется один раз при загрузке через `exec.Command("/bin/sh",
  "-c", …)` с таймаутом 10 с, результат берётся без хвостового `\n`; ненулевой код — ошибка.
- `Inject` применяет все подходящие правила по порядку (последнее выигрывает): хост совпал
  (точно или по `*.suffix`), путь начинается с `Path`, метод в списке. Заголовок ставится, если
  его нет у клиента или `override: true`.
- `vals` наружу не отдаются; в `Request`/логи попадает только число и имена заголовков.

#### Связь с остальным

- Загрузка — один раз в `internal/proxy/netguard.go` при старте слушателя. Ошибка загрузки —
  **отказ стартовать**: файл есть, но не применился — худший вариант, запросы уйдут без токена
  или с ним в открытую.
- **Требование политики.** Если `net-secrets.yaml` есть, а `Engine.HasNet()` (из 40) ложен —
  ошибка старта: `net-secrets.yaml requires at least one Action::"connect" policy: otherwise
  any request the agent makes gets the token`. Подставлять секреты без ограничения, куда можно
  ходить, нельзя.
- **Окружение shell'а.** В `proxy.go` из `cmd.Env` вычёркиваются переменные, названные в
  `Secrets.Env()`: токен остаётся у прокси и уходит в заголовок, но ни агент, ни пользовательские
  команды в этом shell его не видят (`env`, `echo $GITHUB_TOKEN` — пусто). В процессе прокси
  переменная остаётся.
- Подстановка происходит **после** решения политики: правило `connect` видит заголовки, которые
  прислал клиент, и не видит подставленных. Так и задумано — иначе политику можно было бы
  обойти, прислав заголовок самому.

Отвергнуто:
- Ключ в `config.toml` — путь фиксированный (`config.Dir()/net-secrets.yaml`), чтобы не
  трогать `config.go`, который правят четыре открытые задачи.
- Глоб `/repos/**` — префикса достаточно, а полноценный glob по пути тянет зависимость.
- Маскировать значение секрета, если сервер вернул его в ответе, — ответ идёт на экран
  пользователя и в журнал как есть; что видит модель, решает 33-2.23.

### Границы

- Маскирование вывода и журнала — 33-2.23, не здесь.
- `internal/config` не трогать.
- Ключ API модели (`api_key_env`, `ANTHROPIC_API_KEY`/`OPENAI_API_KEY`) в `net-secrets.yaml` не
  класть и в README об этом сказать: после 13-1A прокси берёт его из окружения *shell*
  (`cfg.KeyFrom(ex.Getenv)` в `Proxy.prepare`), а эта задача вычёркивает названные переменные
  именно оттуда — ключ пропадёт, запросы к модели перестанут ходить. Трафик к API модели идёт
  из прокси, не из shell, и под фильтр всё равно не попадает.
- `internal/agent` не трогать: агент о секретах не знает ничего, и это главное свойство.
- Не прятать от агента сам файл `net-secrets.yaml` политикой — это иллюзия защиты (тот же uid);
  вместо этого README рекомендует `command:` вместо литерала.
- Не поддерживать подстановку в тело запроса и в query — только заголовки.

### Документация

`README.md`, раздел «Сетевой фильтр» — подраздел «Подстановка секретов» после «Сетевые
политики»: формат файла с обоими примерами значений, права 0600, что переменные из `${…}`
исчезают из окружения shell'а, что файл требует хотя бы одного правила `connect`, и абзац
честных ограничений — агент может пользоваться токеном в рамках сетевой политики, может
прочитать сам файл (тот же uid), а эхо секрета в ответе сервера попадёт на экран.

`CLAUDE.md`, «Конфигурация и пути», после строки про `mcp.yaml`: «`~/.config/aish/net-secrets.yaml`
(0600) — заголовки, которые сетевой фильтр подставляет в исходящие запросы; переменные,
названные в нём, вычёркиваются из окружения shell'а».

### Критерий готовности

- `secrets_test.go`: файл с правами 0644 — ошибка; `${VAR}` разрешается, отсутствующая
  переменная — ошибка с именем; `command:` берёт stdout без `\n`, ненулевой код — ошибка;
  `*.example.com` совпадает с `api.example.com` и не совпадает с `example.com.evil.tld`;
  префикс пути и список методов отсекают; `override: false` не трогает заголовок клиента,
  `true` заменяет; `Env()` возвращает только имена из `${…}`.
- `http_test.go` (дописать в файл из 40): запрос к совпавшему хосту доходит до тестового сервера
  с подставленным заголовком, а `Request.Headers`, которые видела политика, его не содержат.
- `internal/proxy`: тест, что `cmd.Env` не содержит переменных из `Secrets.Env()`, а окружение
  самого процесса — содержит.
- `gofmt -l .` пуст, `go build ./... && go vet ./... && go test ./...`.
- Руками: положить `net-secrets.yaml` с `Authorization: "Bearer ${GH}"`, экспортировать `GH`,
  запустить aish; в shell `echo "$GH"` пусто, `curl -sS https://api.github.com/user` возвращает
  профиль; убрать все правила `connect` из политики — aish не стартует с внятной ошибкой.
