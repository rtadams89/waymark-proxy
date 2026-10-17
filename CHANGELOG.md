# Waymark Proxy changelog

Waymark Proxy is a reverse proxy based on [Zoraxy](https://github.com/tobychui/zoraxy) by tobychui and contributors. This file lists Waymark's changes; for Zoraxy's own history see its [releases](https://github.com/tobychui/zoraxy/releases).

**Versioning:** [semantic versioning](https://semver.org), `MAJOR.MINOR.PATCH`.

- **PATCH** (1.0.1): bug and security fixes only.
- **MINOR** (1.1.0): new features that keep existing configuration working.
- **MAJOR** (2.0.0): changes that need action when upgrading.
- The version lives in `src/version_custom.go` (`PRODUCT_VERSION`). Release tags are `v<version>` (`v1.0.0`); CI refuses a tag that doesn't match the version built into the binary.
- Waymark was forked from Zoraxy 3.3.5, which **Settings > About** and `waymark -version` still show. Since 2.6.2 it no longer follows Zoraxy releases.

## 2.6.3

**Security**
- Sticky session cookies (load balancing) are signed with their own random key instead of the host UUID. The host UUID is sent to every upstream in the `X-Forwarded-Server` header, so an upstream could forge the cookies and choose which upstream its requests went to. The key is kept in the database and copied to standby nodes by HA configuration sync, so sticky sessions survive a failover; each node can keep its own host UUID (`sys.uuid`). Existing sticky session cookies stop matching once, so those clients are assigned an upstream again.

## 2.6.2

2.6.1 was tagged but not released (a test failed in CI); its changes are included here.

**Changed**
- Waymark no longer follows Zoraxy releases: the daily "Watch upstream releases" workflow, the "Merge upstream release" workflow and their scripts (`check-upstream.sh`, `merge-upstream.sh`) are removed. Waymark stays based on Zoraxy 3.3.5 for configuration migrations and the version plugins see.
- HTTP proxy destinations: an upstream's weight is set in its edit form (a number from 0 to 100, with an explanation), instead of with the up and down arrow buttons, which looked like they reordered the list. The weight is checked on the server too.
- Long upstream addresses no longer wrap mid-word (`…:800` / `6`): the address stays on one line unless it is too long for the panel.

**Fixed**
- Opening a proxy rule's editor before any certificate was installed raised a script error (the certificate list is empty then), and the certificate dropdown in the TLS section did not appear. Certificate names in that dropdown are now escaped.
- Upstream addresses in the destinations list are escaped.
- Statistics: the per-day counters (client addresses, user agents, URLs and so on) could end up above their size limit when many new entries arrived while one was being trimmed, because nothing trimmed again afterwards. The trim now repeats until the counter is within its limit. This also made a test fail now and then.

## 2.6.0

Based on Zoraxy 3.3.5 (`310b0cc`).

**Added: stream proxy upstream pools and health checks**
- A stream proxy (TCP and/or UDP) can have up to 32 upstream servers. Existing rules keep their single target as the first upstream.
- Load balancing: round robin, least connections (open TCP connections or UDP sessions), source IP hash (a client keeps going to the same upstream while the healthy set stays the same), or failover (the first healthy upstream in the list).
- TCP: if the chosen upstream refuses or doesn't answer, the next one is tried before the client connection is given up. With no healthy upstream, new TCP connections are closed at once and UDP packets are dropped.
- Health checks, using the same engine and thresholds as HTTP rules: TCP connect, TCP send data and expect a reply (or just a banner), UDP send data and expect a reply, ping, or none. Data can be text (with `\r \n \t \0 \\ \xHH`) or hex. Passive monitoring counts failed connections (and for UDP, an upstream reporting its port unreachable).
- Option to close open connections and sessions to an upstream when it is taken out (off by default).
- The Stream Proxies list shows each upstream's health and open connections; stream rules with active checks appear on **Health & Uptime** and in the Status page's upstream health panel.
- Stream rule settings (in `conf/streamproxy`) are copied to HA standby nodes with the rest of the configuration, as before.

**Added: traffic capture**
- **Monitoring > Traffic Capture** records proxied traffic on this node as a pcapng file for Wireshark: HTTP and/or stream proxies, filtered by HTTP rule or host name, stream rule and client address; the client side, the upstream side or both.
- HTTPS is recorded decrypted (Waymark terminates TLS). Each HTTP request and response is written as an HTTP/1.1 conversation on port 80, with a comment giving the real addresses, ports and protocol. Bodies are kept up to a set size per request. **Hide credentials** (on by default) replaces Authorization, Proxy-Authorization, Cookie and Set-Cookie values.
- Stream proxy TCP connections and UDP sessions are recorded as they pass.
- One capture at a time, always with a time limit (up to 60 minutes) and a size limit (up to 1 GB). Files are kept in `captures/` (mode 0700), deleted after 24 hours (at most 50 kept), and are not synced or backed up. Start, stop, download and delete are logged with who did it and the filters used. Captures work on read-only HA standby nodes.
- The packets are reconstructed from what Waymark sent and received, so no privileges are needed; it doesn't show network-level detail such as retransmissions.

**Fixed**
- Stream proxy TCP: a copy that ended with an error never signalled completion, so the connection's goroutine waited forever and its other side was not closed. Both sides now close when either ends, and byte counters are updated safely.

**Documentation**
- README: stream proxy pools and health checks, and a **Traffic capture** section.

## 2.5.0

Based on Zoraxy 3.3.5 (`310b0cc`).

**Added**
- **Where logs go** on **Monitoring > Logs**: system logs (Waymark's own messages) and traffic logs (one line per HTTP request) can each be written to the log file, sent to syslog servers, both, or neither. Both go to the log file by default, as before. System messages are still printed to standard output (the systemd journal) either way.
- **Syslog servers** (up to 8), each with: IP address or host name, port, protocol (UDP, TCP or TLS), framing for TCP and TLS (newline or RFC 6587 octet counting), message format (RFC 5424, RFC 3164, JSON with an RFC 5424 header, or JSON lines), facility, which streams to send, and for TLS the certificate check (system CAs or a pasted CA certificate, an optional name to check, or no check). **Send test message** tries a server before saving; each server shows its connection state and how many messages were sent and dropped.
- Each server has its own queue (4,096 messages) and reconnects on its own (every 1 to 30 seconds), so a slow or unreachable server never holds up the proxy; messages beyond the queue are dropped and counted.
- Settings are stored in `conf/log_outputs.json`.

**Changed**
- HA configuration sync also copies the log rotation settings (`log_conf.json`) and log output settings (`log_outputs.json`), so they can be changed on the active node; before, a standby's log rotation settings could not be changed while sync was on. Each node sends syslog messages with its own host name.
- Traffic log lines cut very long request paths (over 4,096 characters) and user agents (over 512) short.
- Logs page: the rotation section is titled **Log file rotation**, and it says rotated files are compressed with gzip (it said ZIP).

**Documentation**
- README: new **Logs and syslog** section; the feature list, requirements (outbound HTTPS for update checks) and the data folder table cover the recent changes.

## 2.4.0

Based on Zoraxy 3.3.5 (`310b0cc`).

**Added**
- **Make active** on the HA page's cluster table moves the floating IPs to a healthy standby. The active node reports itself unhealthy for a few seconds so keepalived hands over, then goes back to being a healthy standby; nothing is restarted and keepalived's settings don't change. Either node can start it: a standby forwards the request to the active node over the sync connection, and the active node checks it again. Needs configuration sync on. With preempt on, a higher-priority node takes the floating IPs back about 30 seconds later (the confirmation says so).
- **Updates from the web interface.** Waymark checks GitHub for the latest release once a day (and on **Check now**). **Settings > About** shows it with its release notes and an **Install** button, and a pill next to the logo says when an update is available. Install downloads the binary for this platform from the GitHub release (HTTPS, only from this project's release downloads), checks its SHA-256 checksum, runs it with `-version`, keeps the running binary as `waymark.prev`, swaps the new one in and restarts Waymark in place. If the new version is started three times without staying up for two minutes, the previous binary is put back. **Roll back** swaps the two by hand. Only newer releases are installed this way.
- In a cluster, each node has an **Update** button on the HA page; a node asks another to update itself over the authenticated sync connection. Updating, switching over and rolling back work on read-only standby nodes too.
- Updating needs the folder holding the binary to be writable by the `waymark` user, as it is with the README's install steps. If it isn't, the page says why.

**Changed**
- The sync connection between nodes also carries actions (switch-over, update) as authenticated POST requests, alongside the state and snapshot requests.
- Cluster table values are escaped for attributes too.

**Documentation**
- README: switch-over, updating from the web interface, and a corrected note on binary ownership: in `/opt/waymark`, which belongs to `waymark`, the service can replace its binary whoever owns the file. Install it in a root-owned folder if you don't want that.

## 2.3.0

Based on Zoraxy 3.3.5 (`310b0cc`).

**Changed**
- The side menu is reorganised into **Proxy** (HTTP Proxies, Stream Proxies, Redirects, Default Site), **Access** (Access Rules, Authentication (SSO), Trusted Proxies), **Certificates**, **Monitoring** (Health & Uptime, Statistics, Logs), **System** (Listeners, High Availability, Static Web Server, Settings) and **Plugins** (Installed Plugins, Plugin Store, and pages added by plugins). Status stays at the top.
- **Add Proxy Rule** is a button on HTTP Proxies instead of a menu entry, and virtual directories are opened from each rule. Both pages have a link back to HTTP Proxies.
- **Certificates** and **ACME and auto-renew** are tabs of one Certificates page, which also opens the DNS credentials.
- **Logs** (log rotation settings and the log viewer) is its own page under Monitoring. Utilities is now **Settings** with Accounts, System (Geo-IP, backup and restore, statistics operations) and About tabs; About includes the supporters list.
- Renamed: Ingress to **Listeners**, Uptime Monitor to **Health & Uptime**, Statistical Analysis to **Statistics**, Plugin Manager to **Installed Plugins**, App Store to **Plugin Store**. Page headings match the menu.
- Links to pages that no longer exist (such as an old `#iface` bookmark) open Status.

**Removed**
- The **Recent** section at the top of the side menu (recently opened and pinned pages), with its code, styles and icons. Browsers keep the old `zoraxy_recent_tabs` and `zoraxy_pinned_tabs` entries in local storage, unused.
- The **Network Interfaces** page. High Availability shows the addresses that matter for the cluster; the `/api/stats/listnic` API stays for plugins.

**Documentation**
- README: quick start and HA instructions use the new menu paths.
- README: new **Why Waymark** section with a comparison to Zoraxy, Nginx Proxy Manager, Traefik and Caddy, and an updated list of what's different from Zoraxy (security changes, removed features, new look).

## 2.2.3

Based on Zoraxy 3.3.5 (`310b0cc`).

**Security**
- OAuth2 SSO and Waymark Auth: the redirect after login is now built only from the path and query of the return address (after the host check), with leading slashes and backslashes collapsed, so it always stays on the current host.
- Web UI notifications show messages as text. They often carry server error messages, which can include values you entered.
- Access rules: country names in the blacklist and whitelist tables are escaped.
- Switching tabs only calls the handlers that pages registered, never a property chosen by the tab ID.
- Proxy rule host names and aliases may only contain letters, digits, `.`, `-`, `_`, the wildcards `*` and `?`, and `:`, `[`, `]` (ports and IPv6 addresses). Internationalised domain names must be entered in their `xn--` form.
- HTTP Proxy list: upstream addresses, virtual directories, tags and error messages are escaped.

## 2.2.2

Based on Zoraxy 3.3.5 (`310b0cc`).

Security fixes from a review of GitHub code scanning (CodeQL) findings.

**Security**
- **OAuth2 SSO:** after login, the browser is only sent back to an address on the protected host. Before, the return address came from the `state` parameter and could point to any site (open redirect). The `z-token` access token cookie and the PKCE verifier cookie are now `HttpOnly`.
- **Waymark Auth:** the login callback only redirects to the host that received it (open redirect). The gateway login page no longer carries on after redirecting a signed-in user who arrived without a return address.
- **Passwords are hashed with bcrypt:** admin accounts, Waymark Auth users, HTTP basic auth credentials on proxy rules, and WebDAV credentials.
  - Before, they were stored as unsalted SHA-512, which is quick to crack if the database leaks.
  - Existing passwords keep working. Admin and Waymark Auth passwords are re-hashed with bcrypt at the next successful login. Basic auth and WebDAV passwords are re-hashed when they are next saved.
  - Basic auth and WebDAV checks remember a verified password for 5 minutes, so the slower hash doesn't slow down every request.
  - Passwords are limited to 72 bytes (bcrypt's limit).
- **Error pages no longer load jQuery and Semantic UI scripts from CDNs:** the forbidden, host error and proxy error pages, which are served on your sites. They used them for two lines of text, now plain JavaScript.
- **File names:** a certificate name from the ACME tool, or a proxy rule's host name, can no longer contain `/`, `\` or `..`, so their files always stay in `conf/`. In development mode (`-dev`), web UI file paths are cleaned.
- **Admin web UI:** access rule names, virtual directory paths and targets, tags and listening ports are escaped when shown.

**Fixed**
- Error responses are built with a JSON encoder, so messages containing quotes no longer produce invalid JSON.
- Changing the admin password no longer deletes and re-creates the account, so a failed change can't leave it without a password. The request now returns OK.
- Wildcard certificate names replace every `*`, not only the first.

**Removed**
- Unused files: the unminified Semantic UI sources (`semantic.js`, `semantic.css`, `components/`; the UI loads the minified files), the old file manager page (`tools/fs.html`), and unused copies of the error pages in `src/web`.

**Code scanning**
- CodeQL now runs from `.github/workflows/codeql.yml` with `.github/codeql/codeql-config.yml`, which leaves out the Zoraxy plugin library (`src/mod/plugins/zoraxy_plugin`, compiled into plugins, not run by Waymark) and vendored minified libraries.

## 2.2.1

Based on Zoraxy 3.3.5 (`310b0cc`).

**Removed**
- Upgrade-only code from 2.2.0, now that the HA integration on every node is from 2.2.0 or later:
  - Saving more than one floating IP is no longer refused while a node's HA helper is older. The **Services** row still shows when the helper doesn't match this Waymark version.
  - The settings file always has `EXTRA_VIPS` (empty with one floating IP). Helpers from before 2.2.0 reject it, so a node still running one needs the installer run again.

## 2.2.0

Based on Zoraxy 3.3.5 (`310b0cc`).

**Upgrading:** this release changes the HA integration. After upgrading each node, run the installer again on it (`/opt/waymark/waymark -ha-export-installer /tmp/waymark-ha && sudo bash /tmp/waymark-ha/install.sh`). With a single floating IP everything keeps working until you do; more than one needs the new helper on every node.

**Added**
- **Multiple floating IPs** (up to 16) on the HA page, with **Add floating IP** and a remove button per address, and an optional label for each.
  - They form one VRRP group: the active node holds all of them, on the nodes' interface. The first one is the primary; node addresses must be in its subnet, while the others can be in any IPv4 subnet.
  - Your existing floating IP becomes the primary one; no settings change.
  - The root helper accepts the extra addresses as `EXTRA_VIPS` and checks each one (valid IPv4 address and prefix length, not a node address, no duplicates, at most 15). It also now rejects a primary floating IP that equals a node address.
  - The cluster overview, the status page panel and the header badge list the floating IPs. A warning appears if the active node holds only some of them, and an error if they are split between nodes.
- **HA helper version check:** the **Services** row on the HA page shows whether the installed helper and keepalived scripts match this Waymark version, with the commands to update them. Saving more than one floating IP is refused while the helper is older.
- **Warning for stream proxies on a floating IP:** a stream proxy that listens on one specific floating IP can only start on the node holding it unless `net.ipv4.ip_nonlocal_bind=1` is set. The HA page explains this, with the commands, and warns when such stream proxies exist and the setting is off.

## 2.1.1

Based on Zoraxy 3.3.5 (`310b0cc`).

**Fixed**
- HA cluster overview: after a failover, the node that took over reported "More than one node is active" because it counted the unreachable node's last known role as current.
  - The cluster view (`/api/ha/cluster`) now gives a node's role, health, floating IP and sync state only when it answered the latest poll. For an unreachable node it gives `lastRole` and `lastSeen` instead.
  - The overview shows that role faded, labelled "when last seen". A failover is reported as one warning, e.g. "Failed over to waymark01 · waymark02 unreachable (was active, last seen 3 min ago)", or "No standby" when the unreachable node was the standby. Both nodes reporting active while reachable is still an error.

## 2.1.0

Based on Zoraxy 3.3.5 (`310b0cc`).

**Changed**
- New colour scheme built on the Waymark logo colours (navy `#123B5C`, teal `#14B8A6`), in light and dark mode:
  - Light mode uses cool grey-teal surfaces and navy text; dark mode uses navy-tinted backgrounds instead of neutral greys.
  - The active menu item, header line, status banner, toggles, links, blue buttons, icons and info messages use the brand teal instead of blue and purple.
  - The status page charts, world map and traffic flow diagram, and the login page backgrounds, use the same palette.

**Removed**
- The startup conversion of per-certificate DNS credentials into credential sets, added in 2.0.0. Upgrading straight from a version before 2.0.0 no longer converts them; upgrade through 2.0.0 first.

**Fixed**
- The TLS / SSL certificates page loaded a script that doesn't exist (`jsrsasign-all-min.js`); the reference is removed.
- The HTTP Proxy list raised a script error while it had no proxy rules.

## 2.0.0

Based on Zoraxy 3.3.5 (`310b0cc`).

A major version because features were removed (see Versioning above). Configuration carries over; the one change that can need action is the removed `-sshlb` flag.

**Removed**
- **Per-certificate DNS challenge credentials.** DNS-01 credentials now come only from DNS credential sets; give certificates their own credentials by creating a set for each. The ACME tool no longer has the "Enter credentials manually" option, and a certificate's challenge settings offer only credential sets or HTTP-01.
  - At startup, Waymark turns any per-certificate credentials still stored into credential sets (named after the certificate; certificates with identical credentials share one set) and assigns them, so those certificates keep renewing. Credentials of deleted certificates, or of certificates that renew with HTTP-01, are deleted.
  - Removed APIs: `/api/acme/autoRenew/setDNS`, `/api/acme/dnsProfiles/importFromCert` and `/api/acme/dnsProfiles/deleteLegacy`.
- **Quick Start** page and its guided tour.
- **Utilities > Toolbox** tab (IP range / CIDR converters and the Geo-IP lookup with access rule check), with its API `/api/info/ipcheck`.
- **Networking > Network Tools** page and everything behind it:
  - mDNS discovery viewer, IP scanner and port scanner;
  - traceroute, ping and WHOIS lookup;
  - Web SSH (with its embedded terminal binaries, which makes the binary smaller) and the `-sshlb` flag. **Remove `-sshlb` from your service's `ExecStart` before upgrading**, or Waymark won't start;
  - the HTTP forward proxy (it stops listening; its `fwdproxy` settings stay in the database, unused);
  - Wake-on-LAN and its saved addresses (`wolmac` table, kept unused);
  - the APIs under `/api/tools/` and `/api/mdns/discover`.
  - mDNS itself is kept: Waymark still announces itself and scans the network for plugins (`/api/mdns/list`, `-mdns` and `-mdnsname` flags).
- HA config sync no longer copies the `fwdproxy` and `wolmac` tables.

## 1.0.1

Based on Zoraxy 3.3.5 (`310b0cc`).

**Removed**
- The upgrade path from the Zoraxy-based builds (`3.3.5-custom.N`), which are no longer in use: the README section and its steps, and the HA installer's removal of their `zoraxy-ha-apply` integration. The HA helper no longer treats a keepalived config generated by `zoraxy-ha-apply` as its own, so on a machine that still has one it is backed up as `keepalived.conf.pre-waymark` like any other existing config.

**Fixed**
- `CAPTCHA_FEATURE.md` named the CAPTCHA cookie `zoraxy_captcha_session`; it is `waymark_captcha_session`.

## 1.0.0

Based on Zoraxy 3.3.5 (`310b0cc`).

The first release under the Waymark Proxy name. It contains everything from the Zoraxy-based builds `3.3.5-custom.1` to `custom.16` (listed below), plus the rename.

**Renamed**
- Zoraxy is now **Waymark Proxy** ("Waymark" for short), with a new logo, a new footer and About page that credit Zoraxy, and the version scheme above. The source code is at [github.com/rtadams89/waymark-proxy](https://github.com/rtadams89/waymark-proxy).
- The release binary is `waymark_linux_amd64`, and `waymark -version` prints e.g. `Waymark Proxy 1.0.0 (based on Zoraxy 3.3.5)`.

**Identifiers**
- Service user, folder and unit are `waymark`, `/opt/waymark` and `waymark.service`.
- High availability:
  - The helper is `waymark-ha-apply`, with units `waymark-ha-apply.path` and `waymark-ha-apply.service`, scripts in `/usr/local/lib/waymark-ha`, and folders `/var/lib/waymark/ha` and `/var/lib/waymark-ha`.
  - The installer reads `WAYMARK_USER` and `WAYMARK_UNIT`.
  - keepalived's instance and script are named `WAYMARK` and `waymark_health`.
  - Nodes sync over `X-Waymark-Ha-*` headers.
- Cookies: `Waymark` (session), `waymark_csrf` and `waymark_captcha_session`. The CAPTCHA verify path is `/.waymark/captcha/verify`.
- Headers and identifiers:
  - Headers sent to upstreams: `X-Forwarded-Server: waymark-<id>` and `X-Auth-Method: waymark_sso`.
  - Response headers: `x-proxy-by: waymark/<version>` and `X-Waymark-Cache`.
  - User-Agents: `waymark-healthcheck`, `waymark-uptime` and `waymark-wsproxy`.
  - mDNS identity `waymark-proxy`, log files `waymark_…`, configuration exports `waymark-config-…zip`.
- The built-in SSO is called Waymark Auth (TOTP issuer and passkey name).

**Compatibility with Zoraxy**
- Configuration (`conf/`, `sys.db`) uses Zoraxy 3.3.5's formats.
- Zoraxy plugins work: the plugin protocol (`X-Zoraxy-*` plugin headers and the plugin SDK) and the Zoraxy plugin store are unchanged.

## Before 1.0.0: the Zoraxy-based builds

These builds were released as `3.3.5-custom.N`, a customised Zoraxy 3.3.5.

### 3.3.5-custom.16

Base: upstream v3.3.5 (`310b0cc`).

**Fixed**
- The HA role badge (and the read-only note on a standby) now sits next to the logo instead of on a second line. The header is back to its normal height and no longer covers the top of each page and the menu.

### 3.3.5-custom.15

Base: upstream v3.3.5 (`310b0cc`).

**Added**
- HA cluster overview, on the High Availability page (**Cluster**) and as a **High Availability** panel on the Status page. The panel is shown only while HA is on and can be hidden in the panel settings.
  - It lists every node with its role (Active, Standby, Fault, keepalived stopped), whether it holds the floating IP, its health, its configuration sync state (Source, In sync, Updating, or the problem), its Zoraxy version and how long it has been up.
  - A summary line shows which node holds the floating IP and how many nodes are healthy, with warnings for unreachable or unhealthy nodes, version differences, and more than one active node.
  - Other nodes' status comes over the configuration sync connection, so it is shown while sync is on. Every node now checks the others every 10 seconds, the active one included. An unreachable node shows its last known role, faded, and when it was last seen.
- The active node reports it in its sync status when another node also claims to be active (both holding the floating IP).

**Changed**
- **Generate** for the cluster key makes the key in the browser instead of asking Zoraxy for one.
- If your login on a node has ended (for example after signing in to another node on the same host name, even on another port), the HA page says so. Before, it put the login page's HTML into the cluster key field, or reported "Saved" for a save that never happened.

### 3.3.5-custom.14

Base: upstream v3.3.5 (`310b0cc`).

**Added**
- High availability, phase 2: configuration sync. Turn on **Copy the active node's configuration to the standby nodes** in the HA cluster settings, generate a cluster key, and use Copy settings / Paste settings once to put the same settings on the other node.
  - **What is copied** from the active node: proxy and stream rules, redirects, access and path rules, SSO settings, certificates and keys, ACME settings and account, admin accounts and the login session key (so a login keeps working after failover), system settings, and this fork's settings including the HA settings. What stays per node: statistics, logs, the Geo-IP database, plugins, static web server files and live SSO sessions.
  - **How:**
    - A standby checks the other nodes every 10 seconds.
    - When the active node's configuration has changed and then stayed the same for one check, the standby downloads a snapshot, checks it, and restarts Zoraxy in place (same process) to load it. The snapshot is applied at startup, before anything loads its configuration.
    - The configuration follows whichever node is active: after a failover, changes made on the new active node are copied to the old one when it returns as standby.
  - **Security:**
    - Each node listens on its own address and the sync port (default 9280), using TLS 1.3.
    - Both sides prove they hold the cluster key with an HMAC tied to that TLS session, so a proof can't be replayed or relayed. Requests from addresses outside the node list are refused.
    - Snapshots may only contain the files and tables listed above, and are refused if they come from a different Zoraxy version or their cluster settings don't include the receiving node.
    - Staged and applied snapshot files are readable only by Zoraxy's user.
  - **Read-only standby:** while sync is on, the standby refuses setting changes and names the active node. Its HA settings stay editable, so sync can be paused on a node. The menu bar shows "Read-only · make changes on <active node>".
  - **HA page:** a Configuration sync panel shows the sync state, the last copy (when and from which node), which standby nodes are checking in and copying, the listener address, and a **Sync now** button.

**Fixed**
- HA settings: Save and apply gave up after 30 seconds and reported a failure while the server was still waiting for keepalived (up to 75 seconds).

### 3.3.5-custom.13

Base: upstream v3.3.5 (`310b0cc`).

**Fixed**
- High availability: turning HA off left keepalived running with no VRRP instance. Debian's keepalived never reports ready to systemd in that state, so its start hung for 90 seconds and failed (also at every boot). keepalived now runs only while HA is on: turning HA off stops and disables it, turning it on enables and starts it. The installer no longer enables keepalived itself.
- High availability: a save made while the apply helper was still running was ignored (systemd skips path changes while the helper runs). The helper now checks for a newer save after each apply and applies it too.
- The apply helper limits each keepalived start or reload to 20 seconds and checks keepalived is actually running before reporting success. If keepalived does not come up, it restores the previous working config, or stops keepalived and says so when there is none. Before, it could report a restore that didn't happen.
- Zoraxy waited only 20 seconds for the apply result and reported "no response" while keepalived was still starting. It now waits up to 75 seconds.

**Added**
- The installer finds Zoraxy's systemd service. If it is sandboxed (`ProtectSystem=strict`), the installer adds a drop-in (`ReadWritePaths=/var/lib/zoraxy/ha`) and asks you to restart Zoraxy. Set `ZORAXY_UNIT` if it can't find the service.
- HA page: a **Services** row showing whether the settings watcher (`zoraxy-ha-apply.path`) and keepalived are running, with the command to fix each.
- HA page: when Zoraxy can't write its HA folder because its service is sandboxed, the not-installed message says so and how to fix it.
- The role badge in the menu bar shows the node name (for example "zrx-b · Standby"), so it's clear which node a page came from.

**Changed**
- If systemd reports keepalived is not running, the node counts as "keepalived stopped" even when keepalived was killed without reporting it. This holds certificate renewal and shows it in the badge.

### 3.3.5-custom.12

Base: upstream v3.3.5 (`310b0cc`).

**Added**
- High availability, phase 1: an active/passive pair (2 to 4 nodes) sharing a floating IP managed by keepalived (Networking > **High Availability**).
  - Cluster settings in the UI: floating IP and prefix, VRRP router ID, optional VRRP password, and each node's name, address, interface and priority. Each node finds itself in the list by its own address. Uses unicast VRRP between the node addresses. By default the floating IP stays where it is when a higher-priority node recovers; it can be set to move back after 30 seconds.
  - Zoraxy (running as the unprivileged `zoraxy` user) never writes keepalived's config. It writes a settings file; a root-owned helper (`zoraxy-ha-apply`, started by a systemd path unit) validates every value, builds `/etc/keepalived/keepalived.conf` from a fixed template, tests it with `keepalived -t`, and reloads keepalived. If the reload fails, the previous config is restored. The result is shown in the UI. The first time, a hand-written keepalived.conf is kept as `keepalived.conf.pre-zoraxy`.
  - keepalived runs two root-owned scripts as the `zoraxy` user (`enable_script_security`). The health check needs a fresh heartbeat from Zoraxy, written every 2 seconds while the proxy listener answers (TCP connect plus TLS handshake). The notify script records the node's role. The floating IP moves to the other node within about 10 seconds when Zoraxy stops answering or exits, when keepalived stops, or when the machine goes down.
  - Role badge in the menu bar (Active and holding the floating IP, Standby, Fault, keepalived stopped). The HA page shows this node's role, health, certificate renewal state, last apply result and a preview of the generated keepalived config.
  - Automatic certificate renewal runs only on the active node. Standby, faulted and stopped nodes skip it; "Renew now" still works anywhere.
  - Installer built into the binary: `zoraxy -ha-export-installer <folder>` writes `install.sh`, the helper, scripts and systemd units for review; then run `sudo bash <folder>/install.sh` on each node (needs `apt install keepalived`). The files can also be downloaded from the HA page.
  - Settings are not synced between nodes yet. Enter them on each node, or use **Copy settings** / **Paste settings**. Syncing configuration, admin accounts and keys from the active node is planned for phase 2.

### 3.3.5-custom.11

Base: upstream v3.3.5 (`310b0cc`).

**Added**
- Maintenance mode per rule (rule editor > **Maintenance**). While on, every request to the host, including its virtual directories, gets a "Down for maintenance" page (HTTP 503, `Cache-Control: no-store`) with an optional message and expected duration (also sent as `Retry-After`), instead of reaching the upstream. Access rules and authentication still apply first; health checks keep running. A preview button shows the page. Replace the built-in page with `<webroot>/templates/maintenance.html` (placeholders `{{message}}`, `{{retry}}`, `{{host}}`). The HTTP Proxy list marks the rule with an orange wrench.
- Status page: **Upstream Health** panel with counts of healthy and down health-checked upstreams and rules in maintenance, and a list of what is down (rule, upstream, how long, reason) or in maintenance. Can be hidden in the panel settings.
- Health checks: optional **Response must contain** text for HTTP GET checks. The upstream is healthy only if the status matches and the first 64 KB of the response include the text (catches apps that answer 200 with an error page).
- Health checks for virtual directories: **Also check virtual directories** checks each of the rule's virtual directory backends with the rule's settings, including passive monitoring. While a backend is down its path gets the 521 page; the rest of the host is unaffected. Shown with a "virtual directory /path" label on the Health Checks page, in the log and in events, and with a red heart next to the virtual directory in the HTTP Proxy list.

**Changed**
- Static file caching now also covers a rule's virtual directories (using the rule's cache settings, keyed on the full request path) and rules that accept plain HTTP.

### 3.3.5-custom.10

Base: upstream v3.3.5 (`310b0cc`).

**Added**
- Setting to turn Geo-IP lookups off (Utilities > System > Geo-IP Lookups). While off, the Geo-IP database is not loaded (about 170 MB less memory in testing), statistics record no visitor countries, and scheduled database downloads are skipped. It can't be turned off while an enabled access rule list contains countries; while it's off, countries can't be added and the access rule page says why. Turning it back on loads the database without a restart.
- Upstream release tracking (GitHub Actions):
  - **Watch upstream releases** runs daily. When tobychui/zoraxy publishes a final release (release candidates are skipped, but a newer one is noted in the run summary) that isn't merged into `custom` yet, it opens an issue (label `upstream-release`) and starts the merge workflow. Needs Issues turned on for the repository.
  - **Merge upstream release** (also runnable by hand, including for a release candidate) merges the release into a new `upstream-merge/<tag>` branch, keeps the folders this fork removed removed, resets the version to `<new base>-custom.1` with a changelog entry, runs vet, tests and govulncheck, and opens a pull request into `custom` (a draft if a check failed). On merge conflicts it reports the files in an issue instead. Opening pull requests needs "Allow GitHub Actions to create and approve pull requests" in the repository's Actions settings.

### 3.3.5-custom.9

Base: upstream v3.3.5 (`310b0cc`).

**Changed**
- On rules with health checks, the checks now decide routing whatever the number of upstreams. When every upstream is disabled (including a rule's only upstream), visitors get the "Web server is down" (521) error page at once instead of being forwarded to a dead server and timing out. Rules without health checks still always forward to their only upstream, as upstream Zoraxy does.
- The legacy "disable auto fallback" option is ignored on rules with health checks, so a failed upstream is always taken out of the pool.

**Added**
- HTTP Proxy list: a red heart next to each upstream that is down (hover for the reason), and next to the host when all of its upstreams are down.

**Fixed**
- Health check settings: the Upstream status table's headings no longer stack above the values in the side panel. The single-upstream note now describes the actual behaviour.

### 3.3.5-custom.8

Base: upstream v3.3.5 (`310b0cc`).

**Added**
- HTTP Proxy list: a **Health / Cache** column showing whether each rule has health checks (active, passive or both) and static file caching on. The health indicator is green when all checked upstreams are healthy and red with "n of m down" otherwise, refreshed every 15 seconds; hovering shows the settings.
- Status page: two new panels, each can be hidden in the panel settings.
  - **Static File Cache (Today)**: today's hit rate and bandwidth saved across all hosts with caching on, compared with yesterday. Counters reset at local midnight and are kept across restarts.
  - **Cache Memory**: how full the shared in-memory cache is (percentage, size against the limit, number of files).

### 3.3.5-custom.7

Base: upstream v3.3.5 (`310b0cc`).

**Added**
- Static file caching, turned on per proxy rule in the rule editor's new **Caching** tab. Cached files are kept in memory and served without contacting the upstream.
  - Only static file types are cached: a per-rule list of extensions (CSS, JS, images, fonts, audio/video, wasm by default). URLs without an extension can also be cached by response type (image, font, stylesheet, script, audio, video). Pages and API calls always go to the upstream.
  - Freshness follows the upstream's `Cache-Control` / `Expires` headers, with a per-rule default lifetime (1 hour) when it sends none. Responses marked `no-store`, `no-cache` or `private`, responses that set cookies, error responses and files over the per-rule size limit (10 MB) are never stored.
  - Expired files with an `ETag` or `Last-Modified` are revalidated with a conditional request, and the stored copy is kept when the upstream answers 304.
  - Separate copies per `Vary` value (for example gzip and uncompressed). Browser conditional requests (304) and range requests (206) are answered from the cache.
  - One memory limit shared by all rules (256 MB by default, adjustable in the Caching tab); least recently used files are dropped first. The cache starts empty after a restart.
  - Per-rule stats (hit rate, files and bytes stored, data served from cache) and a button to clear a rule's cache. Responses carry `X-Zoraxy-Cache: HIT | REVALIDATED | MISS | BYPASS`.
  - The cache runs after access rules, rate limiting, CAPTCHA and authentication. Requests carrying their own `Authorization` header for the upstream, WebSocket connections and virtual directories are never cached.

### 3.3.5-custom.6

Base: upstream v3.3.5 (`310b0cc`).

**Changed**
- Health checks replace upstream's separate uptime monitor. The uptime monitor no longer probes anything; the Uptime Monitor page shows proxy rules with active health checks, fed by those checks in 5 minute blocks.
- New proxy rules get an active health check by default (HTTP GET every 10s, disabled after 3 failures, re-enabled after 2 successes). The create form's uptime option is now "Enable health checks", with an optional health check path.
- The rule editor's "Monitor Uptime" checkbox and health-check URI field are gone (hidden); health checks are configured under Upstream → Health Checks.

**Migration**
- On first start of this version, every rule that had "Monitor Uptime" on and no health check settings gets the default active health check, using its old health-check URI as the path, so failover and uptime history continue. This runs once per installation; rules that had "Monitor Uptime" off are left without health checks.

**Fixed**
- DNS Credential Sets: the table no longer stacks into a column of headers followed by a column of values in the narrow side panel; each set is one row under Name / Provider / Used By.
- A certificate's own (per-certificate) DNS credentials can now be deleted from the credential set picker once the certificate uses a credential set or HTTP-01. While the certificate still renews with them, the delete button is hidden and a note explains why.

**Note**
- Rules without active health checks, including passive-only rules and virtual directories, no longer get automatic failover from the uptime monitor and are not shown on the Uptime Monitor page.

### 3.3.5-custom.5

Base: upstream v3.3.5 (`310b0cc`).

**Fixed**
- Health Checks panel: the check type menu now always shows all four options. In the narrow side panel it was limited to about three entries and scrolled, which hid HTTP GET after picking another type.

### 3.3.5-custom.4

Base: upstream v3.3.5 (`310b0cc`).

**Changed**
- Rules with active health checks now feed the Uptime Monitor page from those checks, and the uptime monitor's own 5 minute probe is skipped for their upstreams. Results are rolled up into one entry per 5 minutes (so the page still covers a day); an entry is down if the upstream was out of load balancing at any point in it, whether active checks or passive monitoring took it out.
- Rules with passive monitoring only, virtual directories, and rules without health checks are still probed by the uptime monitor as before.

### 3.3.5-custom.3

Base: upstream v3.3.5 (`310b0cc`).

**Added**
- Health checks for load balanced upstreams, set per proxy rule from the Upstream tab of the rule editor (**Health Checks** button).
  - Active checks: HTTP GET, HTTP HEAD, TCP connect or ICMP ping, with path, healthy status codes, optional Host header, interval, timeout, and how many consecutive failures disable / successes re-enable an upstream.
  - Passive monitoring, enabled separately: counts 5xx responses, timeouts and connection failures (each selectable) in real proxied traffic and disables an upstream after N errors within W seconds. It comes back through the active checks, or after a recovery time when no active check is configured.
  - Every enable/disable is logged with its reason under `[healthcheck]`, and shown in the rule's Health Checks panel with live per-upstream status.
  - The existing uptime monitor keeps recording history, but no longer switches upstreams on or off for rules that use health checks.
- Status page: every panel below the server status banner can be shown or hidden (**Panels** button in the banner). Choices are saved on the server. The GeoIP visitor map and the countries card are hidden by default.

**Changed**
- Traffic flow diagram: the "Internet" node is now "Clients", with a people icon instead of a globe.

### 3.3.5-custom.2

Base: upstream v3.3.5 (`310b0cc`).

**Security**
- Replaced github.com/gorilla/csrf with the Go standard library's `net/http.CrossOriginProtection`, as the advisory recommends. gorilla/csrf has an unpatched advisory affecting every version through 1.7.3 (hosts in TrustedOrigins also trust their `http://` origin). Zoraxy never set TrustedOrigins, so this build was not exploitable, but there is no fixed version to move to.
- State-changing requests (POST, PUT, DELETE, ...) are now rejected when the browser reports them as cross-origin, using the `Sec-Fetch-Site` header (all browsers since 2023) or, for older browsers, an Origin/Host mismatch. This works the same over HTTP, HTTPS, an SSH tunnel, or behind Zoraxy's own proxy. CSRF tokens are no longer checked.

**Removed**
- The plain-HTTP workaround added in custom.1 (`src/csrf_plaintext.go`), which the standard library check makes unnecessary.

### 3.3.5-custom.1

Base: upstream v3.3.5 (`310b0cc`).

**Added**
- Shared DNS challenge credential sets: save credentials once (for example a Cloudflare API token) and pick them when issuing a certificate. Existing certificates can be switched between a saved set, their own per-certificate credentials, or HTTP-01 from the key button in the TLS/SSL certificate list. Per-certificate credentials can be turned into a reusable set. Saved secrets are never sent back to the browser.
- Version shown in the web UI and `zoraxy -version` includes the fork revision.

**Fixed**
- Admin UI saves failed with 403 over plain HTTP (direct `http://host:8000` or an SSH tunnel) after the gorilla/csrf 1.7.3 update. Requests are now marked as plaintext unless they arrived over TLS or through an HTTPS proxy. (Superseded in custom.2.)

**Security**
- golang.org/x/net 0.59.0 → 0.60.0 (HTTP/2 fixes GO-2026-6603, -6610, -6611, -6612, -6617).
- github.com/gorilla/csrf 1.7.2 → 1.7.3 (GO-2025-3607).
- Builds use the latest Go 1.26.x patch release instead of 1.26.0, and CI fails on any reachable known vulnerability (govulncheck).

**Build**
- GitHub Actions workflow (`custom-build.yml`): vet, test, govulncheck and a linux/amd64 build on every push to `custom`; GitHub Release on version tags.
- `src/dist/` added to `.gitignore`.
