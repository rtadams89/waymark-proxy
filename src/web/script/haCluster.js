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

    // problems(): the summary line's problems, as {errors, warnings}.
    // Only nodes that answered the latest poll have a role; an unreachable
    // node has just lastRole/lastSeen, which describe the past.
    function problems(data){
        let errors = [], warnings = [], nodes = data.nodes || [];
        let me = nodes.find(n => n.self);
        let active = nodes.filter(n => n.reachable && n.role == "MASTER");
        if (active.length > 1) errors.push("More than one node is active (" + active.map(n => esc(n.name)).join(", ") + ")");
        // All floating IPs belong to one group, so one node should hold all of them
        let total = (data.floatingIPs || []).length;
        if (total > 1){
            let holders = nodes.filter(n => n.reachable && n.vipsHeld > 0);
            if (holders.length > 1){
                errors.push("Floating IPs are split between " + holders.map(n => esc(n.name)).join(" and "));
            }else if (holders.length == 1 && holders[0].vipsHeld < total){
                warnings.push(esc(holders[0].name) + " holds " + holders[0].vipsHeld + " of " + total + " floating IPs (settings not applied yet?)");
            }
        }
        if (data.syncEnabled){
            nodes.filter(n => !n.self && !n.reachable).forEach(n => {
                let seen = n.lastSeen ? "last seen " + since(n.lastSeen) + " ago" : "not seen yet";
                if (me && me.role == "MASTER"){
                    // This node holds the active role: a failover, or a missing standby
                    if (n.lastRole == "MASTER"){
                        warnings.push("Failed over to " + esc(me.name) + " · " + esc(n.name) + " unreachable (was active, " + seen + ")");
                    }else{
                        warnings.push("No standby · " + esc(n.name) + " unreachable (" + seen + ")");
                    }
                }else{
                    errors.push(esc(n.name) + " unreachable (" + (n.lastRole == "MASTER" ? "was active, " : "") + seen + ")");
                }
            });
            nodes.filter(n => n.reachable && n.version && !n.versionMatch).forEach(n => errors.push(esc(n.name) + " runs " + esc(n.version)));
        }
        nodes.filter(n => n.reachable && n.role && n.heartbeatOk === false).forEach(n => errors.push(esc(n.name) + " unhealthy"));
        nodes.filter(n => n.reachable && (n.role == "FAULT" || n.role == "STOP")).forEach(n => errors.push(esc(n.name) + ": " + ROLES[n.role][0]));
        return {errors: errors, warnings: warnings};
    }

    // haRenderCluster fills container with the overview. data: {enabled,
    // syncEnabled, vip, floatingIPs, nodes}. opts.compact drops the version column.
    window.haRenderCluster = function(container, data, opts){
        opts = opts || {};
        let nodes = data.nodes || [];
        let holder = nodes.find(n => n.vipHeld);
        let healthy = nodes.filter(n => n.reachable && n.heartbeatOk && (n.role == "MASTER" || n.role == "BACKUP")).length;
        let probs = problems(data);
        let fips = (data.floatingIPs || []).map(f => f.address);
        if (fips.length == 0 && data.vip) fips = [data.vip];
        let fipText = `<span style="font-family: monospace;">${fips.map(esc).join(", ")}</span>`;
        let fipNoun = fips.length > 1 ? "Floating IPs" : "Floating IP";
        let summary = `<div style="margin-bottom: 0.6em;">` +
            (holder ? `<i class="green map marker alternate icon"></i>${fipNoun} ${fipText} on <b>${esc(holder.name)}</b>`
                    : `<i class="red map marker alternate icon"></i>${fipNoun} ${fipText} ${fips.length > 1 ? "are" : "is"} not held by any node it can see`) +
            ` · ${healthy} of ${nodes.length} nodes healthy` +
            (probs.errors.length ? `<div style="margin-top: 0.3em; color: #db2828;"><i class="exclamation triangle icon"></i>${probs.errors.join(" · ")}</div>` : "") +
            (probs.warnings.length ? `<div style="margin-top: 0.3em; color: var(--warning_text, #a36b00);"><i class="exclamation circle icon"></i>${probs.warnings.join("<br>")}</div>` : "") +
            (!data.syncEnabled && nodes.length > 1 ? `<div style="margin-top: 0.3em; opacity: 0.7;">Other nodes' status appears when configuration sync is on.</div>` : "") +
            `</div>`;

        let rows = nodes.map(n => {
            let name = `<b>${esc(n.name)}</b>` + (n.self ? ` <span class="ui mini basic label" style="margin-left: 0.3em;">this node</span>` : "") +
                `<div style="font-family: monospace; font-size: 0.85em; opacity: 0.7;">${esc(n.address)}</div>`;
            let role, health, sync, ver, seen;
            if (!n.reachable && !n.lastRole){
                role = `<span style="opacity: 0.6;">-</span>`;
                health = `<span style="opacity: 0.7;" title="${esc(n.error)}"><i class="grey question circle icon"></i>${esc(n.error || "Unknown")}</span>`;
                sync = ver = seen = `<span style="opacity: 0.6;">-</span>`;
            }else{
                role = rolePill(n.role) + (n.vipHeld ? ` <i class="green map marker alternate icon" title="Holds the floating IP${fips.length > 1 ? "s" : ""}"></i>` : "");
                if (!n.reachable){
                    // Only the role it had when last seen, faded and labelled as history
                    role = `<span style="opacity: 0.45;" title="Role when last seen, ${since(n.lastSeen)} ago">${rolePill(n.lastRole)}</span><div style="font-size: 0.8em; opacity: 0.6;">when last seen</div>`;
                    health = `<i class="red unlink icon"></i>Unreachable <span style="opacity: 0.7;" title="${esc(n.error)}">(last seen ${since(n.lastSeen)} ago)</span>`;
                    sync = seen = ver = `<span style="opacity: 0.6;">-</span>`;
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
