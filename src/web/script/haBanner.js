/*
    haBanner.js (custom fork)

    Shows this node's name and high availability role next to the logo when
    HA is on (for example "zrx-b · Standby"), so it is clear which node a
    page came from. Click opens the High Availability page.
*/
(function(){
    const styles = {
        MASTER: ["Active", "#1a9c3a", "rgba(33,186,69,0.15)", "check circle"],
        BACKUP: ["Standby", "#2185d0", "rgba(33,133,208,0.15)", "pause circle"],
        FAULT:  ["Fault", "#db2828", "rgba(219,40,40,0.15)", "exclamation circle"],
        STOP:   ["keepalived stopped", "#db2828", "rgba(219,40,40,0.15)", "stop circle"]
    };
    function esc(t){ return $("<div>").text(t).html(); }
    function update(){
        $.get("/api/ha/status", function(d){
            let badge = $("#haRoleBadge");
            if (!d || d.error != undefined || !d.config || !d.config.enabled){
                badge.hide();
                return;
            }
            let s = styles[d.role.state] || ["HA state unknown", "#767676", "rgba(128,128,128,0.15)", "question circle"];
            let name = d.self >= 0 ? d.config.nodes[d.self].name : "";
            let vip = d.vipHeld ? " · holds " + d.config.vip : "";
            badge.html(`<span style="display:inline-block; padding: 0.25em 0.8em; border-radius: 1em; font-weight: 600; font-size: 0.85em; color: ${s[1]}; background: ${s[2]};" title="High availability role of this node${name ? " (" + name + ")" : ""}"><i class="${s[3]} icon"></i>${name ? esc(name) + " · " : ""}${s[0]}${vip}</span>`);
            badge.css("display", "flex").css("align-items", "center");
        });
    }
    $(function(){ setTimeout(update, 800); });
    setInterval(update, 15000);
})();
