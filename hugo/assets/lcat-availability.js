/*
  libcatalog client-side availability (libcatalog tasks/004). Live availability is
  fetched in the browser at view time and kept OUT of the graph (ARCHITECTURE §5), so
  the static build stays backend-free. Each provider plugs in as an adapter -- the
  runtime sibling of an ingest provider (tasks/006) -- mapping its source to one
  normalized model the UI renders:

    Availability { provider, id, status, copiesOwned?, copiesAvailable?, holdsCount?,
                   estimatedWaitDays?, actionUrl?, fetchedAt }
    status: "available" | "holdable" | "unavailable" | "unknown"

  This ships the OverDrive/Thunder reference adapter (direct call, no auth). The core
  is pure and dependency-injected so it is testable off-DOM; the DOM wiring runs only
  in a browser. A failed or slow fetch degrades to status "unknown" and never blocks
  render.

  Config is a JSON <script id="lcat-availability-config"> the Hugo template emits from
  the site's [params.availability] (see README): { enabled, overdrive: { slug, ... } }.
*/
(function (root, factory) {
  var api = factory();
  if (typeof module !== "undefined" && module.exports) {
    module.exports = api; // node: unit tests require the pure core
  } else {
    root.LcatAvailability = api; // browser
  }
  // Browser auto-init: wire the DOM once the document is ready.
  if (typeof document !== "undefined") {
    if (document.readyState === "loading") {
      document.addEventListener("DOMContentLoaded", function () {
        api.init();
      });
    } else {
      api.init();
    }
  }
})(typeof self !== "undefined" ? self : this, function () {
  "use strict";

  var THUNDER_BASE = "https://thunder.api.overdrive.com/v2";
  var OVERDRIVE_BATCH = 25; // Thunder /media/availability caps ids per call
  var DEFAULT_TTL_MS = 5 * 60 * 1000; // short cache: availability is volatile
  var DEFAULT_TIMEOUT_MS = 8000;

  // ---- OverDrive / Thunder reference adapter ------------------------------------

  // overdriveStatus maps Thunder copy counts to the normalized status: an "always"
  // availabilityType (Always Available / simultaneous-use) is always borrowable; else
  // available when a copy is free, holdable when owned but all out, unavailable when
  // the library does not own it.
  function overdriveStatus(item) {
    if (item.availabilityType === "always") return "available";
    if ((item.availableCopies || 0) > 0) return "available";
    if ((item.ownedCopies || 0) > 0) return "holdable";
    return "unavailable";
  }

  // overdriveActionUrl is the borrow/hold deep link for a Reserve ID: an explicit
  // template when configured (with {id}), else the library's classic OverDrive site.
  function overdriveActionUrl(reserveID, cfg) {
    if (cfg && cfg.actionUrlTemplate) {
      return cfg.actionUrlTemplate.replace("{id}", reserveID);
    }
    if (cfg && cfg.slug) {
      return "https://" + cfg.slug + ".overdrive.com/media/" + reserveID;
    }
    return undefined;
  }

  // normalizeOverdrive maps one Thunder availability item to the normalized model.
  // holdsCount falls back to the older holdsPlaced field name.
  function normalizeOverdrive(item, cfg, now) {
    var holds = item.holdsCount != null ? item.holdsCount : item.holdsPlaced;
    return {
      provider: "overdrive",
      id: item.id,
      status: overdriveStatus(item),
      copiesOwned: item.ownedCopies,
      copiesAvailable: item.availableCopies,
      holdsCount: holds,
      estimatedWaitDays: item.estimatedWaitDays == null ? undefined : item.estimatedWaitDays,
      actionUrl: overdriveActionUrl(item.id, cfg),
      fetchedAt: now,
    };
  }

  // fetchOverdriveBatch POSTs one <=25-id batch to Thunder and returns an id->model
  // map. It aborts on a timeout and rejects on a non-2xx response so the caller can
  // degrade the whole batch to "unknown". deps.fetch / deps.now are injectable.
  async function fetchOverdriveBatch(ids, cfg, deps) {
    var fetchFn = (deps && deps.fetch) || (typeof fetch !== "undefined" ? fetch : null);
    if (!fetchFn) throw new Error("lcat-availability: no fetch available");
    if (!cfg || !cfg.slug) throw new Error("lcat-availability: overdrive.slug not configured");
    var now = (deps && deps.now) || Date.now();
    var base = cfg.baseUrl || THUNDER_BASE;
    var url = base + "/libraries/" + encodeURIComponent(cfg.slug) + "/media/availability";

    var ctrl = typeof AbortController !== "undefined" ? new AbortController() : null;
    var timer = ctrl
      ? setTimeout(function () {
          ctrl.abort();
        }, cfg.timeoutMs || DEFAULT_TIMEOUT_MS)
      : null;
    try {
      var resp = await fetchFn(url, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ ids: ids }),
        signal: ctrl ? ctrl.signal : undefined,
      });
      if (!resp.ok) throw new Error("thunder HTTP " + resp.status);
      var data = await resp.json();
      var out = {};
      (data.items || []).forEach(function (item) {
        if (item && item.id) out[item.id] = normalizeOverdrive(item, cfg, now);
      });
      return out;
    } finally {
      if (timer) clearTimeout(timer);
    }
  }

  // ---- adapter registry ---------------------------------------------------------

  var adapters = {};

  // registerAdapter binds a provider adapter. providerKey namespaces it; domAttr is
  // the edition element attribute carrying the id it keys on (its ingest counterpart
  // emits that id, tasks/006/009); batchSize caps ids per fetch; fetchBatch(ids, cfg,
  // deps) returns an id->model map for one batch.
  function registerAdapter(a) {
    adapters[a.providerKey] = a;
  }

  registerAdapter({
    providerKey: "overdrive",
    domAttr: "data-overdrive-reserve",
    batchSize: OVERDRIVE_BATCH,
    fetchBatch: fetchOverdriveBatch,
  });

  // ---- batching + cache + in-flight de-dup --------------------------------------

  // chunk splits a list into slices of at most n.
  function chunk(list, n) {
    var out = [];
    for (var i = 0; i < list.length; i += n) out.push(list.slice(i, i + n));
    return out;
  }

  // makeStore is a short-TTL value cache with per-key in-flight de-dup, so a page with
  // the same id in several places issues one request and re-render is cache-served.
  function makeStore(ttl) {
    var values = {}; // key -> { model, exp }
    var inflight = {}; // key -> Promise<model|undefined>
    return {
      get: function (key, now) {
        var e = values[key];
        return e && e.exp > now ? e.model : undefined;
      },
      set: function (key, model, now) {
        values[key] = { model: model, exp: now + ttl };
      },
      inflight: inflight,
    };
  }

  // resolve returns an id->model map for one provider's ids, serving fresh cache hits
  // immediately, joining in-flight requests, and batching the rest through the
  // adapter. Any id the source omits or a failed batch degrades to status "unknown"
  // -- the UI always gets a model per id. deps: { fetch, now, store, ttl }.
  async function resolve(providerKey, ids, cfg, deps) {
    var adapter = adapters[providerKey];
    if (!adapter) throw new Error("lcat-availability: no adapter for " + providerKey);
    deps = deps || {};
    var ttl = deps.ttl || DEFAULT_TTL_MS;
    var store = deps.store || (adapter._store = adapter._store || makeStore(ttl));
    var now = deps.now || Date.now();

    var uniq = Object.keys(
      ids.reduce(function (m, id) {
        m[id] = true;
        return m;
      }, {})
    );

    var result = {};
    var missing = [];
    var joined = [];
    uniq.forEach(function (id) {
      var key = providerKey + ":" + id;
      var hit = store.get(key, now);
      if (hit) {
        result[id] = hit;
      } else if (store.inflight[key]) {
        joined.push(
          store.inflight[key].then(function (m) {
            if (m) result[id] = m;
          })
        );
      } else {
        missing.push(id);
      }
    });

    var batches = chunk(missing, adapter.batchSize).map(function (batch) {
      // One promise per batch; register it per-id for de-dup, resolve to a per-id map.
      var p = fetchBatchSafe(adapter, batch, cfg, { fetch: deps.fetch, now: now });
      batch.forEach(function (id) {
        var key = providerKey + ":" + id;
        store.inflight[key] = p.then(function (map) {
          return map[id];
        });
      });
      return p.then(function (map) {
        batch.forEach(function (id) {
          var model = map[id] || unknownModel(providerKey, id, now);
          store.set(providerKey + ":" + id, model, now);
          delete store.inflight[providerKey + ":" + id];
          result[id] = model;
        });
      });
    });

    await Promise.all(batches.concat(joined));
    return result;
  }

  // fetchBatchSafe runs an adapter batch, turning any failure into an empty map so one
  // provider outage degrades to "unknown" rather than throwing.
  async function fetchBatchSafe(adapter, ids, cfg, deps) {
    try {
      return await adapter.fetchBatch(ids, cfg, deps);
    } catch (e) {
      if (typeof console !== "undefined" && console.warn) {
        console.warn("lcat-availability: " + adapter.providerKey + " fetch failed:", e);
      }
      return {};
    }
  }

  function unknownModel(providerKey, id, now) {
    return { provider: providerKey, id: id, status: "unknown", fetchedAt: now };
  }

  // ---- rendering ----------------------------------------------------------------

  // statusText is the human string for a status, using wait/holds detail when holdable.
  function statusText(model) {
    switch (model.status) {
      case "available":
        return "Available now";
      case "holdable":
        var t =
          model.estimatedWaitDays != null
            ? "Estimated wait ~" + model.estimatedWaitDays + " days"
            : "Place a hold";
        if (model.holdsCount) {
          t += " · " + model.holdsCount + (model.holdsCount === 1 ? " hold" : " holds");
        }
        return t;
      case "unavailable":
        return "Not available";
      default:
        return ""; // unknown: say nothing rather than mislead
    }
  }

  // renderInto writes a resolved model into a status element: data-status for styling,
  // a data-action-url a theme can turn into a borrow link, and the status text.
  function renderInto(el, model) {
    el.setAttribute("data-status", model.status);
    if (model.actionUrl) el.setAttribute("data-action-url", model.actionUrl);
    el.textContent = statusText(model);
  }

  // ---- DOM wiring (browser only) ------------------------------------------------

  // readConfig parses the JSON config the template emits, or null when absent/disabled.
  // Hugo's `jsonify` emits a plain object in a standalone site but a JSON-encoded
  // *string* when the module is imported into another site (a config-merge quirk);
  // both are valid JSON, so a second parse unwraps the string form.
  function readConfig(doc) {
    var node = doc.getElementById("lcat-availability-config");
    if (!node) return null;
    try {
      var cfg = JSON.parse(node.textContent || "null");
      if (typeof cfg === "string") cfg = JSON.parse(cfg);
      return cfg && cfg.enabled ? cfg : null;
    } catch (e) {
      return null;
    }
  }

  // collect maps each configured provider to its on-page { id -> [statusElements] }.
  // An edition element carries the adapter's domAttr (the id) and holds a
  // [data-availability] status span the model renders into.
  function collect(doc) {
    var byProvider = {};
    Object.keys(adapters).forEach(function (providerKey) {
      var adapter = adapters[providerKey];
      var editions = doc.querySelectorAll("[" + adapter.domAttr + "]");
      var map = {};
      Array.prototype.forEach.call(editions, function (ed) {
        var id = ed.getAttribute(adapter.domAttr);
        var el = ed.querySelector("[data-availability]") || ed;
        if (!id) return;
        (map[id] = map[id] || []).push(el);
      });
      if (Object.keys(map).length) byProvider[providerKey] = map;
    });
    return byProvider;
  }

  // init fetches availability for every configured provider on the page and renders
  // it. Safe to call with no config (no-op) and never throws into the page.
  function init(opts) {
    opts = opts || {};
    var doc = opts.document || (typeof document !== "undefined" ? document : null);
    if (!doc) return Promise.resolve();
    var cfg = opts.config || readConfig(doc);
    if (!cfg) return Promise.resolve();

    var byProvider = collect(doc);
    var jobs = Object.keys(byProvider).map(function (providerKey) {
      var map = byProvider[providerKey];
      var ids = Object.keys(map);
      return resolve(providerKey, ids, cfg[providerKey], { fetch: opts.fetch, now: opts.now })
        .then(function (models) {
          ids.forEach(function (id) {
            var model = models[id] || unknownModel(providerKey, id, opts.now || Date.now());
            map[id].forEach(function (el) {
              renderInto(el, model);
            });
          });
        })
        .catch(function () {
          /* resolve already degrades to unknown; never block the page */
        });
    });
    return Promise.all(jobs);
  }

  return {
    // pure core (unit-tested)
    overdriveStatus: overdriveStatus,
    normalizeOverdrive: normalizeOverdrive,
    fetchOverdriveBatch: fetchOverdriveBatch,
    statusText: statusText,
    chunk: chunk,
    makeStore: makeStore,
    resolve: resolve,
    registerAdapter: registerAdapter,
    adapters: adapters,
    // DOM entry point
    init: init,
    renderInto: renderInto,
    readConfig: readConfig,
  };
});
