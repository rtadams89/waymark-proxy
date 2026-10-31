/*
    The Access Logs page (Monitoring) and the System Logs page (System):
    lines of Waymark's log file, read through /api/log/read, shown as a
    table, newest first. Both kinds of line are in the same file; access
    lines start with [router:...] and system lines contain [system:...].

    wmLogPage(rootSelector, "access" | "system") fills the page's
    .lpTools, .lpNotice, .lpCount and table.
*/
(function(){
    function esc(s){ return htmlEscape(s); }
    // The API returns HTML-escaped text; turn it back into plain text
    function unescapeHTML(s){ let t = document.createElement("textarea"); t.innerHTML = s; return t.value; }
    function fmtSize(n){
        if (n >= 1048576) return (n / 1048576).toFixed(1) + " MB";
        if (n >= 1024) return Math.round(n / 1024) + " KB";
        return n + " B";
    }

    // [2026-10-19 18:07:57.149637] [router:host] [origin:a.example] [client: 1.2.3.4] [useragent: Mozilla...] GET /path 200
    const ACCESS_RE = /^\[([^\]]+)\] \[router:([^\]]*)\] \[origin:([^\]]*)\] \[client: ?([^\]]*)\] \[useragent: ?(.*)\] (\S+) (.*) (\d{3})$/;
    // [2026-10-19 18:07:57.149637] [healthcheck] [system:info] Destination ...
    const SYSTEM_RE = /^\[([^\]]+)\] \[([^\]]*)\] \[system:(\w+)\] (.*)$/;

    const KINDS = {
        access: {
            filter: "request", re: ACCESS_RE, noun: "requests",
            head: "<tr><th>Time</th><th>Status</th><th>Method</th><th>Host</th><th>Path</th><th>Client</th><th>Browser / client</th></tr>",
            parse: m => ({time: m[1], cls: m[2], host: m[3], client: m[4], ua: m[5], method: m[6], uri: m[7], status: +m[8]}),
            text: e => [e.host, e.uri, e.client, e.ua, e.method, e.status, e.cls].join(" "),
            row: e => {
                return `<tr><td class="lpTime">${esc(e.time)}</td><td><span class="lpTag s${String(e.status).charAt(0)}" title="${esc(e.cls)}">${e.status}</span></td>` +
                    `<td>${esc(e.method)}</td><td class="lpWrap">${esc(e.host)}</td><td class="lpWrap lpMono">${esc(e.uri)}</td>` +
                    `<td class="lpMono">${esc(e.client)}</td><td class="lpWrap lpUA" title="${esc(e.ua)}">${esc(e.ua)}</td></tr>`;
            },
            levels: [["", "All statuses"], ["2", "2xx success"], ["3", "3xx redirects"], ["4", "4xx client errors"], ["5", "5xx server errors"]],
            level: (e, v) => String(e.status).charAt(0) == v,
            setting: "trafficToFile", syslogKey: "traffic"
        },
        system: {
            filter: "system", re: SYSTEM_RE, noun: "messages",
            head: "<tr><th>Time</th><th>Level</th><th>Module</th><th>Message</th></tr>",
            parse: m => ({time: m[1], module: m[2], level: m[3], msg: m[4]}),
            text: e => [e.module, e.level, e.msg].join(" "),
            row: e => {
                let lvl = `<span class="lpTag ${e.level == "error" ? "err" : "info"}">${esc(e.level)}</span>`;
                return `<tr class="${e.level == "error" ? "lpError" : ""}"><td class="lpTime">${esc(e.time)}</td><td>${lvl}</td><td class="lpNowrap">${esc(e.module)}</td><td class="lpWrap">${esc(e.msg)}</td></tr>`;
            },
            levels: [["", "All levels"], ["error", "Errors only"]],
            level: (e, v) => e.level == v,
            setting: "systemToFile", syslogKey: "system"
        }
    };

    window.wmLogPage = function(rootSel, kindName){
        const kind = KINDS[kindName];
        const root = $(rootSel);
        let entries = [];      // parsed lines, newest first
        let unparsed = 0;      // lines of this kind the table could not split into columns
        let timer = null;

        root.find(".lpTools").html(`
            <div class="ui form"><div class="fields" style="flex-wrap: wrap; row-gap: 0.6em;">
                <div class="field"><label>Log file</label><select class="lpFile"></select></div>
                <div class="field"><label>Lines</label><select class="lpLines">
                    <option value="500">Last 500</option><option value="2000" selected>Last 2,000</option><option value="10000">Last 10,000</option><option value="all">Whole file</option>
                </select></div>
                <div class="field"><label>Show</label><select class="lpLevel">${kind.levels.map(l => `<option value="${l[0]}">${l[1]}</option>`).join("")}</select></div>
                <div class="field" style="min-width: 16em; flex: 1;"><label>Search</label><input type="text" class="lpSearch" placeholder="Text to look for"></div>
                <div class="field" style="align-self: flex-end;">
                    <div class="ui checkbox" style="margin-bottom: 0.7em;"><input type="checkbox" class="lpAuto"><label>Refresh every 5 s</label></div>
                </div>
                <div class="field" style="align-self: flex-end;">
                    <button class="ui basic icon button lpRefresh" title="Refresh"><i class="green sync icon"></i></button>
                    <a class="ui basic icon button lpDownload" title="Download these lines" target="_blank"><i class="download icon"></i></a>
                </div>
            </div></div>`);
        root.find(".lpTable thead").html(kind.head);
        root.find(".ui.checkbox").checkbox();

        function file(){ return root.find(".lpFile").val() || ""; }

        function loadFiles(callback){
            $.get("/api/log/list", function(data){
                let files = [];
                if (data && typeof data == "object" && data.error == undefined){
                    Object.values(data).forEach(list => (list || []).forEach(f => files.push(f)));
                }
                files.sort((a, b) => (b.Modified || 0) - (a.Modified || 0));
                let current = file();
                let sel = root.find(".lpFile").empty();
                files.forEach((f, i) => sel.append(`<option value="${esc(f.Filename)}">${esc(f.Title)}${i == 0 ? " (current)" : ""} · ${fmtSize(f.Filesize)}</option>`));
                if (files.length == 0) sel.append(`<option value="">No log files yet</option>`);
                if (current && files.some(f => f.Filename == current)) sel.val(current);
                if (callback) callback();
            });
        }

        function load(){
            let f = file();
            let lines = root.find(".lpLines").val();
            let url = "/api/log/read?file=" + encodeURIComponent(f) + "&filter=" + kind.filter + "&lines=" + lines;
            root.find(".lpDownload").attr("href", "/api/log/read?file=" + encodeURIComponent(f) + "&filter=" + kind.filter + "&lines=all");
            if (!f){ entries = []; render(); return; }
            root.find(".lpRefresh").addClass("loading");
            $.get(url, function(text){
                root.find(".lpRefresh").removeClass("loading");
                if (typeof text != "string"){
                    entries = []; render(text && text.error ? text.error : "Could not read the log file"); return;
                }
                entries = []; unparsed = 0;
                unescapeHTML(text).split("\n").forEach(line => {
                    if (line.trim() == "") return;
                    let m = kind.re.exec(line);
                    if (m) entries.push(kind.parse(m)); else unparsed++;
                });
                entries.reverse();
                render();
            }).fail(function(){ root.find(".lpRefresh").removeClass("loading"); render("Could not read the log file"); });
        }

        function render(error){
            let search = (root.find(".lpSearch").val() || "").toLowerCase().trim();
            let level = root.find(".lpLevel").val();
            let shown = entries.filter(e => (!level || kind.level(e, level)) && (!search || kind.text(e).toLowerCase().includes(search)));
            const MAX = 2000; // rows drawn at once
            let body = shown.slice(0, MAX).map(kind.row).join("");
            if (error){
                body = `<tr><td colspan="8"><i class="red times circle icon"></i>${esc(error)}</td></tr>`;
            }else if (shown.length == 0){
                body = `<tr><td colspan="8" style="opacity: 0.7;">${entries.length ? "No lines match" : "No " + kind.noun + " in these lines of this file"}</td></tr>`;
            }
            root.find(".lpTable tbody").html(body);
            let count = entries.length ? `${shown.length.toLocaleString()} of ${entries.length.toLocaleString()} ${kind.noun}` : "";
            if (shown.length > MAX) count += ` · the newest ${MAX.toLocaleString()} are shown; narrow the search to see others`;
            if (unparsed) count += ` · ${unparsed} line${unparsed == 1 ? "" : "s"} in another format left out (download to see them)`;
            root.find(".lpCount").text(count);
        }

        // Whether this kind of log is written to the file (System > Logging)
        function loadNotice(){
            $.get("/api/logger/outputs", function(d){
                let o = d && d.outputs ? d.outputs : d;
                if (!o || typeof o != "object") return;
                let box = root.find(".lpNotice");
                if (o[kind.setting]){ box.hide(); return; }
                let syslog = (o.syslog || []).filter(s => s.enabled && s[kind.syslogKey]).map(s => s.name || s.host);
                let where = syslog.length ? " They are sent to " + syslog.map(esc).join(", ") + " instead." : "";
                box.html(`<i class="info circle icon"></i>${kindName == "access" ? "Access" : "System"} logs are not written to the log file, so new ${kind.noun} don't appear here.${where} Change this on <a href="#logsettings" onclick="openTabById('logsettings'); return false;">System &gt; Logging</a>.`).show();
            });
        }

        root.find(".lpFile, .lpLines").on("change", load);
        root.find(".lpLevel").on("change", function(){ render(); });
        root.find(".lpSearch").on("input", function(){ render(); });
        root.find(".lpRefresh").on("click", function(){ loadFiles(load); });
        root.find(".lpAuto").on("change", function(){
            clearInterval(timer);
            if (this.checked){
                timer = setInterval(function(){ if (root.is(":visible") && !document.hidden) load(); }, 5000);
            }
        });

        // Loaded when the page is opened (tabSwitchEventBind), not at start-up
        let tab = root.attr("id");
        tabSwitchEventBind[tab] = function(){
            loadNotice();
            loadFiles(load);
        };
        if (root.is(":visible")) tabSwitchEventBind[tab]();
    };
})();
