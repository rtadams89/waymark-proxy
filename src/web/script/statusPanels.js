/*
    Status page panels: each panel (a .zrTile in #zrPanelGrid) can be shown
    or hidden and moved. The choices are saved server-side
    (/api/custom/statusPanels, stored in conf/ui.json, which HA config sync
    copies to the other nodes) and apply in every browser.

    Customize mode: drag a panel (or use its arrow buttons) to move it, its
    width buttons to make it narrower or wider, and its eye button to hide
    or show it. Every change is saved at once.
*/
(function(){
    // def: shown when the user has not chosen yet. The default order is the
    // order of the tiles in components/status.html.
    // sizes: the widths a panel offers (its default is data-size in the markup)
    const CARD = ["quarter", "third", "half"];
    const WIDE = ["half", "full"];
    const DETAIL = ["third", "half", "full"];
    const PANELS = [
        { id: "flowDiagram",        group: "Overview",      label: "Traffic flow diagram",            def: true,  sizes: ["half", "twothirds", "full"] },
        { id: "networkIO",          group: "Overview",      label: "Realtime network input / output", def: true,  sizes: ["third", "half", "full"] },
        { id: "visitorLocations",   group: "Overview",      label: "Visitor locations (GeoIP map)",   def: false, sizes: WIDE },
        { id: "cardTotalRequests",  group: "Summary cards", label: "Total requests",                  def: true,  sizes: CARD },
        { id: "cardUniqueVisitors", group: "Summary cards", label: "Unique visitors",                 def: true,  sizes: CARD },
        { id: "cardBandwidth",      group: "Summary cards", label: "Bandwidth",                       def: true,  sizes: CARD },
        { id: "cardErrorRate",      group: "Summary cards", label: "Error rate",                      def: true,  sizes: CARD },
        { id: "cardActiveConns",    group: "Summary cards", label: "Active connections",              def: true,  sizes: CARD },
        { id: "cardProxyHosts",     group: "Summary cards", label: "Proxy rules",                     def: true,  sizes: CARD },
        { id: "cardUpstreams",      group: "Summary cards", label: "Destination servers",             def: true,  sizes: CARD },
        { id: "cardCountries",      group: "Summary cards", label: "Countries (GeoIP)",               def: false, sizes: CARD },
        { id: "haCluster",          group: "Details",       label: "High availability (when HA is on)", def: true, sizes: WIDE },
        { id: "clusterStats",       group: "Details",       label: "Cluster nodes (when HA is on)",   def: true,  sizes: WIDE },
        { id: "upstreamHealth",     group: "Details",       label: "Destination health",              def: true,  sizes: WIDE },
        { id: "cacheToday",         group: "Details",       label: "Static file cache (today)",       def: true,  sizes: DETAIL },
        { id: "cacheMemory",        group: "Details",       label: "Cache memory use",                def: true,  sizes: DETAIL },
        { id: "topPaths",           group: "Details",       label: "Top requested paths",             def: true,  sizes: DETAIL },
        { id: "requestMethods",     group: "Details",       label: "Request methods",                 def: true,  sizes: DETAIL },
        { id: "tlsOverview",        group: "Details",       label: "SSL / TLS overview",              def: true,  sizes: DETAIL },
        { id: "systemResources",    group: "Details",       label: "System resources and uptime",     def: true,  sizes: DETAIL }
    ];
    const SIZE_LABELS = { quarter: "¼", third: "⅓", half: "½", twothirds: "⅔", full: "Full" };
    const SIZE_TITLES = { quarter: "A quarter of the row", third: "A third of the row", half: "Half the row", twothirds: "Two thirds of the row", full: "The whole row" };
    const DEFAULT_ORDER = PANELS.map(p => p.id);

    let visible = {};   // panel id -> true/false (only panels the user changed)
    let order = [];     // saved panel order (may miss panels added later)
    let sizes = {};     // panel id -> width (only panels the user changed)
    let defaultSizes = {}; // panel id -> width in the markup
    let customizing = false;
    let dragged = null;

    function panel(id){ return PANELS.find(p => p.id == id); }
    function grid(){ return $("#zrPanelGrid"); }
    function tile(id){ return grid().children(".zrTile").filter(function(){ return $(this).attr("data-panel") == id; }); }

    function sizeOf(p){
        let s = sizes[p.id];
        return (s && p.sizes.includes(s)) ? s : defaultSizes[p.id];
    }

    function isEnabled(p){
        return Object.prototype.hasOwnProperty.call(visible, p.id) ? visible[p.id] === true : p.def;
    }

    // The saved order with unknown and repeated ids dropped, and panels it
    // does not list (e.g. added in a later version) placed after the panel
    // they follow by default
    function fullOrder(){
        let list = order.filter((id, i) => panel(id) && order.indexOf(id) == i);
        DEFAULT_ORDER.forEach(function(id, i){
            if (list.includes(id)) return;
            let pos = 0;
            for (let j = i - 1; j >= 0; j--){
                let k = list.indexOf(DEFAULT_ORDER[j]);
                if (k >= 0){ pos = k + 1; break; }
            }
            list.splice(pos, 0, id);
        });
        return list;
    }

    function currentOrder(){
        return grid().children(".zrTile").map(function(){ return $(this).attr("data-panel"); }).get();
    }

    function redrawCharts(){
        setTimeout(function(){
            try {
                [typeof networkInChart !== "undefined" ? networkInChart : null,
                 typeof networkOutChart !== "undefined" ? networkOutChart : null,
                 typeof methodChart !== "undefined" ? methodChart : null].forEach(c => { if (c) c.resize(); });
                if (typeof resizeWorldMap === "function"){ resizeWorldMap(); }
                let flow = document.getElementById("trafficFlowDiagram");
                if (flow && flow.contentWindow){ flow.contentWindow.dispatchEvent(new Event("resize")); }
            } catch (e) {}
        }, 200);
    }

    function apply(){
        let wanted = fullOrder();
        if (wanted.join(",") != currentOrder().join(",")){
            wanted.forEach(id => grid().append(tile(id)));
        }
        PANELS.forEach(function(p){
            let t = tile(p.id);
            if (t.length && sizeOf(p)) t.attr("data-size", sizeOf(p));
            t.find(".zrTileSizeBtn").each(function(){ $(this).toggleClass("active", $(this).attr("data-size") == sizeOf(p)); });
            t.toggleClass("zrPanelHidden", !isEnabled(p));
            t.find(".zrTileVisBtn i").attr("class", isEnabled(p) ? "eye icon" : "eye slash icon");
            t.find(".zrTileVisBtn").attr("title", isEnabled(p) ? "Hide this panel" : "Show this panel");
        });
        syncToggles();
        redrawCharts();
    }

    /* Saving: every change is saved at once */

    let saveTimer = null;
    function save(){
        clearTimeout(saveTimer);
        saveTimer = setTimeout(function(){
            $.cjax({
                url: "/api/custom/statusPanels",
                method: "POST",
                data: { panels: JSON.stringify(visible), order: JSON.stringify(order), sizes: JSON.stringify(sizes) },
                success: function(data){
                    if (data && data.error != undefined && typeof msgbox === "function"){
                        msgbox(data.error, false);
                    }
                },
                error: function(){
                    if (typeof msgbox === "function") msgbox("The panel layout could not be saved", false);
                }
            });
        }, 300);
    }

    function setVisible(id, on){
        visible[id] = on;
        apply();
        save();
    }

    function setSize(id, size){
        if (size == defaultSizes[id]){
            delete sizes[id];
        }else{
            sizes[id] = size;
        }
        apply();
        save();
    }

    function orderChanged(){
        order = currentOrder();
        save();
        redrawCharts();
    }

    /* Customize mode */

    function addOverlays(){
        grid().children(".zrTile").each(function(){
            let t = $(this);
            if (t.children(".zrTileOverlay").length) return;
            let p = panel(t.attr("data-panel"));
            if (!p) return;
            let sizeBtns = p.sizes.map(sz => `<button class="ui mini basic button zrTileSizeBtn" data-size="${sz}" title="Width: ${SIZE_TITLES[sz]}">${SIZE_LABELS[sz]}</button>`).join("");
            t.append(`<div class="zrTileOverlay" draggable="true" title="Drag to move">
                <span class="zrTileName"><i class="grip vertical icon"></i>${htmlEscape(p.label)}</span>
                <span class="zrTileButtons">
                    <span class="ui mini buttons zrTileSizes">${sizeBtns}</span>
                    <button class="ui mini basic icon button zrTileMoveBtn" data-dir="-1" title="Move earlier"><i class="arrow left icon"></i></button>
                    <button class="ui mini basic icon button zrTileMoveBtn" data-dir="1" title="Move later"><i class="arrow right icon"></i></button>
                    <button class="ui mini basic icon button zrTileVisBtn"><i class="eye icon"></i></button>
                </span>
            </div>`);
        });
    }

    function setCustomizing(on){
        customizing = on;
        grid().toggleClass("zrCustomizing", on);
        $("#zrPanelSettings").toggle(on);
        $("#zrPanelSettingsBtn").toggleClass("active", on).html(on ? `<i class="green check icon"></i> Done` : `<i class="sliders horizontal icon"></i> Customize`);
        if (on){
            addOverlays();
        }else{
            grid().children(".zrTile").children(".zrTileOverlay").remove();
        }
        apply();
    }

    function bindCustomizeEvents(){
        let g = grid();
        g.on("click", ".zrTileMoveBtn", function(e){
            e.preventDefault();
            let t = $(this).closest(".zrTile");
            let dir = parseInt($(this).attr("data-dir"));
            if (dir < 0 && t.prev(".zrTile").length){
                t.insertBefore(t.prev(".zrTile"));
            }else if (dir > 0 && t.next(".zrTile").length){
                t.insertAfter(t.next(".zrTile"));
            }else{
                return;
            }
            orderChanged();
            $(this).trigger("focus");
        });
        g.on("click", ".zrTileSizeBtn", function(e){
            e.preventDefault();
            setSize($(this).closest(".zrTile").attr("data-panel"), $(this).attr("data-size"));
        });
        g.on("click", ".zrTileVisBtn", function(e){
            e.preventDefault();
            let id = $(this).closest(".zrTile").attr("data-panel");
            setVisible(id, !isEnabled(panel(id)));
        });

        // Drag and drop: the dragged panel moves as the pointer passes over
        // the others, before or after one by which half the pointer is in
        g.on("dragstart", ".zrTileOverlay", function(e){
            dragged = $(this).closest(".zrTile");
            dragged.addClass("zrDragging");
            let dt = e.originalEvent.dataTransfer;
            if (dt){
                dt.effectAllowed = "move";
                try { dt.setData("text/plain", dragged.attr("data-panel")); } catch (x) {}
            }
        });
        g.on("dragover", ".zrTile", function(e){
            if (!dragged) return;
            e.preventDefault();
            let t = $(this);
            if (t.is(dragged)) return;
            let rect = this.getBoundingClientRect();
            let ev = e.originalEvent;
            let fullRow = rect.width > g[0].getBoundingClientRect().width * 0.9;
            let after = fullRow ? (ev.clientY > rect.top + rect.height / 2) : (ev.clientX > rect.left + rect.width / 2);
            if (after){
                if (!t.next().is(dragged)) dragged.insertAfter(t);
            }else{
                if (!t.prev().is(dragged)) dragged.insertBefore(t);
            }
        });
        g.on("drop", ".zrTile", function(e){ e.preventDefault(); });
        g.on("dragend", ".zrTileOverlay", function(){
            if (!dragged) return;
            dragged.removeClass("zrDragging");
            dragged = null;
            orderChanged();
        });
    }

    /* Settings card (shown in customize mode) */

    let settingState = false;
    function syncToggles(){
        settingState = true;
        try {
            $(".zrPanelToggle").each(function(){
                let p = panel($(this).data("panel"));
                $(this).checkbox(isEnabled(p) ? "set checked" : "set unchecked");
            });
        } finally {
            settingState = false;
        }
    }

    function renderSettings(){
        if ($("#zrPanelSettings").length){
            return;
        }
        let groups = {};
        PANELS.forEach(p => { (groups[p.group] = groups[p.group] || []).push(p); });
        let html = `<div class="standardContainer nobottom" id="zrPanelSettings" style="display:none;">
            <div class="dashboard-card">
                <h4>Customize panels</h4>
                <p class="cardSubtitle">Drag a panel below, or use its arrows, to move it, and its width buttons to resize it. Turn panels on or off here or with their eye button. Changes are saved at once for every browser and copied to the other nodes by HA config sync.</p>
                <div class="ui three column stackable grid">`;
        Object.keys(groups).forEach(g => {
            html += `<div class="column"><b>${g}</b>`;
            groups[g].forEach(p => {
                html += `<div style="margin-top: 0.5em;"><div class="ui toggle checkbox zrPanelToggle" data-panel="${p.id}">
                    <input type="checkbox"><label>${htmlEscape(p.label)}</label></div></div>`;
            });
            html += `</div>`;
        });
        html += `</div>
                <button class="ui basic small button" style="margin-top: 1em;" id="zrPanelReset"><i class="undo icon"></i> Reset to the default layout</button>
            </div>
        </div>`;
        grid().before(html);

        // The input's native change event (delegated, survives
        // re-initialization of .ui.checkbox)
        $(document).off("change.zrPanels").on("change.zrPanels", ".zrPanelToggle input", function(){
            if (settingState){
                return; // a programmatic "set checked" also fires change
            }
            setVisible($(this).closest(".zrPanelToggle").data("panel"), this.checked);
        });
        $(".zrPanelToggle").checkbox();
        $("#zrPanelReset").on("click", function(){
            visible = {};
            order = [];
            sizes = {};
            apply();
            save();
        });

        $("#zrPanelHeaderButtons").html(`<button class="ui basic button" id="zrPanelSettingsBtn" title="Show, hide and move the panels"><i class="sliders horizontal icon"></i> Customize</button>`);
        $("#zrPanelSettingsBtn").on("click", function(){ setCustomizing(!customizing); });
        syncToggles();
    }

    $("<style>").text(`
        #status .zrPanelHidden{ display: none !important; }
        #status #zrPanelGrid.zrCustomizing .zrTile.zrPanelHidden{ display: flex !important; opacity: 0.4; }
        #status #zrPanelGrid.zrCustomizing .zrTile{ min-height: 6em; }
        #status .zrTileOverlay{
            position: absolute; inset: 0; z-index: 5;
            border: 2px dashed var(--theme_green, #21ba45); border-radius: 1em;
            background: rgba(127, 127, 127, 0.12);
            display: flex; justify-content: space-between; align-items: flex-start; gap: 0.5em;
            padding: 0.6em; cursor: grab;
        }
        #status .zrTileOverlay .zrTileName{
            background: var(--theme_bg_primary); color: var(--text_color);
            border-radius: 0.5em; padding: 0.3em 0.6em; font-weight: 600;
            box-shadow: 0 1px 3px rgba(0,0,0,0.2);
        }
        #status .zrTileOverlay .zrTileButtons{ white-space: nowrap; display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 0.2em; }
        #status .zrTileOverlay .zrTileSizes{ margin-right: 0.3em; }
        #status .zrTileOverlay .zrTileSizes .button{ padding: 0.5em 0.6em !important; }
        #status .zrTileOverlay .zrTileSizes .button.active{ background: var(--theme_green, #21ba45) !important; color: #fff !important; }
        #status .zrTileOverlay .zrTileButtons .button{ background: var(--theme_bg_primary) !important; margin: 0 0 0 0.2em; }
        #status .zrTile.zrDragging{ opacity: 0.35; }
    `).appendTo("head");

    grid().children(".zrTile").each(function(){ defaultSizes[$(this).attr("data-panel")] = $(this).attr("data-size"); });
    apply();
    bindCustomizeEvents();
    $.get("/api/custom/statusPanels", function(data){
        if (data && data.error == undefined && typeof data === "object"){
            visible = (data.visible && typeof data.visible === "object") ? data.visible : {};
            order = Array.isArray(data.order) ? data.order : [];
            sizes = (data.sizes && typeof data.sizes === "object") ? data.sizes : {};
        }
        renderSettings();
        apply();
    }).fail(function(){
        renderSettings();
    });

    // Exposed for debugging and tests
    window.zrStatusPanels = {
        panels: PANELS.map(p => ({ id: p.id, label: p.label, def: p.def })),
        isEnabled: id => isEnabled(panel(id)),
        order: () => currentOrder(),
        customize: on => setCustomizing(on !== false)
    };
})();
