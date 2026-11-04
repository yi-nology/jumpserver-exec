package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yi-nology/jumpserver-mcp-kit/pkg/audit"
)

// renderDoctor is the exec-side wording. Only "fail" counts towards the exit
// code here -- a warning is something to look at, not a broken setup -- and
// the verdict text differs from the management side's on purpose.
func TestRenderDoctorCountsOnlyFailures(t *testing.T) {
	text, failures := renderDoctor("jumpserver-mcp-exec",
		[]string{"bastion: 192.0.2.41:2222 as ai", "key:     /root/.ssh/id_rsa"},
		[]doctorCheck{
			{Status: "ok", Name: "key_file", Detail: "present, 3381 bytes"},
			{Status: "warn", Name: "rest_client", Detail: "disabled"},
		})
	if failures != 0 {
		t.Errorf("failures = %d, want 0 (a warning must not fail the check)", failures)
	}
	for _, want := range []string{
		"jumpserver-mcp-exec doctor",
		"bastion: 192.0.2.41:2222 as ai",
		"key:     /root/.ssh/id_rsa",
		"[OK  ] key_file",
		"[WARN] rest_client",
		"result: healthy",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("renderDoctor output missing %q:\n%s", want, text)
		}
	}
}

func TestRenderDoctorCountsFailures(t *testing.T) {
	text, failures := renderDoctor("jumpserver-mcp-exec", []string{"bastion: x"},
		[]doctorCheck{
			{Status: "fail", Name: "key_type", Detail: "not ssh-rsa"},
			{Status: "fail", Name: "koko_port", Detail: "cannot reach"},
			{Status: "warn", Name: "rest_client", Detail: "disabled"},
		})
	if failures != 2 {
		t.Errorf("failures = %d, want 2", failures)
	}
	if !strings.Contains(text, "[FAIL] key_type") {
		t.Errorf("renderDoctor output missing the FAIL marker:\n%s", text)
	}
	// The exec side has its own verdict wording; it must not borrow the
	// management side's.
	if !strings.Contains(text, "result: 2 problem(s) need fixing (see FAIL above)") {
		t.Errorf("renderDoctor output missing the failure verdict:\n%s", text)
	}
	if strings.Contains(text, "needs attention") {
		t.Errorf("renderDoctor used the management side's verdict wording:\n%s", text)
	}
}

// The koko side's configuration has no defaults, so the doctor has to say
// what is missing — and the port check cannot: an unset host makes it say
// "cannot reach :2222", which reads like a network problem rather than an
// unset variable.
//
// ⚠️ The severity depends on the backend: the connection-token backend
// never touches koko, so an unset host costs nothing there; "auto" only
// loses its fallback; and with no REST credential either, nothing can open
// a session at all.
func TestTheDoctorSaysWhenTheKokoSideIsNotConfigured(t *testing.T) {
	ck := bastionCheck("", "", 2222, "auto", false)
	if ck.Status != "fail" {
		t.Errorf("no host, no user and no REST credential reported %q, want fail", ck.Status)
	}
	if ck.Name != "bastion" {
		t.Errorf("the row is named %q, want bastion", ck.Name)
	}
	for _, want := range []string{"JMS_BASTION_HOST", "JMS_KOKO_USER"} {
		if !strings.Contains(ck.Detail, want) {
			t.Errorf("the row does not name %s: %s", want, ck.Detail)
		}
	}

	// The connection-token backend does not need them at all.
	ck = bastionCheck("", "", 2222, "token", true)
	if ck.Status != "ok" {
		t.Errorf("an unset host reported %q for the connection-token backend, want ok", ck.Status)
	}

	// ⚠️ "auto" with a working REST credential loses the fallback, and
	// nothing else.
	ck = bastionCheck("", "ai", 2222, "auto", true)
	if ck.Status != "warn" {
		t.Errorf("an unset host reported %q for the auto backend with a credential, want warn", ck.Status)
	}
	if !strings.Contains(ck.Detail, "JMS_BASTION_HOST") {
		t.Errorf("the row does not name the missing variable: %s", ck.Detail)
	}

	// A fully configured koko side is fine.
	ck = bastionCheck("bastion.example.com", "ai", 2222, "auto", true)
	if ck.Status != "ok" {
		t.Errorf("a configured koko side reported %q (%s), want ok", ck.Status, ck.Detail)
	}
	if !strings.Contains(ck.Detail, "bastion.example.com:2222 as ai") {
		t.Errorf("the row does not state the wiring: %s", ck.Detail)
	}
}

// ⚠️ The severity must be judged on "can the connection-token path open a
// session", NOT on "is a credential configured".
//
// A credential without a URL cannot open anything: the request goes out
// with a relative path and comes back "unsupported protocol scheme". Saying
// "only the connection-token path can open a session" about a dead path
// tells the operator the wrong thing — and the row is the one they read to
// decide whether the deployment can work at all.
func TestTheDoctorJudgesTheTokenPathOnWhetherItWorks(t *testing.T) {
	// The token backend with a DEAD token path: nothing can open a session.
	ck := bastionCheck("", "", 2222, "token", false)
	if ck.Status != "fail" {
		t.Errorf("a dead token path reported %q, want fail", ck.Status)
	}
	if !strings.Contains(ck.Detail, "no session can be opened") {
		t.Errorf("the row does not say that no session can be opened: %s", ck.Detail)
	}

	// "auto" with a dead token path: the koko side is unset too, so there
	// is no path left at all.
	ck = bastionCheck("", "", 2222, "auto", false)
	if ck.Status != "fail" {
		t.Errorf("no usable path reported %q, want fail", ck.Status)
	}
	if !strings.Contains(ck.Detail, "no session can be opened") {
		t.Errorf("the row does not say that no session can be opened: %s", ck.Detail)
	}

	// "auto" with a LIVE token path: only the fallback is lost.
	ck = bastionCheck("", "", 2222, "auto", true)
	if ck.Status != "warn" {
		t.Errorf("a live token path reported %q, want warn", ck.Status)
	}
}

// The doctor must say when the file trail stopped.
//
// A file over the cap means the rotation failed, and the writer stops the
// file trail when it cannot rotate — the trail is then in the LOG only.
// Nothing tested this: deleting the check left the suite green.
func TestTheDoctorSaysWhenTheFileTrailStopped(t *testing.T) {
	dir := t.TempDir()

	status, detail := auditTrailCheck(dir)
	if status != "ok" {
		t.Errorf("an empty trail directory reported %q (%s), want ok", status, detail)
	}

	// A file over the cap: the cap is 32 MiB, so the file is written with
	// a hole in it (a sparse file) rather than 32 MiB of zeros.
	path := filepath.Join(dir, "audit-20261028-1-aaaaaaaa.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := f.Truncate(audit.MaxTrailBytes + 1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	f.Close()

	status, detail = auditTrailCheck(dir)
	if status != "warn" {
		t.Errorf("a trail file over the cap reported %q (%s), want warn", status, detail)
	}
	if !strings.Contains(detail, "over the") {
		t.Errorf("the warning does not say what is wrong: %s", detail)
	}
}

// A directory that is not one must not be reported as fine.
func TestTheDoctorSaysWhenTheTrailDirectoryIsNotOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	status, _ := auditTrailCheck(path)
	if status != "fail" {
		t.Errorf("a file where the trail directory should be reported %q, want fail", status)
	}
	status, _ = auditTrailCheck("")
	if status != "warn" {
		t.Errorf("an unset trail directory reported %q, want warn", status)
	}
}
