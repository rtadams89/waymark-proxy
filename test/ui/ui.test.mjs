// Browser tests of the Waymark admin UI.
//
// Run against a fresh instance (no admin account yet), e.g.
//   ./waymark -port :8000 -mdns=false -default_inbound_port 8443
//   BASE_URL=http://127.0.0.1:8000 npm test
//
// The first test creates the admin account on the sign-in page (ADMIN_USER,
// ADMIN_PASSWORD; on an instance that has one already, give its
// credentials). The tests create what they need (rules, an ACL, a trusted
// proxy) and remove it again. Any page error, native browser dialog or
// Content-Security-Policy violation fails the run.

import { createRequire } from "node:module";
const require = createRequire(import.meta.url);
const { chromium } = require("playwright");

const BASE = process.env.BASE_URL || "http://127.0.0.1:8000";
const ADMIN_USER = process.env.ADMIN_USER || "uitest";
const ADMIN_PASSWORD = process.env.ADMIN_PASSWORD || "ui-test-password-1";
const results = [];
let page, browser;
const pageErrors = [];

function assert(cond, msg) {
    if (!cond) throw new Error(msg);
}

async function test(name, fn) {
    const before = pageErrors.length;
    try {
        await fn();
        assert(pageErrors.length == before, "page error: " + pageErrors.slice(before).join("; "));
        results.push({ name, ok: true });
        console.log("ok   " + name);
    } catch (e) {
        results.push({ name, ok: false, error: e.message });
        console.log("FAIL " + name + "\n     " + e.message);
        await page.screenshot({ path: "failure-" + name.replace(/\W+/g, "_") + ".png", fullPage: true }).catch(() => {});
    }
}

const ev = (fn, arg) => page.evaluate(fn, arg);
const sleep = ms => page.waitForTimeout(ms);
const dialogShown = () => ev(() => $("#wmDialog").is(":visible"));
const dialogButton = i => ev(i => $("#wmDialog .wmDialogButtons button").eq(i).click(), i);
const keepEditing = () => dialogButton(0);
const discard = () => dialogButton(1);
const currentTab = () => ev(() => $(".functiontab:visible").attr("id"));

async function openTab(id) {
    await ev(id => openTabById(id), id);
    await page.waitForFunction(id => $("#" + id).is(":visible"), id, { timeout: 10000 });
    await sleep(800);
}

// Calls an admin API from the page (with the page's CSRF token)
function api(url, data, method = "POST") {
    return ev(([url, data, method]) => new Promise(resolve => {
        $.cjax({ url, method, data, success: d => resolve(d), error: x => resolve({ error: "HTTP " + x.status }) });
    }), [url, data, method]);
}

async function main() {
    browser = await chromium.launch({ args: ["--no-proxy-server"] });
    page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
    page.on("pageerror", e => pageErrors.push(e.message));
    page.on("dialog", d => { pageErrors.push("native " + d.type() + " dialog: " + d.message()); d.dismiss(); });
    page.on("console", m => { if (/Content.Security.Policy/i.test(m.text())) pageErrors.push("CSP: " + m.text()); });
    // Violations in frames too
    await page.addInitScript(() => document.addEventListener("securitypolicyviolation", e => console.error("Content-Security-Policy violation: " + e.violatedDirective + " " + e.blockedURI)));

    await test("sign-in page: creates the admin account, shows errors under the form, signs in", async () => {
        await page.goto(BASE + "/", { waitUntil: "networkidle" });
        await sleep(1000);
        assert(page.url().includes("login"), "the console opened without signing in: " + page.url());
        const register = await page.evaluate(() => $("#regsiterbtn").is(":visible"));
        if (register) {
            await page.fill("#username", ADMIN_USER);
            await page.fill("#magic", ADMIN_PASSWORD);
            await page.fill("#repeatMagic", ADMIN_PASSWORD + "x");
            await page.click("#regsiterbtn");
            await sleep(500);
            assert(await page.evaluate(() => $("#errmsg").is(":visible") && $("#errmsg").text().includes("do not match")), "a password mismatch is not shown under the form");
            await page.fill("#repeatMagic", ADMIN_PASSWORD);
            await page.click("#regsiterbtn");
            await page.waitForLoadState("networkidle");
            await sleep(1500);
        }
        await page.fill("#username", ADMIN_USER);
        await page.fill("#magic", ADMIN_PASSWORD + "-wrong");
        await page.click("#loginbtn");
        await sleep(1200);
        assert(await page.evaluate(() => $("#errmsg").is(":visible") && $("#errmsg").text().trim().length > 0), "a wrong password is not shown under the form");
        await page.fill("#magic", ADMIN_PASSWORD);
        await page.click("#loginbtn");
        await page.waitForFunction(() => typeof openTabById == "function", null, { timeout: 15000 });
        await page.waitForLoadState("networkidle");
        await sleep(2000);
        assert(!page.url().includes("login"), "still on the sign-in page");
    });

    const TABS = ["status", "httprp", "rules", "redirectset", "setroot", "access", "aclobjects", "trustproxy", "sso",
        "cert", "acme", "dnscreds", "utm", "stats", "logs", "capture", "globalsettings", "ha", "webserv",
        "logsettings", "syslogs", "utils", "account", "about", "plugins", "appstore"];

    await test("every page opens without errors and with nothing unsaved", async () => {
        for (const t of TABS) {
            await openTab(t);
            assert(await currentTab() == t, "page " + t + " did not open");
        }
        await sleep(1500);
        const dirty = [];
        for (const t of TABS) if (await ev(t => wmHasUnsavedChanges("#" + t), t)) dirty.push(t);
        assert(dirty.length == 0, "pages report unsaved changes when nothing was changed: " + dirty.join(", "));
    });

    await test("leaving a page with unsaved changes asks first; Discard puts the saved value back", async () => {
        await openTab("globalsettings");
        const saved = await ev(() => $("#idleTimeoutInput").val());
        await ev(() => $("#idleTimeoutInput").val("12345").trigger("input"));
        await sleep(200);
        assert(await ev(() => $("#proxyTimeoutSaveBtn").hasClass("wmUnsaved")), "the Save button is not highlighted");
        await ev(() => openTabById("status"));
        await sleep(300);
        assert(await dialogShown(), "no unsaved-changes dialog");
        await keepEditing();
        await sleep(200);
        assert(await currentTab() == "globalsettings", "Keep editing left the page");
        assert(await ev(() => $("#idleTimeoutInput").val()) == "12345", "Keep editing lost the change");
        const prevented = await ev(() => { const e = new Event("beforeunload", { cancelable: true }); window.dispatchEvent(e); return e.defaultPrevented; });
        assert(prevented, "closing the browser tab does not warn");
        await ev(() => openTabById("status"));
        await sleep(300);
        await discard();
        await sleep(500);
        assert(await currentTab() == "status", "Discard did not leave the page");
        await openTab("globalsettings");
        assert(await ev(() => $("#idleTimeoutInput").val()) == saved, "Discard did not restore the saved value");
        assert(!(await ev(() => wmHasUnsavedChanges())), "still unsaved after Discard");
    });

    await test("proxy rule editor: one Save, and closing with changes asks first", async () => {
        const add = await api("/api/proxy/add", { type: "host", rootname: "uitest.example.com", tls: false, ep: "127.0.0.1:9", tags: "" });
        assert(!add || add.error == undefined, "could not create the test rule: " + JSON.stringify(add));
        await openTab("httprp");
        await page.waitForFunction(() => $("tr[eptuuid='uitest.example.com']").length > 0, null, { timeout: 10000 });
        await ev(() => editEndpoint("uitest.example.com".hexEncode()));
        await page.waitForFunction(() => $("#httprpEditModalWrapper").is(":visible"), null, { timeout: 10000 });
        await sleep(1500);
        assert(!(await ev(() => wmHasUnsavedChanges())), "the editor reports changes right after opening");
        await ev(() => $("#httprpEditModalContentWindow .ui.checkbox:visible").filter(function () { return $(this).closest("[data-wm-untracked]").length == 0; }).first().checkbox("toggle"));
        await sleep(300);
        assert(await ev(() => $(".httprpEditSaveGroup .wmUnsaved").length) > 0, "the editor's Save button is not highlighted");
        await ev(() => closeHttpRuleEditor());
        await sleep(300);
        assert(await dialogShown(), "closing the editor did not ask");
        await discard();
        await sleep(500);
        assert(!(await ev(() => $("#httprpEditModalWrapper").is(":visible"))), "the editor stayed open after Discard");
        // Virtual directories open in the editor's side panel
        await ev(() => editEndpoint("uitest.example.com".hexEncode()));
        await sleep(1500);
        await ev(() => quickEditVdir("uitest.example.com"));
        await page.waitForFunction(() => document.querySelector("#httprpEditModal .editor_side_wrapper iframe")?.src.includes("vdirEditor"), null, { timeout: 5000 });
        await sleep(1500);
        const vf = page.frames().find(f => f.url().includes("vdirEditor"));
        assert(vf, "virtual directory panel did not load");
        assert(await vf.evaluate(() => $("#vdirList").text().includes("No virtual directories")), "virtual directory list not shown");
        assert(!(await vf.evaluate(() => wmHasUnsavedChanges())), "the empty virtual directory form reports changes");
        await ev(() => closeHttpRuleEditor());
        await sleep(500);
        assert(!(await dialogShown()), "closing the unchanged editor asked about unsaved changes");
        await api("/api/proxy/del", { ep: "uitest.example.com" });
    });

    await test("side panel: clicking outside with unsaved changes asks first", async () => {
        await openTab("acme");
        await ev(() => openACMEManager());
        await page.waitForFunction(() => $(".sideWrapper").is(":visible"), null, { timeout: 5000 });
        await sleep(2000);
        const frame = page.frames().find(f => f.url().includes("snippet/acme.html"));
        assert(frame, "ACME tool did not load");
        await frame.evaluate(() => $("#caRegisterEmail").val("ui-test@example.com").trigger("input"));
        await sleep(300);
        await page.mouse.click(200, 500);
        await sleep(400);
        assert(await dialogShown(), "no dialog when closing the panel");
        assert(await ev(() => $(".sideWrapper").is(":visible")), "the panel closed before the answer");
        await discard();
        await sleep(600);
        assert(!(await ev(() => $(".sideWrapper").is(":visible"))), "the panel stayed open after Discard");
    });

    await test("confirmations use the Waymark dialog", async () => {
        const created = await api("/api/acl/create", { name: "UI test ACL", description: "" });
        assert(created && created.id, "could not create the test ACL: " + JSON.stringify(created));
        await openTab("access");
        await ev(id => aclDelete(id, "UI test ACL"), created.id);
        await sleep(300);
        assert(await dialogShown(), "no Waymark confirmation dialog");
        assert(await ev(() => $("#wmDialog .wmDialogMessage").text().includes("UI test ACL")), "the dialog does not name the ACL");
        await dialogButton(0); // Cancel
        await sleep(500);
        let list = await ev(() => $.get("/api/acl/list"));
        assert((list.acls || list).some(a => a.id == created.id), "Cancel deleted the ACL");
        await ev(id => aclDelete(id, "UI test ACL"), created.id);
        await sleep(300);
        await dialogButton(1); // Delete
        await sleep(800);
        list = await ev(() => $.get("/api/acl/list"));
        assert(!(list.acls || list).some(a => a.id == created.id), "the ACL was not deleted");
    });

    await test("names are shown as text, not run as HTML", async () => {
        // A redirect's URLs are stored as given
        const evil = `uitest.example.com/"><img src=x onerror="window.__wmXss=1">`;
        let r = await api("/api/redirect/add", { redirectUrl: evil, destUrl: "https://example.com/", forwardChildpath: true, requireExactMatch: false, redirectType: 307, deviceType: "all" });
        assert(!r || r.error == undefined, "could not add the redirect: " + JSON.stringify(r));
        await openTab("redirectset");
        await ev(() => initRedirectionRuleList());
        await sleep(1500);
        assert(await ev(() => $("#redirectionRuleList, body").find("td:contains('onerror')").length) > 0, "the URL is not shown as text");
        assert(!(await ev(() => window.__wmXss)), "the URL ran as HTML");
        await api("/api/redirect/delete", { redirectUrl: evil });
        // Trusted proxy descriptions are stored and shown as typed
        r = await api("/api/trustedproxy/add", { ip: "198.51.100.77", desc: `Lab "1" & <img src=x onerror="window.__wmXss=1">` });
        assert(!r || r.error == undefined, "could not add the trusted proxy: " + JSON.stringify(r));
        await openTab("trustproxy");
        await ev(() => window.initTrustedProxyTable());
        await sleep(1500);
        const shown = await ev(() => $(".trustedProxyItem[data-ip='198.51.100.77'] .proxy-desc-display").text().trim());
        assert(shown == `Lab "1" & <img src=x onerror="window.__wmXss=1">`, "the description is shown as " + JSON.stringify(shown));
        assert(!(await ev(() => window.__wmXss)), "the description ran as HTML");
        await api("/api/trustedproxy/remove", { ip: "198.51.100.77" });
    });

    await test("TCP / UDP rule editor: one Save, and closing with changes asks first", async () => {
        const r = await api("/api/l4proxy/config/add", { name: "UI test L4", listenAddr: ":18999", proxyAddr: "127.0.0.1:9", useTCP: true, useUDP: false, timeout: 10, proxyProtocolVersion: 0, enableLogging: false, accessRuleUUID: "allow-all" });
        assert(r && r.error == undefined, "could not create the test rule: " + JSON.stringify(r));
        await openTab("httprp");
        await ev(() => typeof initL4ProxyList == "function" && initL4ProxyList(true));
        await page.waitForFunction(id => $(`tr.l4Row[uuid="${id}"]`).length > 0, r, { timeout: 10000 });
        await ev(id => editL4Proxy(id), r);
        await page.waitForFunction(() => $("#l4EditModalWrapper").is(":visible"), null, { timeout: 5000 });
        await sleep(1500);
        assert(!(await ev(() => wmHasUnsavedChanges("#l4EditModalWrapper"))), "the editor reports changes right after opening");
        await ev(() => { let i = $("#l4EditModalContentWindow input[type=text]:visible").filter(function () { return $(this).closest("[data-wm-untracked]").length == 0; }).first(); i.val(i.val() + "1").trigger("input"); });
        await sleep(300);
        assert(await ev(() => $("#l4EditModal .l4EditSaveBtn").hasClass("wmUnsaved")), "the editor's Save button is not highlighted");
        await ev(() => closeL4ProxyEditor());
        await sleep(300);
        assert(await dialogShown(), "closing the editor did not ask");
        await discard();
        await sleep(600);
        assert(!(await ev(() => $("#l4EditModalWrapper").is(":visible"))), "the editor stayed open after Discard");
        await api("/api/l4proxy/config/delete", { uuid: r });
    });

    await test("high availability page: unsaved settings are kept until Save and apply", async () => {
        await openTab("ha");
        await sleep(1500);
        assert(!(await ev(() => wmHasUnsavedChanges("#ha"))), "the HA page reports changes when nothing changed");
        await ev(() => $("#haRouterId").val("77").trigger("input"));
        await sleep(200);
        assert(await ev(() => $("#haSaveBtn").hasClass("wmUnsaved")), "Save and apply is not highlighted");
        await ev(() => openTabById("status"));
        await sleep(300);
        assert(await dialogShown(), "leaving the HA page did not ask");
        await discard();
        await sleep(1500);
        await openTab("ha");
        assert(await ev(() => $("#haRouterId").val()) != "77", "Discard kept the change");
    });

    await test("admin console sends its security headers", async () => {
        const h = await ev(() => fetch("/", { credentials: "same-origin" }).then(r => ({ csp: r.headers.get("content-security-policy") || "", xfo: r.headers.get("x-frame-options") || "", nosniff: r.headers.get("x-content-type-options") || "" })));
        assert(h.csp.includes("frame-ancestors 'self'") && h.csp.includes("object-src 'none'"), "Content-Security-Policy missing or incomplete: " + h.csp);
        assert(h.xfo == "SAMEORIGIN" && h.nosniff == "nosniff", "X-Frame-Options / X-Content-Type-Options missing");
    });

    await test("status panels: move, resize, hide; kept after a reload", async () => {
        await openTab("status");
        await page.waitForFunction(() => $("#zrPanelSettingsBtn").length > 0, null, { timeout: 5000 });
        await ev(() => $("#zrPanelSettingsBtn").click());
        await sleep(400);
        for (let i = 0; i < 30; i++) await ev(() => $(".zrTile[data-panel=systemResources] .zrTileMoveBtn[data-dir=-1]").click());
        await ev(() => $(".zrTile[data-panel=flowDiagram] .zrTileSizeBtn[data-size=full]").click());
        await ev(() => $(".zrTile[data-panel=cardTotalRequests] .zrTileVisBtn").click());
        await ev(() => $("#zrPanelSettingsBtn").click());
        await sleep(1200);
        await page.reload({ waitUntil: "networkidle" });
        await sleep(2500);
        assert(await ev(() => zrStatusPanels.order()[0]) == "systemResources", "the order was not kept");
        assert(await ev(() => $(".zrTile[data-panel=flowDiagram]").attr("data-size")) == "full", "the width was not kept");
        assert(!(await ev(() => $(".zrTile[data-panel=cardTotalRequests]").is(":visible"))), "the hidden panel is shown");
        await ev(() => { zrStatusPanels.customize(true); $("#zrPanelReset").click(); zrStatusPanels.customize(false); });
        await sleep(1200);
        const saved = await ev(() => $.get("/api/custom/statusPanels"));
        assert(saved.order.length == 0 && Object.keys(saved.visible).length == 0 && Object.keys(saved.sizes).length == 0, "Reset did not clear the layout");
    });

    await test("signing out returns to the sign-in page", async () => {
        await ev(() => logout());
        await page.waitForURL(/login/, { timeout: 10000 });
        const body = await page.evaluate(() => fetch("/api/proxy/list?type=host").then(r => r.text()));
        assert(!body.trim().startsWith("["), "the API still returns data after signing out");
    });

    await browser.close();
    const failed = results.filter(r => !r.ok);
    console.log(`\n${results.length - failed.length} passed, ${failed.length} failed`);
    process.exit(failed.length ? 1 : 0);
}

main().catch(async e => {
    console.error(e);
    if (browser) await browser.close();
    process.exit(1);
});
