package config

import (
	"strings"
	"testing"
	"time"
)

// The execution plane's settings are read by the same machinery as the
// management plane's: the `env` tag says where a value comes from, the
// `default` tag says what it falls back to, and an unknown value for a
// field with an `options` tag is an error rather than a silently wrong path.
//
// These tests pin the two things that cost: the configuration has ONE home,
// and a missing token disables the inventory tool instead of the server.

// load is Load with the error folded into a failure, so the tests read
// cleanly.
func load(t *testing.T) Config {
	t.Helper()
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestAPIClientNeedsAToken(t *testing.T) {
	t.Setenv("JUMPSERVER_TOKEN", "")
	cfg := load(t)
	if c := cfg.APIClient(); c != nil {
		t.Fatal("APIClient built a client without a token")
	}
}

func TestAPIClientCarriesTheConfiguredEndpoint(t *testing.T) {
	t.Setenv("JUMPSERVER_TOKEN", "t0ken")
	t.Setenv("JUMPSERVER_URL", "http://bastion.example/")
	cfg := load(t)
	if cfg.API.BaseURL != "http://bastion.example" {
		t.Errorf("BaseURL = %q, want the trailing slash removed", cfg.API.BaseURL)
	}
	c := cfg.APIClient()
	if c == nil {
		t.Fatal("APIClient returned nil with a token configured")
	}
	if c.BaseURL != "http://bastion.example" {
		t.Errorf("client BaseURL = %q", c.BaseURL)
	}
	if c.Token != "t0ken" {
		t.Errorf("client Token = %q", c.Token)
	}
}

func TestAPIConfigHasTheSameCeilingsAsTheManagementPlane(t *testing.T) {
	// The two servers talk to the same monolith. A limit that differs
	// between them is one more thing to remember when the bastion complains.
	t.Setenv("JMS_REQUESTS_PER_SECOND", "")
	t.Setenv("JMS_BURST", "")
	cfg := load(t)
	if cfg.API.RequestsPerSecond != 20 {
		t.Errorf("RequestsPerSecond = %v, want 20", cfg.API.RequestsPerSecond)
	}
	if cfg.API.Burst != 40 {
		t.Errorf("Burst = %d, want 40", cfg.API.Burst)
	}
	if cfg.API.Timeout <= 0 {
		t.Errorf("Timeout = %v, want a positive bound", cfg.API.Timeout)
	}
}

func TestAPIConfigReadsTheCeilingsFromTheEnvironment(t *testing.T) {
	t.Setenv("JMS_REQUESTS_PER_SECOND", "5")
	t.Setenv("JMS_BURST", "7")
	cfg := load(t)
	if cfg.API.RequestsPerSecond != 5 {
		t.Errorf("RequestsPerSecond = %v, want 5", cfg.API.RequestsPerSecond)
	}
	if cfg.API.Burst != 7 {
		t.Errorf("Burst = %d, want 7", cfg.API.Burst)
	}
}

func TestAPIConfigReadsTheTimeoutFromTheEnvironment(t *testing.T) {
	// The configuration guide lists JUMPSERVER_TIMEOUT as one both servers
	// read. The execution plane used to hard-code its own timeout and never
	// looked, so the guide was wrong until now.
	t.Setenv("JUMPSERVER_TIMEOUT", "")
	if got := load(t).API.Timeout; got != 90*time.Second {
		t.Errorf("default Timeout = %v, want 90s", got)
	}
	t.Setenv("JUMPSERVER_TIMEOUT", "45s")
	if got := load(t).API.Timeout; got != 45*time.Second {
		t.Errorf("Timeout = %v, want 45s", got)
	}
}

func TestReadOnlyIsStillReadFromTheEnvironment(t *testing.T) {
	// The mode switch is the execution plane's own business; it must not have
	// been caught up in the move of the REST settings.
	t.Setenv("JMS_READ_ONLY", "true")
	if !load(t).ReadOnly {
		t.Error("ReadOnly = false, want true")
	}
	t.Setenv("JMS_READ_ONLY", "")
	if load(t).ReadOnly {
		t.Error("ReadOnly = true, want false")
	}
}

func TestKokoSettingsAreUnchanged(t *testing.T) {
	// The koko side was already in the configuration; this pins that moving
	// the REST settings did not disturb it.
	t.Setenv("JMS_BASTION_HOST", "10.0.0.9")
	t.Setenv("JMS_KOKO_USER", "someone")
	cfg := load(t)
	if cfg.Koko.Host != "10.0.0.9" {
		t.Errorf("Koko.Host = %q", cfg.Koko.Host)
	}
	if cfg.Koko.Port != 2222 {
		t.Errorf("Koko.Port = %d, want 2222", cfg.Koko.Port)
	}
	if cfg.Koko.User != "someone" {
		t.Errorf("Koko.User = %q", cfg.Koko.User)
	}
	if cfg.Koko.Timeout != 90*time.Second {
		t.Errorf("Koko.Timeout = %v", cfg.Koko.Timeout)
	}
}

// The backend mode is an enum, and an unknown value must not be accepted:
// it would silently pick a path nobody asked for.
func TestTheBackendModeIsAnEnum(t *testing.T) {
	t.Setenv("JMS_BACKEND", "token")
	if got := load(t).Koko.Backend; got != "token" {
		t.Errorf("Backend = %q, want token", got)
	}
	t.Setenv("JMS_BACKEND", "telnet")
	if _, err := Load(); err == nil {
		t.Error("an unknown backend was accepted")
	}
}

// The pool's idle window is a setting, and it is also how long a revoked
// right stays effective — so it has to be adjustable without a rebuild.
func TestThePoolWindowIsReadFromTheEnvironment(t *testing.T) {
	t.Setenv("JMS_POOL_TTL", "")
	cfg := load(t)
	if cfg.Koko.Pool == nil {
		t.Fatal("no connection pool was built")
	}
	t.Setenv("JMS_POOL_TTL", "45s")
	if _, err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// The key path is written with a tilde by people, and the configuration
// hands it over as written.
func TestTheKeyPathExpandsTheTilde(t *testing.T) {
	t.Setenv("JMS_KOKO_KEY", "~/.ssh/id_rsa")
	got := load(t).Koko.KeyPath
	if got == "~/.ssh/id_rsa" {
		t.Error("the tilde was not expanded")
	}
	if !strings.HasSuffix(got, "/.ssh/id_rsa") {
		t.Errorf("KeyPath = %q, want it to end with /.ssh/id_rsa", got)
	}
}

// ⚠️ The koko terminal backend needs the bastion's address and the login
// name, and both have NO default (a default would point one deployment's
// clients at somebody else's bastion).
//
// So this branch is the one that stops the server from starting with a
// configuration it cannot use — and it is the branch that decides whether
// the fallback dials a real bastion or THIS machine (an empty host is
// ":2222", and Go reads that as localhost). It gets a test because a
// branch that decides where a private key is sent must not be untested.
func TestTheTerminalBackendRefusesToStartWithoutItsVariables(t *testing.T) {
	t.Setenv("JMS_BACKEND", "tui")
	t.Setenv("JMS_BASTION_HOST", "")
	t.Setenv("JMS_KOKO_USER", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() accepted the terminal backend without a host and a user")
	}
	for _, want := range []string{"JMS_BASTION_HOST", "JMS_KOKO_USER"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %s: %v", want, err)
		}
	}
}

// A configured terminal backend loads, and the host is trimmed.
func TestTheTerminalBackendLoadsWhenItIsConfigured(t *testing.T) {
	t.Setenv("JMS_BACKEND", "tui")
	t.Setenv("JMS_BASTION_HOST", " bastion.example.com ")
	t.Setenv("JMS_KOKO_USER", " ai ")

	cfg := load(t)
	if cfg.Koko.Host != "bastion.example.com" {
		t.Errorf("Host = %q, want it trimmed", cfg.Koko.Host)
	}
	if cfg.Koko.User != "ai" {
		t.Errorf("User = %q, want it trimmed", cfg.Koko.User)
	}
}

// "auto" is allowed to start without them: the connection-token path may be
// the one that works, and the doctor says which case it is.
func TestAutoStartsWithoutTheTerminalVariables(t *testing.T) {
	t.Setenv("JMS_BACKEND", "auto")
	t.Setenv("JMS_BASTION_HOST", "")
	t.Setenv("JMS_KOKO_USER", "")

	cfg := load(t)
	if cfg.Koko.Host != "" || cfg.Koko.User != "" {
		t.Errorf("the terminal variables were filled in: host=%q user=%q", cfg.Koko.Host, cfg.Koko.User)
	}
}
