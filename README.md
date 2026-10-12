# Backup - Docker Container

A small, hardened container that backs up your data to [restic](https://restic.net/) repositories.
A Go daemon, `control`, schedules the jobs, serialises access to each repository, keeps a run history and offers a CLI.
An optional second process, `control-portal`, serves a web portal to manage the jobs and browse history and snapshots. The daemon runs fine without it.

- Any number of **backup**, **forget/prune** and **check** jobs, each with its own schedule, source and repository
- Create, edit, pause and delete jobs in the portal or edit `control.yaml`, changes apply immediately
- Locking per repository, so jobs never overlap on one repository while others run in parallel
- Real exit codes and complete output (stdout and stderr) of every run, kept in a history
- Portal with live output, history, snapshots and a restore dialog, all loading and updating on their own
- Daemon and portal are independent processes that talk over one websocket (xrpc, JSON-RPC with protobuf field numbers)
- Runs as a non-root user (`PUID`/`PGID`), read-only root filesystem, no shell involved when running restic
- Meaningful healthcheck: fails when a job failed or no backup succeeded recently
- Clean shutdown: running restic processes get `SIGTERM`, stale locks are removed

> **There is no authentication.** Whoever can reach the API or the portal can run and change jobs and restore files. Keep both on loopback or a private network, and put a reverse proxy that authenticates in front of the portal if others can reach it.

## Usage

```bash
docker pull ghcr.io/valentin-kaiser/backup:latest
```

Use the [compose file](./compose.yaml) as a starting point. It starts the daemon (`backup`) and the optional web portal (`backup-portal`); delete the second service to run the daemon alone.

The daemon alone, controlled with the CLI:

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

docker exec backup control status
```

The portal, in front of a daemon that is reachable by name or address:

```bash
docker run -d \
    --name backup-portal \
    --read-only --tmpfs /tmp --user 1000:1000 \
    -e PORTAL_DAEMON_URL=http://backup:9080 \
    -p 127.0.0.1:8443:8443 \
    ghcr.io/valentin-kaiser/backup:latest portal
```
### Volumes and secrets

| Path | Purpose |
|---|---|
| `/source` | Data to back up (mount read-only) |
| `/backup` | Local restic repository (or use any other restic repository type via `RESTIC_REPOSITORY`) |
| `/run/secrets/backup_password` | Repository password file (use a Docker secret) |
| `/var/lib/control` | State of the daemon: configuration, run history, logs |
| `/var/cache/restic` | restic cache, a volume makes check/forget much faster |
| `/restore` | Restores are only allowed below this directory (mount it, the root filesystem is read-only) |
| `/.backupignore` | Optional exclude file, see [.backupignore](./.backupignore) |

restic supports `local`, `sftp`, `rest`, `s3`, `gs`, `b2`, `azure` and `rclone` repositories, all restic environment variables work.

> The container starts as root only to apply `PUID`/`PGID` and then drops privileges. The user must be able to read `/source`, write `/backup` and read the password file.
> Existing setups that ran as root: `chown -R 1000:1000 ./backup` or set `PUID`/`PGID` to the current owner.

## Configuration

Settings of the daemon come from command line flags, `CONTROL_*` environment variables, `control.yaml` in the data directory (reloaded on change) and the legacy environment variables below, which only provide the defaults.
Run `docker run --rm ghcr.io/valentin-kaiser/backup:latest daemon --help` for the full list. The environment variable of a setting is `CONTROL_` plus its dotted name in upper case, e.g. `CONTROL_API_PORT`.
The portal has its own settings (`portal.yaml`, `PORTAL_*` variables), see [Portal](#portal).
### Legacy variables (still supported)

| Variable | Default |
|---|---|
| `BACKUP_SCHEDULE` | `0 0 * * *` |
| `FORGET_SCHEDULE` | `0 3 * * 0` (was `0 0 * * *`, which collided with the backup) |
| `RESTIC_SOURCE` | `/source/` |
| `RESTIC_REPOSITORY` | `/backup/` |
| `RESTIC_PASSWORD_FILE` | `/run/secrets/backup_password` |
| `RESTIC_COMPRESSION` | `auto` |
| `RESTIC_BACKUP_ARGS` | `--exclude-caches --exclude-if-present=.nobackup --exclude-file=/.backupignore` |
| `RESTIC_FORGET_ARGS` | `--prune --keep-last 7 --keep-daily 14 --keep-weekly 4 --keep-monthly 6 --keep-yearly 1` |

Arguments are split like a shell would (quotes and backslashes work) but are never expanded or executed.

### Jobs

Jobs are a list in `control.yaml`. The file is created with three default jobs on the first start. Manage them in the [portal](#portal) or edit the file, it is reloaded on change:

```yaml
jobs:
  - name: photos
    type: backup
    enabled: true
    schedule: 0 1 * * *          # cron: minute hour day month weekday
    source: /photos              # defaults to the global source
    repository: /repos/photos    # defaults to the global repository
    passwordfile: /run/secrets/photos_password
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
| `source` | Directory to back up (backup jobs only) |
| `repository`, `passwordfile` | Restic repository and password, default to the global settings |
| `timeoutminutes`, `maxagehours` | Abort the run after this long, flag the daemon unhealthy when there was no success for this long |

Without a `jobs` list the defaults are used: `backup` (daily 00:00), `forget` (Sundays 03:00) and `check` (Wednesdays 05:00).
Only one job touches a repository at a time, a job that is already running is not started again. Jobs on different repositories run in parallel. Repositories are created on first use and a new repository gets its first backup right away.

> Jobs are no longer configured with `CONTROL_BACKUP_*`, `CONTROL_FORGET_*` or `CONTROL_CHECK_*` variables, a list does not map to environment variables. The legacy variables below still set the defaults of the three default jobs.
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
CONTROL_MOUNT_CHECK_PATH=/backup
CONTROL_MOUNT_CHECK_MARKER=.mounted
```

Runs are then `skipped` (and the health check keeps its last good state) while the marker is missing.
This replaces the former `MOUNT_LOCK_TEST` / `MOUNT_LOCK_FILE` variables.

### Notifications

Set `CONTROL_WEBHOOK_URL` to a [healthchecks.io](https://healthchecks.io) compatible URL. It is called with `/start` and `/fail` suffixes and without a suffix on success.

## Portal

The portal is a separate process, `control-portal`, started with `docker run ... ghcr.io/valentin-kaiser/backup:latest portal` or as the `backup-portal` service of the compose file. It serves the Angular app on `https://localhost:8443` (self-signed certificate unless `certificate-file` and `key-file` are set) and forwards the websocket of the browser to the daemon.

- **Overview**: health, running jobs, the state of every job with run, pause and cancel, live output and the latest runs
- **Jobs**: create, edit, enable or disable, run and delete jobs. A cron builder (presets, or every/at/range/interval/list per field) describes the schedule in words and previews the next runs, following the daemon's rules where the day of the month and the weekday both have to match. An options form builds the restic arguments for the job type (exclusions, retention rules, check depth, ...) and keeps any argument it does not know. Saving writes `control.yaml` and reschedules at once
- **History**: all runs, filtered by job or status, new runs appear on their own. Every run has a page with its complete output, which streams while the run is active
- **Snapshots**: the snapshots and statistics of each repository, loaded when you open the page and again after every backup or prune. Open a snapshot to browse its files folder by folder, filter them, and restore a single file, a folder or the whole snapshot from there

| Setting (`portal.yaml`, `PORTAL_*`) | Default |
|---|---|
| `daemon.url` (`PORTAL_DAEMON_URL`) | `http://127.0.0.1:8080`, `http://backup:9080` in the compose file |
| `daemon.skip-certificate-verification` | `false`, set it when the daemon serves a self-signed certificate |
| `address`, `port`, `tls` | `127.0.0.1`, `8443`, `true` (`0.0.0.0` in the image) |
| `certificate-file`, `key-file` | self-signed when empty |

Security notes:

- **There is no sign-in.** Anyone who can open the portal can run jobs, change the configuration and restore files. The same holds for the daemon API, which the portal and the CLI use.
- Publish the portal port on `127.0.0.1` only, or put a reverse proxy in front that authenticates, and do not publish the daemon API port at all, only the portal needs it.
- Websockets are only accepted from the origin of the page that opened them, so other web pages in your browser cannot talk to the daemon or the portal.
- Restores are limited to `/restore`.
- Saving a job also writes the settings that were overridden by flags or environment variables into `control.yaml`. They still win over the file.
## CLI

The CLI talks to the daemon API, it works with or without the portal.

```bash
docker exec backup control status
docker exec backup control run photos -wait      # any job name from control.yaml
docker exec backup control runs -job photos -n 10
docker exec backup control cancel photos
docker exec backup control pause documents-prune # stop scheduling (manual runs still work)
docker exec backup control resume documents-prune
docker exec backup control snapshots -job photos # repository of a job, global repository without -job
docker exec backup control restore -job photos -snapshot latest -target /restore/now
docker exec backup control logs -f
docker exec backup control health                # used by the Docker HEALTHCHECK
```

`/restore.sh [SNAPSHOT] TARGET_DIR` remains as a thin wrapper around `control restore`.
## Migrating

From the shell script version:

- `backup.sh`, `forget.sh` and cron are gone, `control` replaces them. Output is available through `docker logs`, `control logs` and the portal.
- The container no longer runs as root, see `PUID`/`PGID` above.
- `/var/log` no longer needs to be mounted.
- `RESTIC_COMMPRESSION` (typo) never worked, use `RESTIC_COMPRESSION`.

From earlier versions of the Go daemon:

- Jobs are a `jobs` list in `control.yaml` instead of the fixed `backup`, `forget` and `check` sections. A fresh start writes the three default jobs, copy your changed `backup.*`, `forget.*` and `check.*` values into them.
- The API settings moved from `web.*` (`CONTROL_WEB_*`) to `api.*` (`CONTROL_API_*`) and the API listens on port 9080 in the image.
- The API token is gone, together with the sign-in. `CONTROL_TOKEN`, `CONTROL_TOKEN_FILE` and the `token` file are not used anymore, see the note on authentication above.
- The web UI is no longer part of the daemon. Add the `backup-portal` service from the compose file, the published port 8443 now belongs to it.
## Development

```bash
go test ./...
make build   # npm ci + ng build, then bin/control and bin/control-portal
docker build -t backup .
```

The layout follows the bleep project:

- `pkg/` shared packages: `config`, `runner`, `manager`, `store` and the generated `proto`
- `worker/control` the daemon: `cmd` bootstrap and Makefile, `pkg/web` API server, `pkg/web/api/v1` xrpc service, `pkg/cli` client commands
- `worker/portal` the portal process: `cmd`, `pkg/web` (embedded frontend and the proxy to the daemon), `ui/` the Angular frontend
- `proto/` the protobuf services, the single source of the API

The API is defined in `proto/backup/api/v1`. After changing it run `make proto` (needs Docker) to regenerate the Go code in `pkg/proto`, the TypeScript models in `worker/portal/ui/src/app/global/model` and the OpenAPI spec in `docs/openapi`; CI fails when the generated code is out of date.

To work on the frontend, run a daemon with `make -C worker/control/cmd run` and start the Angular dev server with `npm --prefix worker/portal/ui start`, which proxies `/api` to `http://localhost:8080` (set `CONTROL_TARGET` to change it).