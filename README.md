# notify-relay

`notify-relay` forwards `notify-send` notifications from a VM or remote shell to a Linux desktop session.

- `notify-relayd`: host-side relay for Linux desktops using `org.freedesktop.Notifications`
- `notify-send-proxy`: guest-side `notify-send` compatible client for Linux or macOS

## Features

- forwards notifications over Unix sockets or TCP
- defaults to `/run/user/<uid>/notify-relay.sock` on Linux when available
- supports `notify-send` urgency, icons, categories, hints, replacement IDs, actions, `--wait`, and `--print-id`
- auto-generates a token for TCP mode; Unix socket mode uses no token by default

## Install

From a GitHub release:

```bash
curl -fsSL https://raw.githubusercontent.com/xtruder/notify-relay/main/scripts/install.sh | bash
```

From Homebrew/Linuxbrew:

```bash
brew tap xtruder/tap
brew install notify-relay
```

Installed binaries:

- `notify-relayd`
- `notify-send-proxy`

If you want the proxy to replace `notify-send`, create the symlink yourself:

```bash
ln -sfn ~/.local/bin/notify-send-proxy ~/.local/bin/notify-send
```

## Host usage

Run the relay as your desktop user:

```bash
notify-relayd --unix /run/user/$(id -u)/notify-relay.sock
```

The packaged systemd unit uses `%t/notify-relay.sock`, which resolves to `/run/user/<uid>/notify-relay.sock`.

For TCP mode instead:

```bash
notify-relayd --listen 127.0.0.1:8787 --token-file ~/.config/notify-relay/token
```

## Guest usage

Unix socket mode:

```bash
export NOTIFY_RELAY_SOCKET=/run/user/$(id -u)/notify-relay.sock
notify-send-proxy "Build finished" "Tests passed"
```

On Linux, `notify-send-proxy` uses `/run/user/<uid>/notify-relay.sock` automatically when it exists.

TCP mode:

```bash
export NOTIFY_RELAY_URL=http://HOST_IP:8787
export NOTIFY_RELAY_TOKEN=$(cat ~/.config/notify-relay/token)
notify-send-proxy "Build finished" "Tests passed"
```

## SSH forwarding

Remote-forward a Unix socket into a VM:

```sshconfig
Host ubuntu-dev
  HostName ubuntu-dev
  User offlinehq
  StreamLocalBindUnlink yes
  RemoteForward /run/user/1000/notify-relay.sock /run/user/1000/notify-relay.sock
  ExitOnForwardFailure yes
```

Pattern reference:

```sshconfig
RemoteForward /run/user/1000/gnupg/S.gpg-agent /run/user/1000/gnupg/S.gpg-agent.extra
```

Remote-forward TCP instead:

```sshconfig
Host ubuntu-dev
  HostName ubuntu-dev
  User offlinehq
  RemoteForward 8787 127.0.0.1:8787
  ExitOnForwardFailure yes
```

## OpenCode

If a plugin already uses `notify-send`, place `notify-send-proxy` in `PATH` as `notify-send`.

## More

- Development notes: `DEVELOPMENT.md`
- API endpoints: `GET /healthz`, `POST /notify`, `POST /close`, `GET /capabilities`, `GET /server-info`
