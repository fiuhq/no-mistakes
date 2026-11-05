# no-mistakes in Paseo

A [Paseo](https://paseo.sh) plugin that shows no-mistakes runs where the agents doing the work are.

| Placement | Shows |
|---|---|
| Composer pill | on every agent whose workspace has a gated branch checked out: that branch's newest run's state (`nm · review 3/9`, `nm · needs you · review`, `nm · passed · #12`), with a menu to the run panel and the pull request |
| Chat rows | in the chat of the agent working on that checkout: the run started, a step parked on a decision, failed or reported findings, the pull request opened, the run finished |
| Workspace panel | that checkout's runs: steps, findings, errors, pull request (also from the Command Center: "Open no-mistakes run") |
| Sidebar page | every run on this host that is unfinished or changed in the last 7 days (the 200 most recent), each with a link to its workspace |

It is read-only: it never answers, aborts or reruns a run. Those stay with whoever drives the run.

## How it reads runs

- The daemon half reads `$NM_HOME/state.sqlite` (default `~/.no-mistakes`) read-only. `server/db.test.ts`
  builds a database from `internal/db/schema.go` and runs the same queries, so a schema change that breaks
  them fails that test.
- A run belongs to the Paseo workspaces whose directory is a worktree of the checkout no-mistakes recorded for
  its gate (`git worktree list`) with the run's branch checked out.
- Chat rows are driven by the daemon writing its database (an fs watch on `$NM_HOME`), not by a timer.
  What was already announced is kept in `$PASEO_HOME/no-mistakes/seen.json`; on its first start on a host
  the plugin records the existing runs without announcing them.
- Rows go to the most recently active agent in a workspace on that checkout.

## Install

```sh
paseo plugin install https://github.com/fiuhq/no-mistakes.git:integrations/paseo
```

Requires Paseo 0.13 or newer and Node 22.13 or newer for the daemon (`node:sqlite`).

## Develop

```sh
bun install
bun run verify   # tsc --noEmit && bun test
```
