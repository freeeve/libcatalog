package httpapi

import (
	"encoding/json"
	"net/http"
)

// writeJSON encodes v as the response body with the given status. Encoding
// happens BEFORE the header goes out: an unencodable value (e.g. a NaN that
// slipped into a float field) must surface as a 500, not a silent 200 with
// an empty body the client reads as "no data".
func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"response encoding failed"}` + "\n"))
		return
	}
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}

// writeError emits the API's uniform error shape.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
