/*
    updateNotice.js (custom fork)

    Waymark updates (selfupdate_custom.go):
    - a pill next to the logo when a newer release is available on GitHub
      (click opens System > About)
    - the Updates box on System > About: current and latest version,
      release notes, Check now, Install and Roll back
    - while this node installs an update and restarts, waits for it to come
      back and reloads the page on the new version

    In an HA cluster, Rolling update (updRollingStart) updates every node
    one at a time (rollingupdate_custom.go); its progress is shown on the
    High Availability page. Other nodes can also be updated one by one there.
*/
(function(){
    let st = null;          // last /api/update/status
    let pageVersion = null; // version this page was loaded from
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
        openTabById("about");
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
        if (st.available && !st.busy && !waitFrom && !rollingRunning()){
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
            if (st.haCluster && !rollingRunning()){
                actions += ` <button class="ui basic small green button" id="updRollingBtn" onclick="updRollingStart();" title="Update every node of the HA cluster, one at a time"><i class="sync alternate icon"></i> Rolling update of all nodes</button>`;
                warn += `<div class="ui message"><i class="info circle icon"></i>This node is part of an HA cluster. <b>Rolling update of all nodes</b> updates the standby nodes first, one at a time, then makes an updated node active and updates the node that was active last. Progress is shown on System &gt; High Availability.` +
                    (st.haActive ? ` <b>Install</b> updates only this node, the active one: the floating IPs move to a standby while it restarts.` : "") + `</div>`;
            }else if (st.haActive){
                warn += `<div class="ui message"><i class="info circle icon"></i>This node is the active HA node: restarting it moves the floating IPs to the standby. To choose when that happens, use <b>Make active</b> on the other node first (System &gt; High Availability), and update the other nodes from there too.</div>`;
            }
            if (latest.notes){
                notes = `<details style="margin-top: 0.8em;"><summary style="cursor: pointer;">Release notes for ${esc(latest.version)}</summary><pre class="updNotes">${esc(latest.notes)}</pre></details>`;
            }
        }else if (latest && !st.available && !waitFrom){
            rows.push(`<div><i class="green check circle icon"></i>This node runs the latest release.</div>`);
        }
        if (rollingRunning()){
            let r = st.rolling.state;
            warn = `<div class="ui info message"><i class="sync loading icon"></i>Rolling update to ${esc(r.to)}: ${esc(r.message)}. <a href="#" onclick="openTabById('ha'); return false;">Show progress</a></div>` + warn;
        }
        if (st.previous && !st.busy && !waitFrom && !rollingRunning()){
            // After a roll back, the previous binary is the newer one
            let back = window.haVersionNewer && haVersionNewer(st.previous, st.current) ? "Return to " : "Roll back to ";
            actions += ` <button class="ui basic small button" id="updRollbackBtn" onclick="updRollback();" title="Swap back to the binary that ran before the last update or roll back"><i class="undo icon"></i> ${back}${esc(st.previous)}</button>`;
        }
        box.html(rows.join("") + lastLine() + `<div style="margin-top: 0.8em;">${warn}${actions}</div>` + notes +
            `<div class="updHint" style="margin-top: 0.8em;">Waymark checks <a href="${esc(st.repo)}/releases" target="_blank" rel="noopener">GitHub</a> for a new release once a day. Install downloads <span class="updMono">${esc(st.asset)}</span> from the release, checks its SHA-256 checksum and that it starts, keeps the current binary for Roll back, and restarts Waymark. If the new version does not stay up, the previous one is put back automatically.</div>`);
    }

    function rollingRunning(){
        return !!(st && st.rolling && st.rolling.state && st.rolling.state.phase == "running");
    }

    // updRollingStart asks for confirmation and starts a rolling update of
    // every HA node to the latest release. done(ok) runs afterwards.
    window.updRollingStart = function(done){
        if (!st || !st.latest){ msgbox("Check for updates first", false); return; }
        let v = st.latest.version;
        $.get("/api/ha/status", function(d){
            if (!d || typeof d != "object" || d.error != undefined){ msgbox("Could not read the cluster state", false); return; }
            let cfgNodes = (d.config && d.config.nodes) || [];
            let prio = n => ((cfgNodes.find(c => c.name == n.name) || {}).priority || 0);
            let nodes = d.cluster || [];
            let active = nodes.find(n => n.reachable && n.role == "MASTER");
            let standbys = nodes.filter(n => n != active).sort((a, b) => prio(a) - prio(b));
            if (!active){ msgbox("No node is active right now; a rolling update starts from the active node", false); return; }
            let behind = standbys.filter(n => n.version != v);
            let next = standbys.length ? standbys[standbys.length - 1] : null;
            let text = "Update every node to Waymark " + v + ", one at a time?\n\n";
            let i = 1;
            if (behind.length){
                text += (i++) + ". " + behind.map(n => n.name).join(", ") + (behind.length > 1 ? " (standbys) install " : " (standby) installs ") + v +
                    " and restart" + (behind.length > 1 ? ", one at a time. Each must come back healthy before the next starts." : " and must come back healthy.") + "\n";
            }
            if (active.version != v && next){
                text += (i++) + ". " + next.name + " becomes the active node: the floating IPs move to it, and connections in progress through them are cut.\n";
                text += (i++) + ". " + active.name + " (active now) installs " + v + " and restarts as a standby.\n";
                if (d.config.preempt && prio(active) > prio(next)){
                    text += "\nPreempt is on and " + active.name + " has the higher priority, so it takes the floating IPs back about 30 seconds after it restarts on " + v + ".\n";
                }
            }
            text += "\nIt stops at the first problem, and " + active.name + " is only updated once every other node runs " + v + " and has taken over.";
            if (st.major) text += "\n\n" + v + " is a new major version: read its release notes first.";
            if (!confirm(text)) return;
            $("#updRollingBtn, #haRollingBtn").addClass("loading disabled");
            $.cjax({
                url: "/api/update/rolling", method: "POST", data: { version: v }, timeout: 90000,
                success: function(r){
                    $("#updRollingBtn, #haRollingBtn").removeClass("loading disabled");
                    if (r.error != undefined){ msgbox(r.error, false); if (done) done(false); return; }
                    msgbox(r.message, true);
                    poll();
                    if (done) done(true);
                },
                error: function(){
                    $("#updRollingBtn, #haRollingBtn").removeClass("loading disabled");
                    msgbox("Request failed", false);
                    if (done) done(false);
                }
            });
        });
    };

    function poll(){
        $.get("/api/update/status", function(d){
            if (!d || typeof d != "object" || d.error != undefined) return;
            if (pageVersion == null) pageVersion = d.current;
            if (d.current != pageVersion){
                // This node now runs another version (an install, a roll back or a
                // rolling update): reload so the page matches it
                waitFrom = null;
                pageVersion = d.current;
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
        let fast = waitFrom || (st && st.busy) || rollingRunning();
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
