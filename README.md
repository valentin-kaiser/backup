# Backup - Docker Container

A small, hardened container that backs up your data to [restic](https://restic.net/) repositories.
A Go program, `control`, schedules the jobs, serialises access to each repository and keeps a run history. It runs in one of two [modes](#modes):

- **standalone** (default): one source, one repository and one job per type (backup, forget, check), configured with environment variables and a password file. No API, no portal, nothing else to run.
- **portal**: the same program as the daemon of a web portal. Any number of jobs with their own sources and repositories, repository passwords kept as encrypted secrets.

- **backup**, **forget/prune** and **check** jobs, each with its own cron schedule
- Locking per repository, so jobs never overlap on one repository while others run in parallel
- Real exit codes and complete output (stdout and stderr) of every run, kept in a history
- portal mode: portal with live output, history, snapshots and a restore dialog; create, edit, pause and delete jobs, changes apply immediately
- portal mode: daemon and portal are independent processes that talk over one websocket (xrpc, JSON-RPC with protobuf field numbers)
- Runs as a non-root user (`PUID`/`PGID`), read-only root filesystem, no shell involved when running restic
- Meaningful healthcheck: fails when a job failed or no backup succeeded recently
- Clean shutdown: running restic processes get `SIGTERM`, stale locks are removed

> **Portal mode has one password.** The same password signs you in to the portal and authenticates the portal, the CLI and every other client with the daemon API. It lives in one file, a Docker secret (`openssl rand -hex 12 > ./secrets/portal_password`), that the daemon and the portal both read. There are no users, roles or tokens. Keep the portal on loopback or behind a TLS reverse proxy anyway.

## Modes

| | `standalone` (default) | `portal` (`CONTROL_MODE=portal`) |
|---|---|---|
| Jobs | the three default jobs `backup`, `forget`, `check`, with the defaults below, one per type | the same three default jobs at first start, then any number, created in the portal or in `control.yaml` |
| Source, repository | global only | per job |
| Repository password | the password file (`passwordfile`, a Docker secret) | a stored [secret](#secrets) per job or global, password files are refused |
| API, portal | none, only a health probe on `127.0.0.1:9080` for the Docker healthcheck | API on port 9080 for the portal and the CLI, protected by the password, `master.key` and `secrets.json` in the data directory |
| Run a job by hand | `control once backup` (or `forget`, `check`) | portal, or `control run NAME` |
| Compose | `CONTROL_MODE=standalone docker compose up -d backup` | `docker compose up -d` (the default, also starts `backup-portal`) |

Pick the mode with `CONTROL_MODE` (or the first argument of the image: `standalone`, `portal`, and `web` for the web portal itself). A configuration that does not fit the mode is refused at start with the reason, e.g. a second backup job in standalone mode, or a job with a password file in portal mode.

## Usage

```bash
docker pull ghcr.io/valentin-kaiser/backup:latest
```

Use the [compose file](./compose.yaml) as a starting point. By default it starts `backup` in portal mode together with the web portal (`backup-portal`), both with the password from `./secrets/portal_password`. Standalone mode (`CONTROL_MODE=standalone docker compose up -d backup`) needs only `./secrets/backup_password` and has no portal.

Standalone, one source and one repository (schedules and arguments are set in `control.yaml`, see [Jobs](#jobs)):

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

portal mode: the same container with `-e CONTROL_MODE=portal` (no password file, the passwords are stored as secrets), controlled with the CLI or the portal in front of it:

```bash
docker run -d --name backup-portal \
    --read-only --tmpfs /tmp --user 1000:1000 \
    -e PORTAL_DAEMON_URL=http://backup:9080 \
    -p 127.0.0.1:8443:8443 \
    ghcr.io/valentin-kaiser/backup:latest web

# the daemon wants the same password file (-e CONTROL_API_PASSWORDFILE=...), the CLI reads CONTROL_PASSWORD_FILE
docker exec -u 1000 backup control status
```

### Volumes and secrets

| Path | Purpose |
|---|---|
| `/source` | Data to back up (mount read-only) |
| `/backup` | Local restic repository (or use any other restic repository type via `repository`, `CONTROL_REPOSITORY`) |
| `/run/secrets/backup_password` | Repository password file (use a Docker secret) |
| `/var/lib/control` | State of the daemon: configuration, run history, logs |
| `/var/cache/restic` | restic cache, a volume makes check/forget much faster |
| `/restore` | Restores are only allowed below this directory (mount it, the root filesystem is read-only) |
| `/.backupignore` | Optional exclude file, see [.backupignore](./.backupignore) |

restic supports `local`, `sftp`, `rest`, `s3`, `gs`, `b2`, `azure` and `rclone` repositories, all restic environment variables work.

> The container starts as root only to apply `PUID`/`PGID` and then drops privileges. The user must be able to read `/source`, write `/backup` and read the password file.
> Existing setups that ran as root: `chown -R 1000:1000 ./backup` or set `PUID`/`PGID` to the current owner.

## Configuration

Settings of the daemon come from command line flags, `CONTROL_*` environment variables, `control.yaml` in the data directory (reloaded on change) and the defaults of the image.
Run `docker run --rm ghcr.io/valentin-kaiser/backup:latest daemon --help` for the full list. The environment variable of a setting is `CONTROL_` plus its dotted name in upper case, e.g. `CONTROL_API_PORT`.
The portal has its own settings (`portal.yaml`, `PORTAL_*` variables), see [Portal](#portal).
Arguments of restic are split like a shell would (quotes and backslashes work) but are never expanded or executed.

### Jobs

Jobs are a list in `control.yaml`. In **standalone** mode the file holds the three default jobs (one per type, the source, repository and password are the global ones), adjust their `schedule` and `args`. In **portal** mode the file starts with the same three default jobs, which use the global source, repository and the stored secret `backup` as password (create it under *Secrets*, until then the runs fail with that reason). Manage the jobs in the [portal](#portal) or edit the file, it is reloaded on change. The example below is a portal mode configuration:

```yaml
passwordsecret: documents      # global default of jobs without their own passwordsecret
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
  - name: documents
    type: backup
    enabled: true
    schedule: 30 1 * * *
    source: /docs
    repository: /repos/docs
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
| `type` | `backup`, `forget` or `check` selects the restic command |
| `schedule`, `enabled` | Cron expression and whether the job is scheduled. Disabled jobs can still be run by hand |
| `args` | Additional restic arguments |
| `source` (backup jobs), `repository` | portal mode only: directory to back up and restic repository, default to the global settings |
| `passwordsecret` | portal mode: name of the stored [secret](#secrets) that holds the repository password, default to the global one. `passwordfile` is only used in standalone mode (global) |
| `timeoutminutes`, `maxagehours` | Abort the run after this long, flag the daemon unhealthy when there was no success for this long |

Without a `jobs` list in standalone mode the defaults are used: `backup` (daily 00:00), `forget` (Sundays 03:00) and `check` (Wednesdays 05:00).
Only one job touches a repository at a time, a job that is already running is not started again. Jobs on different repositories run in parallel. Repositories are created on first use and a new repository gets its first backup right away.

### Performance tuning

- `nice` (default 10) lowers the priority of restic
- `read-concurrency` and `pack-size` (MiB) tune restic for fast or slow storage
- `one-file-system` avoids descending into other mounts
- `compression` (`auto`, `off`, `max`)
- keep `/var/cache/restic` on a volume
- `lock-retry` (default `5m`) lets restic wait for a lock instead of failing

### Ensure the target is mounted

If `/backup` is a network mount that is not available, restic would write to the container's disk.
Create a marker file on the real disk and set

```
CONTROL_MOUNTCHECK_PATH=/backup
CONTROL_MOUNTCHECK_MARKERFILE=.mounted
```

Runs are then `skipped` (and the health check keeps its last good state) while the marker is missing.

### Notifications

Set `CONTROL_WEBHOOK_URL` to a [healthchecks.io](https://healthchecks.io) compatible URL. It is called with `/start` and `/fail` suffixes and without a suffix on success.

## Portal

The portal is a separate process, `control-portal`, started with `docker run ... ghcr.io/valentin-kaiser/backup:latest web` or as the `backup-portal` service of the compose file. It serves the Angular app on `https://localhost:8443` (self-signed certificate unless `certificate-file` and `key-file` are set) and relays the websocket of the browser to the selected control daemon. Its own calls (sign-in, daemons, audit log) are xrpc services at `/api/v1/portal`, defined in `proto/backup/api/v1`, see [docs/openapi](./docs/openapi).

The portal is one page with three sections below each other and a drawer for details:

- **Overview**: health, running jobs, the state of every job with run, pause and cancel, and the live output of running jobs
- **History**: all runs, filtered by job or status, new runs appear on their own
- **Snapshots**: the snapshots and statistics of each repository, reloaded after every backup or prune
- **Drawer**: creating or editing a job (with the cron builder and the restic options form, saving writes `control.yaml` and reschedules at once, deleting is there too), the complete output of a run (streams while the run is active) and the file browser of a snapshot, where a file, a folder or the whole snapshot can be restored. Close it with the button, `Esc` or a click outside; on very wide screens it docks next to the page. Every drawer has its own URL, so views can be linked
- Light and dark theme, following the system until you pick one

A portal manages **one control daemon**, run a portal per daemon. It authenticates with the daemon using the password of the portal, which it reads from `password-file`. The browser never sees it. The daemon reads the same file (`api.password-file`, `CONTROL_API_PASSWORDFILE`) and refuses every call without it. A self-signed certificate of the daemon is accepted by pinning its fingerprint or trusting its CA. The portal reads the settings when it is called, so a changed `portal.yaml` or a replaced token file applies at once and ends the open connections.

The portal is configured with:

| Setting (`portal.yaml`, `PORTAL_*`) | Default |
|---|---|
| `daemon.url` (`PORTAL_DAEMON_URL`) | Base address of the daemon, `http://127.0.0.1:8080`, `http://backup:9080` in the compose file |
| `daemon.ca-file`, `daemon.fingerprint` | Trust a CA, or pin the SHA-256 fingerprint of the daemon certificate (accepts a self-signed one) |
| `daemon.skip-certificate-verification` | `false`, only for development |
| `daemon.client-certificate-file`, `daemon.client-key-file` | Client certificate for mutual TLS, the daemon needs `api.client-ca-file` |
| `address`, `port`, `tls` | `127.0.0.1`, `8443`, `true` (`0.0.0.0` in the image) |
| `certificate-file`, `key-file`, `client-ca-file` | self-signed when empty, a client CA turns on mutual TLS for browsers |
| `password-file` (`PORTAL_PASSWORDFILE`) | The password: you sign in with it, and the portal presents it to the daemon. Read again when the file changes, which ends every session |
| `audit-retention-days` | `365`, how long the [audit log](#audit-log) is kept, `0` keeps it forever |
| `PORTAL_PATH` | Where `portal.db` (password, sessions, audit log) lives, `/var/lib/portal` in the image, keep it on a volume |

Security notes:

- **The daemon API requires the password** as bearer credential, the one of the portal. Without a usable `api.password-file` it refuses every call (set `api.disable-auth` to turn that off, do not). Five wrong passwords lock an address for five minutes. The CLI presents `CONTROL_PASSWORD` or the file in `CONTROL_PASSWORD_FILE` (set in the compose file). To change the password replace the secret file, both processes pick it up and every portal session ends.
- **There is one password, no users and no roles.** Whoever knows it can do everything. The portal has no password change of its own, the secret is the single place. A lost password is replaced by writing a new file.
- The password has at least 12 characters. Five wrong passwords lock an address for five minutes. Sessions are an opaque HttpOnly, SameSite=Strict cookie, stored as a hash bound to the password, valid for 12 hours and 2 hours idle. The portal only forwards calls of the control API, anything else is refused and logged.
- Publish the portal port on `127.0.0.1` only, or put a reverse proxy with a proper certificate in front, and do not publish the daemon API port at all, only the portal needs it.
- Websockets are only accepted from the origin of the page that opened them, so other web pages in your browser cannot talk to the daemon or the portal.
- Restores are limited to `/restore`.
- Saving a job also writes the settings that were overridden by flags or environment variables into `control.yaml`. They still win over the file.
## Audit log

The portal records what happens through it, under *Audit* (and in `portal.db`): sign-ins and failed sign-ins, and every call that changes something on a daemon: running, cancelling, pausing, creating, changing and deleting jobs, restores and secrets. Each entry has the time, the target (job, secret), the address it came from and the result: *worked*, *failed* (with the reason the daemon gave), *denied* (the portal refused it) or *unknown* (the connection ended before the daemon answered, the call may have worked).

- Because the portal has one password and no users, an entry says **what, to what, when and from which address**, not by whom. If that matters, put a reverse proxy with its own sign-in in front and keep its log too.
- Reading (status, history, snapshots, file listings) is not recorded. Secret values, passwords and the arguments of restic are never recorded, only the names of what they belong to, and credentials in repository addresses are replaced by `***`.
- Filter by action, result, date and text, page through older entries, and export what matches as CSV or JSON lines (spreadsheet formulas in the data are neutralised). An export is recorded too.
- Entries older than `audit-retention-days` (default 365) are deleted once a day, at most one million rows are kept.
- The log lives in the portal database and can be changed by anyone with access to that file, it shows what happened, it does not prove it. Calls made on the daemon directly (CLI, API with the password) are not in this log, the daemon logs them, see `docker logs`.

## Secrets

In portal mode repository passwords are kept **encrypted on the daemon**, password files are not used (standalone mode uses the password file and has no secrets). A job names a secret (`passwordsecret`, or the global one) and the daemon gives restic the password for every call: to its own restic through a private temporary file (`0600`, in a directory only the user can enter, removed right after the call). Nothing is written to `control.yaml`, the logs or the API responses, no API returns a value.

```bash
control secret set photos            # reads the password from the terminal or stdin
control secret set photos -generate  # a strong random one, printed once
control secret list                  # names, change time and the jobs that use each
control secret passwd photos         # change the password of the repositories that use it
control secret delete photos         # only when no job uses it
```

The portal does the same under *Secrets*, and a job picks its secret in the advanced settings.

- **Set value is not a password change.** `secret set` (and *Set value*) only changes what the daemon presents to restic. To change the password of a repository use `secret passwd` (*Change password*): it adds the new password to every repository that uses the secret, stores it, and only then removes the old key. If a step fails the old password keeps working, a crash leaves the repositories openable with at least one of the two passwords and never with a stored value that does not work. It needs the repositories to be reachable at that moment.
- **A generated password is shown once.** Store it in your password manager, a repository cannot be restored without its password.
- The secrets are in `secrets.json` in the data directory, each value sealed with AES-256-GCM and bound to its name. The key is `master.key` next to it (created on first start, `0600`) or the file in `master-key-file` (`CONTROL_MASTERKEYFILE`), e.g. a Docker secret to keep it apart from the secrets. **Back up the key with the secrets**, one without the other is useless. A wrong key is reported at start, not at night.
- `control secret rekey` encrypts the secrets with a new master key (daemon stopped). The old key stays as `master.key.old` until you delete it.

## CLI

Most commands talk to the daemon API, so they need `portal` mode (the portal itself is optional). In `standalone` mode there is no API, `control once` runs a job and waits for it.

```bash
docker exec -u 1000 backup control once backup          # standalone: run the backup job now and exit with its result (also forget, check)
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
```

## Development

```bash
go test ./...
make build   # npm ci + ng build, then bin/control and bin/control-portal
docker build -t backup .
```

The layout follows the bleep project:

- `pkg/` shared packages: `config`, `runner`, `manager`, `store` and the generated `proto`
- `worker/control` the daemon: `cmd` and Makefile, `pkg/bootstrap` start-up steps, `pkg/web` API server, `pkg/web/api/v1` xrpc service, `pkg/cli` client commands
- `worker/portal` the portal process: `cmd`, `pkg/web` (embedded frontend and the proxy to the daemon), `pkg/web/api/v1` xrpc services for sign-in and the audit log, `ui/` the Angular frontend
- `proto/` the protobuf services, the single source of the API

The API is defined in `proto/backup/api/v1`. After changing it run `make proto` (needs Docker) to regenerate the Go code in `pkg/proto`, the TypeScript models in `worker/portal/ui/src/app/global/model` and the OpenAPI spec in `docs/openapi`; CI fails when the generated code is out of date.

To work on the frontend, run a daemon in portal mode with `make -C worker/control/cmd run`, a portal with `make -C worker/portal/cmd run` and start the Angular dev server with `npm --prefix worker/portal/ui start`, which proxies `/api` to a running portal at `https://localhost:8443` (set `PORTAL_TARGET` to change it). Write a password of at least 12 characters to a file and give it to both: `CONTROL_API_PASSWORDFILE=FILE` for the daemon and `--password-file FILE --daemon.url http://127.0.0.1:8080` for the portal.