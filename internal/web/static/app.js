// Live updates for the fleet app. Pages work without it; it only adds what the stream says.
// A page marks its stream with data-live. A run page (data-run) appends timeline and log entries;
// the board and inbox (data-refresh) fetch themselves again and swap the refreshed region in.
(function () {
  "use strict";
  var live = document.querySelector("[data-live]");
  if (!live || !window.EventSource) {
    if (live) live.textContent = "Live updates need a newer browser. Reload to refresh.";
    return;
  }
  var region = document.querySelector("[data-refresh]");
  var timer = null;

  function refresh() {
    if (!region) return;
    fetch(location.href, { credentials: "same-origin" })
      .then(function (r) { return r.text(); })
      .then(function (html) {
        var doc = new DOMParser().parseFromString(html, "text/html");
        var next = doc.getElementById(region.id);
        if (next) region.replaceChildren.apply(region, Array.prototype.slice.call(next.childNodes));
      })
      .catch(function () { live.textContent = "Could not refresh. Retrying on the next event."; });
  }

  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text) n.textContent = text;
    return n;
  }

  function appendRun(ev) {
    var tl = document.querySelector("[data-timeline]");
    if (tl) {
      var ol = tl.querySelector("ol.ui-timeline");
      if (!ol) {
        tl.replaceChildren();
        ol = el("ol", "ui-timeline");
        ol.setAttribute("aria-label", "Run timeline");
        tl.appendChild(ol);
      }
      var li = el("li");
      if (ev.timeline.Status) li.setAttribute("data-status", ev.timeline.Status);
      li.appendChild(el("time", "", ev.timeline.Time));
      li.appendChild(el("p", "ui-timeline-title", ev.timeline.Title));
      if (ev.timeline.Body) li.appendChild(el("p", "", ev.timeline.Body));
      ol.appendChild(li);
    }
    var log = document.querySelector("[data-log] .ui-log");
    if (log) {
      if (log.getAttribute("data-started") !== "1") {
        log.replaceChildren();
        log.setAttribute("data-started", "1");
      }
      var line = el("span", "ui-log-line");
      if (ev.log.Status) line.setAttribute("data-status", ev.log.Status);
      line.appendChild(el("time", "", ev.log.Time));
      line.appendChild(document.createTextNode(" " + ev.log.Text));
      log.appendChild(line);
      log.appendChild(document.createTextNode("\n"));
      log.scrollTop = log.scrollHeight;
    }
  }

  var src = new EventSource(live.getAttribute("data-live"));
  src.onopen = function () { live.textContent = "Live"; };
  src.onerror = function () { live.textContent = "Reconnecting…"; };
  src.onmessage = function (m) {
    var ev;
    try { ev = JSON.parse(m.data); } catch (e) { return; }
    if (live.hasAttribute("data-run")) {
      appendRun(ev);
      return;
    }
    clearTimeout(timer);
    timer = setTimeout(refresh, 400);
  };
  var log = document.querySelector("[data-log] .ui-log");
  if (log && log.querySelector(".ui-log-line time")) log.setAttribute("data-started", "1");
})();
