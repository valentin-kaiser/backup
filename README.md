# Backup

A small, hardened container that backs up your data to [restic](https://restic.net/) repositories, with an optional web portal.

A Go program, `control`, schedules the jobs, serialises access to each repository and keeps a run history with the complete output of every run.

- **Jobs**: `backup`, `forget`/prune and `check`, each with its own cron schedule, plus bidirectional [`sync`](#sync) of two volumes (unison or rclone bisync)
- **Safe by default**: runs as a non-root user (`PUID`/`PGID`), read-only root filesystem, all capabilities dropped, restic is started without a shell
- **No overlapping runs**: one job per repository at a time, other repositories run in parallel
- **Honest health**: the healthcheck fails when a job failed or no backup succeeded recently
- **Clean shutdown**: running restic processes get `SIGTERM`, stale locks are removed
- **Portal** (optional): live output, history, snapshots, a restore dialog, job editor, secrets and an [audit log](#audit-log)

## Contents

[Quick start](#quick-start) · [Modes](#modes) · [Volumes](#volumes-and-secrets) · [Configuration](#configuration) · [Jobs](#jobs) · [Sync](#sync) · [Portal](#portal) · [Audit log](#audit-log) · [Secrets](#secrets) · [CLI](#cli) · [Development](#development) · [License](#license)

## Quick start

```bash
docker pull ghcr.io/valentin-kaiser/backup:latest
```

The [compose file](./compose.yaml) is the recommended starting point. It starts the daemon in portal mode together with the web portal.

```bash
mkdir -p secrets
# optional: the one password for the portal sign-in and the daemon API. Without it a random one is created in
# ./secrets/portal_password on the first start
openssl rand -hex 12 > ./secrets/portal_password
docker compose up -d
```

Open `https://localhost:8443` (self-signed certificate) and sign in with that password (`cat ./secrets/portal_password` if it was created for you). The three default jobs fail until you create the secret `backup` under *Secrets*; it holds the repository password.

For a single source and repository without a portal, use standalone mode:

```bash
openssl rand -hex 32 > ./secrets/backup_password
CONTROL_MODE=standalone docker compose up -d backup
```

or without compose:

```bash
docker run -d \
    --name backup \
    --read-only --tmpfs /tmp \
    -e PUID=1000 -e PGID=1000 \
    -v /path/to/your/source:/source:ro \
    -v /path/to/your/backup:/backup \
    -v /path/to/your/restore:/restore \
    -v control:/var/lib/control \
    -v /path/to/your/password:/run/secrets/backup_password:ro \
    ghcr.io/valentin-kaiser/backup:latest

docker exec -u 1000 backup control once backup   # run the backup job now and wait (also forget, check)
```

> Run `docker exec` as the user of the container (`-u 1000`, or your `PUID`). The container drops all capabilities, so root inside it cannot read the secret files of that user and the commands fail with `permission denied`.

## Modes

Pick the mode with `CONTROL_MODE` (or the first argument of the image: `standalone`, `portal`, and `web` for the web portal itself). A configuration that does not fit the mode is refused at start with the reason, e.g. a second backup job in standalone mode, or a job with a password file in portal mode.

| | `standalone` (default of the image) | `portal` (`CONTROL_MODE=portal`, default of the compose file) |
|---|---|---|
| Jobs | `backup`, `forget`, `check`, one each | the same three at first start, then any number, created in the portal or in `control.yaml` |
| Source, repository | global only | per job |
| Repository password | password file (`passwordfile`, a Docker secret) | stored [secret](#secrets) per job or global, password files are refused |
| API, portal | none, only a health probe on `127.0.0.1:9080` | API on port 9080 for the portal and the CLI, protected by the password |
| Extra state | | `master.key` and `secrets.json` in the data directory |
| Run a job by hand | `control once backup` | portal, or `control run NAME` |
| Compose | `CONTROL_MODE=standalone docker compose up -d backup` | `docker compose up -d` (also starts `backup-portal`) |

> **Portal mode has one password.** The same password signs you in to the portal and authenticates the portal, the CLI and every other client with the daemon API. It lives in one file, a Docker secret, that the daemon and the portal both read. There are no users, roles or tokens. Keep the portal on loopback or behind a TLS reverse proxy.

Portal mode without compose: the daemon is the same container with `-e CONTROL_MODE=portal` (no repository password file, plus `-e CONTROL_API_PASSWORDFILE=...` and the secret mounted), and the portal runs next to it:

```bash
docker run -d --name backup-portal \
    --read-only --tmpfs /tmp --user 1000:1000 \
    -e PORTAL_DAEMON_URL=http://backup:9080 \
    -e PORTAL_PASSWORDFILE=/run/secrets/portal_password \
    -v /path/to/portal_password:/run/secrets/portal_password:ro \
    -v portal:/var/lib/portal \
    -p 127.0.0.1:8443:8443 \
    ghcr.io/valentin-kaiser/backup:latest web

docker exec -u 1000 backup control status   # the CLI reads CONTROL_PASSWORD_FILE
```

## Volumes and secrets

| Path | Purpose |
|---|---|
| `/source` | Data to back up (mount read-only) |
| `/backup` | Local restic repository (or any other repository type via `repository` / `CONTROL_REPOSITORY`) |
| `/run/secrets/backup_password` | Repository password file, standalone mode only (use a Docker secret) |
| `/run/secrets/portal_password` | The portal password, portal mode (daemon and portal). Created with a random value when it does not exist, so the directory must be writable |
| `/var/lib/control` | State of the daemon: configuration, run history, logs |
| `/var/lib/portal` | State of the portal: `portal.db` with sessions and audit log, keep it on a volume |
| `/var/cache/restic` | restic cache, a volume makes check and forget much faster |
| `/restore` | Restores are only allowed below this directory (mount it, the root filesystem is read-only) |
| `/.backupignore` | Optional exclude file, see [.backupignore](./.backupignore) |

restic supports `local`, `sftp`, `rest`, `s3`, `gs`, `b2`, `azure` and `rclone` repositories, and all restic environment variables work.

> The container starts as root only to apply `PUID`/`PGID` and then drops privileges. The user must be able to read `/source`, write `/backup` and read the password file.
> Setups that ran as root before: `chown -R 1000:1000 ./backup` or set `PUID`/`PGID` to the current owner.

## Configuration

Settings of the daemon come from, in increasing priority: the defaults of the image, `control.yaml` in the data directory (reloaded on change), `CONTROL_*` environment variables and command line flags. The variable of a setting is `CONTROL_` plus its dotted name in upper case, e.g. `CONTROL_API_PORT`. The full list:

```bash
docker run --rm ghcr.io/valentin-kaiser/backup:latest daemon --help
```

The portal has its own settings (`portal.yaml`, `PORTAL_*` variables), see [Portal](#portal). Arguments of restic are split like a shell would (quotes and backslashes work) but are never expanded or executed.

### Jobs

Jobs are a list in `control.yaml`. In **standalone** mode the file holds the three default jobs, adjust their `schedule` and `args`. In **portal** mode it starts with the same three, using the global source and repository and the stored secret `backup` as password. Manage jobs in the [portal](#portal) or edit the file. The example is a portal mode configuration:

```yaml
passwordsecret: documents      # global default for jobs without their own passwordsecret
jobs:
  - name: photos
    type: backup
    enabled: true
    schedule: 0 1 * * *          # cron: minute hour day month weekday
    source: /photos              # defaults to the global source
    repository: /repos/photos    # defaults to the global repository
    passwordsecret: photos
    args: --exclude-caches
    maxagehours: 26              # unhealthy when no run succeeded for this long, 0 disables
  - name: documents-prune
    type: forget
    enabled: true
    schedule: 0 4 * * 0
    repository: /repos/docs
    args: --prune --keep-last 5
  - name: documents-check
    type: check
    enabled: true
    schedule: 0 5 * * 3
    repository: /repos/docs
    args: --read-data-subset=5%
    timeoutminutes: 360
```

| Field | Meaning |
|---|---|
| `name` | Unique, 1-32 characters of `a-z 0-9 - _`. `restore` is reserved |
| `type` | `backup`, `forget`, `check` (restic commands) or `sync` (see [Sync](#sync)) |
| `schedule`, `enabled` | Cron expression and whether the job is scheduled. Disabled jobs can still be run by hand |
| `after` | Name of a job that starts this one when it succeeded (see [Chaining](#chaining)). The job has no schedule, `enabled` must be `false` |
| `args` | Additional restic arguments |
| `source` (backup jobs), `repository` | Portal mode only: directory to back up and restic repository, default to the global settings |
| `passwordsecret` | Portal mode: the stored [secret](#secrets) with the repository password, defaults to the global one. `passwordfile` is only used (globally) in standalone mode |
| `timeoutminutes`, `maxagehours` | Abort the run after this long; flag the daemon unhealthy when there was no success for this long |

In standalone mode the defaults are `backup` (daily 00:00), followed by `forget` and then `check`, each starting when the previous one succeeded.
Only one job touches a repository at a time and a running job is not started again; jobs on different repositories run in parallel. Repositories are created on first use and a new repository gets its first backup right away.

### Chaining

A job with `after` starts when the named job finished successfully, so the prune only runs after a good backup. Chains of any length work, a failed, skipped or cancelled run ends the chain. Each job keeps its own run history, repository and arguments; the trigger of its runs reads `after:<job>`. A job cannot be deleted while another one follows it.

```yaml
jobs:
  - {name: backup, type: backup, enabled: true, schedule: 0 1 * * *}
  - {name: forget, type: forget, after: backup, args: --prune --keep-last 7}
  - {name: check, type: check, after: forget}
```

### Performance tuning

- `nice` (default 10) lowers the priority of restic
- `read-concurrency` and `pack-size` (MiB) tune restic for fast or slow storage
- `one-file-system` avoids descending into other mounts
- `compression` (`auto`, `off`, `max`)
- keep `/var/cache/restic` on a volume
- `lock-retry` (default `5m`) lets restic wait for a lock instead of failing

### Ensure the target is mounted

If `/backup` is a network mount that is not available, restic would write to the container's disk. Create a marker file on the real disk and set

```
CONTROL_MOUNTCHECK_PATH=/backup
CONTROL_MOUNTCHECK_MARKERFILE=.mounted
```

Runs are then `skipped` (and the health check keeps its last good state) while the marker is missing.

### Notifications

Set `CONTROL_WEBHOOK_URL` to a [healthchecks.io](https://healthchecks.io) compatible URL. It is called with `/start` and `/fail` suffixes, and without a suffix on success.

## Sync

A `sync` job keeps two directories identical in **both directions**: a file created, changed or deleted on one side appears on the other. It needs no repository and no password. Examples are in [examples/sync](./examples/sync). Two layouts are supported:

| Layout | Setup |
|---|---|
| **One worker, both volumes** | mount both volumes into the container and set `path1` and `path2` to them |
| **Two workers, one volume each** | each worker lists the other under `peers`, the job uses `path2: peer:<name>`, see [Two workers](#two-workers) |

### Engines

| | `unison` (recommended) | `rclone` (default of `control.yaml`) |
|---|---|---|
| Maturity | about 20 years, built for two-way sync | `rclone bisync` is still marked **beta** by its authors |
| Remembers the last synchronised state | yes, an archive on both sides | yes, listings on the owner |
| Conflict policies | `newer`, `older`, `path1`, `path2`, `none` | also `larger`, `smaller`; `none` keeps both renamed |
| Losing version | kept as `file (conflict_on_DATE)`, or deleted | `file.conflict1`, `file.path1`/`.path2`, or deleted |
| Guard against an emptied side | always on (`-confirmbigdel`), plus `checkaccess` | `maxdeletepct` and `checkaccess` |
| With a peer | through the control API of the peer (**no sshd, no extra port**) | through a WebDAV server the peer runs (`syncserve`) |
| Preserves | times, permissions, symlinks | times; WebDAV drops ownership and permissions |

Set the engine per job with `engine: unison` or `engine: rclone` (empty means `rclone`; the portal offers unison for new jobs).

```yaml
workername: west                 # how the peers call this worker, defaults to the host name
jobs:
  - name: mirror
    type: sync
    engine: unison
    schedule: "*/5 * * * *"
    enabled: true
    path1: /sync/a               # local
    path2: /sync/b               # local directory, or peer:<name>[/subdirectory]
    conflict: newer              # newer (default), older, path1, path2, none; rclone also larger, smaller
    conflictloser: num           # num/pathname keep the losing version, delete drops it
    checkaccess: true            # abort unless a file RCLONE_TEST exists in both directories
    args: -ignore 'Name *.tmp'   # additional arguments of unison (rclone bisync with the rclone engine)
    # rclone only:
    maxdeletepct: 50             # abort when a run would delete more than this share of the files (default 50)
    force: false                 # do not abort when all files changed, see below
```

**How it behaves**

- **Conflicts** are files changed on both sides since the last run. `conflict` decides the winner and `conflictloser` what happens to the other version, so by default nothing is lost. With unison and `conflict: none` the file is skipped and the run is reported as failed ("some files were skipped") until you resolve it.
- **The first run** (and the first run after `control resync`, the *Resync* button, or after the state in `syncdir` was removed) builds the state of both sides: files missing on one side are copied, nothing is deleted. The state is kept in `syncdir` (`/var/lib/control/bisync`, keep it on a volume). Unison names its archives after the host, so the daemon pins the host name to the worker name and recreating the container does not make it forget the state.
- **Safety**: a run is skipped while a local path does not exist. For a volume that may be mounted but empty, create a file `RCLONE_TEST` in both directories and set `checkaccess: true`. Unison additionally refuses to empty a whole replica; rclone aborts past `maxdeletepct`.
- **rclone, "all files were changed"**: rclone aborts when every file it knew about changed on one side. In a directory with a handful of files that is an ordinary edit and fails with `Safety abort: all files were changed`. Set `force: true` (*Do not stop when all files changed*) for such directories. It also turns off `maxdeletepct`, so combine it with `checkaccess`. Unison does not have this problem.
- **Resilience**: runs of one pair never overlap (locally and, with a peer, through a reservation), so a lock left behind by a killed unison is ignored. An unreachable peer is tried three times (5 s and 20 s apart) before the run fails with the reason; the next schedule tries again. Unison retries failed transfers itself. The tunnel has a keep-alive, so a peer that vanishes without closing the connection ends the run after about a minute instead of hanging it. A cancelled or timed-out run stops unison with `SIGTERM`, which leaves the archives consistent.

### Two workers

Run both workers in portal mode and give each the other as a peer **with the URL of its control API** (`apiurl`, port 9080, with the portal password of that worker). Each worker calls itself by its `workername` (default: the host name), and its peers have to use exactly that name for it.

```yaml
workername: west
peers:
  - name: east
    apiurl: https://east:9080          # control API of east
    apipasswordfile: /run/secrets/portal_password   # or apipasswordsecret, the password of the portal of east
    apicafile: /run/secrets/east_ca.pem             # only for a self-signed certificate
    url: https://east:9081             # rclone engine only: the WebDAV server of east
    username: sync
    passwordfile: /run/secrets/sync_east_password
syncserve:
  tunnel: true                         # unison: let peers sync with this directory through the control API
  path: /sync                          # the directory that is exported, relative paths of peers are below it
  enabled: false                       # rclone: serve the directory over WebDAV (port 9081, needs username and password)
jobs:
  - name: west-east
    type: sync
    engine: unison
    schedule: "*/5 * * * *"
    enabled: true
    path1: /sync                       # local
    path2: peer:east/projects          # peer:<name>[/subdirectory below the directory east exports]
```

- The worker that holds the job is its **owner**. It schedules and runs it and keeps the sync state. It tells the peer about the job, which then shows up there as a **mirror** (`owner: <worker>`): same name, its own directory (below the one it exports) as `path1`, `path2: peer:<owner>`.
- The mirror shows the state of the owner (next run, last run, active run, history, output). *Run now*, *Cancel*, *Pause*, *Resume*, *Resync*, changes of the settings (schedule, conflict policy, ...) and *Delete* in its portal are forwarded to the owner, which sends the changed mirror back. Directories and engine can only be changed at the owner. `control run|cancel|pause|resume|resync NAME` work the same way.
- Before every run the owner **reserves the job at the peer** (and releases it afterwards), so the two workers never sync the same job at once. A run is skipped while the peer is busy and fails with the reason while the peer is unreachable. Reservations expire on their own.
- **How unison reaches the peer**: it starts `control-tunnel` (a link to `control`) instead of `ssh`. That opens a websocket to `/api/v1/tunnel` on the control API of the peer, authenticated with the same bearer password as every other call and over the same TLS settings. The peer runs `unison -server` in the exported directory and relays its standard input and output. Nothing but the control API port has to be reachable.
- **Trust**: the tunnel is only opened for a job that the peer owns here as a mirror, that uses unison, and that the peer has reserved. It still runs `unison -server` as the daemon user, so a peer that has the API password can read and write everything this worker can, not just the exported directory. Switch the tunnel on (`syncserve.tunnel`, off by default) only for peers you trust as much as the portal, use TLS (`CONTROL_API_TLS=true`) between the workers and keep the API port off untrusted networks.
- *Sync* in the portal lists the peers and the exported directory and tests the connections (`control peer test NAME`). Passwords can be stored [secrets](#secrets) instead of password files. Without `apiurl` a peer is only usable with the rclone engine and nothing is mirrored or reserved.

## Portal

The portal is a separate process, `control-portal`, started with `docker run ... ghcr.io/valentin-kaiser/backup:latest web` or as the `backup-portal` service of the compose file. It serves the Angular app on `https://localhost:8443` (self-signed certificate unless `certificate-file` and `key-file` are set) and relays the websocket of the browser to the control daemon. Its own calls (sign-in, audit log) are xrpc services at `/api/v1/portal`, defined in `proto/backup/api/v1`, see [docs/openapi](./docs/openapi).

The UI is one page with three sections and a drawer for details:

- **Overview**: health, running jobs, the state of every job with run, pause, cancel and unlock (removes all locks of the repository), and the live output of running jobs
- **History**: all runs, filtered by job or status; new runs appear on their own
- **Snapshots**: snapshots and statistics of each repository, reloaded after every backup or prune
- **Drawer**: create or edit a job (cron builder and restic options form; saving writes `control.yaml` and reschedules at once; deleting is there too), the complete output of a run (streams while active) and the file browser of a snapshot, where a file, a folder or the whole snapshot can be restored. Close it with the button, `Esc` or a click outside; on very wide screens it docks next to the page. Every drawer has its own URL, so views can be linked
- Also: *Secrets*, *Audit* and a light and dark theme that follows the system until you pick one

A portal manages **one control daemon**, run one portal per daemon. It authenticates with the daemon using the password it reads from `password-file`; the browser never sees it. The daemon reads the same file (`api.password-file`, `CONTROL_API_PASSWORDFILE`) and refuses every call without it. A self-signed certificate of the daemon is accepted by pinning its fingerprint or trusting its CA. The portal reads its settings when it is called, so a changed `portal.yaml` or a replaced password file applies at once and ends the open connections.

| Setting (`portal.yaml`, `PORTAL_*`) | Default |
|---|---|
| `daemon.url` (`PORTAL_DAEMON_URL`) | Base address of the daemon: `http://127.0.0.1:8080`, `http://backup:9080` in the compose file |
| `daemon.ca-file`, `daemon.fingerprint` | Trust a CA, or pin the SHA-256 fingerprint of the daemon certificate (accepts a self-signed one) |
| `daemon.skip-certificate-verification` | `false`, only for development |
| `daemon.client-certificate-file`, `daemon.client-key-file` | Client certificate for mutual TLS, the daemon needs `api.client-ca-file` |
| `address`, `port`, `tls` | `127.0.0.1`, `8443`, `true` (`0.0.0.0` in the image) |
| `certificate-file`, `key-file`, `client-ca-file` | Self-signed when empty; a client CA turns on mutual TLS, browsers without a certificate signed by it are refused |
| `trusted-proxies` | Comma separated addresses or networks of reverse proxies (`PORTAL_TRUSTEDPROXIES`). Only connections from them may set the client address with `X-Forwarded-For`; without it every client behind a proxy shares the address of the proxy, in the login lockout and the audit log |
| `password-file` (`PORTAL_PASSWORDFILE`) | The password: you sign in with it and the portal presents it to the daemon. Read again when the file changes, which ends every session |
| `audit-retention-days` | `365`, how long the [audit log](#audit-log) is kept, `0` keeps it forever |
| `PORTAL_PATH` | Where `portal.db` lives, `/var/lib/portal` in the image, keep it on a volume |

### Security notes

- **One password, no users, no roles.** Whoever knows it can do everything. The password file is the single place to change it (at least 12 characters): replace the file, both processes pick it up and every portal session ends. A lost password is replaced by writing a new file.
- **The daemon API requires the password** as bearer credential. Without a usable `api.password-file` it refuses every call (`api.disable-auth` turns that off, do not). Five wrong passwords lock an address for five minutes. The CLI presents `CONTROL_PASSWORD` or the file in `CONTROL_PASSWORD_FILE` (set in the compose file).
- Sessions are an opaque HttpOnly, SameSite=Strict cookie, stored as a hash bound to the password, valid for 12 hours and 2 hours idle. The portal only forwards calls of the control API; anything else is refused and logged.
- Publish the portal port on `127.0.0.1` only, or put a reverse proxy with a proper certificate in front. Set `trusted-proxies` to its address so lockouts and audit entries use the real client address. Do not publish the daemon API port at all, only the portal needs it.
- Websockets are only accepted from the origin of the page that opened them, so other web pages in your browser cannot talk to the daemon or the portal.
- Restores are limited to `/restore`.
- Saving a job also writes settings that were overridden by flags or environment variables into `control.yaml`. They still win over the file.

## Audit log

The portal records what happens through it, under *Audit* (and in `portal.db`): sign-ins and failed sign-ins, and every call that changes something on a daemon: running, cancelling, pausing, creating, changing and deleting jobs, restores and secrets. Each entry has the time, the target (job, secret), the address it came from and the result: *worked*, *failed* (with the reason the daemon gave), *denied* (the portal refused it) or *unknown* (the connection ended before the daemon answered, the call may have worked).

- Because there is one password and no users, an entry says **what, to what, when and from which address**, not by whom. If that matters, put a reverse proxy with its own sign-in in front and keep its log too.
- Reading (status, history, snapshots, file listings) is not recorded. Secret values, passwords and restic arguments are never recorded, only the names of what they belong to, and credentials in repository addresses are replaced by `***`.
- Filter by action, result, date and text, page through older entries, and export what matches as CSV or JSON lines (spreadsheet formulas in the data are neutralised). An export is recorded too.
- Entries older than `audit-retention-days` (default 365) are deleted once a day; at most one million rows are kept.
- The log lives in the portal database and can be changed by anyone with access to that file: it shows what happened, it does not prove it. Calls made directly on the daemon (CLI, API with the password) are not in this log; the daemon logs them, see `docker logs`.

## Secrets

In portal mode repository passwords are kept **encrypted on the daemon**; password files are not used (standalone mode uses the password file and has no secrets). A job names a secret (`passwordsecret`, or the global one) and the daemon gives restic the password for every call through a private temporary file (`0600`, in a directory only the user can enter, removed right after the call). Nothing is written to `control.yaml`, the logs or API responses, and no API returns a value.

```bash
control secret set photos            # reads the password from the terminal or stdin
control secret set photos -generate  # a strong random one, printed once
control secret list                  # names, change time and the jobs that use each
control secret passwd photos         # change the password of the repositories that use it
control secret delete photos         # only when no job uses it
```

The portal does the same under *Secrets*, and a job picks its secret in the advanced settings.

- **Set value is not a password change.** `secret set` (and *Set value*) only changes what the daemon presents to restic. To change the password of a repository use `secret passwd` (*Change password*): it adds the new password to every repository that uses the secret, stores it, and only then removes the old key. If a step fails the old password keeps working, and a crash leaves the repositories openable with at least one of the two passwords. It needs the repositories to be reachable at that moment.
- **A generated password is shown once.** Store it in your password manager; a repository cannot be restored without its password.
- The secrets are in `secrets.json` in the data directory, each value sealed with AES-256-GCM and bound to its name. The key is `master.key` next to it (created on first start, `0600`) or the file in `master-key-file` (`CONTROL_MASTERKEYFILE`), e.g. a Docker secret to keep it apart from the secrets. **Back up the key together with the secrets**, one without the other is useless. A wrong key is reported at start, not at night.
- `control secret rekey` encrypts the secrets with a new master key (daemon stopped). The old key stays as `master.key.old` until you delete it.

## CLI

Most commands talk to the daemon API, so they need portal mode (the portal process itself is optional). In standalone mode there is no API; `control once` runs a job and waits for it.

```bash
docker exec -u 1000 backup control once backup          # standalone: run the job now and exit with its result (also forget, check)
docker exec -u 1000 backup control status
docker exec -u 1000 backup control run photos -wait      # any job name from control.yaml
docker exec -u 1000 backup control runs -job photos -n 10
docker exec -u 1000 backup control cancel photos
docker exec -u 1000 backup control pause documents-prune # stop scheduling (manual runs still work)
docker exec -u 1000 backup control resume documents-prune
docker exec -u 1000 backup control snapshots -job photos # repository of a job, global repository without -job
docker exec -u 1000 backup control restore -job photos -snapshot latest -target /restore/now
docker exec -u 1000 backup control logs -f
docker exec -u 1000 backup control health                # used by the Docker HEALTHCHECK
docker exec -u 1000 backup control resync mirror -wait   # sync jobs: rebuild the state of both sides (nothing is deleted)
docker exec -u 1000 backup control peer list             # also: peer add NAME -url ... -api-url ..., peer test NAME, peer delete NAME
```

## Development

```bash
go test ./...
make build   # npm ci + ng build, then bin/control and bin/control-portal
docker build -t backup .
```

Layout:

- `pkg/` shared packages: `config`, `runner`, `manager`, `store` and the generated `proto`
- `worker/control` the daemon: `cmd` and Makefile, `pkg/bootstrap` start-up steps, `pkg/web` API server, `pkg/web/api/v1` xrpc service, `pkg/cli` client commands
- `worker/portal` the portal process: `cmd`, `pkg/web` (embedded frontend and the proxy to the daemon), `pkg/web/api/v1` xrpc services for sign-in and the audit log, `ui/` the Angular frontend
- `proto/` the protobuf services, the single source of the API
- `examples/` ready-made compose setups for [sync](./examples/sync)

The API is defined in `proto/backup/api/v1`. After changing it run `make proto` (needs Docker) to regenerate the Go code in `pkg/proto`, the TypeScript models in `worker/portal/ui/src/app/global/model` and the OpenAPI spec in `docs/openapi`. CI fails when the generated code is out of date.

To work on the frontend:

1. Write a password of at least 12 characters to a file.
2. Start a daemon in portal mode: `CONTROL_API_PASSWORDFILE=FILE make -C worker/control/cmd run`
3. Start the portal: `make -C worker/portal/cmd run`, with `--password-file FILE --daemon.url http://127.0.0.1:8080`
4. Start the Angular dev server: `npm --prefix worker/portal/ui start`. It proxies `/api` to a portal at `https://localhost:8443` (set `PORTAL_TARGET` to change it).

## License

[GNU General Public License v3.0](./LICENSE)
