/*
    Status page "High Availability" and "Cluster Nodes" panels, from
    /api/ha/cluster: the overview is drawn by haCluster.js, the nodes'
    traffic and resource figures by renderClusterStats. Shown only while HA
    is on.
*/
(function(){
    function fmtBytes(b){
        if (typeof formatByteSize === "function") return formatByteSize(b);
        return b + " B";
    }
    function fmtCount(n){
        return typeof abbreviateNumber === "function" ? abbreviateNumber(n) : String(n);
    }
    function fmtUptime(started){
        if (!started) return "-";
        let s = Math.max(0, Math.floor(Date.now() / 1000) - started);
        let d = Math.floor(s / 86400), h = Math.floor(s % 86400 / 3600), m = Math.floor(s % 3600 / 60);
        return d > 0 ? `${d}d ${h}h` : (h > 0 ? `${h}h ${m}m` : `${m}m`);
    }
    function pct(v){ return (Math.round(v * 10) / 10).toFixed(1) + "%"; }

    // One row per node: role, proxy state, today's requests, errors and
    // bandwidth, connections, CPU, memory and uptime
    function renderClusterStats(data){
        let head = `<table class="ui very basic compact unstackable table" style="margin: 0;"><thead><tr>
            <th>Node</th><th>Role</th><th>Proxy</th><th>Requests today</th><th>Errors</th><th>Bandwidth today</th>
            <th>Connections</th><th>CPU</th><th>Memory</th><th>Uptime</th></tr></thead><tbody>`;
        let rows = (data.nodes || []).map(function(n){
            let name = `<b>${htmlEscape(n.name)}</b>` + (n.self ? ` <span class="ui mini basic label">this node</span>` : "");
            let roles = {MASTER: "Active", BACKUP: "Standby", FAULT: "Fault", STOP: "Stopped"};
            if (!n.reachable){
                return `<tr><td>${name}</td><td colspan="9" style="opacity: 0.7;">${htmlEscape(n.error || "Not reachable")}</td></tr>`;
            }
            let s = n.stats;
            if (!s){
                return `<tr><td>${name}</td><td>${htmlEscape(roles[n.role] || n.role || "-")}</td><td colspan="8" style="opacity: 0.7;">Figures not available: this node runs Waymark ${htmlEscape(n.version || "")}, which does not send them</td></tr>`;
            }
            let errRate = s.requestsToday > 0 ? ` <small style="opacity: 0.7;">(${pct(s.errorsToday / s.requestsToday * 100)})</small>` : "";
            let proxy = s.proxyRunning ? `<i class="green check circle icon"></i>Running` : `<i class="orange exclamation triangle icon"></i>Stopped`;
            return `<tr>
                <td>${name}</td>
                <td>${htmlEscape(roles[n.role] || n.role || "-")}</td>
                <td style="white-space: nowrap;">${proxy}</td>
                <td>${fmtCount(s.requestsToday)}</td>
                <td>${fmtCount(s.errorsToday)}${errRate}</td>
                <td>${fmtBytes(s.bandwidthToday)}</td>
                <td>${s.activeConnections >= 0 ? s.activeConnections : "-"}</td>
                <td>${s.resourcesReady ? pct(s.cpu) : "-"}</td>
                <td>${s.resourcesReady ? pct(s.ram) : "-"}</td>
                <td>${fmtUptime(n.started)}</td>
            </tr>`;
        }).join("");
        $("#zrClusterStatsBox").html(head + rows + `</tbody></table>`);
    }

    function update(){
        if ($("#zrHARow").length == 0 || !$("#status").is(":visible")){
            return;
        }
        $.ajax({
            url: "/api/ha/cluster",
            timeout: 5000,
            success: function(data){
                if (!data || typeof data != "object" || data.error != undefined){
                    return;
                }
                let on = data.enabled && (data.nodes || []).length > 0;
                $("#zrHARow, #zrClusterStatsRow").toggle(on);
                if (on && window.haRenderCluster){
                    haRenderCluster("#zrHABox", data, {compact: true});
                }
                if (on){
                    renderClusterStats(data);
                }
            }
        });
    }
    window.zrRenderClusterStats = renderClusterStats; // for tests
    $(function(){ update(); });
    setTimeout(update, 1500);
    setInterval(update, 10000);
})();
