package web

import (
	"net/http"

	"github.com/fiatcode-gh/answerbox/internal/exam"
)

// confirmData is the page that asks before a removal or an archive: the
// question, what follows from it, and the form that posts the action.
type confirmData struct {
	adminPage
	Heading  string         // the h1 and the page title
	Question string         // the sentence that asks
	Notes    []string       // what follows, one paragraph each
	Preview  *exam.Question // the question a removal takes away; nil otherwise
	FormID   string
	Action   string // the POST path
	Revision string // posted as "revision"; "" when the action takes none
	Submit   string // the button's label
	Danger   bool   // the action removes something
	Cancel   string // where Cancel leads
}

// confirmPage renders the confirmation page d. It reads nothing and stores
// nothing: the form it shows posts the action.
func (s *server) confirmPage(w http.ResponseWriter, r *http.Request, d confirmData) {
	s.page(w, r, http.StatusOK, "admin_confirm.html", d)
}
