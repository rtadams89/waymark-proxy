/*
    Utils.js

    This script holds utilities function that is commonly use
    across components

*/

// htmlEscape makes a value safe to insert into HTML text or a quoted attribute
function htmlEscape(value) {
    return String(value == undefined ? "" : value)
        .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
        .replace(/"/g, "&quot;").replace(/'/g, "&#39;");
}

function abbreviateNumber(value) {
    var newValue = value;
    var suffixes = ["", "k", "m", "b", "t"];
    var suffixNum = 0;
    while (newValue >= 1000 && suffixNum < suffixes.length - 1) {
        newValue /= 1000;
        suffixNum++;
    }
    if (value > 1000){
        newValue = newValue.toFixed(2);
    }
    
    return newValue + suffixes[suffixNum];
}

Object.defineProperty(String.prototype, 'capitalize', {
    value: function() {
        return this.charAt(0).toUpperCase() + this.slice(1);
    },
    enumerable: false
});

//Add a new function to jquery for ajax override with csrf token injected
$.cjax = function(payload){
    let requireTokenMethod = ["POST", "PUT", "DELETE"];
    if (requireTokenMethod.includes(payload.method) || requireTokenMethod.includes(payload.type)){
        //csrf token is required
        let csrfToken = document.getElementsByTagName("meta")["zoraxy.csrf.Token"].getAttribute("content");
        payload.headers = {
            "X-CSRF-Token": csrfToken,
        }
    }

    $.ajax(payload);
}

// Custom fork: the ACL shown for a rule. aclMap: id -> /api/acl/list row.
// Allow all is shown only in the details column (inTable).
function aclNameLabel(aclMap, id, inTable){
    if (id == "" || id == undefined) id = "allow-all";
    let acl = aclMap[id];
    if (acl == undefined){
        // The proxy treats an ACL that no longer exists as Allow all
        return `<span title="This ACL no longer exists, so the rule allows all addresses. Choose an ACL in the rule's access settings."><i class="ui orange exclamation triangle icon"></i> <b style="color: #c78e00;">ACL missing: allows all</b></span>`;
    }
    if (acl.id == "allow-all" && !inTable) return "";
    let icon = acl.builtIn ? `<i class="ui grey star icon"></i>` : `<i class="ui green hand paper icon"></i>`;
    let title = `ACL "${acl.name}" (${acl.rules == 1 ? "1 rule" : acl.rules + " rules"}). Edit it on Access > ACLs.`;
    return `<span title="${htmlEscape(title)}">${icon} ${htmlEscape(acl.name)}</span>`;
}
