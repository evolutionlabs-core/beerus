(function () {
  var origin = new URL(document.currentScript.src).origin;

  function create(options) {
    var overlay = null;
    var frame = null;

    function layout() {
      var small = window.innerWidth < 480;
      frame.style.width = small ? "100%" : "448px";
      frame.style.height = small ? "100%" : "640px";
      frame.style.maxHeight = "100%";
      frame.style.borderRadius = small ? "0" : "12px";
    }

    function close() {
      window.removeEventListener("message", receive);
      window.removeEventListener("resize", layout);
      document.removeEventListener("keydown", keydown);
      overlay.remove();
      overlay = null;
      frame = null;
    }

    function exit(error) {
      close();
      if (options.onExit) options.onExit(error);
    }

    function receive(event) {
      var msg = event.data;
      if (event.origin !== origin || event.source !== frame.contentWindow || !msg) return;
      if (msg.type === "success") {
        close();
        if (options.onSuccess) options.onSuccess(msg.public_token, msg.metadata);
      } else if (msg.type === "exit") {
        exit(msg.error || null);
      }
    }

    function keydown(event) {
      if (event.key === "Escape") exit(null);
    }

    function open() {
      if (overlay) return;
      overlay = document.createElement("div");
      overlay.style.position = "fixed";
      overlay.style.inset = "0";
      overlay.style.zIndex = "2147483000";
      overlay.style.display = "flex";
      overlay.style.alignItems = "center";
      overlay.style.justifyContent = "center";
      overlay.style.background = "rgba(23, 23, 23, 0.45)";
      overlay.style.backdropFilter = "blur(2px)";
      overlay.addEventListener("click", function (event) {
        if (event.target === overlay) exit(null);
      });
      frame = document.createElement("iframe");
      frame.title = "Beerus Connect";
      frame.src = origin + "/link?token=" + encodeURIComponent(options.token) + "&mode=overlay";
      frame.style.border = "0";
      frame.style.background = "#ffffff";
      frame.style.boxShadow = "0 20px 50px rgba(0, 0, 0, 0.18)";
      layout();
      overlay.appendChild(frame);
      document.body.appendChild(overlay);
      window.addEventListener("message", receive);
      window.addEventListener("resize", layout);
      document.addEventListener("keydown", keydown);
    }

    return { open: open };
  }

  window.BeerusConnect = {
    create: create,
    open: function (token, options) {
      var handle = create(Object.assign({ token: token }, options));
      handle.open();
      return handle;
    },
  };
})();
