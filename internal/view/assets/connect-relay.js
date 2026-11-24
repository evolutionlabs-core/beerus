(function () {
  var result = JSON.parse(document.body.dataset.result);
  if (window.opener) {
    window.opener.postMessage({ type: "beerus-result", result: result }, window.location.origin);
    window.close();
  } else if (window.parent !== window) {
    window.parent.postMessage({ type: "exit", error: result.error || null }, document.body.dataset.parent);
  }
})();
