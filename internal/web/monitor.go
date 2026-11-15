package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/fiatcode-gh/go-exam/internal/attempt"
)

type eventResponse struct {
	Recorded bool `json:"recorded"`
}

// event records a monitor event for the cookie's attempt. Form fields: kind
// (an attempt.EventKind) and an optional question ID. With the exam's monitor
// off, or past the per-attempt cap, it answers recorded false.
func (s *server) event(w http.ResponseWriter, r *http.Request) {
	token, ok := s.studentJSONForm(w, r)
	if !ok {
		return
	}
	var questionID int64
	if q := r.PostFormValue("question"); q != "" {
		var err error
		if questionID, err = strconv.ParseInt(q, 10, 64); err != nil {
			s.writeJSON(w, r, http.StatusUnprocessableEntity, errorResponse{"invalid"})
			return
		}
	}
	recorded, err := s.deps.Attempts.RecordEvent(r.Context(), token, attempt.EventKind(r.PostFormValue("kind")), questionID)
	switch {
	case err == nil:
		s.writeJSON(w, r, http.StatusOK, eventResponse{Recorded: recorded})
	case errors.Is(err, attempt.ErrNotFound):
		s.writeJSON(w, r, http.StatusUnauthorized, errorResponse{"no_attempt"})
	case errors.Is(err, attempt.ErrFinished):
		s.writeJSON(w, r, http.StatusConflict, errorResponse{"finished"})
	case errors.Is(err, attempt.ErrInvalidEvent):
		s.writeJSON(w, r, http.StatusUnprocessableEntity, errorResponse{"invalid"})
	default:
		s.serverError(w, r, err)
	}
}

// eventText is how the teacher reads each event kind.
var eventText = map[attempt.EventKind]string{
	attempt.LeftPage:   "Left the exam page",
	attempt.Copy:       "Tried to copy",
	attempt.Paste:      "Tried to paste",
	attempt.RightClick: "Right-clicked",
	attempt.Resize:     "Resized the window",
}

// eventRow is one line of the attempt detail's flags table.
type eventRow struct {
	Time, Text string
	Question   int // 0 when unknown
}
