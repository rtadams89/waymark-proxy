/*
    statusPanels.js (custom fork)

    Lets each status page panel be turned on or off. Panels are located by
    stable element ids inside them, so components/status.html only needs
    this script included. Choices are saved server-side
    (/api/custom/statusPanels) and apply in every browser.

    The server status banner (with Start / Stop) is always shown.
*/
(function(){
    // def: shown when the user has not chosen yet. GeoIP-based panels
    // default to hidden for this fork's internal-only deployment.
    const PANELS = [
        { id: "flowDiagram",      group: "Overview",       label: "Traffic flow diagram",           def: true,  find: () => $("#trafficFlowDiagramWrapper").closest(".statusFlowCol") },
        { id: "networkIO",        group: "Overview",       label: "Realtime network input / output", def: true,  find: () => $("#status .statusNetCol") },
        { id: "visitorLocations", group: "Overview",       label: "Visitor locations (GeoIP map)",  def: false, find: () => $("#worldMapRow").closest(".standardContainer") },
        { id: "cardTotalRequests",  group: "Summary cards", label: "Total requests",       def: true,  find: () => $("#cardTotalRequests").closest(".column") },
        { id: "cardUniqueVisitors", group: "Summary cards", label: "Unique visitors",      def: true,  find: () => $("#cardUniqueVisitors").closest(".column") },
        { id: "cardBandwidth",      group: "Summary cards", label: "Bandwidth",            def: true,  find: () => $("#cardBandwidth").closest(".column") },
        { id: "cardErrorRate",      group: "Summary cards", label: "Error rate",           def: true,  find: () => $("#cardErrorRate").closest(".column") },
        { id: "cardActiveConns",    group: "Summary cards", label: "Active connections",   def: true,  find: () => $("#cardActiveConns").closest(".column") },
        { id: "cardProxyHosts",     group: "Summary cards", label: "Proxy hosts",          def: true,  find: () => $("#cardProxyHosts").closest(".column") },
        { id: "cardUpstreams",      group: "Summary cards", label: "Upstream servers",     def: true,  find: () => $("#cardUpstreams").closest(".column") },
        { id: "cardCountries",      group: "Summary cards", label: "Countries (GeoIP)",    def: false, find: () => $("#cardCountries").closest(".column") },
        { id: "topPaths",         group: "Details",        label: "Top requested paths",            def: true,  find: () => $("#topPathsList").closest(".column") },
        { id: "requestMethods",   group: "Details",        label: "Request methods",                def: true,  find: () => $("#methodChart").closest(".column") },
        { id: "cacheToday",       group: "Details",        label: "Static file cache (today)",      def: true,  find: () => $("#zrCacheTodayCard").closest(".column") },
        { id: "cacheMemory",      group: "Details",        label: "Cache memory use",               def: true,  find: () => $("#zrCacheMemoryCard").closest(".column") },
        { id: "tlsOverview",      group: "Details",        label: "SSL / TLS overview",             def: true,  find: () => $("#certTotal").closest(".column") },
        { id: "systemResources",  group: "Details",        label: "System resources",               def: true,  find: () => $("#cpuUsageBar").closest(".column") }
    ];

    let saved = {};

    function isEnabled(p){
        return Object.prototype.hasOwnProperty.call(saved, p.id) ? saved[p.id] === true : p.def;
    }

    function apply(){
        PANELS.forEach(p => p.find().toggleClass("zrPanelHidden", !isEnabled(p)));

        // Hide rows whose panels are all hidden, and let a lone remaining
        // column in a two column row use the full width
        $("#status > .standardContainer, #status .standardContainer").each(function(){
            let row = $(this);
            if (row.find("#serverstatus").length || row.is("#zrPanelSettings")){
                return;
            }
            let cols = row.find("> .ui.grid > .column, > .statusTopRow > div");
            if (cols.length == 0){
                return;
            }
            let visible = cols.not(".zrPanelHidden");
            if (!row.hasClass("zrPanelHidden") || row.data("zrAutoHidden")){
                row.toggleClass("zrPanelHidden", visible.length == 0);
                row.data("zrAutoHidden", visible.length == 0);
            }
            let grid = row.find("> .ui.two.column.grid");
            cols.removeClass("zrFullWidth");
            if (grid.length && visible.length == 1){
                visible.addClass("zrFullWidth");
            }
        });

        // Charts and the world map measure their container; redraw once visible
        setTimeout(function(){
            try {
                [typeof networkInChart !== "undefined" ? networkInChart : null,
                 typeof networkOutChart !== "undefined" ? networkOutChart : null,
                 typeof methodChart !== "undefined" ? methodChart : null].forEach(c => { if (c) c.resize(); });
                if (typeof worldMap !== "undefined" && worldMap){ worldMap.updateSize(); }
                let flow = document.getElementById("trafficFlowDiagram");
                if (flow && flow.contentWindow){ flow.contentWindow.dispatchEvent(new Event("resize")); }
            } catch (e) {}
        }, 200);
    }

    function renderSettings(){
        if ($("#zrPanelSettings").length){
            return;
        }
        let groups = {};
        PANELS.forEach(p => { (groups[p.group] = groups[p.group] || []).push(p); });
        let html = `<div class="standardContainer nobottom" id="zrPanelSettings" style="display:none;">
            <div class="dashboard-card">
                <h4>Status page panels</h4>
                <p class="cardSubtitle">Choose which panels to show. Saved for all browsers.</p>
                <div class="ui three column stackable grid">`;
        Object.keys(groups).forEach(g => {
            html += `<div class="column"><b>${g}</b>`;
            groups[g].forEach(p => {
                html += `<div style="margin-top: 0.5em;"><div class="ui toggle checkbox zrPanelToggle" data-panel="${p.id}">
                    <input type="checkbox"><label>${p.label}</label></div></div>`;
            });
            html += `</div>`;
        });
        html += `</div>
                <button class="ui basic small button" style="margin-top: 1em;" id="zrPanelReset"><i class="undo icon"></i> Reset to defaults</button>
            </div>
        </div>`;
        $("#serverstatus").closest(".standardContainer").after(html);

        // index.html re-initializes every .ui.checkbox after the components
        // load, which drops any onChange setting, so listen to the input's
        // native change event instead (delegated, survives re-initialization)
        $(document).off("change.zrPanels").on("change.zrPanels", ".zrPanelToggle input", function(){
            if (settingState){
                return; // programmatic "set checked" also fires change; only save real clicks
            }
            let id = $(this).closest(".zrPanelToggle").data("panel");
            saved[id] = this.checked;
            apply();
            save();
        });
        $(".zrPanelToggle").checkbox();
        syncToggles();
        $("#zrPanelReset").on("click", function(){
            saved = {};
            syncToggles();
            apply();
            save();
        });

        // Button next to Start / Stop in the status banner
        $("#serverstatus .poweroptions").prepend(`<button class="ui small basic inverted button" id="zrPanelSettingsBtn" title="Choose which panels to show"><i class="sliders horizontal icon"></i> Panels</button>`);
        $("#zrPanelSettingsBtn").on("click", function(){
            $("#zrPanelSettings").slideToggle(150);
        });
    }

    let settingState = false;
    function syncToggles(){
        settingState = true;
        try {
            $(".zrPanelToggle").each(function(){
                let p = PANELS.find(x => x.id == $(this).data("panel"));
                $(this).checkbox(isEnabled(p) ? "set checked" : "set unchecked");
            });
        } finally {
            settingState = false;
        }
    }

    function save(){
        $.cjax({
            url: "/api/custom/statusPanels",
            method: "POST",
            data: { panels: JSON.stringify(saved) },
            success: function(data){
                if (data && data.error != undefined && typeof msgbox === "function"){
                    msgbox(data.error, false);
                }
            }
        });
    }

    $("<style>").text(`
        #status .zrPanelHidden{ display: none !important; }
        #status .ui.grid > .column.zrFullWidth{ width: 100% !important; padding-left: 0 !important; padding-right: 0 !important; }
    `).appendTo("head");

    // Apply defaults immediately (avoids a flash of hidden panels), then the saved choices
    apply();
    $.get("/api/custom/statusPanels", function(data){
        if (data && data.error == undefined && typeof data === "object"){
            saved = data;
        }
        renderSettings();
        apply();
    }).fail(function(){
        renderSettings();
    });

    // Exposed for debugging and tests
    window.zrStatusPanels = { panels: PANELS.map(p => ({ id: p.id, label: p.label, def: p.def })), isEnabled: id => isEnabled(PANELS.find(p => p.id == id)) };
})();
