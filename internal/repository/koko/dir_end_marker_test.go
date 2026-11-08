package koko

// dir_end_marker_test.go 钉住 get_dir 的流尾标记纪律（GitHub #16）。
//
// tar 流自带终止块，但「恰好以完整归档结尾的截断」在 extractor 眼里与
// 真结束无法区分——exit code 是唯一证据，而复合命令的退出码属于最后的
// echo，不再是 tar 的。所以远端命令追加 `echo __JMSX_DONE_$rc`：
// 标记在 = 流真跑到头，退出码随标记；标记缺 = 截断（或旧远端，降级
// + warning）。这里用桩 Backend 把五种形状各钉一遍。

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubDirBackend is a Backend whose RunOutput pours a canned stream into
// the caller's writer — every shape a get_dir stream can arrive in.
type stubDirBackend struct {
	stream bytes.Buffer
	code   int
}

func (b *stubDirBackend) Exec(string, time.Duration) (string, int, bool, error) { return "", 0, false, nil }
func (b *stubDirBackend) PullCommandOutput(string, time.Duration) (string, error) {
	return "", nil
}
func (b *stubDirBackend) UploadChunkBytes() int                        { return 0 }
func (b *stubDirBackend) PutBytes(string, []byte, time.Duration) error { return nil }
func (b *stubDirBackend) CanCarryBytes() bool                          { return true }
func (b *stubDirBackend) AppendBytes(string, []byte, time.Duration) error {
	return nil
}
func (b *stubDirBackend) PullBytes(string, int64, int64, time.Duration) ([]byte, error) {
	return nil, nil
}
func (b *stubDirBackend) RunInput(string, io.Reader, time.Duration) (int, error) { return 0, nil }
func (b *stubDirBackend) RunOutput(_ string, stdout io.Writer, _ time.Duration) (int, error) {
	_, err := stdout.Write(b.stream.Bytes())
	if err != nil {
		return b.code, err
	}
	return b.code, nil
}
func (b *stubDirBackend) WalkDuration() time.Duration { return 0 }
func (b *stubDirBackend) Describe() string            { return "stub" }
func (b *stubDirBackend) Name() string                { return "stub" }
func (b *stubDirBackend) Account() string             { return "stub" }
func (b *stubDirBackend) Reused() bool                { return false }
func (b *stubDirBackend) FallbackReason() string      { return "" }
func (b *stubDirBackend) Identity() string            { return "stub" }
func (b *stubDirBackend) Close()                      {}

func stubDirSession(t *testing.T, b *stubDirBackend) *Session {
	t.Helper()
	return &Session{backend: b, asset: "stub", opened: time.Now()}
}

// markerStream 把流尾标记接在归档后面（模拟远端复合命令的真实输出）。
func markerStream(archive []byte, rc int) []byte {
	return append(append([]byte{}, archive...), []byte(dirEndMarkerPrefix+itoa(rc)+"\n")...)
}

// itoa 避免测试里为 strconv 起别名；标记只会带非负退出码。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestGetDirTrustsTheStreamOnlyPastTheEndMarker(t *testing.T) {
	archive := buildTarStream(t)
	local := t.TempDir()

	s := stubDirSession(t, &stubDirBackend{code: 0})
	s.backend.(*stubDirBackend).stream.Write(markerStream(archive, 0))

	res, err := s.GetDir("/srv/tree", local, time.Minute)
	if err != nil {
		t.Fatalf("GetDir: %v", err)
	}
	// 真结束：无 warning，文件落地。
	if res.Note != "" {
		t.Errorf("note = %q, want empty (a marked stream needs no warning)", res.Note)
	}
	// files 计入软链（readDirTar 的口径）：2 个常规文件 + link。
	if res.Files != 3 || res.Bytes != 1024+300 {
		t.Errorf("files = %d bytes = %d, want 3 and %d", res.Files, res.Bytes, 1024+300)
	}
	for _, rel := range []string{"big.bin", "sub", "small.cfg"} {
		if _, err := os.Stat(filepath.Join(local, rel)); err != nil {
			t.Errorf("%s did not land: %v", rel, err)
		}
	}
}

// 标记带了非零退出码 = tar 自己失败了，树可能不完整——显式报错，
// 哪怕归档本身解得干干净净。
func TestGetDirReportsTheExitCodeTheMarkerCarries(t *testing.T) {
	archive := buildTarStream(t)
	s := stubDirSession(t, &stubDirBackend{code: 2}) // exit $rc 也把它带回通道码
	s.backend.(*stubDirBackend).stream.Write(markerStream(archive, 2))

	_, err := s.GetDir("/srv/tree", t.TempDir(), time.Minute)
	if err == nil {
		t.Fatal("GetDir succeeded, want an error — tar exited 2 and the tree may be incomplete")
	}
	if !strings.Contains(err.Error(), "exited 2") || !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("err = %v, want it to name the marker's exit code and the incompleteness", err)
	}
}

// 旧远端：没有标记。降级为原判据（通道退出码），note 打 warning。
func TestGetDirDegradesToTheExitCodeWhenTheMarkerIsMissing(t *testing.T) {
	archive := buildTarStream(t)
	local := t.TempDir()

	// 退出码 0：成功，但边界要写进 note。
	s := stubDirSession(t, &stubDirBackend{code: 0})
	s.backend.(*stubDirBackend).stream.Write(archive) // 纯归档，无标记
	res, err := s.GetDir("/srv/tree", local, time.Minute)
	if err != nil {
		t.Fatalf("GetDir (legacy, exit 0): %v", err)
	}
	if !strings.Contains(res.Note, "no end-of-archive marker") ||
		!strings.Contains(res.Note, "undetectable") {
		t.Errorf("note = %q, want the honest truncation boundary", res.Note)
	}

	// 退出码非零：旧判据仍然报错。
	s2 := stubDirSession(t, &stubDirBackend{code: 3})
	s2.backend.(*stubDirBackend).stream.Write(archive)
	if _, err := s2.GetDir("/srv/tree", t.TempDir(), time.Minute); err == nil {
		t.Error("GetDir (legacy, exit 3) succeeded, want the old exit-code error")
	}
}

// 归档中段被截断：readDirTar 解不开完整条目，显式报错——这正是
// 之前「部分树 + 无 warning」缺口的主场景。
func TestGetDirFailsLoudlyOnATruncatedArchive(t *testing.T) {
	archive := buildTarStream(t)
	cut := archive[:512+100] // 第一个文件的内容只到一半

	s := stubDirSession(t, &stubDirBackend{code: 0})
	s.backend.(*stubDirBackend).stream.Write(cut)

	if _, err := s.GetDir("/srv/tree", t.TempDir(), time.Minute); err == nil {
		t.Fatal("GetDir succeeded on a truncated archive, want an explicit error")
	}
}

// 标记畸形（前缀在、退出码没了）：流被这样改过道就不能悄悄放行。
func TestGetDirRefusesAMalformedEndMarker(t *testing.T) {
	archive := buildTarStream(t)
	stream := append(append([]byte{}, archive...), []byte(dirEndMarkerPrefix+"??\n")...)

	s := stubDirSession(t, &stubDirBackend{code: 0})
	s.backend.(*stubDirBackend).stream.Write(stream)

	_, err := s.GetDir("/srv/tree", t.TempDir(), time.Minute)
	if err == nil || !strings.Contains(err.Error(), "no exit code") {
		t.Errorf("err = %v, want a malformed-marker error", err)
	}
}

func TestParseStreamEndMarker(t *testing.T) {
	if code, seen, err := parseStreamEndMarker("\n__JMSX_DONE_0\r\n"); code != 0 || !seen || err != nil {
		t.Errorf("code=%d seen=%v err=%v, want 0/true/nil", code, seen, err)
	}
	if code, seen, err := parseStreamEndMarker("__JMSX_DONE_127\n"); code != 127 || !seen || err != nil {
		t.Errorf("code=%d seen=%v err=%v, want 127/true/nil", code, seen, err)
	}
	if _, seen, err := parseStreamEndMarker(""); seen || err != nil {
		t.Errorf("seen=%v err=%v, want false/nil on an empty tail", seen, err)
	}
	// tar 归档本体里出现标记字样的字节不会被误认——tail 只收终止块之后。
	if _, seen, err := parseStreamEndMarker(strings.Repeat("\x00", 1024)); seen || err != nil {
		t.Errorf("seen=%v err=%v, want false/nil on zero blocks", seen, err)
	}
}

// 归档里藏着一层名为标记样字样的文件也骗不动 extractor：它被当数据
// 消费，tail 里才作数。
func TestGetDirIsNotFooledByAMarkerShapedFile(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := dirEndMarkerPrefix + "0\n" // 文件内容长得像流尾标记
	if err := tw.WriteHeader(&tar.Header{Name: "evil.txt", Typeflag: tar.TypeReg, Size: int64(len(body)), Mode: 0o644}); err != nil {
		t.Fatalf("header: %v", err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatalf("body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 只给这个文件，不给真标记：标记缺失 → 降级路径 → note 带 warning，
	// 而不是把文件内容误读成「真结束、无 warning」。
	s := stubDirSession(t, &stubDirBackend{code: 0})
	s.backend.(*stubDirBackend).stream.Write(buf.Bytes())

	res, err := s.GetDir("/srv/tree", t.TempDir(), time.Minute)
	if err != nil {
		t.Fatalf("GetDir: %v", err)
	}
	if !strings.Contains(res.Note, "no end-of-archive marker") {
		t.Errorf("note = %q, want the missing-marker warning (an in-archive lookalike is not a marker)", res.Note)
	}
}
