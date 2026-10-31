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

// htmlDecodeEntities turns the character references in text the server
// stored HTML-encoded (e.g. "&#34;") back into characters; the result is
// plain text, to be escaped again when it is put into HTML
function htmlDecodeEntities(value) {
    const named = {amp: "&", lt: "<", gt: ">", quot: '"', apos: "'", nbsp: "\u00a0"};
    return String(value == undefined ? "" : value).replace(/&(#x[0-9a-f]+|#[0-9]+|[a-z]+);/gi, function(m, ref){
        if (ref[0] == "#"){
            let code = ref[1] == "x" || ref[1] == "X" ? parseInt(ref.substr(2), 16) : parseInt(ref.substr(1), 10);
            return code > 0 && code <= 0x10ffff ? String.fromCodePoint(code) : m;
        }
        let c = named[ref.toLowerCase()];
        return c === undefined ? m : c;
    });
}

// jsArg makes a value safe as a JavaScript string argument inside an HTML
// event attribute: onclick="fn(${jsArg(name)})". The browser decodes the
// HTML entities first, which leaves a quoted JavaScript string.
function jsArg(value) {
    return htmlEscape(JSON.stringify(value == undefined ? "" : String(value)));
}

// safeHref returns an escaped link target, or "#" for anything but http(s)
// and mailto links (e.g. a javascript: URL from plugin metadata)
function safeHref(url) {
    url = String(url == undefined ? "" : url).trim();
    return /^(https?:\/\/|mailto:)/i.test(url) ? htmlEscape(url) : "#";
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
        #wmDialog{ position: fixed; inset: 0; z-index: 2000; background: rgba(0,0,0,0.45); display: none; }
        #wmDialog .wmDialogBox{
            position: absolute; top: 30%; left: 50%; transform: translate(-50%, -30%);
            width: min(440px, 92vw); padding: 1.4em; border-radius: 0.6em;
            background: var(--theme_bg_primary, #fff); color: var(--text_color, #222);
            box-shadow: 0 8px 24px rgba(0,0,0,0.3);
        }
        #wmDialog h3{ margin-top: 0; }
        #wmDialog .wmDialogMessage{ white-space: pre-line; }
        #wmDialog .wmDialogInput{ margin-top: 0.6em; }
        #wmDialog .wmDialogButtons{ text-align: right; margin-top: 1.2em; }
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
    // Keyed by position and id or name, not class names (which widgets
    // like Semantic UI's checkbox change when they start)
    let values = wmFields(root).map((el, i) => i + ":" + (el.id || el.name || el.tagName) + "=" + JSON.stringify(wmFieldValue(el)));
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
    // The dialog opens in the top window (wmDialog)
    wmShowUnsavedDialog(function(discard){
        if (discard){
            wmDiscardChanges(scope);
            proceed();
        }
    });
}

/*
    Dialogs (instead of the browser's alert, confirm and prompt, which look
    different and block the page)

    wmDialog(opts, callback) shows one dialog at a time, in the top window
    when this is a same-origin frame. opts:
      title, message (text), icon (Semantic icon classes),
      buttons  [{label, value, cls}] from left to right; the last has focus
      input    {value, placeholder, maxlength} for a text box
      cancel   the value given for Escape or a click outside (default false)
    callback(value, inputText)
*/
function wmTopWindow(){
    try {
        if (window.parent && window.parent !== window && typeof window.parent.wmDialog == "function"){
            return window.parent;
        }
    } catch(e) {}
    return window;
}

function wmDialog(opts, callback){
    let top = wmTopWindow();
    if (top !== window){
        top.wmDialog(opts, callback);
        return;
    }
    callback = typeof callback == "function" ? callback : function(){};
    let dialog = $("#wmDialog");
    if (dialog.length == 0){
        dialog = $(`<div id="wmDialog" role="dialog" aria-modal="true" aria-labelledby="wmDialogTitle">
            <div class="wmDialogBox">
                <h3 id="wmDialogTitle"></h3>
                <p class="wmDialogMessage"></p>
                <div class="ui fluid input wmDialogInput"><input type="text"></div>
                <div class="wmDialogButtons"></div>
            </div>
        </div>`).appendTo("body");
    }
    let cancel = opts.cancel === undefined ? false : opts.cancel;
    let title = dialog.find("#wmDialogTitle").empty();
    if (opts.icon) title.append($("<i>").attr("class", opts.icon + " icon"));
    title.append(document.createTextNode(opts.title || ""));
    title.toggle(!!(opts.title || opts.icon));
    dialog.find(".wmDialogMessage").text(opts.message || "").toggle(!!opts.message);
    let input = dialog.find(".wmDialogInput input");
    dialog.find(".wmDialogInput").toggle(!!opts.input);
    if (opts.input){
        input.val(opts.input.value || "").attr("placeholder", opts.input.placeholder || "").attr("maxlength", opts.input.maxlength || null);
    }
    let finish = function(value){
        dialog.hide();
        $(document).off("keydown.wmDialog");
        callback(value, opts.input ? input.val() : undefined);
    };
    let box = dialog.find(".wmDialogButtons").empty();
    (opts.buttons || [{label: "OK", value: true}]).forEach(function(b){
        $("<button>").addClass("ui button " + (b.cls || "basic")).text(b.label).on("click", () => finish(b.value)).appendTo(box);
    });
    dialog.off("click").on("click", function(e){ if (e.target === this) finish(cancel); });
    $(document).off("keydown.wmDialog").on("keydown.wmDialog", function(e){
        if (e.key == "Escape") finish(cancel);
        if (e.key == "Enter" && opts.input && document.activeElement === input[0]) finish(opts.buttons ? opts.buttons[opts.buttons.length - 1].value : true);
    });
    dialog.show();
    if (opts.input){
        input.trigger("focus").trigger("select");
    }else{
        box.find("button").last().trigger("focus");
    }
}

// Asks a yes/no question: callback(true) if confirmed. opts: title,
// okText, danger (red button), cancelText
function wmConfirm(message, callback, opts={}){
    wmDialog({
        title: opts.title || "",
        icon: opts.danger ? "orange exclamation triangle" : "",
        message: message,
        buttons: [
            {label: opts.cancelText || "Cancel", value: false},
            {label: opts.okText || "OK", value: true, cls: opts.danger ? "red" : "green"}
        ]
    }, function(v){ if (typeof callback == "function") callback(v === true); });
}

// Shows a message with an OK button. opts: title, error (red icon)
function wmAlert(message, callback, opts={}){
    wmDialog({
        title: opts.title || (opts.error ? "Error" : ""),
        icon: opts.error ? "red times circle" : "",
        message: String(message),
        buttons: [{label: "OK", value: true, cls: "basic"}],
        cancel: true
    }, function(){ if (typeof callback == "function") callback(); });
}

// Asks for a line of text: callback(text), or callback(null) if cancelled
function wmPrompt(message, defaultValue, callback, opts={}){
    wmDialog({
        title: opts.title || "",
        message: message,
        input: {value: defaultValue || "", placeholder: opts.placeholder || "", maxlength: opts.maxlength},
        buttons: [
            {label: "Cancel", value: false},
            {label: opts.okText || "OK", value: true, cls: "green"}
        ]
    }, function(v, text){ if (typeof callback == "function") callback(v === true ? text : null); });
}

function wmShowUnsavedDialog(callback){
    wmDialog({
        title: "Unsaved changes",
        icon: "orange exclamation triangle",
        message: "Some changes have not been saved. Leaving now discards them.",
        buttons: [
            {label: "Keep editing", value: false, cls: "basic wmKeepEditing"},
            {label: "Discard changes", value: true, cls: "red wmDiscard"}
        ]
    }, function(v){ callback(v === true); });
}

