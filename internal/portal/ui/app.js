(function () {
  "use strict";

  var ssidSel = document.getElementById("ssid");
  var identityField = document.getElementById("identity-field");
  var identityInput = document.getElementById("identity");
  var passInput = document.getElementById("passphrase");
  var passLabel = document.getElementById("passphrase-label");
  var form = document.getElementById("connect-form");
  var submit = document.getElementById("submit");
  var refresh = document.getElementById("refresh");
  var status = document.getElementById("status");

  var networks = [];

  function setStatus(msg, kind) {
    status.textContent = msg || "";
    status.className = kind || "";
  }

  function selected() {
    for (var i = 0; i < networks.length; i++) {
      if (networks[i].ssid === ssidSel.value) return networks[i];
    }
    return null;
  }

  // Show the username field only for enterprise (802.1X) networks; hide the
  // password field for open networks.
  function syncFields() {
    var net = selected();
    var sec = net ? net.security : "none";
    var enterprise = sec === "enterprise";
    var open = sec === "none";
    identityField.hidden = !enterprise;
    passInput.parentNode.style.display = open ? "none" : "";
    passLabel.style.display = open ? "none" : "";
  }

  function loadNetworks() {
    setStatus("Scanning for networks…", "busy");
    submit.disabled = true;
    fetch("/networks")
      .then(function (r) {
        if (!r.ok) throw new Error("scan failed (" + r.status + ")");
        return r.json();
      })
      .then(function (list) {
        networks = Array.isArray(list) ? list : [];
        ssidSel.innerHTML = "";
        if (networks.length === 0) {
          var opt = document.createElement("option");
          opt.textContent = "No networks found";
          opt.disabled = true;
          ssidSel.appendChild(opt);
          setStatus("No networks found. Try rescanning.", "error");
          return;
        }
        networks.forEach(function (net) {
          var opt = document.createElement("option");
          opt.value = net.ssid;
          opt.textContent = net.ssid + (net.security !== "none" ? " 🔒" : "");
          ssidSel.appendChild(opt);
        });
        syncFields();
        setStatus("", "");
        submit.disabled = false;
      })
      .catch(function (err) {
        setStatus(err.message, "error");
      });
  }

  ssidSel.addEventListener("change", syncFields);
  refresh.addEventListener("click", loadNetworks);

  form.addEventListener("submit", function (e) {
    e.preventDefault();
    var net = selected();
    if (!net) {
      setStatus("Select a network first.", "error");
      return;
    }
    var body = {
      ssid: net.ssid,
      identity: identityField.hidden ? "" : identityInput.value,
      passphrase: net.security === "none" ? "" : passInput.value,
    };
    submit.disabled = true;
    setStatus("Connecting to " + net.ssid + "… The device will leave this network.", "busy");
    fetch("/connect", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    })
      .then(function (r) {
        if (!r.ok) throw new Error("connect request failed (" + r.status + ")");
        setStatus(
          "Credentials submitted. If the connection succeeds, this portal will close. " +
            "If it doesn't, rejoin “" + net.ssid + "” setup and try again.",
          "ok"
        );
      })
      .catch(function (err) {
        submit.disabled = false;
        setStatus(err.message, "error");
      });
  });

  loadNetworks();
})();
