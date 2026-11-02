# Golem guide

How to build, configure and run Golem. The [README](../README.md) explains what
it is; [SECURITY.md](../SECURITY.md) explains what it can do to your machine.

- [Try it](#try-it)
- [Use a model](#use-a-model)
- [Keep work running while you detach](#keep-work-running-while-you-detach)
- [Run it under a service manager](#run-it-under-a-service-manager)
- [Watch it work](#watch-it-work)
- [Continue outcomes, not just executions](#continue-outcomes-not-just-executions)
- [Principles and shared memory in Herma](#principles-and-shared-memory-in-herma)
- [Independent execution endpoint](#independent-execution-endpoint)
- [Slack and ambient events](#slack-and-ambient-events)
- [How it is built](#how-it-is-built)
- [Development and verification](#development-and-verification)

## Try it

Building requires Node.js 22.19+ and Go 1.27+ on macOS or Linux.

```sh
make build
bin/golem demo
```

`make build` installs the host's pinned npm dependencies, bundles the host into
`internal/host/host.mjs` and builds `bin/golem` with that bundle embedded. The
bundle is generated and git-ignored, so `go build` alone fails on the missing
embed until `make build` has run once. To run Golem you need only the `golem`
binary and Node.js 22.19+ on `PATH`; no checkout or `node_modules`.

The demo uses **scripted model decisions, the real Pi runtime, SQLite, and real
local commands**. It runs `golem serve` on a temporary state directory, restarts
the host while a command keeps running, completes an independent task, delivers
a followup directly to the first Worker, cancels the obsolete command, and
returns verified replacement output through the Router. It needs no network or
API key and removes its temporary state afterward.

## Use a model

Set a provider and a model from Pi’s catalog. For example:

```sh
export GOLEM_PROVIDER=openai
export GOLEM_MODEL=gpt-4.1-mini
export OPENAI_API_KEY='your-api-key'
golem serve --workspace /path/to/your/project
```

`openai`, `anthropic`, and `deepseek` use Pi’s native providers and their respective
`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, or `DEEPSEEK_API_KEY`. `GOLEM_API_KEY` overrides
the selected provider’s key. To use a Chat Completions endpoint, set
`GOLEM_PROVIDER=compatible`, `GOLEM_BASE_URL`, `GOLEM_MODEL`, and `GOLEM_API_KEY`.
The compatible adapter declares a 128K context window and 8K maximum output; use
a native provider for catalog-specific limits and capabilities.

Values can also live in `.env` in the working directory; environment variables
take precedence. See [golem.env.example](../golem.env.example). `--env-file PATH`
selects another file. `GOLEM_ROUTER_MODEL` optionally selects a different Router
model on the same provider. Workers and Watchdog use `GOLEM_MODEL`. Claude CLI
OAuth credentials are not imported. At start, `golem serve` logs one `Golem configured`
block naming the provider, models, proactive policy, workspace, execution
environments, Slack and Herma settings, never a key or token. From then on
it narrates the day in one line per event: a message admitted, a Worker
started, a command started or finished with its exit code and duration, a
Worker's report, a reply delivered or failed, a timer fired. Lines carry ids,
states and counts only, never message or command text. On a terminal the log
is coloured and aligned; in a file it is logfmt. Set
`GOLEM_DEBUG=1` for debug logging, including the host's diagnostics.

`golem` with no subcommand is `golem serve`. Its flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--state DIR` | `.golem-pi` | Private state directory. |
| `--workspace DIR` | `.` | Execution root for local commands. |
| `--env-file PATH` | `.env` | Configuration file. |
| `--slack` | off | Receive and reply on Slack. |
| `--herma` | off | Insist on Herma; `GOLEM_HERMA_URL` in `.env` enables it by itself. |
| `--proactive POLICY` | `off` | Ambient events: `off`, `observe` or `act`. |
| `--retain DURATION` | `336h` | How long finished command output is kept. |

## Keep work running while you detach

`golem serve` has no console of its own. Leave it running in its terminal or
under a service manager (see below), and in another terminal connect to it:

```sh
golem attach
```

Use the same `--state DIR` for both commands when choosing a custom directory.
The console talks to the private Unix socket `<state>/controller.sock`; it does
not open a second Pi writer or need model credentials. `/detach`, `/quit`,
Ctrl+C, or closing the attached console leave the service and its commands
running. `golem serve` itself must stay alive; stopping it stops its local
commands. If `golem` crashes, its local commands keep running until the next
start kills them, and their records read `interrupted`. The independent HTTP
endpoint described below can outlive it.

Try a task, then redirect it while it runs:

```text
Inspect this project and run its tests. Report failures with evidence.
Actually, focus on the scheduler tests first.
```

Console commands:

- `/tasks QUERY` searches active and archived work; `/tasks` lists recent tasks.
- `/inspect ID` shows the goal, outcome, next action and retained command records.
- `/resume ID instruction` continues that task in its original Pi conversation
  and execution environment, directing results to the selected console thread.
- `/thread NAME` switches topics and shows recent saved messages. Use a
  different thread for independent work.
- `/history` pages backward through saved console input and Router replies.
- `/status` shows tasks, commands, queued replies, timers and failed model runs;
  `/retry` retries an input whose acknowledgement was lost, using the same
  request identity while attached.
- `/detach` or `/quit` leaves work running. Reattach to inspect progress and
  steer it again.

The console polls for replies it has not yet shown while attached and reconnects
when the service returns. An unacknowledged client request is held in memory for
`/retry`; it is not a durable offline client outbox. Inspect saved history before
resubmitting an uncertain request after exiting that client. Saved Router
messages describe the queued reply; external-channel delivery status is reported
separately. A request the application refuses (for example "Worker limit
reached.") is shown as written; any other failure shows a generic message and
the cause goes to the service's log.

## Run it under a service manager

If the host process exits, `golem serve` logs it and exits non-zero, so a service
manager should restart it. The service manager throttles a process crash loop;
Pi's `restartLimit` only ends a durable task a crash keeps cutting off inside an
opened harness.

Install it as a user service with the same flags you pass to `serve`:

```sh
golem service install --slack --workspace /path/to/project
```

On macOS this writes `~/Library/LaunchAgents/local.golem.plist` and loads it
with `launchctl`; on Linux it writes `~/.config/systemd/user/golem.service`
and enables it. The unit runs this binary's absolute path with absolute
`--state`, `--workspace` and `--env-file`, carries your current `PATH` so
Node and `herma` resolve, restarts on failure, and throttles a crash loop
(launchd `ThrottleInterval` 30 s; systemd five starts per five minutes). The
log goes to `~/Library/Logs/local.golem.log` or
`~/.local/state/golem/local.golem.log`. `golem service status` and
`golem service uninstall` manage it; `--label` names a second service.
Installing refuses while another `golem serve` holds the state directory, so
stop a terminal session first.

`node` must be on the service's `PATH`. Only one `golem serve` may own a state
directory: it holds a lock on `<state>/lock`, which its host inherits. A
replacement started while the previous host is still exiting waits up to ten
seconds for the lock and otherwise refuses to start.

## Watch it work

```sh
golem watch --state .golem-pi
```

`golem watch` fills the terminal with the service's hierarchy, drawn live from
the same socket the console uses: the Router at the top with the threads it
serves and its reply queue, the Watchdog beside it with a heartbeat and a
sweeping beam, and one box per Worker hanging from the Router. A box shows the
Worker's id, title, thread and state, the command line it is running with a
pipeline of chevrons flowing while the process runs, the elapsed time and the
job id; a finished command shows its exit code, and a Worker that is waiting
shows the next actor and action instead. New Workers get a pulse travelling
down from the Router, reports a pulse travelling up, and finished Workers fade
out after ten seconds into a short "recently finished" list, so an idle
screen still says what Golem did last. Press `q` or Ctrl-C to leave; the
service is untouched.
If the service stops, the footer says so and the watcher keeps retrying. When
stdout is not a terminal it prints one frame and exits.

## Continue outcomes, not just executions

The Router has `find_work` for lexical search across task titles, goals and saved
summaries, including archived tasks. It can read the original conversation with
`recall` and use `resume_task` to continue it. `start_task` also performs a lookup:
an identical active goal on the same thread returns that Worker, and related
candidates are returned before a new Worker is created. A candidate shares at
least half of the goal's words with a task, and either two or more words or one
word with active work on the same thread. Creating a separate outcome despite
those matches requires an explicit `new_reason`, kept in Pi's tool history. Matching is lexical, not embeddings or an automatic promise
that two differently worded requests describe the same task.

Workers report an outcome independently of command completion:

| Outcome | Meaning |
| --- | --- |
| `open` | Work is in progress. |
| `waiting_reply` | Another party must respond. |
| `needs_decision` | A decision is needed before continuing. |
| `check_later` | A future check should continue this same task. |
| `resolved` | The requested outcome has been verified. |

Unresolved reports name the next actor and concrete next action. `check_later`
also requires a future `check_at`; Pi persists that wakeup across service
restarts. A newer report, cancellation or resolution invalidates the superseded
check; a user message alone does not, so a Worker that should stop checking
reports again. Resuming clears the stale next actor and action and does not
replay previous commands.
The Worker must inspect current evidence before acting again. A successful shell
command alone does not automatically resolve the outcome.

Fresh state goes to `.golem-pi/`; `--state DIR` selects another directory. Reopen
the same directory to resume Pi conversations, subscriptions, pending events,
timers and unsent replies. Old `.golem/`, `store/`, `data/`, `groups/`,
credentials and running services are not migrated or modified.

```sh
golem status --state .golem-pi
```

`golem status` prints the state as JSON without writing to it, while the service
runs or not, and adds `execution`, the local command counts by status. Model
failures are recorded by Pi and shown in status. Correct provider access, then
send another instruction on the relevant thread to continue. A completed
command is not blindly replayed when the service resumes.

While a Worker follows a thread, every message on that thread goes to the Worker,
not to the Router. A Worker cannot cancel its own task; it returns unrelated
requests to the Router. To reach the Router directly, for example to stop work
whose Worker is failing, switch with `/thread NAME` or use another Slack thread.

A fired timer may reply, start work and stop work as the person who scheduled it
could, but it cannot schedule further timers; only a person can.

Finished task metadata moves to a Pi-backed archive seven days after it ends,
remaining searchable and resumable. The archive keeps the newest five hundred
tasks. Its command records, settled replies and spent timers leave the hot
application state. Pi keeps conversations and event entries, including saved
console replies. Unresolved work is not archived merely because a command
finished. Tasks whose metadata had already been pruned before this feature are
not reconstructed. The deduplication index keeps the most recent ten thousand
event keys.

## Principles and shared memory in Herma

Herma is optional. Without `GOLEM_HERMA_URL` Golem never calls the `herma`
CLI, which then need not be installed, and the roles work from their built-in
prompts with no `memory` tool.

Enable accepted [Herma](https://github.com/moncho/herma) principles as Golem's
behavior source by naming the server in `.env`:

```sh
GOLEM_HERMA_URL=https://herma.example
```

Herma is on whenever `GOLEM_HERMA_URL` is set; `--herma` only insists on it and
fails when the URL is missing. `golem` reads global principles through
`herma --url <GOLEM_HERMA_URL> principles`, with the `herma` CLI found on
`PATH`. The project comes
from the workspace: if the `--workspace` directory, or a parent up to its Git
root, holds a `.herma-project.json` binding (written by `herma project bind`),
its project's accepted principles are loaded too and memory recall and proposals
are scoped to it. `GOLEM_HERMA_PROJECT` overrides that with an explicit project
ID. The `herma` CLI reads its token from `~/.config/herma/credentials.json`;
when that file holds several identities, set `GOLEM_HERMA_IDENTITY` to the one
Golem should use (it is passed to the CLI as `HERMA_IDENTITY`). It does not
create a project or bind the checkout. When principles cannot be loaded at
start, the CLI's own error is logged beside the generic message. The `herma` command receives
only the basic process environment and `HERMA_*` variables, never model or
channel keys.

Principles are read at most once every five seconds and included in every model
request for Router, Workers and Watchdog. Edit and review behavior in Herma;
accepted changes reach the next request after that without restarting Golem. Proposals, knowledge records and task notes
are not loaded as behavioral instructions. Herma defines preferences and working
practices; role/tool permissions, admission and publication checks remain enforced
by Golem. Pi continues to own conversations and execution scheduling.

All three roles also receive a `memory` tool: `recall` searches reviewed Herma
knowledge and principles, `get` follows record references, and `propose` submits
new durable knowledge for human review. Agents are instructed to recall past
decisions before making related choices and to avoid storing facts already
recorded in code, tests or documentation. Proposals carry conversation provenance
and a stable request ID for retries; Golem never accepts them automatically.
Principle changes remain subject to owner confirmation and Herma review.

Recalled records are evidence, not instructions or permission. Herma holds shared
memory across sessions; Pi holds current conversations, jobs and pending work.
Golem's existing `recall` tool reads that operational history, while `memory`
reads and proposes shared Herma knowledge.

Startup requires a successful read. During an outage an already-running process
uses its last complete successful read and logs that fallback. The cache is only
in memory, and refresh resumes automatically when Herma is reachable.

## Independent execution endpoint

`golem serve` runs local commands in its own process and keeps their records and
output under `<state>/execution`. That keeps commands independent of individual
model requests, but stopping the service also stops them. To keep commands alive
across a service exit, run the endpoint independently:

```sh
export GOLEM_NODE_TOKEN='a-long-random-secret'
golem executor --workspace /path/to/project --state /path/to/node-state --listen 127.0.0.1:7373
```

Configure the service with `GOLEM_NODE_URL=http://127.0.0.1:7373` and the same
`GOLEM_NODE_TOKEN`. Ask Golem to use the `remote` environment; `local` remains its
default. For another machine, use TLS or a private tunnel. Command IDs include
the service’s persistent namespace so separate services can share a node.

The endpoint persists intent before starting a process. Repeated starts with the
same ID return the same job; stop requests persist even before a job starts.
Service disconnection does not cancel independently hosted jobs. If an endpoint
itself restarts, unfinished jobs become `interrupted`: Workers must inspect
effects before deciding what to do next. Arbitrary shell effects are not
transactionally reversible or guaranteed exactly once.

Job records are kept permanently; they are what stops a repeated start from
running a command twice. Only command output is swept, once a job has finished
and is older than `--retain` (default `336h`, on both `golem serve` and
`golem executor`). A Worker that reads swept output gets `Output no longer retained.`

Commands run with the execution account’s permissions. Workspace directories
are working directories, **not security sandboxes**; a command can access files
available to that account. Output is bounded and read in chunks.

`golem executor --stdio` serves the same endpoint over its stdin and stdout; the
host's TypeScript tests use it.

## Slack and ambient events

Slack Socket Mode is optional. Configure `SLACK_BOT_TOKEN`, `SLACK_APP_TOKEN`,
and `GOLEM_SLACK_USERS`, then run:

```sh
golem serve --slack --workspace /path/to/project
```

Copy [slack-manifest.example.yaml](../slack-manifest.example.yaml) to
`slack-manifest.yaml`, which git ignores, give the app your own name there,
and create the app from your copy. The manifest turns on
Socket Mode, enables the messages tab for direct messages, and declares the bot
scopes and the `app_mention` and `message.*` event subscriptions Golem relies
on. After creating it, generate an app-level token with `connections:write`
(`SLACK_APP_TOKEN`) and install the app to get the bot token
(`SLACK_BOT_TOKEN`). The manifest cannot carry an icon: upload
[assets/golem-icon.png](../assets/golem-icon.png) under Basic Information, Display
Information.

The manifest declares Golem as a Slack agent (`agent_view`, `assistant:write`),
so it is labelled as one and reachable from Slack's agent entry point. Each
thread is an agent session: Slack shows it as working while a Worker runs,
as waiting when the Worker needs someone, and idle again when it reports or is
cancelled. The session's stop button cancels the Worker following that thread
and its commands, for people on the allowlist. Agent features need a Slack
plan that includes them; where they are unavailable Golem logs that once and
carries on as an ordinary bot.
Explicit requests are mentions and direct messages from allowed people in
allowed channels, and any later reply those people post inside a thread Golem
has already taken part in, mention or not. `GOLEM_SLACK_USERS` decides who may give Golem work. Anyone else who mentions
Golem or writes to it directly gets one fixed reply, sent without a model call
and at most once per person per hour: "I only take requests from people on my
owner's list. Ask them to add you." Set `GOLEM_SLACK_REFUSAL` to your own text,
or to `off` for silence. By default those people
are heard in every conversation the bot is in: the channels it was invited to
and its direct messages. Set `GOLEM_SLACK_CHANNELS` only to restrict it to
specific conversation IDs. Replies go to the originating
Slack thread. The socket
acknowledges an event only after durable admission; repeated deliveries dedupe
by channel/message timestamp. Slack tokens never leave the `golem` process.

Admission checks source, channel and sender before invoking a model. Every active
Worker subscribed to a thread receives its events directly. Those events are not
also dispatched through the Router. A Worker can follow several known threads
or return an unrelated followup to the Router.

`--proactive off` is the default: ambient events without subscribers are ignored.
`observe` allows Router consideration without starting work or publishing.
`act` permits silent speculative work. Publication requires a useful final
report backed by a successful command from that task. `GOLEM_SLACK_BOTS` is an
optional allowlist of bot IDs; bots never grant explicit-request authority.

Replies use Pi’s durable scheduler, retry with backoff, and preserve order within
a thread. A reply Slack rejects permanently (archived channel, bot not in the
channel, message too long) or that still fails after ten attempts is marked
`failed`, logged, listed by `/status`, and no longer blocks later replies on its
thread. An external channel may receive a duplicate if it accepted a reply
but its acknowledgement was lost.

## How it is built

`golem` is the main program. It reads flags and `.env`, holds the state lock,
runs commands, serves the console socket, connects to Slack and runs `herma`.
It extracts the embedded host bundle to `<state>/host/` and runs it under Node
with only the basic process environment. The host opens Pi on
`<state>/golem.sqlite` and runs the Golem application: roles, tools, prompts,
durable tasks and state documents. The two talk over the host protocol, JSON
Lines over the host's stdin and stdout. The host takes all configuration,
including the model key, from the protocol's first message, and asks `golem`
to send replies, run commands and read Herma. Policy checks and application
state stay inside Pi transactions; `golem` keeps no copy of them.

The earlier native Go controller and its container/group/IPC design were
removed, and Golem was rebuilt as a TypeScript application on Pi Durable so Pi
owns the durable runtime. The Go main was then restored around Pi, with Pi
behind the host, so Golem is one binary and TypeScript is limited to what Pi
requires. The host protocol is the seam for replacing Pi later: a new host
side can replace it without changing the Go program.

## Development and verification

```sh
make build
make check
```

`make check` builds, then runs the TypeScript type check and the host tests,
`go vet ./...`, `go test -race ./...` and `bin/golem demo`, which ends with a
`PASS:` line.

`npm --prefix host run test:live` is opt-in and makes billed model requests. Run
`make build` first; it starts `bin/golem executor --stdio`. It reads only exported
environment variables, never `.env`: `GOLEM_PROVIDER`, `GOLEM_MODEL`,
`GOLEM_ROUTER_MODEL`, `GOLEM_BASE_URL`, and `GOLEM_API_KEY` or the provider's
`<PROVIDER>_API_KEY`. Without `GOLEM_MODEL` it selects OpenAI `gpt-4.1-mini`.
It uses a temporary Python fixture, reopens Pi while a command runs, redirects
the Worker to cancel that command, asks it to repair the fixture and run tests,
and independently checks the result. It requires Python 3. Run it to validate
the configured provider and the complete model-driven workflow.

Pi Durable is experimental. Dependencies are pinned to **1.5.0** in
`host/package-lock.json`; upgrades need the integration tests, including
recovery and cancellation.

Source layout:

- `cmd/golem/`: the subcommands `serve`, `attach`, `status`, `demo` and
  `executor`, the service that ties the pieces together, and the end-to-end test.
- `internal/config/`: `.env` parsing, environment precedence and validation.
- `internal/host/`: the embedded bundle, its extraction, the host process, the
  state lock and the protocol peer.
- `internal/executor/`: native command lifecycle, local and over HTTP.
- `internal/console/`: the controller socket server and the `attach` console.
- `internal/slack/`: Socket Mode, event parsing and reply delivery.
- `internal/herma/`: principles with caching and outage fallback, and memory calls.
- `host/src/golem.ts`: Pi extensions, role tools, durable application tasks.
- `host/src/state.ts`: Golem documents stored by Pi; immutable events use Pi entries.
- `host/src/tasks.ts`: historical task matching; task archives and followups stay in Pi.
- `host/src/prompts.ts`: the role prompts.
- `host/src/host.ts`, `host/src/protocol.ts`: the host's `serve` and `status`
  modes and the protocol peer.
- `host/src/models.ts`: model setup from the protocol's first message.
- `host/src/executor.ts`: commands through the protocol.
- `host/src/status.ts`: the status report.
- `host/src/demo.ts`: the scripted decisions behind `golem demo`.
- `host/src/live.ts`, `host/src/stdio.ts`: the live test and its pipe client
  for `golem executor --stdio`.
- `host/test/`: Pi integration and policy tests with deterministic model decisions.

There is no parallel Golem model loop, session store, context compactor or task
scheduler. Those responsibilities belong to Pi.
