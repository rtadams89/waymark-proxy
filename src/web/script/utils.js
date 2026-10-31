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

// The ACL shown for a rule. aclMap: id -> /api/acl/list row.
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

/*
    Unsaved changes

    Save rule: an area with a Save button saves nothing until Save is
    clicked; an area without a Save button applies each change at once.

    wmTrackChanges(root, options) watches the fields of an area that has a
    Save button. Call area.reset() after the area's values are loaded and
    after each successful save; until the first reset the area is never
    reported as changed. While the fields differ from those values, the
    Save button is highlighted and "Unsaved changes" is shown next to it.

    options:
      saveButton  the area's Save button(s) (default: .wmSaveButton in root);
                  a selector string is looked up again on every check, for
                  buttons that are re-rendered (e.g. inline row editors)
      onDiscard   called when the user discards the changes (e.g. reload)
*/
var wmTrackedAreas = [];

(function(){
    if (document.getElementById("wmUnsavedStyle")) return;
    let css = `
        .ui.button.wmUnsaved, .ui.basic.button.wmUnsaved{
            background: #21ba45 !important; color: #fff !important; box-shadow: none !important;
        }
        .ui.button.wmUnsaved i.icon{ color: #fff !important; }
        .wmUnsavedNote{ display: none; color: #c78e00; font-size: 0.9em; margin: 0 0.6em; white-space: nowrap; }
        .wmUnsavedNote.wmShow{ display: inline-block; }
        #wmUnsavedDialog{ position: fixed; inset: 0; z-index: 2000; background: rgba(0,0,0,0.45); display: none; }
        #wmUnsavedDialog .wmDialogBox{
            position: absolute; top: 30%; left: 50%; transform: translate(-50%, -30%);
            width: min(440px, 92vw); padding: 1.4em; border-radius: 0.6em;
            background: var(--theme_bg_primary, #fff); color: var(--text_color, #222);
            box-shadow: 0 8px 24px rgba(0,0,0,0.3);
        }
        #wmUnsavedDialog h3{ margin-top: 0; }
        #wmUnsavedDialog .wmDialogButtons{ text-align: right; margin-top: 1.2em; }
    `;
    let style = document.createElement("style");
    style.id = "wmUnsavedStyle";
    style.textContent = css;
    document.head.appendChild(style);
})();

// The fields of an area that hold values
function wmFields(root){
    root = $(root)[0];
    return $(root).find("input, select, textarea").filter(function(){
        if (this.type == "file" || this.type == "button" || this.type == "submit" || this.type == "search") return false;
        // The typing box of a searchable dropdown is not a value
        if ($(this).hasClass("search") && $(this).closest(".ui.dropdown").length) return false;
        // Fields inside a [data-wm-untracked] element below root belong to
        // another area (or to no area)
        let untracked = $(this).closest("[data-wm-untracked]")[0];
        if (untracked && untracked !== root && $.contains(root, untracked)) return false;
        return true;
    }).get();
}

function wmFieldValue(el){
    return (el.type == "checkbox" || el.type == "radio") ? el.checked : $(el).val();
}

// Field values, plus [data-wm-value] on elements that stand for a value
// without a field (e.g. rows of a list that is saved with the area)
function wmFieldValues(root){
    let r = $(root)[0];
    let values = wmFields(root).map(el => (el.name || el.className || el.id || el.tagName) + "=" + JSON.stringify(wmFieldValue(el)));
    $(r).find("[data-wm-value]").each(function(){
        let untracked = $(this).closest("[data-wm-untracked]")[0];
        if (untracked && untracked !== r && $.contains(r, untracked)) return;
        values.push("item=" + JSON.stringify($(this).attr("data-wm-value")));
    });
    return values.join("\n");
}

// Puts saved values back into fields that still exist
function wmRestoreFields(snapshot){
    snapshot.forEach(function([el, v]){
        if (!document.contains(el)) return;
        if (el.type == "checkbox" || el.type == "radio"){
            el.checked = v;
            return;
        }
        let dd = $(el).closest(".ui.dropdown");
        if (dd.length && (el.type == "hidden" || dd[0] === el || $(el).parent()[0] === dd[0]) && typeof dd.dropdown == "function"){
            if (v == null || v === "" || (Array.isArray(v) && v.length == 0)){
                dd.dropdown("clear");
            }else if (Array.isArray(v) || dd.hasClass("multiple")){
                dd.dropdown("set exactly", Array.isArray(v) ? v : String(v).split(","));
            }else{
                dd.dropdown("set selected", v);
            }
            $(el).val(v);
            return;
        }
        $(el).val(v);
    });
}

function wmTrackChanges(root, options={}){
    let spec = options.saveButton;
    let area = {
        root: $(root)[0],
        saveButton: $(),
        onDiscard: options.onDiscard,
        baseline: null,
        dirty: false
    };
    if (area.root == undefined) return null;

    let findButtons = function(){
        let b = typeof spec == "string" ? $(spec) : (spec ? $(spec) : $(area.root).find(".wmSaveButton"));
        // noteAfter places the note elsewhere, e.g. after an action input
        let anchors = options.noteAfter ? $(options.noteAfter) : b;
        anchors.each(function(){
            if ($(this).next(".wmUnsavedNote").length == 0){
                $(this).after(`<span class="wmUnsavedNote"><i class="circle tiny icon"></i>Unsaved changes</span>`);
            }
        });
        area.saveButton = b;
        area.notes = anchors.next(".wmUnsavedNote");
    };
    findButtons();

    area.check = function(){
        if (typeof spec == "string") findButtons();
        let dirty = area.baseline !== null && wmFieldValues(area.root) !== area.baseline;
        area.dirty = dirty;
        area.saveButton.toggleClass("wmUnsaved", dirty);
        area.notes.toggleClass("wmShow", dirty);
        return dirty;
    };
    area.reset = function(){
        area.baseline = wmFieldValues(area.root);
        area.snapshot = wmFields(area.root).map(el => [el, wmFieldValue(el)]);
        area.check();
    };
    // Drops the changes: the saved values are put back (onDiscard can
    // reload or redraw what the fields alone do not restore). If they cannot
    // all be put back (e.g. rows were added), the area stops tracking until
    // its next reset.
    area.discard = function(){
        if (area.baseline !== null && area.snapshot){
            wmRestoreFields(area.snapshot);
        }
        if (typeof area.onDiscard == "function"){
            area.onDiscard();
        }
        if (area.baseline !== null && wmFieldValues(area.root) !== area.baseline){
            area.baseline = null;
        }
        area.check();
    };

    let pending = null;
    let schedule = function(){
        clearTimeout(pending);
        pending = setTimeout(area.check, 50);
    };
    $(area.root).on("input change", schedule);
    if (window.MutationObserver){
        new MutationObserver(schedule).observe(area.root, {childList: true, subtree: true});
    }
    wmTrackedAreas.push(area);
    return area;
}

function wmScopeContains(scope, el){
    if (scope == undefined) return true;
    let s = $(scope)[0];
    return s != undefined && (s === el || $.contains(s, el));
}

// Reports whether any tracked area inside scope (default: the whole page,
// including same-origin frames) has unsaved changes.
function wmHasUnsavedChanges(scope){
    let found = wmTrackedAreas.some(a => document.contains(a.root) && wmScopeContains(scope, a.root) && a.check());
    if (found) return true;
    let frames = scope == undefined ? $("iframe") : $(scope).find("iframe");
    let inFrame = false;
    frames.each(function(){
        try {
            if (this.contentWindow && typeof this.contentWindow.wmHasUnsavedChanges == "function" && this.contentWindow.wmHasUnsavedChanges()){
                inFrame = true;
                return false;
            }
        } catch(e) {}
    });
    return inFrame;
}

// Discards the unsaved changes of every area inside scope (and its frames).
function wmDiscardChanges(scope){
    wmTrackedAreas.forEach(function(a){
        if (a.dirty && document.contains(a.root) && wmScopeContains(scope, a.root)){
            a.discard();
        }
    });
    let frames = scope == undefined ? $("iframe") : $(scope).find("iframe");
    frames.each(function(){
        try {
            if (this.contentWindow && typeof this.contentWindow.wmDiscardChanges == "function"){
                this.contentWindow.wmDiscardChanges();
            }
        } catch(e) {}
    });
}

// Runs proceed() if nothing inside scope is unsaved; otherwise asks first
// and, if the user discards the changes, runs proceed().
function wmConfirmDiscard(scope, proceed){
    if (!wmHasUnsavedChanges(scope)){
        proceed();
        return;
    }
    let top = window;
    try {
        if (window.parent && window.parent !== window && typeof window.parent.wmShowUnsavedDialog == "function"){
            top = window.parent;
        }
    } catch(e) {}
    top.wmShowUnsavedDialog(function(discard){
        if (discard){
            wmDiscardChanges(scope);
            proceed();
        }
    });
}

function wmShowUnsavedDialog(callback){
    let dialog = $("#wmUnsavedDialog");
    if (dialog.length == 0){
        dialog = $(`<div id="wmUnsavedDialog" role="dialog" aria-modal="true" aria-labelledby="wmUnsavedTitle">
            <div class="wmDialogBox">
                <h3 id="wmUnsavedTitle"><i class="orange exclamation triangle icon"></i>Unsaved changes</h3>
                <p>Some changes have not been saved. Leaving now discards them.</p>
                <div class="wmDialogButtons">
                    <button class="ui basic button wmKeepEditing">Keep editing</button>
                    <button class="ui red button wmDiscard">Discard changes</button>
                </div>
            </div>
        </div>`).appendTo("body");
    }
    let finish = function(discard){
        dialog.hide();
        $(document).off("keydown.wmUnsaved");
        callback(discard);
    };
    dialog.find(".wmKeepEditing").off("click").on("click", () => finish(false));
    dialog.find(".wmDiscard").off("click").on("click", () => finish(true));
    dialog.off("click").on("click", function(e){ if (e.target === this) finish(false); });
    $(document).off("keydown.wmUnsaved").on("keydown.wmUnsaved", function(e){ if (e.key == "Escape") finish(false); });
    dialog.show();
    dialog.find(".wmKeepEditing").focus();
}
