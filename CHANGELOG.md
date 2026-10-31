# Waymark Proxy changelog

Waymark Proxy is a reverse proxy based on [Zoraxy](https://github.com/tobychui/zoraxy) by tobychui and contributors. This file lists Waymark's changes; for Zoraxy's own history see its [releases](https://github.com/tobychui/zoraxy/releases).

**Versioning:** [semantic versioning](https://semver.org), `MAJOR.MINOR.PATCH`.

- **PATCH** (1.0.1): bug and security fixes only.
- **MINOR** (1.1.0): new features that keep existing configuration working.
- **MAJOR** (2.0.0): changes that need action when upgrading.
- The version lives in `src/version_custom.go` (`PRODUCT_VERSION`). Release tags are `v<version>` (`v1.0.0`); CI refuses a tag that doesn't match the version built into the binary.
- Waymark was forked from Zoraxy 3.3.5, which **Settings > About** and `waymark -version` still show. Since 2.6.2 it no longer follows Zoraxy releases.

## 2.19.7

**Save buttons and unsaved changes**
- One rule across the UI: **an area with a Save button saves nothing until you click Save; an area without one applies each change at once.** Pages and cards with only switches apply instantly. List actions (add, delete, duplicate, move, enable a row) apply instantly. Typed values, dropdowns and choices sit in an area with a Save button.
- The proxy rule editor and the TCP / UDP rule editor have one Save button in the side menu. Fields that used to save on change (listen address, certificates, TLS options, Layer 4 listen address and options) are now saved with it. Sections with their own Save (headers, caching, maintenance, access, aliases, tags) keep it.
- Settings that saved as soon as they changed now have a Save button: the main listening port, minimum TLS version, the static web server and WebDAV ports, the plugin auto-reload interval and the access rule picked for a host.
- Save buttons are labelled "Save" with a green save icon ("Apply Change", "Apply Timeouts", "Save Renew Policy" and similar are gone; Default Site's Reset is now Discard).
- When an area has unsaved changes its Save button turns green and "Unsaved changes" appears next to it.
- Leaving with unsaved changes asks first ("Keep editing" or "Discard changes"): switching page, closing or replacing a side panel, closing a rule editor or modal, switching the ACL or object being edited, going back to a list, and closing or reloading the browser tab. Discarding puts the saved values back.
- While a rule editor has unsaved changes, a background refresh only updates its summaries (aliases, destinations, health, virtual directories) instead of overwriting the fields, and renaming the host waits until the changes are saved.

## 2.19.6

**ACME: certificate request progress**
- The certificate request window shows the request's progress as it runs: each step (account, challenge, validation, issuing, saving) with the time since the start, warnings and the error if it fails, and a running timer.
- While a request runs, the window stays open: clicking outside it or Cancel does not close it, other side panels do not replace it, and leaving the page asks first. It can be closed again when the request finishes.
- If the connection drops during a long request, the window keeps following the request until the server reports the result.
- lego's messages go to the system log instead of only to the console.
- Certificate requests run one at a time; a request that has to wait for another (such as an automatic renewal) says so.
- The EAB HMAC key is no longer written to the system log or the browser console.

## 2.19.5

**Security fixes from code scanning**
- SSO: the login gateway only sends a session code to an http(s) address whose host is one of Waymark's proxy rules. Before, a user with no host restrictions could be sent, with a fresh session code, to any host named in the `redirect` parameter. This applies to password, 2FA and passkey logins and to users who are already signed in.
- SSO: redirects after setting a session reject targets starting with `//` or `/\`.
- Proxy: a destination's `Location: //host/...` header is no longer treated as a path inside a virtual directory.
- ACLs page: the Duplicate and Delete buttons no longer break, or run script, when an ACL name contains a quote.
- Config restore: every entry in the zip is checked before anything changes, entries with `..` are refused, `conf/` files go to the configured config folder and `sys.db` to the configured database path, and folders in the zip are created instead of failing the restore.
- ACL and SSO group policy IDs are checked before they are used in file names; a group policy file with an invalid ID is skipped at start with a log message.
- Certificate download and "set default certificate" use the same name check as certificate removal, and the download file name is quoted correctly.
- The TLS check for a destination and the ACME wizard's reachability check only accept a host name or address (with a port for the TLS check). A path in the TLS check is ignored.
- The ACME wizard's public address check trims the newline from the address it looks up.

## 2.19.4

**Docs: screenshots in the README**
- The README shows the status dashboard, proxy rules, the ACL editor and the objects page, in light and dark versions that follow the reader's GitHub theme. The images are in `docs/screenshots/`.
- No code changes.

## 2.19.3

**Security: golang.org/x/net updated to 0.62.0**
- Fixes [GO-2026-6860](https://pkg.go.dev/vuln/GO-2026-6860): parsing some HTML took time that grew with the square of its size. Waymark reaches this code while getting ACME certificates, and govulncheck failed the 2.19.2 CI build on it.
- golang.org/x/crypto (0.58.0), x/sys (0.49.0), x/sync (0.24.0) and x/text (0.43.0) were updated with it.

## 2.19.2

**Changed: code comments**
- Comments throughout the Go code, the web interface (HTML, JavaScript, CSS), the scripts and the build workflows were reviewed. Comments now only say what the code does where that is not clear from the code itself: notes on history ("custom fork", issue numbers, what the code used to do), reasons, things the code does not do, file cross-references and commented-out code were removed or shortened, and comments that only repeated the code were dropped. License headers, build directives and generated-file markers are unchanged.
- No behaviour change: the Go code compiles to the same program (checked by comparing the parsed code with comments ignored), and the web pages' scripts are unchanged apart from comments.

## 2.19.1

**Fixed: streamed responses to POST requests could be cut short**
- When a request with a body (a POST, for example) got a response that the destination streamed (no `Content-Length`, such as a download built on the fly), the response could end after its first part. Once the response started, Go's HTTP server closed the request body, and the proxy's last check of that body, after it had already been sent in full, then failed and closed the connection to the destination. That check is now answered as "finished". This was the cause of the occasional `TestChunkedResponseReachesClientBeforeUpstreamFinishes` failure in CI.

**Fixed: nested objects with a loop**
- If a loop of objects inside each other got into `conf/acl/objects.json` by hand (saving refuses one), what the objects on the loop matched depended on the order they were read in. Each object is now worked out on its own, so every object on such a loop matches everything on it. This made the v2.19.0 CI build fail.

**Changed: README**
- The README now describes Waymark on its own terms: a summary of what stands out, a comparison with Nginx Proxy Manager, Zoraxy, Traefik and Caddy, and a feature list by area (high availability, health checks, load balancing and Layer 4, access control, certificates, caching, operations, security). The list of differences from Zoraxy is gone; Zoraxy is credited in the license section.

## 2.19.0

**New: objects inside objects**
- An object can now contain other objects, as well as addresses and countries: on **Access > Objects**, choose one under **or another object** when adding entries. An ACL rule that uses the outer object matches everything in it, however deeply nested, including the built-in **Private networks**. Changing an inner object changes every rule that uses an object containing it.
- An object cannot contain itself, directly or through others: the editor only offers objects that would not make a loop, and saving refuses one. An object that is inside another cannot be deleted until it is removed from there; the Objects list shows the objects each one is inside next to the ACLs that use it.
- The admin console protection follows nesting too: through Waymark's proxy, a change to an object that would deny your own address on the admin console is refused, whether the ACL uses the object directly or through another object.

**Fixed**
- When adding or editing an ACL rule, changing **Source** did not change the field next to it, so it always showed the address field. It now shows the object or country list, or nothing for **Any address**.

## 2.18.0

**New: ACLs and objects replace access rules**
- **Access > ACLs.** An ACL is an ordered list of rules, checked top down for the client's address. The first matching rule decides; an address no rule matches is denied. Rules can be added, deleted, dragged into a new order, and turned on or off one at a time. Each rule has one source and an action:
  - **Source:** any address, an object, an address, CIDR, range (`10.0.0.10-10.0.0.50`) or IPv4 wildcard (`192.168.*.*`), or a country. **Not** inverts the source ("not US" matches every address that is not in the US).
  - **Action:** **Allow**; **Deny** (HTTP: the 403 page; TCP: the connection is closed; UDP: the packet is ignored); or **Drop** (HTTP/1.1: the connection is closed without a reply; HTTP/2 and HTTP/3: the request stream is reset; TCP: the connection is reset; UDP: the packet is ignored).
- Each proxy rule (HTTP or Layer 4) uses one ACL, chosen in its editor under **Access (ACL)** or when adding the rule. One ACL can be used by many proxy rules. The built-in **Allow all** ACL cannot be edited and is the default for new rules.
- The ACL editor shows how many times each rule matched since Waymark started (per node, not synced), which proxy rules use the ACL, and a test box: enter an address (or leave it empty for your own) to see which rule decides and highlight it. It warns about rules that may not do what they look like, for example a country rule while GeoIP lookups are off (such rules are skipped), or a "not <country>" rule without a rule above it allowing **Private networks** (addresses on your own network have no country, so they match "not <country>").
- **Access > Objects.** An object is a named list of addresses, CIDRs, ranges, wildcards and countries that ACL rules can use as their source; changing it changes every rule that uses it. Entries can be pasted many at a time. The built-in **Private networks** object (RFC 1918, link-local, loopback, IPv6 unique local and link-local, and this server's public address) is read only. The Objects page also lists today's busiest client addresses, with a button to add one to an object.
- Denied and dropped HTTP requests are in the access log with the ACL and rule that refused them (`acl-deny` with status 403, `acl-drop` with 444). Refused Layer 4 connections are written to the system log, summarized: one line per address and rule per minute, with a count. Allowed Layer 4 connections are not logged.
- **Trusting forwarded client addresses is now set per proxy rule**, not per access rule: **Trust the client address forwarded by trusted proxies** in the rule's access settings (and when adding it). When on, and the request comes from an address on **Access > Trusted Proxies**, the ACL, rate limiting and logs use the address it forwards.
- The admin console protection still applies: through Waymark's proxy, a change to an ACL or object, or attaching an ACL to the admin console rule, that would refuse your own address is refused. `-reset-admin-rule` now sets the admin console rule's ACL to Allow all.
- ACLs are stored in `conf/acl/` (one file per ACL, and `objects.json`) and synced to HA standbys. Edits made by two people at once are caught: the second save is refused and asks you to reload.
- Plugins can read ACLs and objects (`/api/acl/list`, `/api/acl/get`, `/api/acl/objects`); the old access rule APIs are gone.

**Upgrading: access rules are converted on first start**
- Each access rule becomes an ACL with the same name, so proxy rules keep using it: its blocklist and allowlist become objects (`<rule> blocklist`, `<rule> allowlist`), and its rules are, in order: deny the blocklist; allow Private networks (if it allowed LAN addresses); allow the allowlist, or allow any address if it had no allowlist. A rule that allowed everything becomes Allow all. The conversion never refuses an address the old rule allowed.
- Proxy rules whose access rule trusted forwarded addresses get the new per-rule setting turned on.
- `conf/access/` is deleted after the conversion. Rolling back to 2.17.x would lose the access rules; restore a backup made before the update instead.
- With HA, update every node: a standby running 2.17.x does not have the new ACLs.

## 2.17.2

**Changed: getting a certificate for a new proxy rule**
- After adding an HTTP rule with TLS, the prompt now reads "Proxy rule added. Get a TLS certificate for …?". Clicking the check opens **Certificates > ACME and Auto-Renew** with the ACME tool filled in for the rule's names, so you choose the CA, HTTP or DNS challenge and DNS credential set there. Until now it requested a certificate straight away with the HTTP challenge, which failed for names that need the DNS challenge or a particular credential set.
- **Get Certificate** on the TLS / SSL page of the rule editor does the same (it closes the editor). It now also works for rules with wildcard names, which it used to disable.
- When the names include a wildcard, the ACME tool starts with **Use a DNS Challenge** ticked. With several names, the matching rule (certificate file name) starts as the first name; **Auto Detect Matching Rule** still suggests a common suffix.

## 2.17.1

**Fixed: ACME account keys are back in sys.db**
- ACME account keys were stored with the ACME preferences, so when 2.15.0 moved those to `conf/acme_prefs.json`, the keys went with them, into a file readable by Waymark's group. They are now kept in sys.db (with the EAB credentials), readable only by Waymark's user, and `acme_prefs.json` holds only the preferred certificate authority. Existing account keys are moved on first start.

**Removed: leftovers and code for upgrading from older versions**
- On first start, sys.db loses the copies of the settings moved to `conf/` in 2.15.0 and 2.16.0 (kept until now for a roll back), the `authelia` and `authentik` tables of old Zoraxy versions, and the values only used to move settings. `conf/version` and an empty `conf/streamproxy` folder are deleted.
- The code that converted older configurations is gone: Zoraxy's config migrations (`mod/update`, which also wrote `conf/version`), copying settings from sys.db to `conf/`, adopting a hand-made admin console rule, converting the uptime monitor setting to health checks, moving `conf/streamproxy` to `conf/l4proxy`, and the Layer 4 load balancing modes of 2.9.1 and earlier. The `-cfgupgrade` flag is accepted but does nothing.
- **Upgrading:** from 2.17.0 only. Rolling back to a version before 2.17.0, or restoring a backup made before 2.17.0, is no longer supported: those versions read settings that are now only in `conf/`.
- Kept on purpose: checking passwords stored with the unsalted SHA-512 digest used before 2.2.2. Admin and Waymark Auth passwords are upgraded when you sign in, but basic auth credentials on proxy rules and the WebDAV password are only upgraded when you set them again, and a stored digest cannot be converted without the password.

## 2.17.0

**New: the admin console rule**
- Waymark now creates and maintains the proxy rule for its own admin console. On first start it answers on this machine's address and DNS name (best guesses; editable), sends requests to the admin console's listener (`-port`), uses TLS with a self-signed certificate covering every host of the rule, has caching on and health checks off.
- With HA on, the rule answers on every floating IP and node address and their DNS names. The HA settings have a new **DNS name** field for each floating IP and node, pre-filled with a reverse DNS guess. The rule's main domain is the primary floating IP's DNS name (or address) until you change it. Adding or removing a node or floating IP adds or removes its hosts; hosts you added yourself stay. The rule is synced to the standbys like the other rules.
- A node that is not in a cluster yet has its own rule; when it joins, the cluster's rule (which by then includes it) replaces it.
- An existing rule whose only destination is the admin console is adopted: it keeps its main domain, aliases, certificate choice, access rule, caching, logging and tags, and gets the required hosts. Settings that could cut off the admin console are reset: it is enabled, listens on every address, and its authentication, virtual directories, custom headers, health checks and maintenance mode are removed. Preferred certificates for hosts the rule doesn't have are dropped.
- The rule cannot be disabled or deleted, and its destination, authentication, virtual directories, headers, health checks, maintenance mode, listening address and extra listening ports cannot be changed. The required hosts cannot be removed, and no other rule can take one of its hosts. Its main domain, extra aliases, certificate, access rule, caching, logging and tags can be changed. The Proxy Rules list marks it **Admin console**, and its editor shows only what can be changed.
- When you use the admin console through Waymark's proxy, an access rule change that would block your own address from the admin console rule is refused: attaching such a rule, or turning on or editing the allowlist or blocklist of the rule it uses.
- `-reset-admin-rule` rebuilds the rule (required hosts, destination, a new self-signed certificate, the default access rule) and exits; restart Waymark to load it.
- A client that connects to an IP address without SNI now gets the certificate for that address when one covers it (the self-signed certificate does), instead of the fallback certificate.

**Changed: no more "loopback" warnings**
- The global settings (listening port, TLS, port 80, HTTPS redirect, minimum TLS version, QUIC) are no longer locked when you use the admin console through Waymark's proxy. The admin console rule keeps working through these changes; a new port or plain HTTP just changes the address you use.
- Turning on PROXY protocol asks for confirmation when you use the admin console through Waymark's proxy, because connections without a PROXY header (yours) are then refused.
- Stopping the proxy from the Status page is still refused through Waymark's proxy, with a clearer message pointing to the admin console's own port.

## 2.16.0

**Fixed: an updated node could roll itself back after a rolling update**
- After an update, a node counts its starts and puts the previous version back if the new one is started three times without staying up for 2 minutes. Restarts to load the configuration from the active node were counted too, so a standby that copied the configuration a few times right after updating rolled itself back, after the rolling update had already reported success. Restarts Waymark does itself are no longer counted.
- An update request that reaches a node while it is restarting to load the configuration is now refused, and the rolling update asks again (for up to a minute). Before, the node could accept the request and then restart without installing, which stopped the rolling update.
- A rolling update now starts only when every standby has the active node's latest configuration. Before, a change made just before starting could be lost: a standby that had not copied it yet became the active node during the update and handed its older configuration back.
- A configuration copied from a node running another version is now reported as such ("it is from Waymark 2.15.0") instead of as an unexpected table or file.

**Changed: SSO and HA settings moved from sys.db to files in `conf/`**
- `sso/oauth2.json`: OAuth2 / OIDC provider (holds the client secret, so only Waymark's user can read it)
- `sso/forward.json`: forward auth server
- `sso/zorxauth/options.json`: Waymark Auth gateway options
- `ha.json`: HA cluster settings (holds the cluster key and VRRP password; only Waymark's user can read it)
- **Upgrading:** nothing to do. On first start the settings are copied from sys.db into the files. HA configuration sync copies them like the other files in `conf/`.
- The copy in sys.db is now left in place (for these and for the settings moved in 2.15.0, where sys.db still had them), so if an update is rolled back to an earlier version, that version still finds its settings, as they were when the update was installed. 2.15.0 removed its rows from sys.db after copying them; a node rolled back from 2.15.0 to 2.14.1 used default settings until it was updated again.
- A record in sys.db notes what has been copied, so each setting is copied once. Restoring a backup from an earlier version copies its settings again.
- Waymark Auth users, passkeys and sessions stay in sys.db.

## 2.15.0

**Changed: settings moved from sys.db to files in `conf/`**
- These settings are now JSON files in the config folder, which you can read, compare and back up like the rest of the configuration:
  - `listener.json`: listening port, TLS on or off and minimum version, HTTP/2, QUIC, proxy protocol, timeouts, listening on port 80, HTTPS redirect, development mode
  - `system.json`: Geo-IP lookups on or off, asset cache memory limit
  - `acme_prefs.json`: preferred certificate authority
  - `redirect_options.json`: regular expressions and case sensitivity for redirection rules
  - `webserv.json`: static web server and WebDAV (holds the WebDAV password hash, so only Waymark's user can read it)
  - `ui.json`: status page panels
  - `plugins.json`: plugins turned on and plugin store sources
- **Upgrading:** nothing to do. On first start the settings are moved out of sys.db into the files. The same happens when restoring a backup from an earlier version.
- A file edited by hand is picked up the next time the setting is read; most take effect after a restart. If a file isn't valid JSON, Waymark refuses to start and says which file to fix, rather than starting with default settings.
- HA configuration sync copies these files from the active node, except `plugins.json`, which each node keeps for itself as before.
- sys.db now holds secrets and accounts (admin accounts, Waymark Auth users, ACME accounts and DNS credentials, the sticky session key), SSO and HA settings (to move in a later release), and per-node state (statistics, counters, sessions).

## 2.14.1

**Fixed: configuration files survive a crash or power cut**
- Every configuration and state file Waymark writes is now replaced safely: written to a temporary file in the same folder, flushed to disk, renamed over the old file, and the folder flushed. A crash or power cut during a save leaves the old file or the new one, never a truncated or empty one. Until now, proxy rules, access rules, trusted proxies, redirects, path rules, ACME settings, log rotation settings, plugin groups and Waymark Auth policies were overwritten in place.
- Covers: HTTP and Layer 4 proxy rules, certificates and keys (uploaded and from ACME), the fallback certificate, access rules, trusted proxies, redirects, path rules, ACME settings and certificate details, Waymark Auth group policies and branding, plugin groups, log rotation and log output settings, the host UUID, the downloaded Geo-IP database, the traffic capture index, files applied by HA configuration sync, and the HA and update state files.
- New files get exactly the permissions intended (configuration 0644, Layer 4 rules and path rules 0640, private keys 0600) instead of 0775 reduced by the umask.

## 2.14.0

**Changed: menus and page titles**
- Browser tabs read "Waymark | <page>" (for example "Waymark | Proxy Rules").
- **Certificates** has three pages instead of tabs: **Installed Certificates**, **ACME and Auto-Renew** and **DNS Credentials** (which was a side panel). A certificate's own credential set is still chosen with its key button on Installed Certificates.
- Access rules have a raised-hand icon, in the menu (Access > Access Rules), under each rule in the Proxy Rules list (hover for the rule's name and whether it is an allowlist or a blocklist), in the rule editors and on the Access Rules page. The colours still say which lists are on (green allowlist, red blocklist, yellow both).

**Changed: logs**
- **System > Logging** has the log settings: where system and access logs go (log file and syslog servers) and log file rotation. "Traffic" logs are called access logs in the interface.
- **Monitoring > Access Logs** shows the access log lines of the log file as a table, newest first: time, status, method, host, path, client and browser. **System > System Logs** shows system messages the same way: time, level, module and message. Both have a log file picker (rotated files too), how many lines to read, a status or level filter, search, refresh every 5 seconds, and download. When a log isn't written to the file, the page says so and where it goes instead.
- The separate log viewer page (with its hit charts) is gone, with its `/api/log/summary` and `/api/log/errors` API calls.

**Changed: Proxy Rules list**
- The cache hit rate in the Details column is for all time: counters are kept across restarts and purges, per node, in `conf/assetcache_totals.json` (not copied by HA configuration sync). The hover text has the counts and what is in memory now. Counters of a removed rule are dropped.

**Fixed**
- A request whose destination failed was counted twice, in the access log and in the statistics: once as the error (521) and again as a 502.

## 2.13.3

**Changed: System menu**
- **System > Admin Account** is its own page (was the Accounts tab of Settings): change the password of the account you are signed in with.
- **System > Settings** is a single page without tabs: Geo-IP lookups, Geo-IP database updates, backup and restore, and statistics.
- The System menu reads Listeners, High Availability, Static Web Server, Settings, Admin Account, About.
- The statistics section no longer says it can export statistics (it only resets them).

## 2.13.2

The same changes as the v2.13.1 tag, which was pushed while the version in the code still said 2.13.0 (so CI could not build it).

**Changed: About is its own page**
- **System > About** (was a tab of System > Settings) holds the updates, this host's details, and the license: Waymark's and Zoraxy's copyright, the AGPL v3 terms, no warranty, and links to the source code and license text. The update pill next to the logo opens it.
- The page footer is now one line: the version and a link to About. The sign-in page keeps a short license line (About needs a sign-in) with a link that opens About after signing in.

## 2.13.0

**Added: an HTTP rule can answer on one floating IP**
- **Listen On** in the rule editor (and in Add Proxy Rule, under Advanced Settings): **Any address** (the default and the behaviour so far) or one floating IP of the HA cluster. With high availability off, this host's own addresses are offered instead.
- A request that arrives on another address is handled as if the rule did not exist: another matching rule answers (for example a wildcard rule bound to that address), or the default site. This applies to the main HTTP(S) listener and the port 80 listener; a rule's additional listening addresses are used as written.
- This routes requests; it does not restrict who can connect. Use an access rule for that. Over HTTP/3, whose listener does not report which address a request arrived on, a bound rule still answers.
- The Proxy Rules list shows the address under **Listens On**, with the floating IP's label, and an orange warning when the address is no longer a floating IP (or an address of this host).

**Changed: Proxy Rules list**
- Health is shown the same way for HTTP and Layer 4 rules. Each destination has an icon in the Destination column: a green heart when healthy, a red heartbeat when down (hover for the reason), a grey outline heart until the first result, a grey circle when the rule has no health checks, and a grey ring when the rule is off. The Health / Balancing column's heartbeat sums it up: green when every destination is healthy, red with "x of n down", grey when the rule is off. Layer 4 rules used to show a grey heartbeat there whatever the state, and HTTP rules used to show a red heart only next to destinations that were down, plus a second one next to the host when all were down (that note is now in the heartbeat's tooltip).
- Caching moved to the **Details** column and is always shown for HTTP rules: **Caching off**, or **Caching** with the hit rate and memory used since Waymark started (hover for the counts).

**Fixed**
- Add Proxy Rule's **Advanced Settings** sometimes didn't open: the page shared an element ID with Virtual Directories.

**API**
- `POST /api/proxy/listenip/set` (`ep`, `ip`; empty for every address), `GET /api/proxy/listenip/options`; `/api/proxy/add` takes `listenip`. Rules have a `ListenIP` field.
- `GET /api/proxy/assetcache/stats` returns every rule's cache counters and hit rate.

## 2.12.0

HTTP and Layer 4 (TCP/UDP) proxy rules are now on one page. Nothing about the rules themselves changes: they are stored and edited as before.

**Changed: one Proxy Rules page**
- **Proxy > Proxy Rules** replaces HTTP Proxies and Layer 4 Proxies. Every rule is in one list, in name order, with the same columns: Type (HTTP, TCP, UDP or TCP + UDP), Rule, Listens On (the shared HTTP(S) port for HTTP rules, the rule's own address for Layer 4), Destination, Health / Balancing, Details, and Tags.
- **All / HTTP / TCP / UDP** buttons pick which rules are shown. The page always opens on All. The tag filter and quick search work across both kinds (Layer 4 rules have no tags, so choosing a tag shows HTTP rules only).
- Details shows authentication and virtual directories for HTTP rules, and Proxy Protocol, UDP idle timeout and logging for Layer 4 rules. HTTP rules' additional listening addresses are now under Listens On.
- Each rule opens its own editor, as before.
- The Layer 4 list's Running / Stopped / Destination down filter is gone; the start/stop slider and red hearts show the same at a glance.

**Changed: one Add Proxy Rule page**
- **Add Proxy Rule** asks for the type first: **HTTP** (websites and web apps, by host name) or **TCP / UDP** (anything else, on its own port). Then it shows that type's fields, the same as before. Opened while the list shows TCP or UDP rules, it starts on TCP / UDP.
- A rule's type can't be changed after it is created.

**Bookmarks:** links to the old pages (`#l4proxy`, `#l4rules`, `#streamproxy`) open the new ones.

## 2.11.0

An HA cluster can now be updated with one button: **Rolling update** installs the latest release on every node, one node at a time, and moves the floating IPs to an updated node before updating the one that was active.

**Added: rolling update**
- On **System > High Availability** (Cluster), **Rolling update to x.y.z** appears when a node runs an older version than the latest release; **System > Settings > About** has **Rolling update of all nodes** too. It can be started on any node; the active node runs it.
- It checks the cluster first: every node must answer, only one may be active, and every standby must be healthy, running keepalived and in sync.
- The standby nodes are updated first, one at a time, lowest priority first. Each must come back on the new version, healthy and as a standby, and stay that way for 20 seconds before the next one starts.
- Then the updated standby with the highest priority is made active (as with **Make active**), and the node that was active is updated last and restarts as a standby. With "move the floating IP back" (preempt) on, it keeps reporting itself unhealthy until it runs the new version, so the floating IPs don't move back to it early; they move back once it is updated.
- It stops at the first problem (an update that fails or is rolled back, a node that doesn't come back within 12 minutes, a standby that becomes active, a switch-over that doesn't happen), and the node that was active is only updated once every other node runs the new version and has taken over. Start it again after fixing the problem: nodes that already run the new version are skipped.
- The HA page shows each step and its progress on every node, including while the node running it restarts. **Make active**, the per-node **Update** buttons, **Install** and **Roll back** are unavailable while it runs.
- The per-node **Update** buttons are still there for updating nodes by hand.

**Changed**
- A standby that is installing an update waits until the update is installed (or has failed) before it restarts to load configuration copied from the active node, so the restart can no longer cut a download short.
- A page whose node now runs another version (after an update, a roll back or a rolling update) reloads itself.
- Failed-update messages in the cluster table wrap instead of running off the page.

**Upgrading to 2.11.0:** the node running a rolling update must run 2.11.0 or later, so update to 2.11.0 node by node as before (README, Upgrading). From then on, use **Rolling update**.

**API**
- `POST /api/update/rolling` (`version`) starts a rolling update; `GET /api/update/rolling` returns its progress, which `/api/ha/status` and `/api/update/status` also include (`rolling`).

## 2.10.0

Load balancing now works the same way for HTTP and Layer 4 proxy rules, with the same settings in the same places. Existing Layer 4 rules are converted automatically on the first start; no action is needed.

**Changed: Layer 4 rules**
- **Edit Destinations** looks and works like the HTTP one: each destination has an on/off toggle and a **weight** (0-100). Weight 0 makes a destination a backup, used only when no destination with a higher weight is healthy. The arrows for reordering destinations are gone, since the order no longer matters.
- **Load Balancing** offers **Weighted** (the default: a random healthy destination in proportion to its weight, as for HTTP) or **Least connections** (the fewest open connections for its weight).
- **Use Sticky Sessions** is its own checkbox, as for HTTP: a client IP address keeps going to the destination it was given while that destination is healthy, and is remembered for a day after its last connection.
- Rules from earlier versions are converted: round robin becomes equal weights; failover becomes the first destination at weight 1 and the others as backups (weight 0); source IP hash becomes equal weights with sticky sessions; least connections stays.
- The Add Layer 4 Rule page has the **Use Sticky Sessions** option, like Add Proxy Rule.
- The list's Health / Balancing column shows the method and "Sticky", as the HTTP list now does.

**Added: HTTP proxy rules**
- **Load Balancing** in the rule editor (Destinations): **Weighted** (the behaviour so far) or **Least connections**, which sends each request to the destination with the fewest requests in progress for its weight.
- The HTTP Proxies list shows the load balancing method (with more than one destination) and "Sticky" in the Health / Cache column.

**Changed: wording**
- "Fallback" destinations are called backups everywhere, and the sticky session options read the same on both kinds of rule.

**API**
- `/api/l4proxy/config/pool` takes `destinations` (`address`, `weight`, `enabled`), `loadBalancing` (`weighted` or `leastconn`) and `stickySessions`. A plain `upstreams` address list is still accepted (each destination with weight 1).
- `/api/proxy/edit` takes `lbmode` (`weighted` or `leastconn`).

## 2.9.1

A terminology clean-up release: the same thing now has the same name everywhere in the web interface, the logs and the documentation. No configuration changes are needed.

**Changed: names**
- **Destinations:** the servers a proxy rule forwards to are called destinations everywhere (they were also called upstreams, upstream origins, upstream endpoints, origin servers and proxy targets). This covers the HTTP rule editor, **Edit Destinations**, Add Proxy Rule, Default Site, Virtual Directories, Health Checks, Health & Uptime, Statistics, the Status page, Traffic Capture and the log messages. A proxy in front of Waymark is now called that, instead of an "upstream proxy".
- **Rules:** an HTTP proxy entry is a **proxy rule** (no longer also an "endpoint" or "proxy host"); a Layer 4 entry is a **Layer 4 rule**. The buttons are **Create Proxy Rule**, **Add Layer 4 Rule** and **Create Layer 4 Rule**.
- **Additional listening addresses:** a rule's extra listeners have one name in the HTTP Proxies list, the rule editor and the Listeners page (were "Alt. Listening Addr", "Alternative Listening Ports" and "Secondary Listening Ports"). The Listeners page's port is the **Main Listening Port**.
- **Layer 4 UDP idle timeout:** the Layer 4 "Timeout" setting is now called **UDP Idle Timeout**, which is what it does: how long a UDP client session is kept after its last packet. It is only shown for rules with UDP on, and the list shows "—" for TCP-only rules.
- Pop-up messages use sentence case throughout, collapsible sections are all called **Advanced Settings**, and a few labels match their page names (**Access Rules** headings on the Add pages, **Virtual Directories** in the HTTP editor, **DNS Credentials** and **ACME and Auto-Renew** on the Certificates pages).
- Help text that pointed at places that don't exist now points at the right menu entry or tab (for example **Destinations > Health Checks**, **HTTP Proxies**, **System > Settings > About**), and the last mentions of the old uptime monitor say health checks.
- The two notes about which certificate authorities auto-renew works with are now the same sentence, without sending you to the Zoraxy wiki.
- README: the system and traffic logs are called logs instead of streams.

**Changed: logs**
- Log module names (the `[module]` in the log file, and the `module` field sent to syslog) are lowercase and hyphenated, with duplicates merged: `acme` (was `ACME` and `cert-renew`), `load-balancer` (`LoadBalancer`, `loadbalancer`), `http-proxy` (`dprouter`, `proxy`), `access` (`Access`, `proxy-access`), `oauth2` (`OAuth2`, `OAuth2Router`), `tls` (`TLS`, `tls-router`), `geodb` (`GeoDB`), `netstat` (`Network`), `proxy-config` (`INFO`, `Config`), `backup` (`Backup`), `uptime` (`uptime-monitor`), `forward-auth` (`Forward Auth`), `waymark-auth` (`zorxauth`) and `system` (`ZeroTier`, `warning`). **Update syslog or SIEM searches that filter on the old names.**
- Health check log lines name the rule instead of its internal ID, for example `Destination 10.0.0.5:5432 of rule Layer 4: Postgres DISABLED: ...` (was `Upstream 10.0.0.5:5432 (Layer 4 proxy Postgres) on rule l4:<uuid> DISABLED`). Layer 4 rules are shown as "Layer 4: name" everywhere.
- The JSON field names sent to syslog (`upstream`, `stream`) are unchanged, so collectors keep parsing them.

**Removed**
- The Docker container picker on Add Proxy Rule, the `/api/docker/...` API and the `-docker` start flag. Waymark doesn't ship a Docker image. **If a service file or script starts Waymark with `-docker`, remove the flag, or Waymark won't start.**

**Fixed**
- Layer 4 rules: the Health Checks panel listed every destination as "Not health checked" even while it was being checked.
- Browser settings (collapsed menu groups and the traffic diagram orientation) are stored under `waymark_` names instead of `zoraxy_`. Existing settings are moved over on the first visit.

## 2.9.0

Stream proxies are now called **Layer 4 proxies**, and their pages work the same way as the HTTP proxy pages. Existing rules are kept and moved automatically; no action is needed.

**Changed: Layer 4 proxies (formerly stream proxies)**
- Renamed everywhere: the menu entry (**Proxy > Layer 4 Proxies**), the pages, Traffic Capture, High Availability, Health & Uptime, the logs (module `l4-proxy`) and the documentation. The servers a proxy forwards to are called **destinations**, as on the HTTP Proxies page.
- **Add Layer 4 Proxy** is its own page, like **Add Proxy Rule**: name, listening address, destination, TCP / UDP, and advanced settings (health checks, timeout, PROXY protocol, access rule, logging). A new proxy starts straight away, like a new HTTP proxy rule, with a TCP connect health check unless that is turned off.
- The editor is laid out like the HTTP proxy rule editor, and each setting is saved as soon as it is changed (there is no Save button any more):
  - **Listener:** name, listening address, TCP / UDP, timeout and connection logging.
  - **Destinations:** the destination list with **Edit Destinations** and **Health Checks** side panels, load balancing, closing connections when a destination goes down, and PROXY protocol.
  - **Access Rules:** the same access rule picker as HTTP proxy rules.
- **Edit Destinations** adds, changes, removes and reorders destinations (the order matters for failover), and shows each one's health and connection counts.
- **Health Checks** is the same panel as for HTTP proxy rules, with the Layer 4 check types (TCP connect, TCP send and expect, UDP send and expect, ping), live destination status and recent changes.
- Turning TCP or UDP off is refused while the proxy's health check uses that protocol.
- Old `#streamproxy` bookmarks open the Layer 4 Proxies page.

**Changed: configuration and API**
- Rule files move from `conf/streamproxy` to `conf/l4proxy` on the first start. A folder restored from an older backup is moved the same way.
- The API is under `/api/l4proxy/config/...`; the old `/api/streamprox/config/...` paths still work for scripts and plugins. New: `/api/l4proxy/healthcheck/get` and `/set`.
- Traffic capture settings use the scope `l4` and the field `l4Rules` (were `stream` and `streamRules`). Captures made before the update are still listed.
- The HA status reports `l4Proxies` (was `streamProxies`).
- The Go package is `mod/l4proxy` (was `mod/streamproxy`).

**Fixed**
- With file logging turned off for both the system and traffic logs, Waymark no longer creates an empty log file (`logs/waymark_<year>-<month>.log`) at start. The file is created when the first line is written to it, and log rotation no longer creates it either.
- HTTP proxy rule editor: the health check summary showed `&amp;` instead of `&`.

## 2.8.2

The v2.8.1 tag was pushed without the version number updated, so its build was refused; its changes are in this release.

**Security**
- Path blocking rules: the rule ID given to the remove API was used as a file name without checking it. A well-formed UUID is now required before Waymark reads or deletes a rule file (GitHub code scanning: "uncontrolled data used in path expression"). The API needs an admin login and only removes rules that exist, so this was defence in depth. Rule files are written `0640` instead of `0775`.
- Traffic capture: the packet writer no longer adds input lengths to work out buffer sizes; buffers grow as data is appended (GitHub code scanning: "size computation for allocation may overflow", still reported after 2.7.1's size limits).

## 2.8.0

**Added**
- HTTPS upstreams: each upstream server has a **Check certificate name against** setting (shown when Require TLS is on). It chooses which name the upstream's certificate is checked against, and sends that name as SNI:
  - **Automatic** (the default, the behaviour so far): the "Host" header override if set, otherwise the name in the upstream address. For an upstream addressed by IP, the host name the client asked for.
  - **Upstream server name**: the host name or IP address in the upstream address, whatever the client asked for and whatever the Host override says.
  - **Proxy host name**: the host name the client asked Waymark for (the rule's host or one of its aliases).
- **Skip Verification** still turns certificate checking off; the chosen name is then only sent as SNI.
- HTTP health checks on such upstreams use the same name. For **Proxy host name** that is the rule's host name, unless it is a wildcard.

**Faster**
- Requests that need their own TLS server name (IP-addressed upstreams, Proxy host name) or HTTP/1.1 (WebSocket and other upgrades, Force HTTP/1.1) reuse connections. A copy of the connection pool was made for every such request before; now one is kept per name (up to 64 per upstream).

## 2.7.1

**Changed: web interface**
- The Stream Proxies page now looks like the HTTP Proxies page:
  - a slider in the first column starts and stops each rule;
  - a filter (running, stopped, TCP, UDP, upstream down) and a quick search above the table;
  - sortable columns: Name, Listening Address, Upstreams, Balancing / Health, Protocol and Timeout;
  - the edit (⋮) and delete buttons at the end of each row.
- Rules with an access rule other than the default show its name under the rule name.
- Deleting a stream proxy asks for confirmation, and a running rule is stopped first instead of the delete being refused.
- HTTP Proxies: access rule names under each host are escaped.

**Fixed**
- Traffic capture: the pcapng writer now checks sizes before building packets and file blocks (GitHub code scanning: "size computation for allocation may overflow"). Data longer than an IP packet can hold is cut, instead of writing length fields that wrapped around. Capture never produced data that large (TCP data is written in 16 KB pieces, UDP packets are at most 1500 bytes), so existing capture files are not affected.

## 2.7.0

A code review release: bug fixes found with the race detector, under load with live configuration changes, and by reading the code; unused code removed; some background work made cheaper; and web interface improvements. No configuration changes are needed.

**Changed: web interface**
- Stream Proxies: rules are added and edited in a pop-up editor like the HTTP proxy rule editor (Listener, Upstreams, Health Checks, Access & Protocol), opened with **Add Stream Proxy** or a rule's edit button, instead of a form at the bottom of the page. Escape or clicking outside closes it. New rules start with TCP on.
- HTTP Proxies: the Basics / Details view switch is gone; the list always shows every column (including alternative listening addresses and authentication) and scrolls sideways when it doesn't fit.
- Certificates: the list of installed certificates comes first; uploading or pasting a certificate is under **Add a certificate** below it.
- The HTTP Proxies and Stream Proxies lists refresh themselves (every 15 and 10 seconds) while they are on screen. They don't refresh while an editor, side panel or menu is open or the HTTP list is sorted by a column; health indicators still update then. The Listeners page's secondary ports list now only refreshes while it is on screen.
- Phones: pages no longer scroll sideways. The content area left room for its margins, tab menus scroll within themselves, and the certificate overview on the status page and the capture file list fit the screen.
- Listeners: secondary listening addresses and host names are escaped.

**Fixed**
- Stream proxies: starting, stopping, editing and listing rules at the same time (for example the web UI refreshing while a rule restarts) touched the same settings from several threads without locking. Rule settings and running state are now locked, and the config files and the list API read a consistent copy.
- Stream proxies: stopping a rule whose UDP listener had failed to start (or whose TCP listener had failed while UDP kept running) could hang the stop request forever.
- Stream proxies: editing a running rule paused for 3 seconds before restarting it. The rule now restarts as soon as its listeners have closed.
- Stream proxies: rules with neither TCP nor UDP enabled, with an unusable listening address, or listening on the same address and protocol as another rule are refused when added or edited, with a message saying why.
- Stream proxy config files are written one at a time and replaced atomically, so two saves at once (or a crash during a save) can't leave a truncated file. They are no longer marked executable (now `0640`).
- Stream proxies: UDP replies are counted in the transferred bytes, the UDP byte counter is updated atomically, and a log line is no longer written for every UDP packet when logging is on.
- Request statistics: the daily counters were updated from many requests at once without synchronisation, so counts could be lost. They are now updated atomically.
- The Uptime Monitor was created in the background at startup while health checks and the API could already use it. It is now created before them.
- Traffic capture: the summary logged when a capture stops read its counters without the lock.
- Logs: writing, the monthly file change and size-based rotation could run at the same time, so lines could go to a closed file, and a failed rotation stopped file logging until restart. They now take turns, and a failed rotation keeps logging to the current file. Two rotations within one second no longer overwrite each other's backup. After the monthly file change, lines no longer carry a second timestamp. New log files are `0640` instead of `0755`.
- HTTP proxy rules were not found for requests to an IPv6 address (`[fd00::1]:443` was read as host `[`).
- Status page: the world map logged script errors while another tab was open (it was resized while hidden).
- The plugin page's frame had an invalid `allow` attribute.

**Faster**
- Proxied HTTP responses reuse their 64 KB copy buffers instead of allocating one per response (this was about 70% of all memory allocated under load).
- Memory usage for the status page is read from the kernel every second instead of starting a shell running `free` and `grep`.
- Waymark no longer runs `df`, `lsusb` and `ip` every minute to collect host hardware information nothing displayed.
- An idle UDP stream proxy no longer wakes up every 100 ms to check whether it should stop.

**Removed**
- APIs the web UI no longer used: `/api/sysinfo/*` (host hardware information), `/api/info/pprof` (a profiling index whose links led nowhere) and, from the main API, `/api/proxy/upstream/setPriority` and `/api/stats/listnic` (both stay available to plugins).
- Unused code: the deprecated Authelia and Authentik packages, and the unused `email`, `expose`, `reverseproxy`, `modh2c` and `hardwareinfo` packages, plus about 50 unused functions and types elsewhere.
- Unused web files: the old plugin store page (replaced by the App Store tab), an unused date picker theme, unused UI functions, and Zoraxy's original artwork sources (`.ai`, `.psd`), which were built into the binary.
- The stream proxy test that listened on port 8080 for 20 seconds and connected to a host on the internet, replaced by tests on the loopback interface.

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
