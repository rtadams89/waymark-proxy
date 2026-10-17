/*
    haCluster.js (custom fork)

    Renders the HA cluster overview (every node's role, health, floating IP,
    configuration sync state and version) for the High Availability page
    and the status page panel. Data: the "cluster" list from /api/ha/status
    or /api/ha/cluster. Other nodes' state comes over the configuration
    sync connection, so it is only known while sync is on.
*/
(function(){
    function esc(s){ return $("<div>").text(s == undefined ? "" : String(s)).html(); }
    function since(ts){
        if (!ts) return "";
        let s = Math.max(0, Math.round(Date.now() / 1000 - ts));
        if (s < 60) return s + "s";
        if (s < 3600) return Math.round(s / 60) + " min";
        if (s < 86400) return (s / 3600).toFixed(s < 36000 ? 1 : 0) + " h";
        return Math.round(s / 86400) + " d";
    }

    const ROLES = {
        MASTER: ["Active", "#1a9c3a", "rgba(33,186,69,0.15)"],
        BACKUP: ["Standby", "#2185d0", "rgba(33,133,208,0.15)"],
        FAULT:  ["Fault", "#db2828", "rgba(219,40,40,0.15)"],
        STOP:   ["keepalived stopped", "#db2828", "rgba(219,40,40,0.15)"]
    };
    function rolePill(role){
        let r = ROLES[role] || ["Unknown", "#767676", "rgba(128,128,128,0.15)"];
        return `<span style="display:inline-block; padding: 0.15em 0.7em; border-radius: 1em; font-weight: 600; font-size: 0.85em; color: ${r[1]}; background: ${r[2]}; white-space: nowrap;">${r[0]}</span>`;
    }

    function syncCell(n, syncEnabled){
        if (!syncEnabled) return `<span style="opacity: 0.6;">Sync off</span>`;
        let st = n.syncStatus || "", d = esc(n.syncDetail || "");
        switch (st){
            case "active":     return `<span title="${d}"><i class="green upload icon"></i>Source</span>`;
            case "in-sync":    return `<span title="${d}"><i class="green check circle icon"></i>In sync</span>`;
            case "waiting":
            case "starting":
            case "restarting": return `<span title="${d}"><i class="blue sync icon"></i>${st == "restarting" ? "Loading changes" : "Updating"}</span>`;
            case "off":        return `<span style="opacity: 0.6;" title="${d}">Sync off on this node</span>`;
            case "":           return `<span style="opacity: 0.6;">-</span>`;
            default:           return `<span title="${d}"><i class="red times circle icon"></i>${d || "Problem"}</span>`;
        }
    }

    // problems(): short warnings for the summary line
    function problems(data){
        let out = [], nodes = data.nodes || [];
        let active = nodes.filter(n => n.role == "MASTER");
        if (active.length > 1) out.push("More than one node is active (" + active.map(n => esc(n.name)).join(", ") + ")");
        if (data.syncEnabled){
            nodes.filter(n => !n.self && !n.reachable).forEach(n => out.push(esc(n.name) + " unreachable"));
            nodes.filter(n => n.version && !n.versionMatch).forEach(n => out.push(esc(n.name) + " runs " + esc(n.version)));
        }
        nodes.filter(n => n.reachable && n.role && n.heartbeatOk === false).forEach(n => out.push(esc(n.name) + " unhealthy"));
        nodes.filter(n => n.reachable && (n.role == "FAULT" || n.role == "STOP")).forEach(n => out.push(esc(n.name) + ": " + ROLES[n.role][0]));
        return out;
    }

    // haRenderCluster fills container with the overview. data: {enabled,
    // syncEnabled, vip, nodes}. opts.compact drops the version column.
    window.haRenderCluster = function(container, data, opts){
        opts = opts || {};
        let nodes = data.nodes || [];
        let holder = nodes.find(n => n.vipHeld);
        let healthy = nodes.filter(n => n.reachable && n.heartbeatOk && (n.role == "MASTER" || n.role == "BACKUP")).length;
        let probs = problems(data);
        let summary = `<div style="margin-bottom: 0.6em;">` +
            (holder ? `<i class="green map marker alternate icon"></i>Floating IP <span style="font-family: monospace;">${esc(data.vip)}</span> on <b>${esc(holder.name)}</b>`
                    : `<i class="red map marker alternate icon"></i>Floating IP <span style="font-family: monospace;">${esc(data.vip)}</span> is not held by any node it can see`) +
            ` · ${healthy} of ${nodes.length} nodes healthy` +
            (probs.length ? `<div style="margin-top: 0.3em; color: #db2828;"><i class="exclamation triangle icon"></i>${probs.join(" · ")}</div>` : "") +
            (!data.syncEnabled && nodes.length > 1 ? `<div style="margin-top: 0.3em; opacity: 0.7;">Other nodes' status appears when configuration sync is on.</div>` : "") +
            `</div>`;

        let rows = nodes.map(n => {
            let name = `<b>${esc(n.name)}</b>` + (n.self ? ` <span class="ui mini basic label" style="margin-left: 0.3em;">this node</span>` : "") +
                `<div style="font-family: monospace; font-size: 0.85em; opacity: 0.7;">${esc(n.address)}</div>`;
            let role, health, sync, ver, seen;
            if (!n.role && !n.reachable){
                role = `<span style="opacity: 0.6;">-</span>`;
                health = `<span style="opacity: 0.7;" title="${esc(n.error)}"><i class="grey question circle icon"></i>${esc(n.error || "Unknown")}</span>`;
                sync = ver = seen = `<span style="opacity: 0.6;">-</span>`;
            }else{
                role = rolePill(n.role) + (n.vipHeld ? ` <i class="green map marker alternate icon" title="Holds the floating IP"></i>` : "");
                if (!n.reachable){
                    // Only the last known role is shown, faded; the rest would be stale
                    role = `<span style="opacity: 0.45;" title="Last known role">${rolePill(n.role)}</span>`;
                    health = `<i class="red unlink icon"></i>Unreachable <span style="opacity: 0.7;" title="${esc(n.error)}">(last seen ${since(n.lastSeen)} ago)</span>`;
                    sync = seen = `<span style="opacity: 0.6;">-</span>`;
                    ver = n.version ? `<span style="opacity: 0.6;">${esc(n.version)}</span>` : "-";
                    return `<tr><td>${name}</td><td>${role}</td><td>${health}</td><td>${sync}</td>` +
                        (opts.compact ? "" : `<td>${ver}</td>`) + `<td>${seen}</td></tr>`;
                }else if (n.heartbeatOk){
                    health = `<i class="green check circle icon"></i>Healthy`;
                }else{
                    health = `<i class="red times circle icon"></i>${esc(n.heartbeatReason || "Unhealthy")}`;
                }
                if (n.keepalived && n.keepalived != "active" && n.keepalived != "unknown"){
                    health += `<div style="font-size: 0.85em; color: #db2828;">keepalived ${esc(n.keepalived)}</div>`;
                }
                sync = syncCell(n, data.syncEnabled);
                ver = n.version ? `<span style="${n.versionMatch ? "" : "color: #db2828; font-weight: 600;"}">${esc(n.version)}</span>` : "-";
                seen = n.started ? `up ${since(n.started)}` : "";
            }
            return `<tr><td>${name}</td><td>${role}</td><td>${health}</td><td>${sync}</td>` +
                (opts.compact ? "" : `<td>${ver}</td>`) + `<td style="white-space: nowrap; opacity: 0.8;">${seen}</td></tr>`;
        }).join("");

        $(container).html(summary + `<table class="ui very basic compact unstackable table" style="margin-top: 0;">
            <thead><tr><th>Node</th><th>Role</th><th>Health</th><th>Configuration</th>${opts.compact ? "" : "<th>Version</th>"}<th></th></tr></thead>
            <tbody>${rows}</tbody></table>`);
    };
})();
