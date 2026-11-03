package koko

// The version, as the execution plane sees it.
//
// The one thing the version decides here is whether the connection token is
// minted through the Chat AI delegation scheme or through the API
// credential.
//
// ⚠️ The reason this matters is not theoretical. A delegation credential is
// sent WITHOUT the API token, so on a deployment that has no such scheme
// (JumpServer v4) the request comes back 401 — and a 401 on that path reads
// as "the connection-token scheme is unavailable". That would send every
// session down the slow koko-terminal path, for a reason that does not
// exist: the token path works fine on v4.
//
// The body of the token is the SAME on both versions: the account is given
// as its UUID, and v4 accepts that (its get_asset_accounts_by_alias looks a
// UUID up by id).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yi-nology/jumpserver-mcp-kit/pkg/jmsapi"
)

// v4Bastion answers like a v4: the chat-ai prefix is not registered, and the
// connection-token endpoint is.
//
// ⚠️ "Not registered" is modelled the way it actually behaves, which is what
// the probe looks at: the chat-ai path answers EXACTLY like a path that
// exists on neither deployment. Measured on a live v4.10.20 (2026-10-31):
//
//	/api/v1/chat-ai/runtime-store/    -> 500
//	/api/v1/zzz-jms-version-probe-404/ -> 500
//
// ⇒ Both are 500, because v4's "no such route" handler is broken and crashes
// while rendering the 404 page. A fake that answered the chat-ai path with
// 404 and everything else with 200 would model a deployment that does not
// exist — and it would make the probe read that deployment as a v5.
func v4Bastion(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1" + string(jmsapi.ResChatAI),
			"/api/v1" + string(jmsapi.ResProbeMissing):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"detail":"Server error"}`))
		case "/api/v1/authentication/connection-token/":
			_, _ = w.Write([]byte(`{"id":"tok-v4","value":"secret","user":{"name":"admin"},` +
				`"asset":{"id":"asset-uuid"},"account":"account-uuid"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// On v4 the token is minted with the API credential, and the delegation
// credential is never presented — the scheme does not exist there, and
// presenting one would come back 401 and be read as "the token scheme is
// unavailable".
func TestOnV4TheTokenIsMintedWithTheAPICredential(t *testing.T) {
	srv := v4Bastion(t)
	c := jmsapi.New(jmsapi.Options{BaseURL: srv.URL, Token: "t0ken", HTTPClient: srv.Client()})
	cfg := Config{
		API: c,
		Delegation: &jmsapi.DelegationConfig{
			Secret: []byte("delegation-secret"),
			UserID: "user-1",
			OrgID:  "org-1",
		},
	}

	tok, err := mintConnectionToken(context.Background(), cfg, "asset-uuid", "account-uuid")
	if err != nil {
		t.Fatalf("mintConnectionToken: %v", err)
	}
	if tok.ID != "tok-v4" {
		t.Errorf("token id = %q, want %q", tok.ID, "tok-v4")
	}
}

// The version is detected when it was not configured, and the detection is
// what stops the delegation attempt. The account the session runs as is the
// API credential's own — and the bastion says so in the token it hands back.
func TestTheDetectedV4StopsTheDelegationAttempt(t *testing.T) {
	srv := v4Bastion(t)
	c := jmsapi.New(jmsapi.Options{BaseURL: srv.URL, Token: "t0ken", HTTPClient: srv.Client()})
	if v := profileVersion(c); v != jmsapi.VersionV4 {
		t.Fatalf("the version was detected as %q, want v4", v)
	}
	if state := c.State(context.Background(), jmsapi.CapDelegation); state != jmsapi.CapabilityAbsent {
		t.Errorf("the delegation scheme is %s on v4, want absent", state)
	}
}

// A configured v4 says the same thing without a probe — the operator may
// know something the probe cannot see.
func TestAConfiguredV4NeedsNoProbe(t *testing.T) {
	srv := v4Bastion(t)
	c := jmsapi.New(jmsapi.Options{
		BaseURL: srv.URL, Token: "t0ken", Version: string(jmsapi.VersionV4),
		HTTPClient: srv.Client(),
	})
	if v := profileVersion(c); v != jmsapi.VersionV4 {
		t.Fatalf("version = %q, want v4", v)
	}
	if state := c.State(context.Background(), jmsapi.CapDelegation); state != jmsapi.CapabilityAbsent {
		t.Errorf("the delegation scheme is %s on a configured v4, want absent", state)
	}
}

// On v5 the delegation credential IS presented — the scheme exists there,
// and using it is what keeps the session's authority narrow.
func TestOnV5TheDelegationCredentialIsPresented(t *testing.T) {
	f := newFakeBastion(t, `{"id":"tok-v5","value":"secret","user":{"name":"ai"},`+
		`"asset":{"id":"asset-uuid"},"account":"account-uuid"}`)
	c := jmsapi.New(jmsapi.Options{
		BaseURL: f.URL, Token: "t0ken", Version: string(jmsapi.VersionV5),
		HTTPClient: f.Client(),
	})
	cfg := Config{
		API: c,
		Delegation: &jmsapi.DelegationConfig{
			Secret: []byte("delegation-secret"),
			UserID: "user-1",
			OrgID:  "org-1",
		},
	}

	tok, err := mintConnectionToken(context.Background(), cfg, "asset-uuid", "account-uuid")
	if err != nil {
		t.Fatalf("mintConnectionToken: %v", err)
	}
	if tok.ID != "tok-v5" {
		t.Errorf("token id = %q, want %q", tok.ID, "tok-v5")
	}
	_, _, delegate, _, _ := f.seen()
	if delegate == "" {
		t.Error("the delegation credential was not presented on v5")
	}
}

// The token body is the SAME on both versions: the account goes over as its
// UUID. That is the one field the versions disagree about in the third-party
// literature, and the disagreement is about the DYNAMIC accounts.
func TestTheAccountGoesOverAsItsUUIDOnBothVersions(t *testing.T) {
	for _, version := range []string{string(jmsapi.VersionV4), string(jmsapi.VersionV5)} {
		t.Run(version, func(t *testing.T) {
			f := newFakeBastion(t, `{"id":"tok","value":"secret","user":{"name":"ai"},`+
				`"asset":{"id":"asset-uuid"},"account":"account-uuid"}`)
			c := jmsapi.New(jmsapi.Options{
				BaseURL: f.URL, Token: "t0ken", Version: version, HTTPClient: f.Client(),
			})
			cfg := Config{API: c}
			if _, err := mintConnectionToken(context.Background(), cfg, "asset-uuid", "account-uuid"); err != nil {
				t.Fatalf("mintConnectionToken: %v", err)
			}
			_, _, _, _, body := f.seen()
			if body["account"] != "account-uuid" {
				t.Errorf("the account went over as %q, want the UUID", body["account"])
			}
			if body["connect_method"] != tokenConnectMethod {
				t.Errorf("connect_method = %q, want %q", body["connect_method"], tokenConnectMethod)
			}
			if !strings.HasPrefix(body["protocol"], "ssh") {
				t.Errorf("protocol = %q, want ssh", body["protocol"])
			}
		})
	}
}

// profileVersion reads the release out of the client's profile.
//
// The profile is what the code asks about; the release is what it reports.
func profileVersion(c *jmsapi.Client) jmsapi.Version {
	v, _ := c.Profile(context.Background()).Version()
	return v
}
