# Security

## What Golem can do

Golem is a personal agent. Its Workers run shell commands on the machine where
`golem serve` runs, with the permissions of the account that started it. A
language model decides which commands. There is no sandbox, no allowlist of
commands and no approval step: the workspace directory is a working directory,
not a boundary.

What limits it:

- Only people in `GOLEM_SLACK_USERS` (and the local console) can give it work.
  Everyone on that list has the same full access.
- Model and channel secrets are kept out of the environment of the commands it
  runs and out of its logs.
- The controller socket is private to your user (`0600`).
- A remote executor requires a bearer token; put it behind TLS or a private
  network.

Treat its Slack allowlist, its `.env` and its state directory as you would
shell access to your account.

## Reporting a vulnerability

Please report security problems privately through GitHub's "Report a
vulnerability" on this repository's Security tab, not in a public issue.
