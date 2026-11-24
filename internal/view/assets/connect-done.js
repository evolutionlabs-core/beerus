(function () {
  var body = document.body;
  var result = JSON.parse(body.dataset.result);

  function succeed() {
    window.parent.postMessage(
      {
        type: "success",
        public_token: result.public_token,
        metadata: {
          link_session_id: result.link_session_id,
          institution: { institution_id: result.institution.id, name: result.institution.name },
          accounts: result.accounts,
        },
      },
      body.dataset.parent
    );
  }

  document.getElementById("done").addEventListener("click", succeed);
  document.getElementById("close").addEventListener("click", succeed);
})();
