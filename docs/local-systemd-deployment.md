# Local SOCPUPPET_UI systemd deployment

This guide describes how to deploy SOCPUPPET_UI locally under `systemd` using the example unit at `docs/examples/socpuppet_ui.service`.

## Placeholders and defaults

The example unit uses these defaults; replace them if your host uses different paths or names:

- Install directory (`<install-dir>`): `/opt/socpuppet_ui` (executable at `/opt/socpuppet_ui/socpuppet_ui`).
- System user and group (`<socpuppet_ui-user>`): `socpuppet_ui` (non-root).
- PostgreSQL service unit (`<pg-unit>`): distribution-specific — for example `postgresql.service` on RHEL/Fedora/Arch or `postgresql@<pg-version>-<pg-cluster>.service` on Debian/Ubuntu (where `<pg-version>/<pg-cluster>` comes from `pg_lsclusters`, such as `18/main`).
- Configuration: `<install-dir>/config.json`, ignored by Git, owner-only mode `0600`.
- Web UI: `http://127.0.0.1:8787`; recording proxy: `127.0.0.1:8788`.
- Example unit file: `docs/examples/socpuppet_ui.service`; installed at `/etc/systemd/system/socpuppet_ui.service`.

## System user and directory permissions

Create a dedicated non-login system user and group before installing the binary:

```bash
sudo useradd --system --user-group --home-dir /opt/socpuppet_ui --shell /usr/sbin/nologin socpuppet_ui
```

Create the install, runtime data, and key directories with explicit ownership and modes:

```bash
sudo install -d -o root -g socpuppet_ui -m 0755 /opt/socpuppet_ui
sudo install -d -o socpuppet_ui -g socpuppet_ui -m 0770 /opt/socpuppet_ui/data
sudo install -d -o socpuppet_ui -g socpuppet_ui -m 0700 /opt/socpuppet_ui/keys
```

Directory ownership and modes:

- `<install-dir>` (`/opt/socpuppet_ui`): `root:<socpuppet_ui-user>`, mode `0755` (executable `/opt/socpuppet_ui/socpuppet_ui` mode `0755`).
- `<install-dir>/data` (`/opt/socpuppet_ui/data`): `<socpuppet_ui-user>:<socpuppet_ui-user>`, mode `0770` so the service user (and, in a root-run deployment using an executor group, that group) can write task run directories while blocking world access.
- `<install-dir>/keys` (`/opt/socpuppet_ui/keys`): `<socpuppet_ui-user>:<socpuppet_ui-user>`, mode `0700`. Holds `jwt.key` and `secret.key` (mode `0600`) outside the browsable `data/` directory. `docs/examples/socpuppet_ui.service` sets `Environment=SOCPUPPET_UI_JWT_KEY_DIR=/opt/socpuppet_ui/keys` so the non-root service writes key files here rather than into `root`-owned `/opt/socpuppet_ui` (if `SOCPUPPET_UI_JWT_KEY_DIR` were left unset, SOCPUPPET_UI would fall back to `<install-dir>` and fail to create `jwt.key` and `secret.key` when `<install-dir>` is `root:<socpuppet_ui-user>` `0755`).
- `<install-dir>/config.json` (`/opt/socpuppet_ui/config.json`): `<socpuppet_ui-user>:<socpuppet_ui-user>`, mode `0600`.

## Database and credentials

Install PostgreSQL on the host (or use an existing reachable PostgreSQL instance) and create a dedicated non-superuser login role and database named `socpuppet_ui` with no role-creation or database-creation privileges.

On Debian/Ubuntu, confirm the cluster name (`<pg-version>/<pg-cluster>`) with `pg_lsclusters`, start `<pg-unit>`, and create the role and database if absent:

```bash
sudo systemctl start <pg-unit>
sudo -u postgres createuser --pwprompt socpuppet_ui
sudo -u postgres createdb --owner=socpuppet_ui socpuppet_ui
```

Enter a unique password at the prompt. In `<install-dir>/config.json`, set `database.host` to `127.0.0.1`, `port` to `5432`, `user` and `dbname` to `socpuppet_ui`, `password` to that secret, and `sslmode` to `disable` (disabling TLS is appropriate only for loopback traffic). Preserve other settings and restrict access:

```bash
sudo chown socpuppet_ui:socpuppet_ui /opt/socpuppet_ui/config.json
sudo chmod 0600 /opt/socpuppet_ui/config.json
```

`SOCPUPPET_UI_PG_DSN`, if set in a systemd drop-in override, takes precedence over `config.json`. Never commit configuration, credentials, encryption/signing keys, or startup logs.

## Build and install

The binary embeds the UI when built with `-tags embedui`. To rebuild the UI from a source checkout, run `npm ci` and `npm run build:static` in `web/`, create the parent directory with `mkdir -p server/webui`, and replace `server/webui/dist` with `web/out`. Build the binary with:

```bash
CGO_ENABLED=0 go build -tags embedui -trimpath -o socpuppet_ui ./cmd/socpuppet_ui
```

Install the binary into `<install-dir>`:

```bash
sudo install -o root -g socpuppet_ui -m 0755 socpuppet_ui /opt/socpuppet_ui/socpuppet_ui
```

For a running deployment, build into a separate staging path and stop the service before replacing `/opt/socpuppet_ui/socpuppet_ui` or generated assets. Avoid rerunning `install.sh` without reviewing its prompts: it regenerates `config.json` and starts a separate process.

Review `User`, `Group`, `WorkingDirectory`, `Environment=SOCPUPPET_UI_JWT_KEY_DIR=...`, and `ExecStart` in `docs/examples/socpuppet_ui.service` before installing it on your host:

```bash
systemd-analyze verify docs/examples/socpuppet_ui.service
sudo install -o root -g root -m 0644 docs/examples/socpuppet_ui.service /etc/systemd/system/socpuppet_ui.service
sudo systemctl daemon-reload
```

### Database dependency and environment overrides

`docs/examples/socpuppet_ui.service` intentionally declares only `After=network.target` and does not hardcode a distribution-specific PostgreSQL unit (such as Debian's `postgresql@<pg-version>-<pg-cluster>.service`), so it works across Linux distributions and with remote PostgreSQL servers.

When PostgreSQL runs locally on the same host, add a systemd drop-in (`sudo systemctl edit socpuppet_ui.service`) so SOCPUPPET_UI starts after and depends on your local PostgreSQL unit:

```ini
[Unit]
Requires=<pg-unit>
After=<pg-unit>
```

For example, set `<pg-unit>` to `postgresql.service` on RHEL/Fedora/Arch, or to `postgresql@<pg-version>-<pg-cluster>.service` on Debian/Ubuntu (and verify `/etc/postgresql/<pg-version>/<pg-cluster>/start.conf` is `auto`, not `disabled`). `SOCPUPPET_UI_JWT_KEY_DIR=/opt/socpuppet_ui/keys` is already set in `docs/examples/socpuppet_ui.service`; override it in the drop-in only if you install SOCPUPPET_UI under a different `<install-dir>`. Run `sudo systemctl daemon-reload` after editing the drop-in.

### Enable and start

If SOCPUPPET_UI is running outside `systemd`, identify its PID/executable and send that verified process `SIGTERM`. Wait for shutdown and release of ports `8787` and `8788` before enabling the service:

```bash
ss -ltnp '( sport = :8787 or sport = :8788 )'
sudo systemctl enable --now socpuppet_ui.service
systemctl status socpuppet_ui.service --no-pager
curl --fail --silent --show-error http://127.0.0.1:8787/api/health
```

The unit runs `/opt/socpuppet_ui/socpuppet_ui` directly without an extra supervisor script. It starts after `network.target`, restarts on failure after five seconds, allows 30 seconds for graceful shutdown, logs to the journal, and uses `UMask=0002` (rather than `0077`, which would block group writes in a root-run deployment where executor privilege drop sets umask `002` for the executor group; sensitive files such as `config.json`, `keys/jwt.key`, and `keys/secret.key` are protected explicitly with `0600`/`0700` permissions). Five starts within 60 seconds trigger the start limit.

### Agent isolation

Because `docs/examples/socpuppet_ui.service` runs the server as the non-root `<socpuppet_ui-user>` (`User=socpuppet_ui`), the `-exec-uid`/`-exec-gid` privilege drop cannot be applied: `ResolveExecPrivilege` disables the drop when the server is not root (and errors if those flags are passed explicitly). The example unit deliberately omits `-exec-uid`/`-exec-gid`, so agents and spawned tools run under the `<socpuppet_ui-user>` service identity rather than a separate executor account. The root-owned run-directory layout and executor-group containment exist only when the server runs as root with explicit `-exec-uid`/`-exec-gid`; if you require unprivileged-executor containment, use the Docker deployment, which is the supported path for that isolation model.

## Web setup

On a new database, visit `http://127.0.0.1:8787/setup`, provide the one-time setup token from the private startup log or journal (`journalctl -u socpuppet_ui.service`), and choose a unique password of at least 12 characters. The username is `SOCPUPPET_UI`. Store the password in your password manager rather than in a plaintext file on disk.

## Reboot persistence verification (example)

Pre-reboot checks confirm persistent SOCPUPPET_UI enablement, active PostgreSQL and SOCPUPPET_UI services, HTTP 200 health, and initialized authentication, but do not by themselves prove survival across a reboot.

To verify reboot persistence on a host, record the current boot ID and timestamp in an ignored runtime file such as `<install-dir>/data/reboot-verification-baseline.json` before rebooting, and do not overwrite that baseline until the test is complete:

```bash
sudo -u socpuppet_ui python3 -c 'import json, time; from pathlib import Path; Path("/opt/socpuppet_ui/data/reboot-verification-baseline.json").write_text(json.dumps({"boot_id": Path("/proc/sys/kernel/random/boot_id").read_text().strip(), "ts": time.time()}))'
```

Save other work before rebooting; this disconnects local and remote sessions:

```bash
sudo systemctl reboot
```

After reconnecting, do not manually start SOCPUPPET_UI before checking automatic startup. Compare the current boot ID with the saved baseline and check the services:

```bash
cd /opt/socpuppet_ui
python3 -c 'import json; from pathlib import Path; old=json.loads(Path("data/reboot-verification-baseline.json").read_text())["boot_id"]; new=Path("/proc/sys/kernel/random/boot_id").read_text().strip(); print("PASS: a reboot occurred" if old != new else "FAIL: no reboot occurred"); raise SystemExit(0 if old != new else 1)'
systemctl is-enabled socpuppet_ui.service
systemctl is-active <pg-unit> socpuppet_ui.service
curl --fail --silent --show-error http://127.0.0.1:8787/api/health
python3 -c 'import json, urllib.request; r=json.load(urllib.request.urlopen("http://127.0.0.1:8787/api/auth/status", timeout=5)); print("Authentication initialized:", r.get("initialized")); raise SystemExit(0 if r.get("initialized") else 1)'
systemctl show socpuppet_ui.service --no-pager -p ActiveState -p SubState -p MainPID -p ActiveEnterTimestamp
```

Success requires a changed boot ID, enabled SOCPUPPET_UI, both services active without manual startup, HTTP 200, and authentication still initialized. A new PID alone is not proof of reboot. If the system has just booted, allow startup time and repeat health checks. Refresh the baseline before running a future independent reboot test.

### Example reboot verification record

Example of a completed post-reboot verification (recorded from the original Debian/PostgreSQL 18 reference deployment on 2026-11-17, without manually starting or restarting either service):

- Current boot ID differed from the saved pre-reboot baseline, confirming an actual reboot.
- `socpuppet_ui.service` remained enabled and was `active (running)` under its non-root service user with `NRestarts=0`.
- The local PostgreSQL cluster service was `active`.
- `/api/health` returned HTTP 200.
- `/api/auth/status` confirmed initialized authentication was preserved.

Observed timestamps, PIDs, and restart counts are host-specific examples, not fixed configuration values.

## Operations and troubleshooting

```bash
sudo systemctl restart socpuppet_ui.service
sudo systemctl stop socpuppet_ui.service
sudo systemctl start socpuppet_ui.service
journalctl -b -u socpuppet_ui.service --no-pager
journalctl -u socpuppet_ui.service -f
```

Treat journal output as potentially sensitive; never share unredacted setup tokens or credentials. If startup fails, check PostgreSQL status, database settings and permissions on `<install-dir>/config.json`, `<install-dir>/data`, and `<install-dir>/keys`, occupied ports (`8787`/`8788`), and the journal. After a PostgreSQL dependency failure, restart SOCPUPPET_UI explicitly once the database is healthy. To recover from the start limit:

```bash
sudo systemctl reset-failed socpuppet_ui.service
sudo systemctl start socpuppet_ui.service
```

Back up PostgreSQL, `<install-dir>/config.json`, `<install-dir>/data`, and `<install-dir>/keys` (`jwt.key` and `secret.key`) securely before upgrades. The application stays loopback-only; remote access requires a separately reviewed tunnel or reverse proxy.
