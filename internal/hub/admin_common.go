package hub

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/store"
)

// ---- admin write helpers (docs/contracts/admin.md §4) ----

// ifMatch parses If-Match ("<version>", W/"<version>" or a bare number). present is false when
// the header is missing; an unparsable value is reported as a version conflict.
func ifMatch(r *http.Request) (version int, present bool, ok bool) {
	h := strings.TrimSpace(r.Header.Get("If-Match"))
	if h == "" {
		return 0, false, true
	}
	h = strings.Trim(strings.TrimPrefix(h, "W/"), `"`)
	v, err := strconv.Atoi(h)
	if err != nil || v < 0 {
		return 0, true, false
	}
	return v, true, true
}

// requireIfMatch returns the If-Match version or answers 428 / 412.
func requireIfMatch(w http.ResponseWriter, r *http.Request) (int, bool) {
	v, present, ok := ifMatch(r)
	if !present {
		writeProblem(w, r, http.StatusPreconditionRequired, hubapi.CodePreconditionRequired, "If-Match with the object's ETag is required.")
		return 0, false
	}
	if !ok {
		writeProblem(w, r, http.StatusPreconditionFailed, hubapi.CodeVersionConflict, "Invalid If-Match.")
		return 0, false
	}
	return v, true
}

// optionalIfMatch returns the If-Match version if given (nil otherwise) or answers 412.
func optionalIfMatch(w http.ResponseWriter, r *http.Request) (*int, bool) {
	v, present, ok := ifMatch(r)
	if !ok {
		writeProblem(w, r, http.StatusPreconditionFailed, hubapi.CodeVersionConflict, "Invalid If-Match.")
		return nil, false
	}
	if !present {
		return nil, true
	}
	return &v, true
}

// validReason answers 400 unless the reason has 1–1024 characters.
func validReason(w http.ResponseWriter, r *http.Request, reason string) bool {
	if n := utf8.RuneCountInString(strings.TrimSpace(reason)); n < 1 || utf8.RuneCountInString(reason) > 1024 {
		badRequest(w, r, "invalid_reason", "reason is required (1-1024 characters).")
		return false
	}
	return true
}

func setETag(w http.ResponseWriter, version int) {
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, version))
}

// change starts the audit record of a write by the caller.
func (s *Server) change(r *http.Request, action, reason string) store.Change {
	return store.Change{Actor: PrincipalFrom(r.Context()).Actor(), Action: action, Reason: reason, Time: s.now().UTC()}
}

// storeError answers the common store errors; it returns false for others (answered as 500).
func (s *Server) storeError(w http.ResponseWriter, r *http.Request, err error, what string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not_found", "Unknown "+what+".")
	case errors.Is(err, store.ErrVersionConflict):
		writeProblem(w, r, http.StatusPreconditionFailed, hubapi.CodeVersionConflict, "The "+what+" was changed by someone else; reload it.")
	case errors.Is(err, store.ErrAlreadyExists):
		writeProblem(w, r, http.StatusConflict, hubapi.CodeAlreadyExists, "A "+what+" with this identity already exists.")
	case errors.Is(err, store.ErrLastAdmin):
		writeProblem(w, r, http.StatusConflict, hubapi.CodeLastAdmin, "This would leave no enabled admin.")
	case errors.Is(err, store.ErrUnknownDatasource):
		badRequest(w, r, "unknown_datasource", "Unknown datasource in metrics.")
	default:
		s.internal(w, r, err)
	}
}

// pageParams parses limit (default 50, max 500) and an integer continue cursor.
func pageParams(w http.ResponseWriter, r *http.Request) (limit, before int, ok bool) {
	limit, err := intParam(r.URL.Query().Get("limit"), 50, 500)
	if err != nil {
		badRequest(w, r, "invalid_limit", "limit "+err.Error())
		return 0, 0, false
	}
	if c := r.URL.Query().Get("continue"); c != "" {
		cur, err := decodeCursor(c)
		if err != nil || cur.Offset < 1 {
			badRequest(w, r, "invalid_continue", "Invalid continue token.")
			return 0, 0, false
		}
		before = cur.Offset
	}
	return limit, before, true
}
