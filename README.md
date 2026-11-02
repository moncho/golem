# Golem

A personal agent you hand goals to and walk away from. It keeps working after
you detach, runs real commands in your workspace, and reports back on the
console or in Slack.

Golem is one Go binary around a [Pi Durable](https://github.com/earendil-works/pi/tree/main/packages/durable)
application, using the coordination model from DeepSeek Harness's
[Exploring Super-agent Design](https://x.com/DeepSeekHarness/status/2116857257245987236).

> **Golem runs shell commands as you.** Workers execute commands on your
> machine with your account's permissions, decided by a language model, with no
> sandbox and no approval step. Point it only at workspaces and credentials you
> would hand to a colleague with a terminal. See [SECURITY.md](SECURITY.md).

## How it works

Three roles share the work:

- The **Router** talks to you and decides. It is the only role that can reply.
- **Workers** pursue one goal each. Only they run commands, and they can take
  new instructions while a command is still running.
- The **Watchdog** audits running work and nudges or stops what has stalled.

Commands are asynchronous: a Worker starts one, gets a reference back, and is
woken when it finishes. Tasks, conversations and schedules live in Pi's durable
storage, so work survives a restart and a finished task can be resumed weeks
later in its original conversation. A Worker that cannot finish says who it is
waiting on and can schedule its own check.

## Quick start

You need Go 1.27+ and Node.js 22.19+ on macOS or Linux.

```sh
make build        # builds bin/golem with the Pi host embedded
bin/golem demo    # scripted run, no network or API key
```

Give it a model and start it:

```sh
export GOLEM_PROVIDER=anthropic GOLEM_MODEL=claude-sonnet-5-5 ANTHROPIC_API_KEY=...
bin/golem serve --workspace /path/to/your/project
```

Then, from other terminals:

```sh
bin/golem attach   # the console: type a request, /detach leaves work running
bin/golem watch    # a live view of the Router, the Workers and their commands
```

## What else it does

- **[Slack](docs/GUIDE.md#slack-and-ambient-events)**: answers mentions and DMs in the
  thread, as a Slack agent with session status and a stop button.
- **[Herma](docs/GUIDE.md#principles-and-shared-memory-in-herma)**: optional shared
  principles and memory for every role.
- **[Follow-through](docs/GUIDE.md#continue-outcomes-not-just-executions)**: outcomes
  that wait on someone, scheduled checks, and resuming old tasks.
- **[A service](docs/GUIDE.md#run-it-under-a-service-manager)**: `golem service install`
  keeps it running under launchd or systemd.
- **[Remote execution](docs/GUIDE.md#independent-execution-endpoint)**: run commands on
  another machine through `golem executor`.

The [guide](docs/GUIDE.md) has the configuration, every command and flag, and
the development workflow.

## How it is built

Go owns the program: configuration, the state lock, command execution, the
console socket, Slack and Herma. TypeScript under `host/` holds only the Pi
application and a thin host, bundled and embedded in the binary. The two speak
JSON Lines over the host's stdin and stdout, and that protocol is the seam for
replacing Pi later. Pi owns model calls, conversations, compaction, retries,
durable storage and scheduling; Golem does not reimplement them.

## License

MIT. See [LICENSE](LICENSE).
