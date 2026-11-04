// Package conf holds the execution plane's configuration.
//
// The tag machinery comes from oekit/core/config — the same machinery the
// management plane uses, so the two planes read the environment the same way:
// the `env` tag says where a value comes from, the `default` tag says what it
// falls back to, and the passes run env → defaults → validation.
//
// It used to be four hand-written helpers (envOr / envInt / envFloat /
// envDuration) and a bag of os.Getenv calls. That is the thing this repo
// warns about: a second way of doing the same thing is a second thing to keep
// in step, and it was already out of step — the management plane could say
// "which value came from where" and the execution plane could not.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/oekit/core/config"

	"github.com/yi-nology/jumpserver-exec/internal/repository/koko"
	"github.com/yi-nology/jumpserver-kit/pkg/audit"
	"github.com/yi-nology/jumpserver-kit/pkg/fspath"
	"github.com/yi-nology/jumpserver-kit/pkg/jmsapi"
)

// Config is the effective configuration of the execution plane.
type Config struct {
	// Koko is the session layer's runtime configuration: where the bastion
	// is, who to log in as, and which backend opens a session.
	Koko koko.Config

	// API is the bastion's REST endpoint.
	API APIConfig

	// ReadOnly refuses every tool that changes the machine. It is the switch
	// to turn when an agent should look at the fleet but not touch it.
	ReadOnly bool

	// Audit is where the gate's trail goes.
	Audit audit.Settings

	// HTTPAddr is where the HTTP surface listens: the REST face
	// (/api/v1/…) plus MCP over streamable HTTP (/mcp). Empty means no
	// HTTP surface — the server speaks stdio only, which is the mode an
	// MCP runtime launches it in.
	//
	// ⚠️ The default is empty ON PURPOSE: turning a stdio server into a
	// network listener is an operator's decision, not a default. And
	// 127.0.0.1, not 0.0.0.0 — these routes run commands on your
	// machines with the deployment's own credential; exposing that to a
	// network needs a proxy and a TLS story this process does not have.
	HTTPAddr string
	// HTTPToken is the bearer token the HTTP surface demands on every
	// route except /api/v1/health.
	//
	// ⚠️ There is no "auth off": JMS_HTTP_ADDR without JMS_HTTP_TOKEN is
	// refused at start-up. A surface that answers tool calls to whoever
	// can reach the port is not a surface this server will serve.
	HTTPToken string
	// RPCAddr is where the kitex face listens (the RPC face: other
	// services calling tools synchronously over thrift). Empty means no
	// RPC face.
	RPCAddr string
	// RPCToken is what every RPC call must present — no auth-off, same
	// discipline as the HTTP face.
	RPCToken string
}

// APIConfig is the REST endpoint of the bastion.
type APIConfig struct {
	// BaseURL is the root of the deployment, without a trailing slash and
	// without /api/v1 (the client appends that itself).
	BaseURL string

	// Token is the JumpServer PrivateToken.
	//
	// ⚠️ It is NOT a v5-only credential: v4 registers
	// PrivateTokenAuthentication too (DRF's TokenAuthentication with
	// model=PrivateToken), so the same header means the same thing on
	// both versions.
	Token string

	// Username and Password are the alternative credential: a LOGIN that
	// the bastion exchanges for a Bearer token
	// (POST /api/v1/authentication/auth/). It is what a deployment that
	// cannot mint a PrivateToken has, and it exists on v4 and on v5 alike.
	Username string
	Password string

	// OTP is the base32 TOTP secret, used when the deployment demands a
	// second factor.
	OTP string

	// Version says which JumpServer generation the deployment runs: "v4",
	// "v5", or "auto" to detect it at the first call that needs the answer.
	//
	// ⚠️ It decides ONE thing here: whether the Chat AI delegation scheme
	// exists. On v4 it does not, so the connection tokens are minted with
	// the API credential and the sessions run with that credential's
	// authority. Everything else is the same on both versions.
	Version string

	// Timeout bounds a single API call.
	Timeout time.Duration

	// RequestsPerSecond and Burst cap how fast the client calls the
	// bastion. The same values the management plane uses, so the two servers
	// behave the same way against the same monolith.
	RequestsPerSecond float64
	Burst             int
}

// settings is the environment-driven part of the configuration.
//
// Every field says where its value comes from and what it falls back to, so
// the effective configuration can be printed by --doctor instead of guessed.
type settings struct {
	// The koko side: where the bastion is and who to log in as.
	//
	// ⚠️ BastionHost and KokoUser have NO defaults: a default would point
	// one deployment's clients at somebody else's bastion, and the failure
	// would look like "the bastion is down" rather than "this was never
	// configured". The key keeps its conventional default (~/.ssh/id_rsa),
	// because a path is not somebody's private information.
	BastionHost string `yaml:"bastion_host" env:"JMS_BASTION_HOST"`
	KokoPort    int    `yaml:"koko_port" env:"JMS_KOKO_PORT" default:"2222"`
	KokoUser    string `yaml:"koko_user" env:"JMS_KOKO_USER"`
	KokoKey     string `yaml:"koko_key" env:"JMS_KOKO_KEY" default:"~/.ssh/id_rsa"`

	// Backend selects how a session is opened: the connection token, the
	// koko terminal, or "auto" (the token first, the terminal as a
	// fallback). The options tag makes an unknown value an error rather
	// than a silently wrong path.
	Backend string `yaml:"backend" env:"JMS_BACKEND" default:"auto" options:"auto,token,tui"`

	// PoolTTL is how long an idle connection stays in the pool. It is also
	// how long a revoked right stays effective, because a pooled
	// connection carries the authority it was minted with.
	PoolTTL time.Duration `yaml:"pool_ttl" env:"JMS_POOL_TTL" default:"2m"`

	// ReadOnly refuses every tool that changes the machine.
	ReadOnly bool `yaml:"read_only" env:"JMS_READ_ONLY" default:"false"`

	// AuditMode says whether the gate's trail is written to a FILE: "file"
	// or "off" (the log only). The file is the half that answers "who tried
	// to run rm -rf on that box and was stopped" — such a call never
	// reaches the bastion, so the bastion has no record of it.
	AuditMode string `yaml:"audit_mode" env:"JMS_AUDIT" default:"file" options:"file,off"`

	// AuditDir is where the trail files are written.
	AuditDir string `yaml:"audit_dir" env:"JMS_AUDIT_DIR" default:"~/.local/state/jumpserver-mgmt/audit"`
	// AuditRetentionDays is how many days of trail files are kept.
	//
	// ⚠️ The trail is one file per PROCESS, so the directory grows with
	// the number of restarts — and every query that scans it pays for the
	// whole history. Zero keeps them all.
	AuditRetentionDays int `yaml:"audit_retention_days" env:"JMS_AUDIT_RETENTION_DAYS" default:"7"`

	// BackendMemoryTTL is how long a remembered backend is trusted.
	//
	// The memory only accelerates the cold open: it says which backend
	// worked last time, so the other one is not paid for again. It expires
	// on purpose — a backend that started working again has to be found
	// again, and the only way to find it is to try it.
	BackendMemoryTTL time.Duration `yaml:"backend_memory_ttl" env:"JMS_BACKEND_MEMORY_TTL" default:"10m"`

	// The bastion's REST endpoint.
	//
	// ⚠️ It has NO default, and it is NOT required here: the execution
	// plane can open sessions through the koko terminal alone. What it
	// does lose is the connection-token backend and jmsx_inventory, and
	// the doctor says so.
	BaseURL string        `yaml:"base_url" env:"JUMPSERVER_URL"`
	Token   string        `yaml:"token" env:"JUMPSERVER_TOKEN"`
	Timeout time.Duration `yaml:"timeout" env:"JUMPSERVER_TIMEOUT" default:"90s"`

	// The alternative credential: a login the bastion exchanges for a
	// Bearer token. It is used only when the PrivateToken is absent, and
	// it exists on v4 and on v5 alike.
	Username string `yaml:"username" env:"JUMPSERVER_USERNAME"`
	Password string `yaml:"password" env:"JUMPSERVER_PASSWORD"`

	// The base32 TOTP secret, used when the deployment demands a second
	// factor. Optional: a deployment without MFA never asks for one.
	OTP string `yaml:"otp" env:"JUMPSERVER_OTP"`

	// Version says which JumpServer generation the deployment runs.
	// "auto" detects it at the first call that needs the answer.
	Version string `yaml:"version" env:"JUMPSERVER_VERSION" default:"auto" options:"v4,v5,auto"`

	// RequestsPerSecond caps how fast the client calls the bastion.
	RequestsPerSecond int `yaml:"requests_per_second" env:"JMS_REQUESTS_PER_SECOND" default:"20"`
	Burst             int `yaml:"burst" env:"JMS_BURST" default:"40"`

	// The HTTP surface: where it listens and the bearer token it demands.
	// Both empty means no HTTP surface (stdio only). ⚠️ One without the
	// other is refused at start-up: JMS_HTTP_ADDR with no JMS_HTTP_TOKEN
	// would answer tool calls to whoever can reach the port.
	HTTPAddr  string `yaml:"http_addr" env:"JMS_HTTP_ADDR"`
	HTTPToken string `yaml:"http_token" env:"JMS_HTTP_TOKEN"`

	// The kitex RPC face: where it listens and the per-call token it
	// demands. Both empty means no RPC face; one without the other is
	// refused at start-up (same discipline as the HTTP face).
	RPCAddr  string `yaml:"rpc_addr" env:"JMS_RPC_ADDR"`
	RPCToken string `yaml:"rpc_token" env:"JMS_RPC_TOKEN"`

	// The Chat AI delegation credential's identity. The secret itself is
	// NOT an env-driven field here: it is read from JMS_DELEGATION_SECRET
	// or from the key file, because it is a secret and not a setting.
	DelegationUserID string `yaml:"delegation_user_id" env:"JMS_DELEGATION_USER_ID"`
	DelegationOrgID  string `yaml:"delegation_org_id" env:"JMS_DELEGATION_ORG_ID"`
}

// Load reads the configuration from the environment.
//
// A configuration that cannot be read is an error, not a set of defaults:
// running with the wrong settings (an unknown backend, a port that is not a
// number) is worse than not running.
func Load() (Config, error) {
	s := &settings{}
	if err := config.ApplyEnv(s); err != nil {
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}
	if err := config.FillDefaults(s); err != nil {
		return Config{}, fmt.Errorf("apply configuration defaults: %w", err)
	}
	if err := config.ValidateTags(s); err != nil {
		return Config{}, fmt.Errorf("validate configuration: %w", err)
	}

	cfg := Config{
		ReadOnly: s.ReadOnly,
		Audit:    audit.Settings{Mode: s.AuditMode, Dir: s.AuditDir, RetentionDays: s.AuditRetentionDays},
		API: APIConfig{
			BaseURL:           strings.TrimRight(strings.TrimSpace(s.BaseURL), "/"),
			Token:             strings.TrimSpace(s.Token),
			Username:          strings.TrimSpace(s.Username),
			Password:          strings.TrimSpace(s.Password),
			OTP:               strings.TrimSpace(s.OTP),
			Version:           strings.ToLower(strings.TrimSpace(s.Version)),
			Timeout:           s.Timeout,
			RequestsPerSecond: float64(s.RequestsPerSecond),
			Burst:             s.Burst,
		},
		HTTPAddr:  strings.TrimSpace(s.HTTPAddr),
		HTTPToken: strings.TrimSpace(s.HTTPToken),
		RPCAddr:   strings.TrimSpace(s.RPCAddr),
		RPCToken:  strings.TrimSpace(s.RPCToken),
	}
	// ⚠️ No "auth off": the HTTP surface answers tool calls on the
	// operator's machines, so it needs a bearer token.
	if cfg.HTTPAddr != "" && cfg.HTTPToken == "" {
		return Config{}, fmt.Errorf("JMS_HTTP_ADDR is set but JMS_HTTP_TOKEN is not: the HTTP surface answers " +
			"tool calls, so it needs a bearer token. Generate one (openssl rand -hex 32) and set " +
			"JMS_HTTP_TOKEN, or unset JMS_HTTP_ADDR to stay on stdio only")
	}
	// ⚠️ The password is read from a file when it is not in the
	// environment, and it is read ONLY when there is no PrivateToken: the
	// token wins, because it does not expire and a long-running process
	// should not have to keep a login alive.
	if cfg.API.Token == "" && cfg.API.Username != "" {
		pw, err := jmsapi.LoadPassword()
		if err != nil {
			return Config{}, fmt.Errorf("JUMPSERVER_USERNAME is set but the password is missing: %w", err)
		}
		cfg.API.Password = pw
	}
	if cfg.API.OTP == "" {
		if otp, err := jmsapi.LoadOTPSecret(); err == nil {
			cfg.API.OTP = otp
		}
	}
	cfg.Koko = koko.Config{
		Host:    strings.TrimSpace(s.BastionHost),
		Port:    s.KokoPort,
		User:    strings.TrimSpace(s.KokoUser),
		KeyPath: fspath.ExpandHome(s.KokoKey),
		Backend: s.Backend,
	}
	// ⚠️ The koko terminal backend is the one that needs the bastion's
	// address and the login name; the connection-token backend talks to
	// the REST API instead. So a missing pair is fatal for the former and
	// merely a lost fast path for the latter — and "auto" is allowed to
	// start without them, because the token path may well be the one that
	// works. The doctor says which case it is.
	if cfg.Koko.Backend == koko.BackendTUI && (cfg.Koko.Host == "" || cfg.Koko.User == "") {
		missing := make([]string, 0, 2)
		if cfg.Koko.Host == "" {
			missing = append(missing, "JMS_BASTION_HOST (the host running koko, usually the same host as JUMPSERVER_URL)")
		}
		if cfg.Koko.User == "" {
			missing = append(missing, "JMS_KOKO_USER (the bastion account whose SSH public key is registered)")
		}
		return Config{}, fmt.Errorf("the koko terminal backend needs %s", strings.Join(missing, " and "))
	}
	if api := cfg.APIClient(); api != nil {
		cfg.Koko.API = api
		cfg.Koko.Assets = api.NewAssetResolver(5 * time.Minute)
	}
	// The pool is built from the configuration, so it is built AFTER the
	// settings are in place.
	//
	// ⚠️ It is built AFTER the delegation and the memory are attached
	// too, and that order matters: the pool keeps a COPY of the
	// configuration, so anything attached after the copy is made is
	// invisible to it — and a pool without the delegation mints its
	// tokens with the API token, which is the ADMINISTRATOR's authority
	// rather than the delegated user's.
	cfg.Koko.Delegation = delegationOf(cfg, s)
	cfg.Koko.Memory = koko.NewBackendMemory(s.BackendMemoryTTL)
	cfg.Koko.Pool = koko.NewPool(cfg.Koko, s.PoolTTL)

	if cfg.RPCAddr != "" && cfg.RPCToken == "" {
		return Config{}, fmt.Errorf("JMS_RPC_ADDR is set but JMS_RPC_TOKEN is not: the RPC face answers " +
			"tool calls to whoever can reach the port, and there is no auth-off mode")
	}
	return cfg, nil
}

// delegationOf attaches the delegation credential, when there is one.
//
// It is a separate function because the ORDER matters: the pool copies the
// configuration, and a copy made before the delegation is attached is a
// copy without it — and a pool without the delegation mints its tokens with
// the API token, which carries the administrator's authority.
func delegationOf(cfg Config, s *settings) *jmsapi.DelegationConfig {
	secret, err := jmsapi.LoadDelegationSecret()
	if err != nil {
		return nil
	}
	return jmsapi.NewDelegationConfig(secret, s.DelegationUserID, s.DelegationOrgID)
}

// APIClient builds the REST client from the configuration.
//
// A nil client means there is NO CREDENTIAL. That is not an error here: the
// koko TUI path — the fallback — works without it, and the connection-token
// path reports the missing client itself. The caller decides what a missing
// client means for it.
//
// ⚠️ "No credential" means neither a PrivateToken nor a login: the token
// path and the inventory both work with either, and a client built without
// one would answer every call with 401 and blame the bastion.
func (c Config) APIClient() *jmsapi.Client {
	if c.API.Token == "" && c.API.Username == "" {
		return nil
	}
	return jmsapi.New(jmsapi.Options{
		BaseURL: c.API.BaseURL,
		Token:   c.API.Token,
		Credential: jmsapi.Credential{
			Username: c.API.Username,
			Password: c.API.Password,
			OTP:      c.API.OTP,
		},
		Version:           c.API.Version,
		Timeout:           c.API.Timeout,
		RequestsPerSecond: c.API.RequestsPerSecond,
		Burst:             c.API.Burst,
	})
}
