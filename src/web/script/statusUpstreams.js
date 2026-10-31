/*
    Fills the status page's "Upstream Health" panel from
    /api/proxy/healthcheck/summary: counts of healthy and down health-checked
    upstreams, a list of those that are down (with the reason and for how
    long), and rules in maintenance mode.
*/
(function(){
    function esc(s){ return htmlEscape(s); }

    function duration(ts){
        let s = Math.max(0, Math.round(Date.now() / 1000 - ts));
        if (s < 60) return s + "s";
        if (s < 3600) return Math.round(s / 60) + " min";
        if (s < 86400) return (s / 3600).toFixed(s < 36000 ? 1 : 0) + " h";
        return Math.round(s / 86400) + " d";
    }

    function update(){
        if ($("#zrHealthRow").length == 0 || !$("#status").is(":visible")){
            return;
        }
        $.ajax({
            url: "/api/proxy/healthcheck/summary",
            timeout: 5000,
            success: function(data){
                if (!data || data.error != undefined){
                    return;
                }
                let statuses = data.statuses || [];
                let down = statuses.filter(s => !s.enabled || !s.receivesTraffic);
                let up = statuses.length - down.length;
                let maint = data.maintenance || [];

                $("#zrHealthUp").text(up);
                $("#zrHealthDown").text(down.length);
                $("#zrHealthMaint").text(maint.length);
                if (data.checkedRules == 0){
                    $("#zrHealthSub").text("No rule has health checks turned on (rule editor → Destinations → Health Checks)");
                }else{
                    $("#zrHealthSub").text(statuses.length + " health-checked destination" + (statuses.length == 1 ? "" : "s") + " on " + data.checkedRules + " rule" + (data.checkedRules == 1 ? "" : "s"));
                }

                let list = $("#zrHealthList");
                if (down.length == 0 && maint.length == 0){
                    list.html(statuses.length > 0
                        ? `<span class="dbstatDelta trendUp"><i class="check circle icon"></i>All health-checked destinations are healthy</span>`
                        : "");
                    return;
                }
                down.sort((a, b) => a.since - b.since);
                let rows = "";
                down.forEach(s => {
                    let what = esc(s.upstream) + (s.label ? ` <span style="opacity: 0.7;">(${esc(s.label)})</span>` : "");
                    let state = s.enabled ? "Held out by another rule" : "Down";
                    rows += `<tr>
                        <td><i class="red heartbeat icon"></i>${esc(s.rule)}</td>
                        <td style="font-family: monospace; word-break: break-all;">${what}</td>
                        <td style="white-space: nowrap;">${state} ${duration(s.since)}</td>
                        <td style="opacity: 0.8;">${esc(s.reason)}</td>
                    </tr>`;
                });
                maint.forEach(m => {
                    let note = m.message ? esc(m.message) : "Default maintenance page";
                    rows += `<tr>
                        <td><i class="orange wrench icon"></i>${esc(m.rule)}</td>
                        <td style="opacity: 0.7;">all requests</td>
                        <td style="white-space: nowrap;">Maintenance ${m.since ? duration(m.since) : ""}</td>
                        <td style="opacity: 0.8;">${note}</td>
                    </tr>`;
                });
                list.html(`<table class="ui very basic compact unstackable table" style="margin-top: 0;">
                    <thead><tr><th>Rule</th><th>Destination</th><th>State</th><th>Reason</th></tr></thead>
                    <tbody>${rows}</tbody></table>`);
            }
        });
    }

    $(function(){ update(); });
    setTimeout(update, 1500);
    setInterval(update, 10000);
})();
