<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="branding/logo-dark.svg">
    <img src="branding/logo.svg" alt="Waymark Proxy" width="420">
  </picture>
</p>

<p align="center">
  A self-hosted reverse proxy with active/standby high availability, real health checks and static file caching, managed from a web UI.
</p>

Waymark Proxy ("Waymark") is a reverse proxy for homelabs and small networks. It terminates TLS for your sites, gets and renews certificates, routes each hostname to one or more upstream servers, and can run as an active/standby pair that shares a floating IP address. It is built on [Zoraxy](https://github.com/tobychui/zoraxy) by tobychui and contributors, and adds the features below.

- [What Waymark adds to Zoraxy](#what-waymark-adds-to-zoraxy)
- [Requirements](#requirements)
- [Install](#install)
- [Quick start: your first proxy](#quick-start-your-first-proxy)
- [High availability (optional)](#high-availability-optional)
- [Upgrading](#upgrading)
- [Building from source](#building-from-source)
- [Where your data lives](#where-your-data-lives)
- [Relationship to Zoraxy, license and credits](#relationship-to-zoraxy-license-and-credits)

## What Waymark adds to Zoraxy

Everything Zoraxy does (HTTP and stream proxying, ACME certificates, access rules, SSO, the plugin store and so on) still works. On top of that:

**High availability**
- **Active/standby pairs** (2 to 4 nodes) sharing a floating IP managed by keepalived. The floating IP moves to a healthy node within about 10 seconds when Waymark stops answering, keepalived stops or the machine goes down.
- **Configured from the web UI.** Waymark itself runs unprivileged. A small root-owned helper validates the settings, writes keepalived's config from a fixed template, tests it and reloads keepalived.
- **Configuration sync.** Standby nodes copy the active node's configuration (proxy rules, certificates and keys, access rules, admin accounts, settings) over TLS, authenticated with a shared cluster key. Standby nodes are read-only, so changes are made in one place.
- **Cluster overview** on the HA page and the Status page: each node's role, health, sync state and version.
- Automatic certificate renewal runs only on the active node.

**Health checks** (per proxy rule)
- Active checks: HTTP GET or HEAD (with expected status codes and optional "response must contain" text), TCP connect or ICMP ping, with interval, timeout and failure and recovery thresholds.
- Passive monitoring of real traffic (5xx responses, timeouts, connection failures).
- Upstreams that fail are taken out of load balancing. When all upstreams of a rule are down, visitors get a "Web server is down" page at once instead of waiting for a timeout. Virtual directories can be checked too.
- Replaces Zoraxy's fixed 5-minute uptime monitor; the Uptime Monitor page is fed by the health checks.

**Static file caching**
- Per-rule in-memory caching of static files (CSS, JS, images, fonts, media). Honours the upstream's `Cache-Control` and `Expires`, revalidates expired files, supports `Vary`, shares one memory limit, and has per-rule statistics and a purge button.

**Operations**
- **Maintenance mode** per rule: visitors get a "Down for maintenance" page (HTTP 503 with `Retry-After`) with your message.
- **Status page panels** can be shown or hidden. New panels show upstream health, cache hit rate and memory, and the HA cluster.
- The HTTP Proxy list shows health check and cache state per rule, with a red heart on upstreams that are down.
- **Shared DNS credential sets** for ACME DNS challenges: save a provider token once and pick it for any certificate.
- **Geo-IP lookups can be turned off**, saving about 170 MB of memory when you don't use country rules.

**Security and maintenance**
- Cross-site request protection uses Go's standard `CrossOriginProtection` instead of the unmaintained gorilla/csrf.
- CI runs vet, tests and `govulncheck` on every change and builds with the latest Go 1.26 patch release.
- A daily workflow watches Zoraxy for new releases and prepares a pull request that merges them.

See [CHANGELOG.md](CHANGELOG.md) for details.

## Requirements

- Linux on x86-64 (`linux/amd64`). Releases are built and tested for Debian 13; other systemd-based distributions should work. Other platforms can be [built from source](#building-from-source).
- systemd (the instructions below use it, and HA needs it).
- Ports 80 and 443 free for the proxy, and a port for the admin UI (8000 by default).
- For high availability: [see below](#high-availability-optional).

## Install

These steps install Waymark in `/opt/waymark`, running as an unprivileged `waymark` user. Run them as a user with `sudo`.

**1. Create the service user and folder**

```bash
sudo useradd --system --home-dir /opt/waymark --shell /usr/sbin/nologin waymark
sudo install -d -o waymark -g waymark -m 0750 /opt/waymark
```

**2. Download the binary** from the [latest release](https://github.com/rtadams89/waymark-proxy/releases/latest) and check it:

```bash
cd /tmp
curl -fLO https://github.com/rtadams89/waymark-proxy/releases/latest/download/waymark_linux_amd64
curl -fLO https://github.com/rtadams89/waymark-proxy/releases/latest/download/waymark_linux_amd64.sha256
sha256sum -c waymark_linux_amd64.sha256
sudo install -o root -g root -m 0755 waymark_linux_amd64 /opt/waymark/waymark
/opt/waymark/waymark -version
```

The binary is owned by root, so the service can't modify itself. Waymark keeps its data next to it in `/opt/waymark` (see [Where your data lives](#where-your-data-lives)).

**3. Create the systemd service** as `/etc/systemd/system/waymark.service`:

```ini
[Unit]
Description=Waymark Proxy
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=waymark
Group=waymark
WorkingDirectory=/opt/waymark
# Admin UI on localhost only; see "Reaching the admin UI" below
ExecStart=/opt/waymark/waymark -port=127.0.0.1:8000
Restart=on-failure
RestartSec=5
# Bind ports 80 and 443 without running as root
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=/opt/waymark
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

ICMP health checks use unprivileged ping sockets, which systemd allows by default. If they fail with a permission error, add `CAP_NET_RAW` to both capability lines.

**4. Start it**

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now waymark
sudo systemctl status waymark
```

**Reaching the admin UI.** With `-port=127.0.0.1:8000` the admin UI only listens on the machine itself. From your workstation, open an SSH tunnel and browse to `http://localhost:8000`:

```bash
ssh -L 8000:127.0.0.1:8000 you@your-waymark-host
```

Later you can publish the admin UI under its own hostname through Waymark, with a proxy rule to `127.0.0.1:8000`. To listen on all interfaces instead, use `-port=:8000` and limit access with your firewall.

**5. Create the admin account.** The first visit shows **Account Setup**: choose a username and password.

## Quick start: your first proxy

This example publishes an app running at `http://10.0.0.20:8080` as `https://app.example.com`.

1. **Point DNS at Waymark.** Create an `A` record for `app.example.com` with Waymark's IP address (the floating IP if you run HA).
2. **Check that the proxy is running.** On **Status**, the banner should say *Online* and *listening on port 443*. Port 80 is served too and redirects to HTTPS.
3. **Set up certificates.** Under **Security > ACME Client**, enter an email address and turn on automatic renewal. Then click **Open ACME Tool** and request a certificate for `app.example.com`:
   - **HTTP challenge**: works when the name is reachable from the internet on port 80.
   - **DNS challenge**: also works for internal-only names. Pick your DNS provider and save its API token as a credential set (**Security > TLS / SSL certificates > Manage DNS Credentials**) so other certificates can reuse it.
4. **Create the proxy rule.** Under **Reverse Proxy > Create Proxy Rule**:
   - **Matching Keyword / Domain**: `app.example.com`
   - **Target IP Address or Domain Name with port**: `10.0.0.20:8080`
   - **Proxy Target require TLS Connection**: off (the app speaks plain HTTP)
   - **Enable health checks**: on (the default)
   - Click **Create Endpoint**.
5. **Open `https://app.example.com`.** The rule now appears under **Reverse Proxy > HTTP Proxy**. Edit it there to add more upstreams for load balancing, adjust health checks, turn on static file caching, add access rules or SSO, or put it into maintenance mode.

Tips:
- For an app that uses HTTPS itself, turn on **Proxy Target require TLS Connection** (and **Ignore TLS/SSL Verification Error** if it has a self-signed certificate).
- Hostnames that match no rule get the **Default Site** (**Ingress > Default Site**): an error page, a redirect, or the built-in static web server.
- To export your configuration, use **Utilities > System Backup & Restore**.

## High availability (optional)

Two (up to four) Waymark nodes on the same network share a **floating IP**. keepalived gives it to the healthy node with the highest priority; the others are standby nodes. Point your DNS records at the floating IP.

**What you need**
- Two Linux machines on the same VLAN, each installed as above and running **the same Waymark version**.
- A free IP address in that subnet for the floating IP, and a VRRP router ID (1-255) that nothing else on the VLAN uses (including pfSense CARP).
- keepalived on each node: `sudo apt install keepalived`.
- Between the nodes: IP protocol 112 (VRRP), and TCP port 9280 for configuration sync (configurable).

**1. Install the HA integration on each node.** It's built into the binary. Write it out, review it, and run it as root:

```bash
/opt/waymark/waymark -ha-export-installer /tmp/waymark-ha
less /tmp/waymark-ha/install.sh /tmp/waymark-ha/waymark-ha-apply
sudo bash /tmp/waymark-ha/install.sh
```

The installer adds:
- a root-owned helper, `/usr/local/sbin/waymark-ha-apply`;
- two small scripts that keepalived runs as the `waymark` user;
- a systemd path unit that applies new settings when Waymark saves them.

Waymark itself can only write to its own folder, `/var/lib/waymark/ha`. Because the service above is sandboxed, the installer adds a drop-in that allows this, so **restart Waymark** when it asks. If Waymark runs as another user or unit, set `WAYMARK_USER` or `WAYMARK_UNIT`.

**2. Configure the cluster on the first node** under **Networking > High Availability**:
- Turn on **Enable high availability**. Enter the **Floating IP**, its **Prefix length** and a **Router ID**, and optionally a VRRP password.
- Under **Configuration sync**, turn on copying to the standby nodes and click **Generate** for the cluster key.
- Add both nodes: a name, the node's own address, its network interface (e.g. `ens18`) and a priority. The node with the higher priority is active when both are healthy.
- Click **Save and apply**. The node becomes **Active** and holds the floating IP; the badge next to the logo shows this.

**3. Join the second node.** On the first node, click **Copy settings**. On the second node, click **Paste settings**, then **Save and apply**. Within about 20 seconds the second node copies the first node's whole configuration (including admin accounts and certificates) and restarts once to load it. Both nodes then appear under **Cluster**, and the second one shows **Standby** and **Read-only**.

**How it behaves**
- Make all changes on the active node. Standby nodes refuse changes, apart from their own HA settings (so sync can be paused there), and copy the active node's configuration within about 20 seconds of each change.
- If the active node fails, the standby takes over the floating IP. By default the floating IP doesn't move back when the other node recovers; it stays until the new active node fails. You can change this in the HA settings.
- After a failover, make changes on the new active node. They are copied to the old one when it returns as standby.
- Only the active node renews certificates automatically.
- Not synced (kept per node): statistics, logs, the Geo-IP database, plugins, static web server files and live SSO sessions.
- Logs: `journalctl -u waymark-ha-apply` for applying settings, `journalctl -u keepalived` for VRRP.

## Upgrading

**Single node:** download the new binary as in [Install](#install) step 2, then run `sudo systemctl restart waymark`. Read [CHANGELOG.md](CHANGELOG.md) first: a new major version (2.0.0) lists the steps it needs.

**HA pair:** nodes only sync while they run the same version, so upgrade them one at a time:
1. Upgrade the standby node. Sync pauses; the active node keeps working.
2. Upgrade the active node. Restarting it moves the floating IP to the upgraded standby, which becomes active.
3. When the old active node is back as standby, sync resumes. Make changes on the new active node.

### Upgrading from the Zoraxy-based builds

If you ran the earlier `3.3.5-custom.N` builds as `zoraxy` in `/opt/zoraxy`, move each node to the new names (in an HA pair, the standby first). Your configuration is kept as it is.

```bash
# 1. Stop Zoraxy (and keepalived, which runs scripts as the zoraxy user)
sudo systemctl stop zoraxy keepalived
sudo systemctl disable zoraxy

# 2. Rename the user, group and folder (files keep their owner)
sudo usermod -l waymark -d /opt/waymark zoraxy
sudo groupmod -n waymark zoraxy
sudo mv /opt/zoraxy /opt/waymark

# 3. Install the Waymark binary (Install, step 2) and remove the old one
sudo rm /opt/waymark/zoraxy

# 4. Create /etc/systemd/system/waymark.service (Install, step 3, keeping
#    your own ExecStart flags), then remove the old unit
sudo rm -rf /etc/systemd/system/zoraxy.service /etc/systemd/system/zoraxy.service.d
sudo systemctl daemon-reload

# 5. HA nodes only: install the Waymark HA integration. It removes the old
#    zoraxy-ha-apply helper, units and folders.
/opt/waymark/waymark -ha-export-installer /tmp/waymark-ha
sudo bash /tmp/waymark-ha/install.sh

# 6. Start Waymark. On HA nodes it applies its HA settings again, which
#    reconfigures and starts keepalived.
sudo systemctl enable --now waymark
```

Sign in again afterwards (the session cookie was renamed). If you proxy the admin UI through Waymark, that rule keeps working. In an HA pair, sync resumes once both nodes run Waymark.

## Building from source

Requires Go 1.26 or later.

```bash
git clone https://github.com/rtadams89/waymark-proxy.git
cd waymark-proxy/src
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o waymark .
./waymark -version
```

`make` in `src/` cross-compiles for other platforms (Linux on ARM and others, Windows, FreeBSD, macOS). Only `linux/amd64` is tested.

The logo files are generated by `branding/build_logos.py` (see the comment at its top).

## Where your data lives

Waymark keeps everything in its working directory (`/opt/waymark` above):

| Path | Contents |
|---|---|
| `conf/` | Proxy rules, certificates and keys, access rules and other configuration files |
| `sys.db` | Settings, admin accounts, ACME account and statistics |
| `log/` | Logs (`waymark_*.log`) |
| `www/` | Static web server files and custom error page templates |
| `plugins/` | Installed plugins |

Back up `conf/` and `sys.db`, or use **Utilities > System Backup & Restore**. HA nodes also have `/var/lib/waymark/ha` (the keepalived settings, role and sync state); it is recreated from the configuration if lost.

## Relationship to Zoraxy, license and credits

Waymark Proxy is a modified version of [Zoraxy](https://github.com/tobychui/zoraxy), created by [tobychui](https://github.com/tobychui) for [imuslab](https://imuslab.com) and developed with many contributors. It started as a set of changes to Zoraxy 3.3.5 and continues as a separate project; new Zoraxy releases are merged in when they're useful. It isn't affiliated with or endorsed by the Zoraxy project. Please report Waymark issues [here](https://github.com/rtadams89/waymark-proxy/issues), not to Zoraxy.

Waymark Proxy is free software under the [GNU Affero General Public License v3.0](LICENSE), the same license as Zoraxy. Copyright © 2021-2026 tobychui and the Zoraxy contributors; changes and additions © 2026 Ryan Adams. It comes with no warranty. If you run a modified version that other people use over a network, the AGPL requires you to offer them its source code.

Zoraxy plugins work with Waymark, and Waymark's App Store lists the plugins published for Zoraxy.

The wordmark uses [Inter](https://rsms.me/inter/) (SIL Open Font License).
