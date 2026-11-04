package koko

// The live tests.
//
// These talk to a real bastion, so they are gated: without the environment
// they skip, and a fresh clone runs green.
//
// What they are here for. The connection pool and the channel transfer are
// the two pieces of this package whose behaviour cannot be checked by
// reading the code: whether a pooled connection is really reused, whether
// the bytes on the channel survive a round trip intact, whether a command's
// exit code comes back untouched. All three were measured by hand, and a
// measurement that is not written down as an assertion gets re-measured
// wrongly later.
//
// The gate is JUMPSERVER_TOKEN plus JMS_TEST_ASSET:
//
//	JUMPSERVER_TOKEN=... JMS_TEST_ASSET=192.0.2.71 go test ./internal/repository/koko/ -run Live

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/yi-nology/jumpserver-mcp-kit/pkg/jmsapi"
)

// liveConfig builds a configuration pointed at the real bastion.
func liveConfig(t *testing.T) Config {
	t.Helper()
	token := os.Getenv("JUMPSERVER_TOKEN")
	if token == "" {
		t.Skip("JUMPSERVER_TOKEN not set")
	}
	base := os.Getenv("JUMPSERVER_URL")
	if base == "" {
		base = "http://192.0.2.41"
	}
	cfg := Config{
		Host:    os.Getenv("JMS_BASTION_HOST"),
		Port:    2222,
		User:    "ai",
		Timeout: 90 * time.Second,
		Backend: BackendToken,
		API:     jmsapi.New(jmsapi.Options{BaseURL: base, Token: token}),
	}
	if cfg.Host == "" {
		cfg.Host = "192.0.2.41"
	}
	if secret, err := jmsapi.LoadDelegationSecret(); err == nil {
		cfg.Delegation = &jmsapi.DelegationConfig{Secret: secret}
	}
	cfg.Assets = cfg.API.NewAssetResolver(5 * time.Minute)
	cfg.Pool = NewPool(cfg, PoolTTL)
	return cfg
}

// liveAsset is the machine the live tests use.
func liveAsset(t *testing.T) string {
	t.Helper()
	asset := os.Getenv("JMS_TEST_ASSET")
	if asset == "" {
		t.Skip("JMS_TEST_ASSET not set")
	}
	return asset
}

// TestLiveThePoolReusesTheConnection is the guard on the pool.
//
// The second session on the same asset must not pay a handshake. The number
// that proves it is the second session's walk: a fresh connection costs
// several hundred milliseconds, a reused one costs almost nothing.
func TestLiveThePoolReusesTheConnection(t *testing.T) {
	cfg := liveConfig(t)
	asset := liveAsset(t)
	ctx := context.Background()

	first, err := Open(ctx, cfg, asset, "", 120*time.Second)
	if err != nil {
		t.Fatalf("open the first session: %v", err)
	}
	if _, _, _, err := first.Exec("id -un", 30*time.Second); err != nil {
		t.Fatalf("run a command: %v", err)
	}
	first.Close()

	second, err := Open(ctx, cfg, asset, "", 120*time.Second)
	if err != nil {
		t.Fatalf("open the second session: %v", err)
	}
	defer second.Close()
	if _, _, _, err := second.Exec("id -un", 30*time.Second); err != nil {
		t.Fatalf("run a command on the second session: %v", err)
	}

	walk := second.WalkDuration()
	// A fresh connection pays the token mint and the handshake; measured at
	// 239ms + 205ms. A reused one is a map lookup. Half a second separates
	// the two beyond any doubt.
	if walk > 500*time.Millisecond {
		t.Errorf("the second session took %v to open; a pooled connection should be nearly free", walk)
	}
	if got := cfg.Pool.Size(); got != 1 {
		t.Errorf("the pool holds %d connections, want 1", got)
	}
}

// TestLiveTheExitCodeComesBackUntouched is the guard on the exit code.
//
// The token path returns the command's own status. The TUI path has to
// reconstruct it from a marker, and that reconstruction is where -1 used to
// come from.
func TestLiveTheExitCodeComesBackUntouched(t *testing.T) {
	cfg := liveConfig(t)
	asset := liveAsset(t)

	b, err := Open(context.Background(), cfg, asset, "", 120*time.Second)
	if err != nil {
		t.Fatalf("open a session: %v", err)
	}
	defer b.Close()

	out, code, truncated, err := b.Exec("sh -c 'exit 42'", 30*time.Second)
	if err != nil {
		t.Fatalf("run the command: %v", err)
	}
	if code != 42 {
		t.Errorf("exit code = %d, want 42", code)
	}
	if truncated {
		t.Error("a command with no output was reported as truncated")
	}
	if out != "" {
		t.Errorf("output = %q, want empty", out)
	}
}

// TestLiveTheChannelCarriesBytesIntact is the guard on the transfer.
//
// The bytes go on the exec channel's stdin and come back on its stdout.
// A file that does not survive that round trip byte for byte is a file
// that cannot be trusted.
func TestLiveTheChannelCarriesBytesIntact(t *testing.T) {
	cfg := liveConfig(t)
	asset := liveAsset(t)
	ctx := context.Background()

	b, err := Open(ctx, cfg, asset, "", 120*time.Second)
	if err != nil {
		t.Fatalf("open a session: %v", err)
	}
	defer b.Close()

	// A megabyte of bytes that are not all the same, so a truncation or a
	// reordering is visible.
	payload := make([]byte, 1<<20)
	for i := range payload {
		payload[i] = byte(i*7 + i/251)
	}
	remote := "/tmp/.jms-live-channel-test.bin"

	if err := b.PutBytes(remote, payload, 120*time.Second); err != nil {
		t.Fatalf("write the file: %v", err)
	}
	got, err := b.PullBytes(remote, 0, int64(len(payload)), 120*time.Second)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("the bytes came back different: %d bytes in, %d out", len(payload), len(got))
	}
	sum, _, _, err := b.Exec("sha256sum "+remote+" | cut -c1-16", 30*time.Second)
	if err != nil {
		t.Fatalf("checksum the file on the host: %v", err)
	}
	local := Sha256Prefix(payload)
	if sum == "" || sum[:16] != local {
		t.Errorf("the host's checksum is %q, want %q", sum, local)
	}
}

// TestLiveTheRelayMovesBytesBetweenTwoMachines is the guard on the relay.
//
// The bytes go from the source machine's channel straight onto the
// destination's, and they never land here. The check is that the file
// arrives with the same content — compared by digest on BOTH machines,
// because a relay that reports success with a corrupted file is worse than
// one that fails.
func TestLiveTheRelayMovesBytesBetweenTwoMachines(t *testing.T) {
	cfg := liveConfig(t)
	asset := liveAsset(t)
	other := os.Getenv("JMS_TEST_ASSET_2")
	if other == "" {
		t.Skip("JMS_TEST_ASSET_2 not set")
	}
	ctx := context.Background()

	// A payload that is not all the same byte, so a truncation or a
	// reordering shows up in the digest.
	payload := make([]byte, 256*1024)
	for i := range payload {
		payload[i] = byte(i*31 + i/97)
	}

	src, err := Open(ctx, cfg, asset, "", 120*time.Second)
	if err != nil {
		t.Fatalf("open the source session: %v", err)
	}
	defer src.Close()
	source := "/tmp/.jms-live-relay-src.bin"
	if err := src.PutBytes(source, payload, 120*time.Second); err != nil {
		t.Fatalf("write the source file: %v", err)
	}

	res, err := Relay(ctx, cfg, asset, source, other, "/tmp/.jms-live-relay-dst.bin", 120*time.Second)
	if err != nil {
		t.Fatalf("relay: %v", err)
	}
	if res.Bytes != int64(len(payload)) {
		t.Errorf("relayed %d bytes, want %d", res.Bytes, len(payload))
	}
	if !res.ChecksumMatch {
		t.Errorf("the checksums differ: source %q, destination %q",
			res.SourceSHA256, res.DestSHA256)
	}

	// Read the destination back and compare it with the payload, byte for
	// byte: the digests are computed on the two machines, and a digest can
	// agree while the content differs if the read was of a different file.
	dst, err := Open(ctx, cfg, other, "", 120*time.Second)
	if err != nil {
		t.Fatalf("open the destination session: %v", err)
	}
	defer dst.Close()
	got, err := dst.PullBytes("/tmp/.jms-live-relay-dst.bin", 0, int64(len(payload)), 120*time.Second)
	if err != nil {
		t.Fatalf("read the relayed file back: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("the relayed file came back different: %d bytes in, %d out", len(payload), len(got))
	}
}
