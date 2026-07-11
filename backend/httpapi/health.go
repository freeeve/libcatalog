package httpapi

import (
	"net/http"
	"sync/atomic"
)

// Health separates the two questions an orchestrator asks a replica, which are
// not the same question.
//
// Liveness ("is this process wedged?") must stay true for as long as the
// process can answer at all. A liveness probe that fails restarts the
// container, so wiring it to anything but the process itself -- a datastore, a
// downstream service -- converts a dependency blip into a restart storm.
//
// Readiness ("should this replica receive traffic?") is the one that has real
// work to do here, and its work is shutdown. Kubernetes removes a terminating
// pod from its Service endpoints concurrently with sending SIGTERM, not before
// it: for the width of that race a load balancer still routes to a server that
// has already stopped listening, and those requests fail. The fix is for the
// pod to fail readiness first, stay up long enough to be deregistered, and only
// then drain.
//
// Readiness deliberately does not check store connectivity. Every replica
// shares one store, so a store blip would fail every replica's probe at once
// and the orchestrator would empty the Service of endpoints -- turning a
// degradation that still serves cached reads into a total outage. A probe whose
// failure mode is "remove all capacity" must depend on nothing shared.
type Health struct {
	draining atomic.Bool
}

// Drain marks the replica as no longer accepting traffic. Readiness fails from
// the next probe onward; liveness is unaffected, because a draining server is
// working exactly as intended and must not be restarted out from under its
// in-flight requests.
func (h *Health) Drain() {
	if h != nil {
		h.draining.Store(true)
	}
}

// Draining reports whether Drain has been called. A nil Health never drains,
// which is what tests and non-orchestrated deployments want.
func (h *Health) Draining() bool {
	return h != nil && h.draining.Load()
}

// handleHealthz answers liveness. It reports on the process and nothing else,
// and it keeps reporting "ok" while the server drains.
func handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz answers readiness: 200 while the replica should receive traffic,
// 503 once it is draining.
func handleReadyz(h *Health) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.Draining() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
