/*
    Shows this node's name and high availability role next to the logo when
    HA is on (for example "zrx-b · Standby"). Click opens the High
    Availability page. On a standby with configuration sync on, a second pill
    says settings are read-only there and which node to change them on.
*/
(function(){
    const styles = {
        MASTER: ["Active", "#1a9c3a", "rgba(33,186,69,0.15)", "check circle"],
        BACKUP: ["Standby", "#2185d0", "rgba(33,133,208,0.15)", "pause circle"],
        FAULT:  ["Fault", "#db2828", "rgba(219,40,40,0.15)", "exclamation circle"],
        STOP:   ["keepalived stopped", "#db2828", "rgba(219,40,40,0.15)", "stop circle"]
    };
    function esc(t){ return htmlEscape(t); }

    // On a standby with configuration sync on, a second pill says settings
    // are read-only here and where to change them
    function readOnlyPill(d){
        if (!d.readOnly) return "";
        let active = d.sync && d.sync.activeNode ? d.sync.activeNode : "the active node";
        return `<span style="display:inline-block; margin-left: 0.4em; padding: 0.25em 0.8em; border-radius: 1em; font-weight: 600; font-size: 0.85em; color: #a36b00; background: rgba(251,189,8,0.18);" title="Settings on this node are copied from ${esc(active)} and are read-only here"><i class="lock icon"></i>Read-only · make changes on ${esc(active)}</span>`;
    }

    function update(){
        $.get("/api/ha/status", function(d){
            let badge = $("#haRoleBadge");
            if (!d || d.error != undefined || !d.config || !d.config.enabled){
                badge.hide();
                return;
            }
            let s = styles[d.role.state] || ["HA state unknown", "#767676", "rgba(128,128,128,0.15)", "question circle"];
            let name = d.self >= 0 ? d.config.nodes[d.self].name : "";
            let held = (d.floatingIPs || []).filter(f => f.held);
            let vip = held.length == 0 ? "" : " · holds " + esc(held[0].address) + (held.length > 1 ? " +" + (held.length - 1) : "");
            badge.html(`<span style="display:inline-block; padding: 0.25em 0.8em; border-radius: 1em; font-weight: 600; font-size: 0.85em; color: ${s[1]}; background: ${s[2]};" title="High availability role of this node${name ? " (" + name + ")" : ""}"><i class="${s[3]} icon"></i>${name ? esc(name) + " · " : ""}${s[0]}${vip}</span>` + readOnlyPill(d));
            // inline-flex keeps it on the logo's line
            badge.css({"display": "inline-flex", "align-items": "center", "vertical-align": "middle", "margin-left": "0.8em",
                       "white-space": "nowrap", "overflow": "hidden", "max-width": "calc(100vw - 300px)"});
        });
    }
    $(function(){ setTimeout(update, 800); });
    setInterval(update, 15000);
})();
