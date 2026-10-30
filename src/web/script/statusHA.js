/*
    Status page "High Availability" panel: the cluster overview from
    /api/ha/cluster, drawn by haCluster.js. Shown only while HA is on.
*/
(function(){
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
                $("#zrHARow").toggle(on);
                if (on && window.haRenderCluster){
                    haRenderCluster("#zrHABox", data, {compact: true});
                }
            }
        });
    }
    $(function(){ update(); });
    setTimeout(update, 1500);
    setInterval(update, 10000);
})();
