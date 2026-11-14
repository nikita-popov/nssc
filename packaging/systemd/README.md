# Running nssc under systemd

Three files, no build-time dependencies:

| File | Installs to | Purpose |
|---|---|---|
| `nssc.service` | `/etc/systemd/system/nssc.service` | the unit itself |
| `nssc.sysusers.conf` | `/etc/sysusers.d/nssc.conf` | creates the unprivileged `nssc` account |
| `nssc.tmpfiles.conf` | `/etc/tmpfiles.d/nssc.conf` | creates `/srv/nssc` with the right owner |

## Install

```sh
install -m 0755 nssc /usr/local/bin/nssc
install -m 0644 packaging/systemd/nssc.sysusers.conf /etc/sysusers.d/nssc.conf
install -m 0644 packaging/systemd/nssc.tmpfiles.conf /etc/tmpfiles.d/nssc.conf
install -m 0644 packaging/systemd/nssc.service      /etc/systemd/system/nssc.service

systemd-sysusers                 # create the nssc user
systemd-tmpfiles --create        # create /srv/nssc as nssc:nssc 0750
```

Create the first user **as `nssc`, not as root**:

```sh
sudo -u nssc /usr/local/bin/nssc adduser /srv/nssc alice 1GiB
# or: runuser -u nssc -- /usr/local/bin/nssc adduser /srv/nssc alice 1GiB
```

The password is read from the terminal without echo. When stdin is a pipe,
two lines are read (`printf 'pw\npw\n' | ...`), which keeps scripted setup
working.

Running it as root is the classic mistake: `db.json` is written `0600` and the
per-user directories `0700`, so a root-owned database is unreadable to the
service and `nssc` fails at startup with *failed to load users database*.
If it already happened, `chown -R nssc:nssc /srv/nssc` fixes it.

```sh
systemctl daemon-reload
systemctl enable --now nssc
systemctl status nssc
journalctl -u nssc -f
```

## Defaults

- **HTTP** on `127.0.0.1:8080`. nssc has no TLS of its own, so it is meant to
  sit behind a reverse proxy. Binding it to `0.0.0.0` exposes plain HTTP with
  Basic auth to the network — do that deliberately or not at all.
- **9P** on `unix:///run/nssc/9p.sock`, mode `0660`, group `nssc`. The socket
  lives in `RuntimeDirectory`, which systemd deletes on stop, so a crashed
  instance cannot leave a stale socket behind.
- **Storage root** `/srv/nssc`, the only writable path (`ReadWritePaths`).

Note that `-p` has no useful default of its own (`:0` picks a random port), so
the unit always passes it explicitly.

## Changing the listen address

Use a drop-in; do not edit the packaged unit:

```sh
systemctl edit nssc
```

```ini
[Service]
ExecStart=
ExecStart=/usr/local/bin/nssc run -p 0.0.0.0:8080 -9p unix:///run/nssc/9p.sock /srv/nssc
```

The empty `ExecStart=` is required to clear the inherited value. Keep `-p`
non-empty: an empty address makes the Go HTTP server fall back to port 80.

To disable 9P entirely, pass an empty value: `-9p ""`.

## Mounting the 9P share

```sh
mount -t 9p -o trans=unix,version=9p2000,uname=alice,aname=<password> \
  /run/nssc/9p.sock /mnt/nssc
```

The username goes in `uname` and the **password** in `aname`; nssc validates
both when the session opens. Note that a wrong password shows up as a hung
mount rather than an immediate error — the session is closed without an
`Rerror`.

## Upgrading from 1.2 or earlier

Those versions wrote user data to `<root>/<name>` while `adduser` created
`<root>/user/<name>`. On first start the current version moves the data to the
canonical location and logs what it moved. The move is a `rename` and is
one-way:

```sh
systemctl stop nssc
tar -C /srv -czf /var/backups/nssc-$(date +%F).tar.gz nssc   # back up first
systemctl start nssc
journalctl -u nssc | grep -i legacy                          # see what moved
```

Rolling back to an older binary requires moving the data back by hand
(`mv /srv/nssc/user/alice /srv/nssc/alice`).
