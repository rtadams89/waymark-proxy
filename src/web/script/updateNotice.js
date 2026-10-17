/*
    updateNotice.js (custom fork)

    Waymark updates (selfupdate_custom.go):
    - a pill next to the logo when a newer release is available on GitHub
      (click opens Settings > About)
    - the Updates box on Settings > About: current and latest version,
      release notes, Check now, Install and Roll back
    - while this node installs an update and restarts, waits for it to come
      back and reloads the page on the new version

    Other cluster nodes are updated from the High Availability page.
*/
(function(){
    let st = null;          // last /api/update/status
    let waitFrom = null;    // version this node ran before an install or roll back
    let waitStart = 0;
    let fastTimer = null;

    function esc(s){ return $("<div>").text(s == undefined ? "" : String(s)).html().replace(/"/g, "&quot;").replace(/'/g, "&#39;"); }
    function ago(ts){
        if (!ts) return "never";
        let s = Math.max(0, Math.round(Date.now() / 1000 - ts));
        if (s < 60) return "just now";
        if (s < 3600) return Math.round(s / 60) + " min ago";
        if (s < 86400) return Math.round(s / 3600) + " h ago";
        return Math.round(s / 86400) + " d ago";
    }

    window.updOpenAbout = function(){
        openTabById("utils");
        setTimeout(function(){ $('.menu .utils.item[data-tab="utiltab5"]').trigger("click"); }, 300);
    };

    function renderPill(){
        let pill = $("#updateBadge");
        if (!pill.length) return;
        if (waitFrom){
            pill.html(`<span class="updPill"><i class="sync loading icon"></i>Updating...</span>`).css("display", "inline-flex");
        }else if (st && st.available){
            pill.html(`<span class="updPill" title="Waymark ${esc(st.latest.version)} is available (this node runs ${esc(st.current)})"><i class="arrow alternate circle up icon"></i>Update ${esc(st.latest.version)}</span>`).css("display", "inline-flex");
        }else{
            pill.hide();
        }
    }

    function lastLine(){
        let l = st.last;
        if (!l || !l.message || l.phase == "installed") return "";
        let icon = l.phase == "rolledback" ? `<i class="orange exclamation triangle icon"></i>` : `<i class="green check circle icon"></i>`;
        return `<div style="margin-top: 0.5em;">${icon}${esc(l.message)} <span class="updHint">${ago(l.finished || l.time)}</span></div>`;
    }

    function renderBox(){
        let box = $("#updateBox");
        if (!box.length || !st) return;
        let rows = [];
        rows.push(`<div><span class="updLabel">This node</span> <b>${esc(st.current)}</b></div>`);
        let latest = st.latest;
        if (latest){
            rows.push(`<div><span class="updLabel">Latest release</span> <b>${esc(latest.version)}</b>` +
                ` <a href="${esc(latest.url)}" target="_blank" rel="noopener">release page</a>` +
                ` <span class="updHint">checked ${ago(st.checked)}</span></div>`);
        }else{
            rows.push(`<div><span class="updLabel">Latest release</span> <span class="updHint">${st.checked ? "unknown" : "not checked yet"}</span></div>`);
        }
        if (st.checkError){
            rows.push(`<div style="color: #db2828;"><i class="times circle icon"></i>Checking GitHub failed: ${esc(st.checkError)}</div>`);
        }

        let actions = `<button class="ui basic small button" id="updCheckBtn" onclick="updCheckNow();"><i class="sync icon"></i> Check now</button>`;
        let notes = "";
        let warn = "";
        if (waitFrom){
            warn = `<div class="ui info message"><i class="sync loading icon"></i>${esc(st.message || "Installing")}. This page reloads when Waymark is back.</div>`;
        }else if (st.busy){
            warn = `<div class="ui info message"><i class="sync loading icon"></i>${esc(st.message)}</div>`;
        }else if (st.phase == "error"){
            warn = `<div class="ui negative message"><i class="times circle icon"></i>${esc(st.message)}</div>`;
        }
        if (st.available && !st.busy && !waitFrom){
            if (!st.installable){
                warn += `<div class="ui warning message">Release ${esc(latest.version)} has no <span class="updMono">${esc(st.asset)}</span> binary with a checksum, so it can't be installed from here.</div>`;
            }else if (!st.writable){
                warn += `<div class="ui warning message">Waymark can't replace its own binary here: ${esc(st.writableReason)}. Update it by hand (README, Upgrading), or make that folder writable by Waymark's user.</div>`;
            }else{
                actions += ` <button class="ui basic small green button" id="updInstallBtn" onclick="updInstall();"><i class="download icon"></i> Install ${esc(latest.version)}</button>`;
            }
            if (st.major){
                warn += `<div class="ui warning message"><i class="exclamation triangle icon"></i>${esc(latest.version)} is a new major version. Read its release notes first: a major version can need steps before or after upgrading.</div>`;
            }
            if (st.haActive){
                warn += `<div class="ui message"><i class="info circle icon"></i>This node is the active HA node: restarting it moves the floating IPs to the standby. To choose when that happens, use <b>Make active</b> on the other node first (System &gt; High Availability), and update the other nodes from there too.</div>`;
            }
            if (latest.notes){
                notes = `<details style="margin-top: 0.8em;"><summary style="cursor: pointer;">Release notes for ${esc(latest.version)}</summary><pre class="updNotes">${esc(latest.notes)}</pre></details>`;
            }
        }else if (latest && !st.available && !waitFrom){
            rows.push(`<div><i class="green check circle icon"></i>This node runs the latest release.</div>`);
        }
        if (st.previous && !st.busy && !waitFrom){
            // After a roll back, the previous binary is the newer one
            let back = window.haVersionNewer && haVersionNewer(st.previous, st.current) ? "Return to " : "Roll back to ";
            actions += ` <button class="ui basic small button" id="updRollbackBtn" onclick="updRollback();" title="Swap back to the binary that ran before the last update or roll back"><i class="undo icon"></i> ${back}${esc(st.previous)}</button>`;
        }
        box.html(rows.join("") + lastLine() + `<div style="margin-top: 0.8em;">${warn}${actions}</div>` + notes +
            `<div class="updHint" style="margin-top: 0.8em;">Waymark checks <a href="${esc(st.repo)}/releases" target="_blank" rel="noopener">GitHub</a> for a new release once a day. Install downloads <span class="updMono">${esc(st.asset)}</span> from the release, checks its SHA-256 checksum and that it starts, keeps the current binary for Roll back, and restarts Waymark. If the new version does not stay up, the previous one is put back automatically.</div>`);
    }

    function poll(){
        $.get("/api/update/status", function(d){
            if (!d || typeof d != "object" || d.error != undefined) return;
            if (waitFrom && d.current != waitFrom){
                // Back on another version: reload so the page matches it
                waitFrom = null;
                msgbox("Waymark is now running " + d.current + ". Reloading...", true);
                setTimeout(function(){ location.reload(); }, 1500);
                return;
            }
            st = d;
            if (waitFrom && st.phase == "error"){
                waitFrom = null; // install failed before restarting
            }
            renderPill();
            renderBox();
            schedule();
        }).fail(function(){
            // Expected while this node restarts
            if (waitFrom && Date.now() - waitStart > 180000){
                waitFrom = null;
                msgbox("Waymark did not come back within 3 minutes. Check the service on this node (journalctl -u waymark).", false);
            }
            schedule();
        });
    }

    function schedule(){
        clearTimeout(fastTimer);
        let fast = waitFrom || (st && st.busy);
        if (fast) fastTimer = setTimeout(poll, 2000);
    }

    function startWaiting(){
        waitFrom = st ? st.current : null;
        waitStart = Date.now();
        renderPill();
        renderBox();
        schedule();
    }

    window.updCheckNow = function(){
        $("#updCheckBtn").addClass("loading disabled");
        $.cjax({
            url: "/api/update/check", method: "POST",
            success: function(d){
                if (d.error != undefined){ msgbox(d.error, false); }
                poll();
            },
            error: function(){ msgbox("Request failed", false); },
            complete: function(){ $("#updCheckBtn").removeClass("loading disabled"); }
        });
    };

    window.updInstall = function(){
        if (!st || !st.latest) return;
        let v = st.latest.version;
        let text = "Install Waymark " + v + " on this node?\n\nIt is downloaded from GitHub and checked, then Waymark restarts (a few seconds).";
        if (st.major) text += "\n\nThis is a new major version: read its release notes first.";
        if (st.haActive) text += "\n\nThis node is active: the floating IPs move to the standby while it restarts.";
        if (!confirm(text)) return;
        $("#updInstallBtn").addClass("loading disabled");
        $.cjax({
            url: "/api/update/install", method: "POST", data: { version: v },
            success: function(d){
                if (d.error != undefined){ msgbox(d.error, false); poll(); return; }
                msgbox(d.message, true);
                startWaiting();
            },
            error: function(){ msgbox("Request failed", false); }
        });
    };

    window.updRollback = function(){
        if (!st || !st.previous) return;
        if (!confirm("Go back to Waymark " + st.previous + " on this node? Waymark restarts (a few seconds).")) return;
        $("#updRollbackBtn").addClass("loading disabled");
        $.cjax({
            url: "/api/update/rollback", method: "POST",
            success: function(d){
                if (d.error != undefined){ msgbox(d.error, false); poll(); return; }
                msgbox(d.message, true);
                startWaiting();
            },
            error: function(){ msgbox("Request failed", false); }
        });
    };

    // Used by the HA page after asking this node to update
    window.updWaitForRestart = startWaiting;
    window.updStatus = function(){ return st; };
    window.updRefresh = poll;

    $(function(){ setTimeout(poll, 1200); });
    setInterval(function(){ if (!waitFrom) poll(); }, 60000);
})();
