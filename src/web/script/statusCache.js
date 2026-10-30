/*
    Fills the status page's "Static File Cache (Today)" and "Cache Memory"
    panels from /api/proxy/assetcache/summary. Today's counters cover every
    host with caching on and reset at local midnight.
*/
(function(){
    // Bandwidth uses the status page's own formatter
    function bytes(n){
        if (typeof formatByteSize === "function"){
            return formatByteSize(n);
        }
        return memBytes(n);
    }

    // Memory figures use 1024-based units
    function memBytes(n){
        const u = ["B","KB","MB","GB","TB"];
        let i = 0;
        n = n || 0;
        while (n >= 1024 && i < u.length - 1){ n /= 1024; i++; }
        return (i == 0 ? n : (n < 10 ? n.toFixed(1) : Math.round(n))) + " " + u[i];
    }

    function pct(rate){
        return rate < 0 ? "-" : (rate * 100).toFixed(rate >= 0.995 || rate == 0 ? 0 : 1) + "%";
    }

    function setDelta(id, cls, text){
        $("#" + id).attr("class", "dbstatDelta" + (cls ? " " + cls : "")).text(text);
    }

    // Hit rate change is shown in percentage points
    function hitRateDelta(today, yesterday){
        if (yesterday < 0){
            setDelta("zrCacheHitRateDelta", "", "No data from yesterday");
            return;
        }
        if (today < 0){
            setDelta("zrCacheHitRateDelta", "", "Yesterday " + pct(yesterday));
            return;
        }
        let pts = (today - yesterday) * 100;
        let arrow = pts >= 0 ? "▲" : "▼";
        setDelta("zrCacheHitRateDelta", pts >= 0 ? "trendUp" : "trendDown",
            arrow + " " + Math.abs(pts).toFixed(1) + " pts vs yesterday (" + pct(yesterday) + ")");
    }

    function savedDelta(today, yesterday){
        if (yesterday <= 0){
            setDelta("zrCacheSavedDelta", "", "No data from yesterday");
            return;
        }
        if (typeof renderTrendDelta === "function"){
            renderTrendDelta("zrCacheSavedDelta", today, yesterday);
        }else{
            setDelta("zrCacheSavedDelta", "", "Yesterday " + bytes(yesterday));
        }
    }

    function update(){
        if ($("#zrCacheRow").length == 0 || !$("#status").is(":visible")){
            return;
        }
        $.ajax({
            url: "/api/proxy/assetcache/summary",
            timeout: 5000,
            success: function(data){
                if (!data || data.error != undefined){
                    return;
                }
                let t = data.today, y = data.yesterday;
                let served = t.hits + t.revalidated;
                let fetched = t.misses + t.uncacheable;

                $("#zrCacheHitRate").text(pct(data.todayHitRate));
                $("#zrCacheSaved").text(bytes(t.bytesServed));
                hitRateDelta(data.todayHitRate, data.yesterdayHitRate);
                savedDelta(t.bytesServed, y.bytesServed);
                if (data.cachedHosts == 0){
                    $("#zrCacheTodaySub").text("No host has caching turned on (rule editor → Caching)");
                }else{
                    $("#zrCacheTodaySub").text("Static file requests on " + data.cachedHosts + " host" + (data.cachedHosts == 1 ? "" : "s") + " with caching on");
                }
                $("#zrCacheTodayDetail").text(served.toLocaleString() + " served from cache · " + fetched.toLocaleString() + " fetched from destinations");

                let u = data.usage;
                let full = u.maxBytes > 0 ? u.bytes / u.maxBytes : 0;
                $("#zrCacheFull").text(full > 0 && full < 0.01 ? "<1%" : Math.round(full * 100) + "%");
                $("#zrCacheUsed").text(memBytes(u.bytes));
                $("#zrCacheFiles").text((u.entries || 0).toLocaleString());
                $("#zrCacheBar").progress({ percent: Math.min(100, full * 100), showActivity: false })
                    .toggleClass("red", full >= 0.95).toggleClass("yellow", full >= 0.8 && full < 0.95).toggleClass("teal", full < 0.8);
                $("#zrCacheMemoryDetail").text(memBytes(u.bytes) + " of " + memBytes(u.maxBytes) + " limit" +
                    (full >= 0.95 ? " · full, least recently used files are being dropped" : ""));
            }
        });
    }

    $(function(){ update(); });
    setTimeout(update, 1500);
    setInterval(update, 10000);
})();
