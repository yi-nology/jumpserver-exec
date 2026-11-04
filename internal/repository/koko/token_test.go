package koko

// Protocol regression tests.
//
// These pin the facts about the bastion's connection-token scheme that the
// token backend depends on. They are here because every one of them was
// measured against the live bastion on 2026-10-28 and every one of them is
// invisible to the compiler: a wrong account field is a runtime rejection, a
// wrong username is a failed login, and a wrong body key is a token for
// nothing.
//
// The account field is the one that cost the most to find. v5 wants the
// account's UUID; a username or an alias comes back as
// perm_account_invalid. That is where the belief that "koko refuses
// connection tokens" came from — it refused the wrong field, not the
// mechanism.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yi-nology/jumpserver-mcp-kit/pkg/jmsapi"
)

// fakeBastion answers the connection-token endpoint and records what it was
// asked.
type fakeBastion struct {
	*httptest.Server

	mu        sync.Mutex
	path      string
	auth      string
	delegate  string
	operation string
	body      map[string]string
}

func newFakeBastion(t *testing.T, reply string) *fakeBastion {
	t.Helper()
	return newFakeBastionStatus(t, 200, reply)
}

func newFakeBastionStatus(t *testing.T, status int, reply string) *fakeBastion {
	t.Helper()
	f := &fakeBastion{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ⚠️ The version probe asks TWO paths and compares their answers,
		// so the fake has to answer them DIFFERENTLY — that difference IS
		// the "this deployment has the scheme" signal.
		//
		// ⇒ They answer the way a v5 answers (measured on a live v5,
		// 2026-10-31): the chat-ai route 401, a path that exists on
		// neither 404.
		//
		// ⚠️ A fake that answered every path the same would be read as a
		// v4 — and the delegation credential would silently not be
		// presented, which is exactly what the delegation test checks.
		switch r.URL.Path {
		case "/api/v1" + string(jmsapi.ResChatAI):
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":"Authentication credentials were not provided."}`))
			return
		case "/api/v1" + string(jmsapi.ResProbeMissing):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Not found."}`))
			return
		}

		f.mu.Lock()
		f.path = r.URL.Path
		f.auth = r.Header.Get("Authorization")
		f.delegate = r.Header.Get(jmsapi.DelegationHeader)
		f.operation = r.Header.Get(jmsapi.OperationHeader)
		f.body = map[string]string{}
		_ = json.NewDecoder(r.Body).Decode(&f.body)
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeBastion) seen() (path, auth, delegate, operation string, body map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.path, f.auth, f.delegate, f.operation, f.body
}

func TestMintConnectionTokenSendsTheShapeTheBastionAccepts(t *testing.T) {
	const reply = `{"id":"tok-1","value":"secret","user":{"name":"ai"},` +
		`"asset":{"id":"asset-uuid"},"account":"account-uuid"}`
	f := newFakeBastion(t, reply)
	c := jmsapi.New(jmsapi.Options{BaseURL: f.URL, Token: "t0ken", HTTPClient: f.Client()})

	tok, err := mintConnectionToken(context.Background(), Config{API: c}, "asset-uuid", "account-uuid")
	if err != nil {
		t.Fatalf("mintConnectionToken: %v", err)
	}

	path, auth, delegate, _, body := f.seen()
	if path != "/api/v1/authentication/connection-token/" {
		t.Errorf("path = %q, want the connection-token endpoint", path)
	}
	// The bastion's PrivateToken is presented as "Token <key>", not Bearer.
	if auth != "Token t0ken" {
		t.Errorf("Authorization = %q, want %q", auth, "Token t0ken")
	}
	if delegate != "" {
		t.Errorf("a delegation header was sent on the ordinary path: %q", delegate)
	}
	if body["asset"] != "asset-uuid" {
		t.Errorf("asset = %q, want the asset's uuid", body["asset"])
	}
	// The account is the account's UUID. A username here is what makes the
	// bastion answer perm_account_invalid.
	if body["account"] != "account-uuid" {
		t.Errorf("account = %q, want the account's uuid", body["account"])
	}
	if body["protocol"] != "ssh" {
		t.Errorf("protocol = %q, want %q", body["protocol"], "ssh")
	}
	if body["connect_method"] != "web_cli" {
		t.Errorf("connect_method = %q, want %q", body["connect_method"], "web_cli")
	}
	if tok.ID != "tok-1" || tok.Value != "secret" {
		t.Errorf("token = %q/%q, want tok-1/secret", tok.ID, tok.Value)
	}
	if tok.User != "ai" {
		t.Errorf("token user = %q, want %q", tok.User, "ai")
	}
}

func TestMintConnectionTokenGoesThroughTheDelegationCredentialWhenThereIsOne(t *testing.T) {
	const reply = `{"id":"tok-2","value":"secret2","user":{"name":"ai"},"asset":{"id":"a"},"account":"c"}`
	f := newFakeBastion(t, reply)
	c := jmsapi.New(jmsapi.Options{BaseURL: f.URL, Token: "t0ken", HTTPClient: f.Client()})
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
	if tok.ID != "tok-2" {
		t.Errorf("token id = %q, want tok-2", tok.ID)
	}

	path, auth, delegate, operation, _ := f.seen()
	if path != "/api/v1/authentication/connection-token/" {
		t.Errorf("path = %q, want the connection-token endpoint", path)
	}
	// The whole point of a delegation credential: the long-lived token is
	// NOT on the request, so the call is authorised as the delegated user
	// and nothing else.
	if auth != "" {
		t.Errorf("the long-lived token was sent alongside the delegation credential: %q", auth)
	}
	if delegate == "" {
		t.Error("no delegation credential was sent")
	}
	if operation != "assets.connect_asset" {
		t.Errorf("operation = %q, want %q", operation, "assets.connect_asset")
	}
}

func TestAMissingDelegationSecretFallsBackToTheTokenWithoutComplaining(t *testing.T) {
	const reply = `{"id":"tok-3","value":"secret3","user":{"name":"admin"},"asset":{"id":"a"},"account":"c"}`
	f := newFakeBastion(t, reply)
	c := jmsapi.New(jmsapi.Options{BaseURL: f.URL, Token: "t0ken", HTTPClient: f.Client()})
	// A delegation config without a secret cannot be used. The token is
	// then minted with the API token — which is WIDER, and the doctor says
	// so — but the call still works.
	cfg := Config{API: c, Delegation: &jmsapi.DelegationConfig{UserID: "u", OrgID: "o"}}

	tok, err := mintConnectionToken(context.Background(), cfg, "asset-uuid", "account-uuid")
	if err != nil {
		t.Fatalf("mintConnectionToken: %v", err)
	}
	if tok.ID != "tok-3" {
		t.Errorf("token id = %q, want tok-3", tok.ID)
	}
	_, auth, delegate, _, _ := f.seen()
	if delegate != "" {
		t.Errorf("a delegation header was sent without a secret: %q", delegate)
	}
	if auth != "Token t0ken" {
		t.Errorf("Authorization = %q, want the token to carry the call", auth)
	}
}

func TestTheSSHUsernameIsTheTokensId(t *testing.T) {
	// koko accepts the token as an SSH credential: the username is the
	// token's id with a JMS- prefix, and the password is the token's value.
	if got := tokenUsername("4e5da125-34ed-419c-8f5b-c5e416dcc369"); got !=
		"JMS-4e5da125-34ed-419c-8f5b-c5e416dcc369" {
		t.Errorf("tokenUsername = %q, want the JMS- prefixed token id", got)
	}
}

func TestATokenWithoutBothHalvesIsRefused(t *testing.T) {
	// A token with an id but no value looks usable and is not. Half a
	// credential is worse than an error.
	for _, reply := range []string{
		`{"id":"tok-4"}`,
		`{"value":"secret4"}`,
		`{"id":"","value":"secret4"}`,
	} {
		if _, err := parseConnectionToken([]byte(reply)); err == nil {
			t.Errorf("a token without both halves was accepted: %s", reply)
		}
	}
}

func TestPickAccountPrefersTheOneThatWasNamed(t *testing.T) {
	accts := []map[string]any{
		{"id": "a1", "name": "opsuser", "username": "opsuser", "is_active": true},
		{"id": "a2", "name": "root", "username": "root", "is_active": true},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "asset-1", "accounts": accts})
	}))
	defer srv.Close()
	c := jmsapi.New(jmsapi.Options{BaseURL: srv.URL, Token: "t", HTTPClient: srv.Client()})

	got, err := pickAccount(context.Background(), Config{API: c}, "asset-1", "root")
	if err != nil {
		t.Fatalf("pickAccount: %v", err)
	}
	if got.ID != "a2" {
		t.Errorf("picked %q, want the account that was named", got.ID)
	}
	if _, err := pickAccount(context.Background(), Config{API: c}, "asset-1", "nobody"); err == nil {
		t.Error("an unknown account name was accepted")
	}
}

func TestPickAccountChoosesDeterministicallyWhenNoneIsNamed(t *testing.T) {
	accts := []map[string]any{
		{"id": "a2", "name": "root", "username": "root", "is_active": true, "privileged": true},
		{"id": "a1", "name": "opsuser", "username": "opsuser", "is_active": true},
		{"id": "a3", "name": "old", "username": "old", "is_active": false},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "asset-1", "accounts": accts})
	}))
	defer srv.Close()
	c := jmsapi.New(jmsapi.Options{BaseURL: srv.URL, Token: "t", HTTPClient: srv.Client()})

	// The active, non-privileged account with the lowest username: the same
	// answer every time, never a disabled one, and never the privileged one
	// when an ordinary account is available.
	for i := 0; i < 3; i++ {
		got, err := pickAccount(context.Background(), Config{API: c}, "asset-1", "")
		if err != nil {
			t.Fatalf("pickAccount: %v", err)
		}
		if got.ID != "a1" {
			t.Fatalf("picked %q, want the active account with the lowest username", got.ID)
		}
	}
}

func TestTheMergedBufferLosesNothingWhenBothStreamsWrite(t *testing.T) {
	// stdout and stderr are copied by two goroutines inside x/crypto/ssh,
	// so the buffer they share has to be safe for concurrent writes. A plain
	// bytes.Buffer is not, and the race does not fail loudly: it shows up as
	// output that silently vanishes, so a command exits 0 and prints nothing.
	m := &mergedBuffer{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			chunk := bytes.Repeat([]byte{byte('a' + i)}, 4096)
			for j := 0; j < 64; j++ {
				_, _ = m.Write(chunk)
			}
		}(i)
	}
	wg.Wait()
	if got := len(m.String()); got != 8*64*4096 {
		t.Errorf("the merged buffer holds %d bytes, want %d", got, 8*64*4096)
	}
}

func TestTheBackendModeIsValidated(t *testing.T) {
	_, err := Open(context.Background(), Config{Backend: "telnet"}, "host", "", 0)
	if err == nil {
		t.Fatal("an unknown backend was accepted")
	}
	if !strings.Contains(err.Error(), "telnet") {
		t.Errorf("the error does not name the bad mode: %v", err)
	}
}

func TestAPermissionProblemIsNotTheSchemeBeingUnavailable(t *testing.T) {
	// The bastion refuses the token with perm_account_invalid: the asset
	// has no usable account. The koko terminal would refuse the same
	// asset the same way — ten seconds later. So this must NOT be
	// classified as "the scheme is unavailable", or the fallback would
	// buy a walk to learn what it already knew.
	f := newFakeBastion(t, `{"code":"perm_account_invalid","detail":"Account not found"}`)
	c := jmsapi.New(jmsapi.Options{BaseURL: f.URL, Token: "t0ken", HTTPClient: f.Client()})

	_, err := mintConnectionToken(context.Background(), Config{API: c}, "asset-uuid", "account-uuid")
	if err == nil {
		t.Fatal("the refusal was accepted")
	}
	if errors.Is(err, ErrTokenUnavailable) {
		t.Errorf("a permission problem was classified as the scheme being unavailable: %v", err)
	}
	if !strings.Contains(err.Error(), "perm_account_invalid") {
		t.Errorf("the error does not name the refusal: %v", err)
	}
}

func TestAMissingEndpointIsTheSchemeBeingUnavailable(t *testing.T) {
	// The endpoint is not there: the deployment does not offer connection
	// tokens. This is the case the terminal fallback exists for.
	f := newFakeBastionStatus(t, 404, `{"detail":"Not found."}`)
	c := jmsapi.New(jmsapi.Options{BaseURL: f.URL, Token: "t0ken", HTTPClient: f.Client()})

	_, err := mintConnectionToken(context.Background(), Config{API: c}, "asset-uuid", "account-uuid")
	if err == nil {
		t.Fatal("the refusal was accepted")
	}
	if !errors.Is(err, ErrTokenUnavailable) {
		t.Errorf("a missing endpoint was not classified as the scheme being unavailable: %v", err)
	}
}

func TestAMissingAPIClientIsTheSchemeBeingUnavailable(t *testing.T) {
	_, err := openToken(context.Background(), Config{}, "asset", "", 0)
	if err == nil {
		t.Fatal("a missing API client was accepted")
	}
	if !errors.Is(err, ErrTokenUnavailable) {
		t.Errorf("a missing API client was not classified as the scheme being unavailable: %v", err)
	}
}
