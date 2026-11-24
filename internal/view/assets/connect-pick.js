(function () {
  var boxes = Array.prototype.slice.call(document.querySelectorAll("input[name=account]"));
  var next = document.getElementById("continue");

  function update() {
    next.disabled = !boxes.some(function (box) {
      return box.checked;
    });
  }

  boxes.forEach(function (box) {
    box.addEventListener("change", update);
  });
})();
