# Working on Golem

Read README.md for what Golem is and docs/GUIDE.md for supported behavior and
commands. Golem is one Go program,
`golem`, around a Pi Durable application. Go owns the program: configuration,
process supervision, execution, channels, Herma and the console. TypeScript under
`host/` owns only the Pi application and the host. The host protocol (JSON Lines
over the host's stdin and stdout) is the seam between them. Keep policy and
application state out of Go. Pi owns model calls, sessions, compaction, retries,
durable storage and scheduling. Do not introduce a second implementation of those
facilities.

- Only the Router publishes to people; the one exception is the Slack adapter's
  fixed, model-free refusal to people outside the allowlist. Only Workers start commands. The Watchdog
  audits independently and may nudge or stop work based on evidence.
- Start commands asynchronously, return references immediately, and deliver
  completion through Pi tasks. Preserve direct thread subscriptions and followups
  while commands run. Pi’s default blocking shell tool does not provide this contract.
- Commit application intent and the safe tool effect receipt in the same Pi
  transaction. Stable, namespaced command IDs prevent duplicate starts. Interrupted
  effects require inspection, not blind replay.
- Keep one source of truth: application documents, event entries and scheduled
  work belong in Pi. Native command records/output belong at the endpoint.
- Search related active and archived tasks before dispatch. Reuse conversation
  and execution ownership when resuming. Retain unresolved outcomes with a next
  actor/action, and keep future checks in Pi's scheduler.
- Console attachment must never own the service lifetime. Disconnecting a client
  leaves work running; only the controller owns its local execution endpoint.
- Herma owns principles and durable shared knowledge. Task archives and current
  execution state remain operational records in Pi, not duplicate Herma memory.
- Preserve deterministic admission and publication checks. Ambient policy is
  owner configuration; a model cannot grant itself permission to publish.
- Keep model/channel secrets out of subprocess environments and public errors.
  Native commands have the OS user's permissions; directories are not sandboxes.
- Do not change existing services, credentials or old data to test source changes.
- Keep Pi versions pinned. Run `make check` for runtime changes after `make build`.
  The demo it runs uses real Pi and processes with scripted decisions.
  `npm --prefix host run test:live` is separate, uses a temporary fixture and
  incurs provider charges.

The previous native Go controller and container/group/IPC design were deliberately
removed. Preserve a small application boundary rather than restoring them. The Go
main was later restored around Pi, with Pi behind the host protocol.
