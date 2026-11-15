// Landing page behaviour: billing-cycle toggle and a shortcut for signed-in visitors.
"use strict";

(function () {
  // Monthly / yearly prices are both in the markup; the section attribute picks one.
  const pricing = document.getElementById("harga");
  document.querySelectorAll(".cycle button").forEach((btn) => {
    btn.addEventListener("click", () => {
      pricing.dataset.cycle = btn.dataset.set;
      document.querySelectorAll(".cycle button").forEach((b) => b.classList.toggle("on", b === btn));
    });
  });

  // Visitors who are already signed in go straight to the dashboard.
  let signedIn = false;
  try { signedIn = !!localStorage.getItem("bm_token"); } catch { /* storage blocked */ }
  if (!signedIn) return;
  document.querySelectorAll("[data-auth]").forEach((a) => {
    if (a.dataset.plan) { a.href = "/app#/billing"; return; }
    a.href = "/app";
    if (a.dataset.auth === "login") a.textContent = "Buka dashboard";
    else if (a.closest(".top")) a.hidden = true;
  });
})();
