package adminauth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/adminauth"
	"github.com/fiatcode-gh/answerbox/internal/platform/sqlite/sqlitetest"
)

func teachers(t *testing.T, svc *adminauth.Service) []adminauth.Teacher {
	t.Helper()
	list, err := svc.Teachers(context.Background())
	if err != nil {
		t.Fatalf("Teachers: %v", err)
	}
	return list
}

// listed returns the listed teacher with email, failing when there is none.
func listed(t *testing.T, svc *adminauth.Service, email string) adminauth.Teacher {
	t.Helper()
	for _, tc := range teachers(t, svc) {
		if tc.Email == email {
			return tc
		}
	}
	t.Fatalf("%s is not listed", email)
	return adminauth.Teacher{}
}

func (h *harness) teacherID(t *testing.T, email string) int64 {
	t.Helper()
	return int64(h.count(t, `SELECT id FROM teacher WHERE email = ?`, email))
}

// addExam stores a raw exam owned by ownerID and returns its id.
func (h *harness) addExam(t *testing.T, ownerID int64, key, code string) int64 {
	t.Helper()
	res, err := h.db.Exec(
		`INSERT INTO exam (title, shuffle, show_result, media_key, created_at, owner_id, code) VALUES ('t', 'none', 'score', ?, 0, ?, ?)`,
		key, ownerID, code)
	if err != nil {
		t.Fatalf("insert exam: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAddTeacher_ListsAndNormalizes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.svc.AddTeacher(ctx, " New.Teacher@Example.Test "); err != nil {
		t.Fatalf("AddTeacher: %v", err)
	}

	list := teachers(t, h.svc)
	if len(list) != 2 || list[0].Email != adminEmail || list[1].Email != "new.teacher@example.test" {
		t.Fatalf("Teachers = %+v, want admin then new.teacher@example.test", list)
	}
	if !list[0].Admin || list[1].Admin {
		t.Errorf("Admin flags = %v, %v; want true, false", list[0].Admin, list[1].Admin)
	}
	for _, tc := range list {
		if !tc.LastLoginAt.IsZero() || tc.ExamCount != 0 || tc.ID == 0 {
			t.Errorf("%s: %+v, want an id, no sign-in and no exams", tc.Email, tc)
		}
	}

	if err := h.svc.AddTeacher(ctx, "NEW.teacher@example.TEST"); err != nil {
		t.Fatalf("AddTeacher again: %v", err)
	}
	if n := h.count(t, `SELECT COUNT(*) FROM teacher WHERE email = 'new.teacher@example.test'`); n != 1 {
		t.Errorf("rows for the email = %d, want 1", n)
	}
	if got := teachers(t, h.svc); len(got) != 2 || got[1] != list[1] {
		t.Errorf("after adding again: %+v, want %+v", got, list)
	}
}

func TestAddTeacher_InvalidEmail(t *testing.T) {
	h := newHarness(t)
	for _, email := range []string{"", "nope", "a@b", "a b@example.test", "a@@example.test"} {
		if err := h.svc.AddTeacher(context.Background(), email); !errors.Is(err, adminauth.ErrInvalidEmail) {
			t.Errorf("AddTeacher(%q) = %v, want ErrInvalidEmail", email, err)
		}
	}
	if n := h.count(t, `SELECT COUNT(*) FROM teacher`); n != 1 {
		t.Errorf("teacher rows = %d, want only the admin", n)
	}
}

func TestTeachers_ExamCountAndLastLogin(t *testing.T) {
	h := newHarness(t)
	id := sqlitetest.Teacher(t, h.db, teacherEmail)
	h.addExam(t, id, "k1", "AAAAAA")
	h.addExam(t, id, "k2", "BBBBBB")
	h.advance(time.Hour)
	mustSignIn(t, h.svc, adminauth.Identity{Email: teacherEmail, EmailVerified: true, Name: "Tess Teacher"})

	got := listed(t, h.svc, teacherEmail)
	if got.ID != id || got.ExamCount != 2 || got.Name != "Tess Teacher" || !got.LastLoginAt.Equal(h.clock) {
		t.Errorf("row = %+v, want id %d, 2 exams, name Tess Teacher, last sign-in %v", got, id, h.clock)
	}
	if admin := listed(t, h.svc, adminEmail); admin.ExamCount != 0 {
		t.Errorf("admin exam count = %d, want 0", admin.ExamCount)
	}
}

func TestRemoveTeacher_EndsSessionsAndBlocksSignIn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := sqlitetest.Teacher(t, h.db, teacherEmail)
	examID := h.addExam(t, id, "k1", "AAAAAA")
	tok1 := mustSignIn(t, h.svc, verified(teacherEmail))
	tok2 := mustSignIn(t, h.svc, verified(teacherEmail))
	adminTok := mustSignIn(t, h.svc, verified(adminEmail))

	if err := h.svc.RemoveTeacher(ctx, id); err != nil {
		t.Fatalf("RemoveTeacher: %v", err)
	}
	for _, tok := range []string{tok1, tok2} {
		if _, ok := session(t, h.svc, tok); ok {
			t.Error("a session of the removed teacher is still valid")
		}
	}
	if _, err := h.svc.SignIn(ctx, verified(teacherEmail)); !errors.Is(err, adminauth.ErrNotAllowed) {
		t.Errorf("SignIn after removal = %v, want ErrNotAllowed", err)
	}
	for _, tc := range teachers(t, h.svc) {
		if tc.Email == teacherEmail {
			t.Error("the removed teacher is still listed")
		}
	}
	if n := h.count(t, `SELECT COUNT(*) FROM teacher WHERE id = ?`, id); n != 1 {
		t.Errorf("teacher rows = %d, want the row kept", n)
	}
	if n := h.count(t, `SELECT COUNT(*) FROM exam WHERE id = ? AND owner_id = ?`, examID, id); n != 1 {
		t.Errorf("exam rows = %d, want the exam kept", n)
	}
	if _, ok := session(t, h.svc, adminTok); !ok {
		t.Error("another teacher's session ended")
	}
}

func TestRemoveTeacher_AdminEmailRefused(t *testing.T) {
	h := newHarness(t)
	adminTok := mustSignIn(t, h.svc, verified(adminEmail))
	id := h.teacherID(t, adminEmail)

	if err := h.svc.RemoveTeacher(context.Background(), id); !errors.Is(err, adminauth.ErrAdminEmail) {
		t.Fatalf("RemoveTeacher(admin) = %v, want ErrAdminEmail", err)
	}
	if _, ok := session(t, h.svc, adminTok); !ok {
		t.Error("the admin's session ended")
	}
	if n := h.count(t, `SELECT allowed FROM teacher WHERE id = ?`, id); n != 1 {
		t.Errorf("admin allowed = %d, want 1", n)
	}
}

func TestRemoveTeacher_Unknown(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.svc.RemoveTeacher(ctx, 9999); !errors.Is(err, adminauth.ErrNotFound) {
		t.Errorf("RemoveTeacher(unknown) = %v, want ErrNotFound", err)
	}
	id := sqlitetest.Teacher(t, h.db, teacherEmail)
	if err := h.svc.RemoveTeacher(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RemoveTeacher(ctx, id); !errors.Is(err, adminauth.ErrNotFound) {
		t.Errorf("RemoveTeacher(removed) = %v, want ErrNotFound", err)
	}
}

func TestAddTeacher_RestoresRemoved(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := sqlitetest.Teacher(t, h.db, teacherEmail)
	h.addExam(t, id, "k1", "AAAAAA")
	if err := h.svc.RemoveTeacher(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.AddTeacher(ctx, teacherEmail); err != nil {
		t.Fatalf("AddTeacher: %v", err)
	}
	got := listed(t, h.svc, teacherEmail)
	if got.ID != id || got.ExamCount != 1 {
		t.Errorf("restored row = %+v, want id %d with 1 exam", got, id)
	}
	mustSignIn(t, h.svc, verified(teacherEmail))
}

func TestDemotedAdmin_IsAnOrdinaryRemovableTeacher(t *testing.T) {
	h := newHarness(t)
	const a, b = "a@example.test", "b@example.test"
	h.service(t, a, b)
	svc := h.service(t, a)

	got := listed(t, svc, b)
	if got.Admin {
		t.Errorf("demoted admin listed with Admin = true")
	}
	mustSignIn(t, svc, verified(b))
	if err := svc.RemoveTeacher(context.Background(), got.ID); err != nil {
		t.Fatalf("RemoveTeacher(demoted admin) = %v", err)
	}
	if _, err := svc.SignIn(context.Background(), verified(b)); !errors.Is(err, adminauth.ErrNotAllowed) {
		t.Errorf("SignIn after removal = %v, want ErrNotAllowed", err)
	}
}
