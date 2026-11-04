// Package doctor is the "doctor" subcommand: the wiring self-check.
//
// ⚠️ 它回答的是「这个 server 能不能开会话？」—— 不用 MCP 会话就能问，
// 而这正是接线出问题时，运维手边唯一能问的问题。
//
// ⚠️ 这一面的判定与**管理面不一样**：只有 FAIL 计入退出码，WARN 只是
// 「看一眼」（koko 走 TUI 还是走 connection-token，取决于部署）。
// 措辞也不借用管理面的（那边把 WARN 也算「需要注意」）。
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/oekit/network/sshx"
	"github.com/oekit/pkg/cli"
	"github.com/yi-nology/jumpserver-exec/internal/config"
	"github.com/yi-nology/jumpserver-exec/internal/repository/koko"
	"github.com/yi-nology/jumpserver-kit/model/jms"
	"github.com/yi-nology/jumpserver-kit/pkg/audit"
	"github.com/yi-nology/jumpserver-kit/pkg/jmsapi"
)

// The wiring check: it answers "would this server be able to open a
// session?" without an MCP session, which is the question an operator has
// when the tools are not answering.
type doctorCheck struct {
	Status string // "ok" | "warn" | "fail"
	Name   string
	Detail string
}

func renderDoctor(name string, header []string, checks []doctorCheck) (string, int) {
	r := cli.NewReport(name + " doctor")
	r.Header(header...)
	failures := 0
	for _, ck := range checks {
		status := cli.StatusOK
		switch ck.Status {
		case "warn":
			status = cli.StatusWarn
		case "fail":
			status = cli.StatusFail
			failures++
		}
		r.Add(status, ck.Name, ck.Detail)
	}
	// The exec side has its own verdict wording on purpose: only a FAIL
	// counts towards the exit code here -- a warning is something to look
	// at, not a broken setup -- so it must not borrow the management
	// side's wording.
	if failures == 0 {
		r.Result("result: healthy")
	} else {
		r.Result(fmt.Sprintf("result: %d problem(s) need fixing (see FAIL above)", failures))
	}
	return r.String(), failures
}

// Run checks the wiring and prints the report.
//
// It takes its streams from the caller and returns the exit code instead of
// terminating the process, so a test can drive it directly.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		// A configuration that cannot be read is the finding; there is
		// nothing else worth checking.
		//
		// ⚠️ It goes to STDERR, in the same wording as the management
		// plane's: the two sides must say the same thing about the same
		// empty value, and an operator who redirects stdout to a log file
		// must not lose the line that says why the server did not start.
		fmt.Fprintf(stderr, "FATAL  %v\n", err)
		return cli.ExitError
	}
	backend := strings.ToLower(strings.TrimSpace(cfg.Koko.Backend))
	if backend == "" {
		backend = koko.BackendAuto
	}
	// The header states the wiring as it is, and an unset variable says so
	// rather than printing an empty field: "bastion: :2222 as " reads like
	// a broken value, not like a missing one.
	host, user := cfg.Koko.Host, cfg.Koko.User
	if host == "" {
		host = "(not configured)"
	}
	if user == "" {
		user = "(not configured)"
	}
	header := []string{
		fmt.Sprintf("bastion: %s:%d as %s", host, cfg.Koko.Port, user),
		fmt.Sprintf("key:     %s", cfg.Koko.KeyPath),
		fmt.Sprintf("backend: %s", backend),
	}
	checks := []doctorCheck{}
	report := func(status, name, detail string) {
		checks = append(checks, doctorCheck{Status: status, Name: name, Detail: detail})
	}

	// The key is only what opens a session when the session is opened by
	// walking the terminal. The connection-token backend authenticates with
	// a one-shot credential instead, so a missing key is a different kind of
	// problem in each mode — and saying "the key is missing" to an operator
	// whose deployment does not use keys would send them after the wrong
	// thing.
	usesKey := backend == koko.BackendTUI || backend == koko.BackendAuto
	keyInfo, err := os.Stat(cfg.Koko.KeyPath)
	switch {
	case err != nil:
		if usesKey {
			detail := fmt.Sprintf("cannot read %s: %v", cfg.Koko.KeyPath, err)
			if backend == koko.BackendAuto {
				detail += " (the connection-token backend does not need it; the TUI fallback does)"
			}
			report("fail", "key_file", detail)
		} else {
			report("ok", "key_file", fmt.Sprintf(
				"not needed by the %s backend; %s is absent", backend, cfg.Koko.KeyPath))
		}
	case keyInfo.IsDir():
		report("fail", "key_file", cfg.Koko.KeyPath+" is a directory")
	default:
		report("ok", "key_file", fmt.Sprintf("present, %d bytes", keyInfo.Size()))
	}

	// koko rejects ed25519 keys while the bastion accepts them silently, so
	// the key type is worth checking before anything else: a wrong type looks
	// exactly like a permission problem.
	if usesKey {
		if _, err := os.Stat(cfg.Koko.KeyPath); err == nil {
			// The probe may shell out to ssh-keygen, which opens /dev/tty
			// for a passphrase-protected key and blocks there forever.
			// A self-check that hangs is worse than one that says "cannot
			// tell" -- so the probe gets its own budget.
			keyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			kind, err := sshx.DetectKeyType(keyCtx, cfg.Koko.KeyPath)
			cancel()
			if err != nil {
				report("warn", "key_type", err.Error())
			} else if kind != "ssh-rsa" {
				report("fail", "key_type", fmt.Sprintf(
					"the key is %s, but koko only accepts ssh-rsa: generate one with "+
						"'ssh-keygen -t rsa -b 4096 -f ~/.ssh/id_rsa' and point JMS_KOKO_KEY at it", kind))
			} else {
				report("ok", "key_type", "ssh-rsa, which is the only type koko accepts")
			}
		}
	}

	// The REST client is needed by the inventory, and by the connection-token
	// backend, which resolves the asset and mints the token through it.
	//
	// ⚠️ cfg.Koko.API is THE client — the one the sessions use — and not
	// cfg.APIClient(), which builds a new one on every call. It matters
	// here because the version is resolved ON THE CLIENT: a second client
	// would ask the deployment again, and the answers could differ.
	api := cfg.Koko.API

	// ⚠️ The REST side is ASKED FIRST, and its answer is what the
	// configuration rows below are judged by. "A credential is configured"
	// is NOT the same question as "the connection-token path can open a
	// session" — a credential without a URL cannot (the request goes out
	// with a relative path and comes back "unsupported protocol scheme"),
	// and judging the severity on the first question would tell the
	// operator that a dead path is the live one.
	var restErr error
	restOK := false
	if api != nil {
		var raw json.RawMessage
		restErr = api.GetQuery(ctx, jmsapi.ResAssets, map[string][]string{"page_size": {"1"}}, &raw)
		restOK = restErr == nil
	}

	// The bastion's address and the login name: both are configuration, and
	// NEITHER has a default (a default would point one deployment's
	// clients at somebody else's bastion).
	//
	// ⚠️ They are checked here, BEFORE the port check, because the port
	// check cannot report them: a missing host makes it say "cannot reach
	// :2222", which reads like a network problem rather than an unset
	// variable — and sends the operator after the wrong thing.
	bc := bastionCheck(cfg.Koko.Host, cfg.Koko.User, cfg.Koko.Port, backend, restOK)
	report(bc.Status, bc.Name, bc.Detail)

	// TCP reachability of the koko port.
	//
	// ⚠️ An unset host is NOT probed: the address would be ":2222", and Go
	// reads an empty host as THIS machine ⇒ the probe would answer a
	// question nobody asked (is something listening on this box's 2222?)
	// and report it as the bastion's reachability — a green light for the
	// wrong box, or a red one for a deployment that never needed the port.
	// So it is skipped, and the configuration row above says what is
	// missing.
	if cfg.Koko.Host == "" {
		report("warn", "koko_port", "not checked: JMS_BASTION_HOST is not set, so there is no bastion to probe")
	} else {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", cfg.Koko.Host, cfg.Koko.Port), 10*time.Second)
		if err != nil {
			report("fail", "koko_port", fmt.Sprintf("cannot reach %s:%d: %v", cfg.Koko.Host, cfg.Koko.Port, err))
		} else {
			conn.Close()
			report("ok", "koko_port", fmt.Sprintf("%s:%d accepted a connection", cfg.Koko.Host, cfg.Koko.Port))
		}
	}

	if api == nil {
		if backend == koko.BackendToken {
			report("fail", "rest_client", "no credential configured (the connection-token backend needs "+
				"JUMPSERVER_TOKEN, or JUMPSERVER_USERNAME with JUMPSERVER_PASSWORD)")
		} else {
			report("warn", "rest_client", "no credential configured (jmsx_inventory is disabled; exec still works)")
		}
	} else if !restOK {
		if backend == koko.BackendToken {
			report("fail", "rest_client", restErr.Error()+" (the connection-token backend needs it)")
		} else {
			report("warn", "rest_client", restErr.Error()+" (jmsx_inventory is disabled; exec still works)")
		}
	} else {
		report("ok", "rest_client", "the bastion API answered")
	}

	// What is known about the delegation scheme, asked ONCE.
	//
	// ⚠️ It is asked once because two rows below need it (the version row
	// and the identity row) and a question asked twice is a question that
	// can be answered twice differently. It has THREE answers, so the two
	// rows have to agree on which one they are talking about.
	state := jmsapi.CapabilityUnknown
	if api != nil {
		state = api.Profile(ctx).State(jmsapi.CapDelegation)
	}

	// Which version the deployment runs.
	//
	// Which version the deployment runs — and, more usefully, WHICH
	// CAPABILITIES it has.
	//
	// The capability is the actionable half: it decides whether a
	// delegation credential may be presented, and a 401 on that path is
	// read as "the connection-token scheme is unavailable", which sends
	// every session down the slow terminal path. So the report says both:
	// the release (what the operator configured) and what the deployment
	// can actually do.
	if api != nil {
		rep := api.ReportVersion(ctx)
		// ⚠️ The judgement AND the sentences come from pkg/jmsapi: the two
		// doctors used to write this row separately, and the copies had
		// already drifted.
		row := jmsapi.VersionRowFor(rep, state, jmsapi.UnknownPresented)
		detail := row.Message
		if row.Advice != "" {
			// ⚠️ The execution plane's report has no advice column — it is
			// one string per row — ⇒ the advice rides along in the same
			// string. The WORDING is still the shared one.
			detail += "; " + row.Advice
		}
		report(row.Status.String(), "version", detail)
	}

	// WHO the sessions are authorised as. This is the one thing an operator
	// cannot work out from the rest of the report, and the difference is not
	// cosmetic: a session authorised as the API token's owner carries an
	// administrator's reach, while a delegated one carries the `ai`
	// account's — the same authority the sessions have always had.
	//
	// The check resolves the identity rather than assuming it, because a
	// delegation secret without a resolvable user id falls back to the API
	// token — and that fallback is exactly the widening this is here to
	// make visible.
	//
	// ⚠️ The name in these rows is the EFFECTIVE one — the name the
	// sessions will actually act as — not the raw configuration string.
	// JMS_KOKO_USER has no default (a default would point one deployment's
	// clients at somebody else's account), and an unset one falls back to
	// "ai" inside the shared layer ⇒ a doctor that printed the raw value
	// would say the sessions act as "" while they act as "ai" — a report
	// that is not true, in the one row an operator cannot work out from
	// the rest of the report.
	effUser := jmsapi.DelegationUsername(cfg.Koko.User)
	unsetUser := ""
	if cfg.Koko.User == "" {
		unsetUser = " (JMS_KOKO_USER is not set, so the fallback account is used — " +
			"set it to the bastion account whose SSH public key is registered)"
	}
	switch {
	case api == nil:
		report("warn", "identity", "the bastion API is not reachable, so who the sessions run as "+
			"cannot be established")
	case state == jmsapi.CapabilityUnknown:
		// ⚠️ NOT "this deployment has no scheme": the scheme's presence
		// was NOT established, and saying otherwise would send the operator
		// to configure something that may not be there.
		//
		// The execution plane's policy is to present anyway: a deployment
		// that HAS the scheme keeps its narrow sessions, and one that does
		// not falls back to the slow terminal path.
		report("warn", "identity", jmsapi.Reason(jmsapi.CapDelegation, state)+
			"; it is treated as available, so a credential is presented (the sessions stay narrow, "+
			"but a deployment without the scheme would fall back to the slow terminal path)")
	case state == jmsapi.CapabilityAbsent:
		// The deployment does not HAVE the delegation scheme. There is
		// nothing to configure into existence, and the consequence is the
		// point: the sessions run with the API credential's authority.
		// ⚠️ The wording comes from the capability declaration, so this
		// line and the management plane's line cannot drift apart.
		report("warn", "identity", jmsapi.Explain(jmsapi.CapDelegation, state)+
			" (the sessions here run as the "+effUser+" account only when the "+
			"delegation scheme is available)"+unsetUser)
	case cfg.Koko.Delegation == nil:
		report("warn", "identity", "no delegation secret: connection tokens are minted with the API token, "+
			"so a session carries that token's authority (an administrator on this deployment) "+
			"rather than the "+effUser+" account. Set JMS_DELEGATION_SECRET or write the secret to "+
			"~/.config/jumpserver-mgmt/delegation.key to keep the narrower identity"+unsetUser)
	default:
		dc := *cfg.Koko.Delegation
		if err := api.EnsureDelegationIdentityAs(ctx, &dc, cfg.Koko.User); err != nil {
			report("warn", "identity", fmt.Sprintf(
				"a delegation secret is configured but the delegated identity could not be resolved: %v", err))
		} else {
			report("ok", "identity", fmt.Sprintf(
				"connection tokens are minted as the delegated user %q (the account the sessions have always run as)%s",
				effUser, unsetUser))
		}
	}

	// The connection pool: how many connections are open right now, and how
	// long an idle one is kept. It is the answer to "why is my repeat sweep
	// so fast", and also to "why does the bastion show so many open
	// sessions".
	if cfg.Koko.Pool != nil {
		report("ok", "connection_pool", fmt.Sprintf(
			"%d connection(s) open; an idle one is closed after %s. They do NOT show up in "+
				"the bastion's session list (it records the per-command exec session, which ends with "+
				"the command), but each one holds an sshd session process on its asset",
			cfg.Koko.Pool.Size(), koko.PoolTTL))
	}

	// The trail: where the gate's own record of this machine is kept. It is
	// the only place a REFUSED call leaves a trace — a dangerous command
	// without a confirmation never reaches the bastion, so the bastion has
	// no session and no log line for it.
	//
	// The mode comes first: a trail that is switched off must not be
	// reported as present, or the doctor says "healthy" about something
	// that is not there.
	switch {
	case cfg.Audit.Mode != "file":
		report("warn", "audit_trail", fmt.Sprintf(
			"off (JMS_AUDIT=%s): the gate's trail goes to the log only, so a call that did NOT run "+
				"leaves no record on this machine", cfg.Audit.Mode))
	default:
		status, detail := auditTrailCheck(cfg.Audit.TrailDir())
		report(status, "audit_trail", detail)
	}

	// The backend memory: which backend worked last time, and how long ago.
	//
	// It is worth reporting because it changes the ORDER the backends are
	// tried in — and a stale memory is a thing an operator should be able
	// to see, not a thing that silently shapes the traffic.
	if m := cfg.Koko.Memory; m != nil {
		entries := m.Entries()
		switch {
		case len(entries) == 0:
			report("ok", "backend_memory", "nothing remembered yet; the connection-token path is tried first")
		case entries[0].Backend == koko.BackendTUI && !entries[0].Expired:
			// The fast path is not being tried, and nothing says so in
			// the rest of the report: a doctor that says "healthy"
			// while the fast path is switched off is the thing this
			// line exists to prevent.
			report("warn", "backend_memory", fmt.Sprintf(
				"the koko terminal path worked %s ago and is being tried FIRST: the connection-token path "+
					"has not been tried since. The memory expires on its own, and then the connection-token "+
					"path is tried again", entries[0].Age.Truncate(time.Second)))
		case entries[0].Expired:
			report("warn", "backend_memory", fmt.Sprintf(
				"%s worked %s ago, but the memory has expired: the connection-token path is tried first again",
				entries[0].Backend, entries[0].Age.Truncate(time.Second)))
		default:
			report("ok", "backend_memory", fmt.Sprintf(
				"%s worked %s ago; it is tried first while the memory is fresh",
				entries[0].Backend, entries[0].Age.Truncate(time.Second)))
		}
	}

	// The conclusive check: open a real session and run a command.
	if asset := firstLinuxAsset(); asset != "" {
		k, err := koko.Open(ctx, cfg.Koko, asset, "", 120*time.Second)
		if err != nil {
			report("fail", "koko_login", fmt.Sprintf("could not open a session on %s: %v", asset, err))
		} else {
			defer k.Close()
			out, code, _, err := k.Exec("id -un; hostname", 60*time.Second)
			if err != nil {
				report("fail", "koko_login", fmt.Sprintf("session on %s opened but the probe failed: %v", asset, err))
			} else if reason := k.FallbackReason(); reason != "" {
				// The session worked, but through the slow path. That is
				// worth a warning rather than a pass: the fast path is not
				// working, and every session is paying ten extra seconds
				// for it without anybody being told.
				report("warn", "koko_login", fmt.Sprintf(
					"the session on %s fell back to the koko terminal, so the connection-token path is "+
						"not working: %s. It works, but it costs about ten seconds more to open", asset, reason))
				report("ok", "koko_login_probe", fmt.Sprintf(
					"logged in on %s as %s (exit %d, host says %q)",
					asset, k.Identity(), code, strings.TrimSpace(out)))
			} else {
				report("ok", "koko_login", fmt.Sprintf(
					"logged in on %s via the %s backend as %s (opening took %.2fs, exit %d, host says %q)",
					asset, k.Name(), k.Identity(), k.WalkDuration().Seconds(), code,
					strings.TrimSpace(out)))
			}
		}
	} else {
		report("warn", "koko_login", "no asset was available to test a login against")
	}

	text, failures := renderDoctor("jumpserver-exec", header, checks)
	fmt.Fprint(stdout, text)
	if failures == 0 {
		return cli.ExitOK
	}
	return cli.ExitError
}

// bastionCheck says whether the koko side is configured well enough to open
// a session.
//
// It is a function of its own so that a test can drive it WITHOUT a bastion:
// the check is about THIS installation's configuration, and a check nobody
// can exercise is a check that quietly stops checking.
//
// ⚠️ The severity is not one thing: what an unset host MEANS depends on how
// the session would be opened. The connection-token backend never touches
// koko, so an unset host costs nothing there; the terminal backend cannot
// work without it; and "auto" only loses its fallback.
//
// ⚠️ `tokenPathWorks` is NOT "a credential is configured" — it is "the
// connection-token path actually reaches the bastion". A credential without
// a URL is configured and useless, and judging the severity on the former
// would tell the operator that a dead path is the live one.
func bastionCheck(host, user string, port int, backend string, tokenPathWorks bool) doctorCheck {
	if host != "" && user != "" {
		return doctorCheck{Status: "ok", Name: "bastion",
			Detail: fmt.Sprintf("%s:%d as %s", host, port, user)}
	}
	missing := make([]string, 0, 2)
	if host == "" {
		missing = append(missing, "JMS_BASTION_HOST (the host running koko, usually the same host as JUMPSERVER_URL)")
	}
	if user == "" {
		missing = append(missing, "JMS_KOKO_USER (the bastion account whose SSH public key is registered)")
	}
	detail := "not set: " + strings.Join(missing, "; ")
	switch {
	case backend == koko.BackendToken && tokenPathWorks:
		return doctorCheck{Status: "ok", Name: "bastion",
			Detail: detail + " (the connection-token backend does not need them)"}
	case backend == koko.BackendToken:
		return doctorCheck{Status: "fail", Name: "bastion",
			Detail: detail + " — and the connection-token backend cannot reach the bastion either " +
				"(see rest_client), so no session can be opened at all"}
	case !tokenPathWorks:
		return doctorCheck{Status: "fail", Name: "bastion",
			Detail: detail + " — and the bastion's REST API is not usable either, so no session can be opened at all"}
	default:
		return doctorCheck{Status: "warn", Name: "bastion",
			Detail: detail + " — the koko terminal fallback is unavailable, so only the connection-token path can open a session"}
	}
}

func firstLinuxAsset() string {
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	c := cfg.APIClient()
	if c == nil {
		return ""
	}
	var raw json.RawMessage
	if err := c.GetQuery(context.Background(), jmsapi.ResAssets, map[string][]string{"page_size": {"200"}}, &raw); err != nil {
		return ""
	}
	rows, err := jmsapi.List(raw)
	if err != nil {
		return ""
	}
	for _, r := range rows {
		var a map[string]any
		if json.Unmarshal(r, &a) != nil {
			continue
		}
		if strings.EqualFold(fmt.Sprint(jmsapi.Nested(a, "platform", "name")), "Linux") {
			return fmt.Sprint(a["name"])
		}
	}
	return ""
}

// auditTrailCheck says what the trail directory looks like from the
// operator's side.
//
// It is a function of its own so that a test can drive it WITHOUT the
// bastion: this check is about THIS machine, and a check nobody can
// exercise is a check that quietly stops checking.
//
// ⚠️ The check itself lives in the shared base (audit.CheckTrail), because
// the management plane has to run it too: the two servers write the same
// directory BY DEFAULT, but the directory is configurable, so each server
// has to check its OWN. Two copies of the check would drift, and the
// drifted one is the one that decides whether a broken trail is reported.
func auditTrailCheck(dir string) (status, detail string) {
	st, reason, detail := audit.CheckTrail(dir)
	// ⚠️ The advice comes from the shared base, so both doctors give the
	// same one — a check that is one check and an advice that only one
	// doctor can give is advice half the operators never get.
	if advice := audit.Advice(reason); advice != "" {
		detail = detail + "; " + advice
	}
	switch st {
	case jms.Status_warn:
		return "warn", detail
	case jms.Status_fail:
		return "fail", detail
	default:
		return "ok", detail
	}
}
