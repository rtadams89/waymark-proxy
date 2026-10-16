# Custom fork changelog

Changes in this fork on top of upstream [tobychui/zoraxy](https://github.com/tobychui/zoraxy).

**Versioning:** `<upstream base>-custom.<revision>`, for example `3.3.5-custom.1`.

- The revision goes up by one for every change that ships, and resets to 1 after rebasing onto a new upstream release (`3.3.6-custom.1`).
- The revision lives in `src/version_custom.go` (`FORK_REVISION`). `SYSTEM_VERSION` in `src/def.go` stays the plain upstream version, because Zoraxy's config migrations parse it as a number.
- Release tags are `v<version>` (for example `v3.3.5-custom.1`). CI refuses to publish a release whose tag doesn't match the version built into the binary.

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
