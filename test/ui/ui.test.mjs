// Browser tests of the Waymark admin UI.
//
// Run against a fresh instance started with -noauth, e.g.
//   ./waymark -noauth -port :8000 -mdns=false -default_inbound_port 8443
//   BASE_URL=http://127.0.0.1:8000 npm test
//
// The tests create what they need (a proxy rule, an ACL, a trusted proxy)
// and remove it again. Any page error or native browser dialog fails the run.

import { createRequire } from "node:module";
const require = createRequire(import.meta.url);
const { chromium } = require("playwright");

const BASE = process.env.BASE_URL || "http://127.0.0.1:8000";
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
    await page.goto(BASE + "/", { waitUntil: "networkidle" });
    await sleep(2000);

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
        // Trusted proxy descriptions are stored HTML-encoded and shown decoded
        r = await api("/api/trustedproxy/add", { ip: "198.51.100.77", desc: `Lab "1" & <img src=x onerror="window.__wmXss=1">` });
        assert(!r || r.error == undefined, "could not add the trusted proxy: " + JSON.stringify(r));
        await openTab("trustproxy");
        await ev(() => window.initTrustedProxyTable());
        await sleep(1500);
        const shown = await ev(() => $(".trustedProxyItem[data-ip='198.51.100.77'] .proxy-desc-display").text().trim());
        assert(shown == `Lab "1" &`, "the description is shown as " + JSON.stringify(shown));
        assert(!(await ev(() => window.__wmXss)), "the description ran as HTML");
        await api("/api/trustedproxy/remove", { ip: "198.51.100.77" });
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
