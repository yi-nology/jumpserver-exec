package koko

// Minting a connection token: resolve the asset, pick the account, sign
// the one-shot credential, and log into koko with it.
//
// This is the token path's SETUP half — everything that happens before a
// session exists. Once the connection is open, tokenBackend (in token.go)
// runs commands on it; what lives here is the part that can fail BEFORE
// anything is sent, which is why every failure below is classified as
// not-sent (the command never left this process) rather than guessed at.
//
// The classification matters as much as the minting: "koko refused the
// token" (a mechanism failure — the terminal fallback exists for it) and
// "no account is authorized on that asset" (an asset problem — the
// terminal would fail the same way) get different words, because an
// operator fixes them differently.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/oekit/concurrency/syncx"
	"github.com/oekit/network/sshx"
	"github.com/oekit/pkg/slicex"
	"github.com/oekit/pkg/stringx"

	"github.com/yi-nology/jumpserver-kit/pkg/errs"
	"github.com/yi-nology/jumpserver-kit/pkg/jmsapi"
)

type connectionToken struct {
	ID    string
	Value string
	// User is who the token was minted for. Under delegation this is the
	// delegated user, not the API token's owner.
	User string
	// AssetID and AccountID are what the token is bound to.
	AssetID   string
	AccountID string
}

// openOwnConnection opens a connection that belongs to one session.
func openOwnConnection(ctx context.Context, cfg Config, asset, account string, timeout time.Duration) (*pooledConn, error) {
	ref, err := resolveAsset(ctx, cfg, asset)
	if err != nil {
		return nil, err
	}
	acct, tok, err := resolveAndMint(ctx, cfg, ref, account)
	if err != nil {
		return nil, err
	}
	client, err := dialKoko(ctx, cfg, tok, timeout)
	if err != nil {
		// The same refusal the pooled dial (dialPooled) wraps: koko
		// refusing a token login means the TOKEN MECHANISM does not work
		// on this bastion, which is exactly the case the terminal
		// fallback exists for — and openAuto decides that by asking
		// errors.Is(err, ErrTokenUnavailable). Without this wrap, a
		// deployment with no pool (every session opening its own
		// connection) lost the fallback on the very same refusal a
		// pooled deployment survives.
		if isAuthRefusal(err) {
			return nil, fmt.Errorf("%w: %w", ErrTokenUnavailable, err)
		}
		return nil, err
	}
	conn := &pooledConn{
		client:   client,
		gate:     syncx.NewGate(1),
		done:     make(chan struct{}),
		lastUse:  time.Now(),
		account:  acct.Username,
		identity: tok.User,
	}
	sshx.StartKeepAlive(conn.poke, conn.done, func() { conn.broken.Store(true) }, sshx.DefaultKeepAliveInterval)
	return conn, nil
}

// resolveAsset finds the asset the caller named.
//
// It goes through the cached resolver when there is one, because a fleet
// sweep resolves one asset per host and each resolution would otherwise
// fetch the whole estate.
//
// The two ways it can fail are filed differently, because the TUI fallback
// is only worth trying for one of them: a reference that names nothing is a
// problem with the REQUEST (the terminal would refuse it too, ten seconds
// later), while an API that cannot be reached is a problem with the
// MECHANISM (the terminal does not need the API at all).
func resolveAsset(ctx context.Context, cfg Config, asset string) (jmsapi.AssetRef, error) {
	if cfg.Assets != nil {
		out, err := cfg.Assets.Resolve(ctx, cfg.API, []string{asset})
		if err != nil {
			if errors.Is(err, jmsapi.ErrAssetNotFound) {
				return jmsapi.AssetRef{}, err
			}
			// ⚠️ %w and not %v, twice: the OUTER %w says "the token
			// mechanism is not usable here — the terminal may still
			// work", and the INNER one keeps the real cause in the chain
			// (a stale listing, an unreachable bastion). With %v the
			// inner error was flattened into text and nothing could tell
			// the two apart afterwards.
			//
			// ⚠️ What it does NOT do: give the stale-listing case a
			// handler of its own. Nothing inspects ErrListingStale yet;
			// the chain is simply inspectable now, so the handler can be
			// written when someone needs it.
			return jmsapi.AssetRef{}, fmt.Errorf("%w: %w", ErrTokenUnavailable, err)
		}
		if len(out) == 0 {
			return jmsapi.AssetRef{}, fmt.Errorf("%w: %q", jmsapi.ErrAssetNotFound, asset)
		}
		return out[0], nil
	}
	out, err := cfg.API.ResolveAsset(ctx, asset)
	if err != nil {
		if errors.Is(err, jmsapi.ErrAssetNotFound) {
			return jmsapi.AssetRef{}, err
		}
		return jmsapi.AssetRef{}, fmt.Errorf("%w: %w", ErrTokenUnavailable, err)
	}
	return out, nil
}

// pickAccount chooses the account the session runs as.
//
// The token binds ONE account, so there is no walking into a list and picking
// one interactively — the choice has to be made here, and it has to be
// visible to the caller, which is why the answer carries the account's name.
//
// When the caller names one, that one wins. When it does not, the choice is:
//
//  1. an ACTIVE account (a disabled one cannot be logged into);
//  2. among those, a NON-privileged one — a session that silently runs as
//     root when the caller expected an ordinary account is the worse
//     surprise, and the bastion's own guidance is the same: grant the
//     account by name, not "@ALL";
//  3. among those, the lowest username, so the same asset always gets the
//     same account.
//
// The TUI path let koko pick the first account of its list, which is neither
// stable nor stated. This is.
func pickAccount(ctx context.Context, cfg Config, assetID, want string) (jmsapi.AccountRef, error) {
	accts, err := assetAccounts(ctx, cfg, assetID)
	if err != nil {
		return jmsapi.AccountRef{}, fmt.Errorf("read the accounts of the asset: %w", err)
	}
	if len(accts) == 0 {
		return jmsapi.AccountRef{}, fmt.Errorf("%w: a session needs one (an asset permission that "+
			"grants no account cannot be logged into)", ErrNoAccountOnAsset)
	}
	if want != "" {
		for _, a := range accts {
			if strings.EqualFold(a.Username, want) || strings.EqualFold(a.Name, want) {
				return a, nil
			}
		}
		names := slicex.Map(accts, func(a jmsapi.AccountRef) string { return a.Username })
		sort.Strings(names)
		return jmsapi.AccountRef{}, fmt.Errorf("no account %q on that asset (have: %s)",
			want, strings.Join(names, ", "))
	}

	var active []jmsapi.AccountRef
	for _, a := range accts {
		if a.Active {
			active = append(active, a)
		}
	}
	if len(active) == 0 {
		return jmsapi.AccountRef{}, fmt.Errorf("%w: every account on it is disabled", ErrNoAccountOnAsset)
	}
	sort.Slice(active, func(i, j int) bool { return active[i].Username < active[j].Username })
	for _, a := range active {
		if !a.Privileged {
			return a, nil
		}
	}
	return active[0], nil
}

// assetAccounts reads the accounts of an asset, through the cached resolver
// when there is one.
//
// The failure is classified before it leaves, because the class decides what
// happens next: an API that cannot answer is a problem with the MECHANISM,
// and the terminal path does not need the API at all — so it is worth the
// walk. An asset that is not there is a problem with the REQUEST, and the
// terminal would answer exactly the same way, ten seconds later.
func assetAccounts(ctx context.Context, cfg Config, assetID string) ([]jmsapi.AccountRef, error) {
	var (
		accts []jmsapi.AccountRef
		err   error
	)
	if cfg.Assets != nil {
		accts, err = cfg.Assets.AssetAccountsOf(ctx, cfg.API, assetID)
	} else {
		accts, err = cfg.API.AssetAccounts(ctx, assetID)
	}
	if err != nil {
		return nil, classifyAccountRead(err)
	}
	return accts, nil
}

// classifyAccountRead says whether a failure to read an asset's accounts is
// worth falling back to the terminal for.
//
// 4xx is the REQUEST — the asset is not there, or the caller may not see it
// — and the terminal refuses it the same way. 5xx and transport failures are
// the MECHANISM, and the terminal does not go through the API at all.
//
// ⚠️ This is why the account cache does NOT serve a stale list (see
// jmsapi.NewAssetResolver): the list decides who the session runs as, and a
// stale one can name an account that has since been demoted.
func classifyAccountRead(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *errs.APIError
	if errors.As(err, &apiErr) && apiErr.Status < 500 {
		return err
	}
	return fmt.Errorf("%w: %w", ErrTokenUnavailable, err)
}

// mintConnectionToken asks the bastion for a one-shot credential.
//
// The delegation path is tried first: a token minted that way carries the
// DELEGATED user's authority, which is the same authority the sessions have
// always had. The ordinary path — the API token's own — is wider, and the
// doctor reports which one is in force so the difference is never invisible.
//
// ⚠️ The version decides whether the delegation path is TAKEN at all.
//
// JumpServer v4 has no Chat AI delegation scheme (no chat_ai application, no
// X-JMS-AI-Delegation authentication class, no chat-ai API prefix), and a
// delegation credential is sent WITHOUT the API token — so on v4 the request
// comes back 401. A 401 here is classified as "the connection-token scheme is
// unavailable", and that would send every session down the slow koko-terminal
// path, for a reason that does not exist: the token path works fine on v4.
//
// So on v4 the delegation branch is simply not entered, and the token is
// minted with the API credential. That is WIDER than a delegated session, and
// it is not hidden: the doctor's identity row says which version the
// deployment runs and what the sessions consequently run as.
func mintConnectionToken(ctx context.Context, cfg Config, assetID, accountID string) (connectionToken, error) {
	// The version check below reaches for the client, and so does the
	// ordinary path — so a client-less configuration is refused HERE, with
	// the same class of error the caller already knows how to read
	// (ErrTokenUnavailable means "the terminal may still work").
	if cfg.API == nil {
		return connectionToken{}, fmt.Errorf("%w: the bastion's REST API is not configured", ErrTokenUnavailable)
	}
	body := map[string]string{
		"asset":          assetID,
		"account":        accountID,
		"protocol":       tokenProtocol,
		"connect_method": tokenConnectMethod,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return connectionToken{}, err
	}

	if dc := cfg.Delegation; dc != nil && cfg.API.MayPresent(ctx, jmsapi.CapDelegation) {
		// The identity has to be resolved before the credential can be
		// signed: a delegation config with a secret but no user id cannot
		// be used, and falling back to the API token would quietly widen
		// the session's authority to that of the token's owner — an
		// administrator on this deployment.
		//
		// So the identity is resolved here, once, and kept in the config
		// for the next call.
		if ok, _ := dc.Available(); !ok {
			if len(dc.Secret) > 0 {
				if err := cfg.API.EnsureDelegationIdentityAs(ctx, dc, cfg.User); err != nil {
					return connectionToken{}, fmt.Errorf(
						"the delegation secret is configured but the delegated identity could not be resolved (%v); "+
							"minting a connection token with the API token instead would give the session that "+
							"token's authority rather than %s's", err, jmsapi.DelegationUsername(cfg.User))
				}
			}
		}
		if ok, _ := dc.Available(); ok {
			call := jmsapi.DelegationCall{
				Method:      "POST",
				Path:        string(jmsapi.ResConnectionToken),
				Body:        raw,
				OperationID: "assets.connect_asset",
			}
			status, resp, err := cfg.API.DelegationRequest(ctx, *dc, call)
			if err != nil {
				return connectionToken{}, fmt.Errorf("mint a connection token through the delegation credential: %w", err)
			}
			if status < 200 || status >= 300 {
				return connectionToken{}, classifyTokenRefusal(status, resp)
			}
			return parseConnectionToken(resp)
		}
	}

	var out map[string]any
	if err := cfg.API.Post(ctx, jmsapi.ResConnectionToken, body, &out); err != nil {
		return connectionToken{}, classifyTokenError(err)
	}
	return connectionTokenFromMap(out)
}

// parseConnectionToken reads the token out of a response body.
func parseConnectionToken(raw []byte) (connectionToken, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return connectionToken{}, fmt.Errorf("the connection token response was not JSON: %s",
			jmsapi.Truncate(string(raw), 200))
	}
	// A body that carries an error instead of a token. It is classified like
	// a refusal, so a deployment that answers an error with 200 is not
	// mistaken for one that works.
	if _, ok := m["id"]; !ok {
		if tokenErrorCode(raw) != "" {
			return connectionToken{}, classifyTokenRefusal(0, raw)
		}
	}
	return connectionTokenFromMap(m)
}

// connectionTokenFromMap reads the token out of a decoded response.
//
// The shape is {id, value, user: {...}, asset: {...}, account: "<uuid>",
// is_reusable, expire_time}. Both `id` and `value` have to be there: a token
// with one of them missing cannot be used, and a half-token that looks usable
// is worse than an error.
func connectionTokenFromMap(m map[string]any) (connectionToken, error) {
	tok := connectionToken{
		ID:        jmsapi.Str(m["id"]),
		Value:     jmsapi.Str(m["value"]),
		User:      jmsapi.Str(jmsapi.Nested(m, "user", "name")),
		AssetID:   jmsapi.Str(jmsapi.Nested(m, "asset", "id")),
		AccountID: jmsapi.Str(m["account"]),
	}
	if tok.ID == "" || tok.Value == "" {
		encoded, _ := json.Marshal(m)
		return connectionToken{}, fmt.Errorf("the connection token response carried no id/value: %s",
			jmsapi.Truncate(string(encoded), 300))
	}
	return tok, nil
}

// classifyTokenRefusal decides whether a refused connection-token request is
// about the asset or about the scheme.
//
// The bastion answers a refusal with a code, and the codes that start with
// "perm_" are about the caller's rights to that asset — no account on it, no
// permission covering it. Those would fail identically through the koko
// terminal, so the fallback would buy a ten-second walk to learn nothing.
//
// Everything else — the endpoint not being there, the deployment not
// offering tokens, the credential not being allowed to use them — is about
// the SCHEME, and that is exactly what the terminal is there for.
func classifyTokenRefusal(status int, body []byte) error {
	code := tokenErrorCode(body)
	detail := strings.TrimSpace(jmsapi.Truncate(string(body), 300))
	if strings.HasPrefix(code, "perm_") {
		return fmt.Errorf("the bastion refused the connection token: %s: %s", where(status, code), detail)
	}
	return fmt.Errorf("%w: the bastion refused the connection token: %s: %s",
		ErrTokenUnavailable, where(status, code), detail)
}

// where renders the status and code of a refusal, and copes with a refusal
// that carried no status (a body that is an error rather than a token).
func where(status int, code string) string {
	switch {
	case status <= 0 && code == "":
		return "the response was an error"
	case status <= 0:
		return code
	case code == "":
		return fmt.Sprintf("HTTP %d", status)
	default:
		return fmt.Sprintf("HTTP %d %s", status, code)
	}
}

// classifyTokenError is classifyTokenRefusal for the ordinary token path,
// whose failure arrives as a typed API error.
func classifyTokenError(err error) error {
	var apiErr *errs.APIError
	if errors.As(err, &apiErr) {
		return classifyTokenRefusal(apiErr.Status, []byte(apiErr.Body))
	}
	// The API could not be reached at all. The scheme is unusable here, and
	// the terminal may still work — it does not need the API.
	return fmt.Errorf("%w: %w", ErrTokenUnavailable, err)
}

// tokenErrorCode reads the code out of a refusal body.
//
// The bastion answers a refusal as {"detail": "...", "code": "..."}. A body
// that does not have one is not an error: the code is what tells a
// permission problem from a broken endpoint.
func tokenErrorCode(body []byte) string {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	return jmsapi.Str(m["code"])
}

// isAuthRefusal says whether koko refused the token as a credential.
//
// x/crypto/ssh has no typed error for it, so the message is all there is:
// "ssh: unable to authenticate, attempted methods ...". It is worth telling
// apart from a network failure, because only this one means the terminal
// fallback could work.
func isAuthRefusal(err error) bool {
	return err != nil && stringx.ContainsAny(err.Error(), "unable to authenticate")
}

// tokenUsername is the SSH username a connection token is used with.
//
// koko accepts the token as an SSH credential: the username is the token's
// id with a JMS- prefix, and the password is the token's value. Logging in
// that way lands straight on the asset.
func tokenUsername(id string) string {
	return "JMS-" + id
}

// dialKoko logs into koko with the token.
//
// The host key is accepted unauthenticated, which deserves an honest note:
// koko presents a host key this tool has no way to pin — there is no
// known_hosts contract for a bastion reached with a one-shot token, and the
// token itself is what authenticates the session. The trade-off is that an
// on-path attacker could impersonate koko and read or alter the session;
// what they cannot do is reuse it, because the token is single-use and
// MFA-bounded. The TUI path takes the same host key on the same host.
//
// see sshx.Dial, where the reason the handshake needs its own bound is
// spelled out (it lives in oekit/network/sshx now).
func dialKoko(ctx context.Context, cfg Config, tok connectionToken, timeout time.Duration) (*ssh.Client, error) {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	cc := &ssh.ClientConfig{
		User:            tokenUsername(tok.ID),
		Auth:            []ssh.AuthMethod{ssh.Password(tok.Value)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	client, err := dialWithHandshakeRetry(ctx, addr, cc, timeout, cfg)
	if err != nil {
		return nil, fmt.Errorf("log into koko at %s as the connection token: %w", addr, err)
	}
	return client, nil
}

// isHandshakeFailure says whether a dial error is a HANDSHAKE TIMEOUT: the
// peer accepted the connection but never spoke SSH — a link blip, safe to
// redial (nothing was ever delivered, so the never-replay rule does not
// apply to a dial that delivered nothing). The typed sentinel (network#116,
// absorbed this round) is the judgement, not the wording: the previous
// string matcher (`Contains("handshake")`) matched the REJECTION text too
// ("the SSH handshake with %s failed: unable to authenticate"), so an auth
// refusal has been silently redialled since this retry shipped — redialling
// wrong credentials and burying the verdict (AGENTS §5 said no-retry; the
// wording said retry). A rejection is a verdict about the credential, and
// stays un-retried (ErrHandshakeRejected fails this check).
func isHandshakeFailure(err error) bool {
	return errors.Is(err, sshx.ErrHandshakeTimeout)
}

// Exec runs one command on the asset.
//
// The command goes out on its own exec channel, so the exit code is the
// command's own and the output is exactly what the command wrote — nothing
// is reconstructed from a painted screen.
//
// Two failures are told apart, because only one of them may be retried:
//
//   - the channel could not be OPENED: the command never reached the
//     machine, so trying again is safe.
//   - the command was delivered and then the connection broke, or it ran
//     past the timeout: the command may still be running on the machine, so
//     it must NOT be run again.
