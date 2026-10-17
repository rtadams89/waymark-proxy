# Custom fork changelog

Changes in this fork on top of upstream [tobychui/zoraxy](https://github.com/tobychui/zoraxy).

**Versioning:** `<upstream base>-custom.<revision>`, for example `3.3.5-custom.1`.

- The revision goes up by one for every change that ships, and resets to 1 after rebasing onto a new upstream release (`3.3.6-custom.1`).
- The revision lives in `src/version_custom.go` (`FORK_REVISION`). `SYSTEM_VERSION` in `src/def.go` stays the plain upstream version, because Zoraxy's config migrations parse it as a number.
- Release tags are `v<version>` (for example `v3.3.5-custom.1`). CI refuses to publish a release whose tag doesn't match the version built into the binary.
- New upstream releases are picked up by the "Watch upstream releases" workflow (daily), which opens an issue and starts "Merge upstream release" to prepare a pull request. Merge those pull requests with a merge commit.

## 3.3.5-custom.12

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

## 3.3.5-custom.11

Base: upstream v3.3.5 (`310b0cc`).

**Added**
- Maintenance mode per rule (rule editor > **Maintenance**). While on, every request to the host, including its virtual directories, gets a "Down for maintenance" page (HTTP 503, `Cache-Control: no-store`) with an optional message and expected duration (also sent as `Retry-After`), instead of reaching the upstream. Access rules and authentication still apply first; health checks keep running. A preview button shows the page. Replace the built-in page with `<webroot>/templates/maintenance.html` (placeholders `{{message}}`, `{{retry}}`, `{{host}}`). The HTTP Proxy list marks the rule with an orange wrench.
- Status page: **Upstream Health** panel with counts of healthy and down health-checked upstreams and rules in maintenance, and a list of what is down (rule, upstream, how long, reason) or in maintenance. Can be hidden in the panel settings.
- Health checks: optional **Response must contain** text for HTTP GET checks. The upstream is healthy only if the status matches and the first 64 KB of the response include the text (catches apps that answer 200 with an error page).
- Health checks for virtual directories: **Also check virtual directories** checks each of the rule's virtual directory backends with the rule's settings, including passive monitoring. While a backend is down its path gets the 521 page; the rest of the host is unaffected. Shown with a "virtual directory /path" label on the Health Checks page, in the log and in events, and with a red heart next to the virtual directory in the HTTP Proxy list.

**Changed**
- Static file caching now also covers a rule's virtual directories (using the rule's cache settings, keyed on the full request path) and rules that accept plain HTTP.

## 3.3.5-custom.10

Base: upstream v3.3.5 (`310b0cc`).

**Added**
- Setting to turn Geo-IP lookups off (Utilities > System > Geo-IP Lookups). While off, the Geo-IP database is not loaded (about 170 MB less memory in testing), statistics record no visitor countries, and scheduled database downloads are skipped. It can't be turned off while an enabled access rule list contains countries; while it's off, countries can't be added and the access rule page says why. Turning it back on loads the database without a restart.
- Upstream release tracking (GitHub Actions):
  - **Watch upstream releases** runs daily. When tobychui/zoraxy publishes a final release (release candidates are skipped, but a newer one is noted in the run summary) that isn't merged into `custom` yet, it opens an issue (label `upstream-release`) and starts the merge workflow. Needs Issues turned on for the repository.
  - **Merge upstream release** (also runnable by hand, including for a release candidate) merges the release into a new `upstream-merge/<tag>` branch, keeps the folders this fork removed removed, resets the version to `<new base>-custom.1` with a changelog entry, runs vet, tests and govulncheck, and opens a pull request into `custom` (a draft if a check failed). On merge conflicts it reports the files in an issue instead. Opening pull requests needs "Allow GitHub Actions to create and approve pull requests" in the repository's Actions settings.

## 3.3.5-custom.9

Base: upstream v3.3.5 (`310b0cc`).

**Changed**
- On rules with health checks, the checks now decide routing whatever the number of upstreams. When every upstream is disabled (including a rule's only upstream), visitors get the "Web server is down" (521) error page at once instead of being forwarded to a dead server and timing out. Rules without health checks still always forward to their only upstream, as upstream Zoraxy does.
- The legacy "disable auto fallback" option is ignored on rules with health checks, so a failed upstream is always taken out of the pool.

**Added**
- HTTP Proxy list: a red heart next to each upstream that is down (hover for the reason), and next to the host when all of its upstreams are down.

**Fixed**
- Health check settings: the Upstream status table's headings no longer stack above the values in the side panel. The single-upstream note now describes the actual behaviour.

## 3.3.5-custom.8

Base: upstream v3.3.5 (`310b0cc`).

**Added**
- HTTP Proxy list: a **Health / Cache** column showing whether each rule has health checks (active, passive or both) and static file caching on. The health indicator is green when all checked upstreams are healthy and red with "n of m down" otherwise, refreshed every 15 seconds; hovering shows the settings.
- Status page: two new panels, each can be hidden in the panel settings.
  - **Static File Cache (Today)**: today's hit rate and bandwidth saved across all hosts with caching on, compared with yesterday. Counters reset at local midnight and are kept across restarts.
  - **Cache Memory**: how full the shared in-memory cache is (percentage, size against the limit, number of files).

## 3.3.5-custom.7

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

## 3.3.5-custom.6

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

## 3.3.5-custom.5

Base: upstream v3.3.5 (`310b0cc`).

**Fixed**
- Health Checks panel: the check type menu now always shows all four options. In the narrow side panel it was limited to about three entries and scrolled, which hid HTTP GET after picking another type.

## 3.3.5-custom.4

Base: upstream v3.3.5 (`310b0cc`).

**Changed**
- Rules with active health checks now feed the Uptime Monitor page from those checks, and the uptime monitor's own 5 minute probe is skipped for their upstreams. Results are rolled up into one entry per 5 minutes (so the page still covers a day); an entry is down if the upstream was out of load balancing at any point in it, whether active checks or passive monitoring took it out.
- Rules with passive monitoring only, virtual directories, and rules without health checks are still probed by the uptime monitor as before.

## 3.3.5-custom.3

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

## 3.3.5-custom.2

Base: upstream v3.3.5 (`310b0cc`).

**Security**
- Replaced github.com/gorilla/csrf with the Go standard library's `net/http.CrossOriginProtection`, as the advisory recommends. gorilla/csrf has an unpatched advisory affecting every version through 1.7.3 (hosts in TrustedOrigins also trust their `http://` origin). Zoraxy never set TrustedOrigins, so this build was not exploitable, but there is no fixed version to move to.
- State-changing requests (POST, PUT, DELETE, ...) are now rejected when the browser reports them as cross-origin, using the `Sec-Fetch-Site` header (all browsers since 2023) or, for older browsers, an Origin/Host mismatch. This works the same over HTTP, HTTPS, an SSH tunnel, or behind Zoraxy's own proxy. CSRF tokens are no longer checked.

**Removed**
- The plain-HTTP workaround added in custom.1 (`src/csrf_plaintext.go`), which the standard library check makes unnecessary.

## 3.3.5-custom.1

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
