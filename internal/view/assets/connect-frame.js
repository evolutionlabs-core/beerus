(function () {
  var body = document.body;
  var overlay = body.dataset.overlay === "true";
  var fixed = body.dataset.fixed === "true";
  var parentOrigin = body.dataset.parent;
  var screens = ["list", "success", "down"];
  var forms = Array.prototype.slice.call(document.querySelectorAll("form[data-bank]"));
  var search = document.getElementById("search");
  var none = document.getElementById("none");
  var blocked = document.getElementById("blocked");
  var popup = null;
  var chosen = null;
  var finished = null;

  function show(name) {
    screens.forEach(function (id) {
      var el = document.getElementById(id);
      if (el) el.hidden = id !== name;
    });
  }

  function tell(message) {
    window.parent.postMessage(message, parentOrigin);
  }

  function succeed() {
    tell({
      type: "success",
      public_token: finished.public_token,
      metadata: {
        link_session_id: finished.link_session_id,
        institution: { institution_id: finished.institution.id, name: finished.institution.name },
        accounts: finished.accounts,
      },
    });
  }

  function receive(event) {
    var msg = event.data;
    if (event.origin !== window.location.origin || event.source !== popup || !msg || msg.type !== "beerus-result") return;
    var result = msg.result;
    if (result.public_token) {
      finished = result;
      document.getElementById("bank-ok").textContent = result.institution.name;
      show("success");
    } else if (result.error.code === "institution_down") {
      document.getElementById("bank-down").textContent = chosen.dataset.name;
      show("down");
    } else {
      tell({ type: "exit", error: result.error });
    }
  }

  function filter() {
    var query = search.value.trim();
    var shown = 0;
    forms.forEach(function (form) {
      var match = form.dataset.name.toLowerCase().indexOf(query.toLowerCase()) >= 0;
      form.hidden = !match;
      if (match) shown++;
    });
    document.getElementById("query").textContent = query;
    none.hidden = shown > 0;
  }

  function openBank(event) {
    if (!overlay) return;
    chosen = event.currentTarget;
    popup = window.open("", "beerus_bank", "popup,width=480,height=720");
    blocked.hidden = popup !== null;
    if (!popup) event.preventDefault();
  }

  search.addEventListener("input", filter);
  forms.forEach(function (form) {
    form.addEventListener("submit", openBank);
  });

  if (!overlay) return;

  window.addEventListener("message", receive);
  document.getElementById("close").addEventListener("click", function (event) {
    event.preventDefault();
    if (finished) succeed();
    else tell({ type: "exit", error: null });
  });
  document.getElementById("done").addEventListener("click", succeed);
  document.getElementById("retry").addEventListener("click", function () {
    chosen.requestSubmit();
  });
  document.getElementById("other").hidden = fixed;
  document.getElementById("other").addEventListener("click", function () {
    show("list");
  });
  if (fixed && forms.length === 1) forms[0].requestSubmit();
})();
