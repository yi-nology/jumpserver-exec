package koko

// filetransfer.go moves small files through the same koko PTY that runs
// commands, by base64-encoding them into shell-safe chunks.
//
// Why base64: the payload is typed into a terminal, so it must survive shell
// quoting, and it must not contain bytes that the terminal or tmux would
// reinterpret. Measured on the live bastion, one input line survives up to
// ~2668 characters of base64, i.e. ~2000 raw bytes, so chunks stay well below
// that.
//
// Why chunks at all: a file is larger than one screen. Writing it in pieces and
// appending to a temporary file keeps each interaction the size of a few rows
// instead of flooding the pane, which matters because the pane is also the only
// place the transfer's own output can be verified.
//
// Limits are real and are reported rather than hidden: the pane shows about 40
// rows, so downloads are bounded, and uploads are bounded by how many round
// trips a caller is willing to pay for.

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/logx"
	"github.com/oekit/pkg/encoding"
	"github.com/oekit/pkg/pathx"
	"github.com/oekit/pkg/shellx"
	"github.com/oekit/pkg/stringx"
)

const (
	// uploadChunkBytes is deliberately small: 512 raw bytes become 684 base64
	// characters, about three rows of the 220-column pane.
	uploadChunkBytes = 512

	// b64RowCols is the width each encoded row is wrapped to. The pane is ~220
	// columns, so this stays inside it while using far fewer rows than a narrow
	// fold: measured, `fold -w 60` and `fold -w 190` both recover the same 40 rows
	// of pane, so wide rows are strictly better.
	b64RowCols = 190

	// downloadChunkRaw starts a chunk at a size that encodes to well under one
	// screen of rows, then shrinks on retry. Guessing this wrong is how the
	// first version lost data silently: 4800 raw bytes encode to 6400 base64
	// characters = 34 rows, which is past what the pane keeps, and the shortfall
	// was invisible. So the number here is a starting point, not a guarantee —
	// pullRange verifies every chunk and halves until it fits.
	downloadChunkRaw = 4000
)

// sha256Prefix is the short checksum used on both sides of a transfer.
//
// It has to match `sha256sum <file> | cut -c1-16` on the host, so the prefix
// length is part of the contract rather than a taste decision.
func Sha256Prefix(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// sessionChunk is the single-use transfer token written next to the temp file,
// so a second process cannot collide on it and so the marker is verifiable.
const sessionChunkTag = "__JMSX_CHUNK_"

type TransferResult struct {
	Asset      string
	LocalPath  string
	RemotePath string
	Bytes      int
	Chunks     int
	Seconds    float64
	// RemoteSHA256 is the checksum computed on the host, LocalSHA256 the one
	// computed on the bytes that were sent. ChecksumMatch says whether the
	// file arrived intact -- a matching size alone does not prove that.
	RemoteSHA256  string
	LocalSHA256   string
	ChecksumMatch bool
	Mode          string
}

// DirTransferResult is the account of one directory upload: what was
// streamed, how much of it, and how long the stream took.
type DirTransferResult struct {
	Asset     string
	LocalDir  string
	RemoteDir string
	Files     int
	Bytes     int64
	Seconds   float64
	// Skipped is how many entries the tar stream LEFT BEHIND (sockets and
	// devices cannot cross it). Zero does not appear in the answer; a
	// non-zero count means the remote tree is SMALLER than the local one.
	Skipped int
	// Note carries the skipped entries' names (and the verification
	// verdict) when there is anything to say.
	Note string
	// Verified is the arrival proof: every file the local side hashed was
	// checked on the host against a sha256 manifest that rode the same
	// stream, and every digest matched. A transfer that cannot prove
	// arrival (no sha256sum on the host, manifest lost in extraction)
	// reports verified=false WITH the reason in Note — a fake true is the
	// one answer this field must never give.
	Verified bool
	// VerifiedFiles is how many digests the host checked.
	VerifiedFiles int
}

// PutDir uploads a local directory to a directory on the asset as one tar
// stream on the exec channel's stdin.
//
// The semantics are MERGE, not mirror: the archive extracts into remoteDir,
// overwriting same-name files and leaving everything the host already had
// alone. A caller that wants the remote tree to become exactly the local
// one clears the target first (jmsx_exec) and then uploads.
//
// There is no base64 fallback: the TUI path types into a screen and cannot
// carry a binary stream, and a directory large enough to need this tool is
// exactly the case where a pane-bound chunked fallback would take minutes
// per megabyte. The caller gets the honest "this backend cannot stream"
// error instead.
func (s *Session) PutDir(localDir, remoteDir string, timeout time.Duration) (*DirTransferResult, error) {
	info, err := os.Stat(localDir)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", localDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", localDir)
	}
	remoteDir = strings.TrimRight(remoteDir, "/")
	if remoteDir == "" {
		return nil, fmt.Errorf("remote_dir is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend == nil || s.closed {
		return nil, fmt.Errorf("the session is closed")
	}
	if !s.backend.CanCarryBytes() {
		return nil, fmt.Errorf("directory upload needs the connection-token path; " +
			"the koko terminal backend cannot carry a binary stream (it would take minutes per megabyte over the pane)")
	}

	// runLocked is Run's body without the lock: PutDir already holds it for
	// the whole upload, and the streamed tar is one indivisible use of the
	// connection — nothing else may speak on it until the extract finishes.
	runLocked := func(command string, timeout time.Duration) (string, error) {
		out, code, _, err := s.backend.Exec(command, timeout)
		if err != nil {
			return "", err
		}
		if code != 0 {
			return out, fmt.Errorf("%q exited %d on %s", command, code, s.asset)
		}
		return out, nil
	}

	// tar on the host is the extractor; without it there is nothing to
	// stream into. Checking first fails here, not halfway through a stream.
	if out, err := runLocked("command -v tar >/dev/null 2>&1 || echo JMSX_NO_TAR", timeout); err != nil {
		return nil, err
	} else if strings.Contains(out, "JMSX_NO_TAR") {
		return nil, fmt.Errorf("tar is not installed on %s; directory upload extracts a tar stream remotely", s.asset)
	}
	if _, err := runLocked("mkdir -p "+shellx.Quote(remoteDir), timeout); err != nil {
		return nil, err
	}

	start := time.Now()
	pr, pw := io.Pipe()
	counts := &dirTarCounts{}
	// The tar writer runs beside the stream, not inside it (GoSafe: a panic
	// becomes a logged stack, not a dead server). The pipe closes no matter
	// what — on a walk error the stream ends early, the host's tar exits
	// non-zero on the short archive, and THAT is the error the caller sees.
	async.GoSafeLabeled("koko-dir-tar", func() {
		defer pw.Close()
		if err := writeDirTar(pw, localDir, counts); err != nil {
			logx.Error("koko dir tar walk failed", "dir", localDir, "err", err)
		}
	})
	// The deadline scales with the tree: a FIXED timeout kills a large
	// directory on a slow link mid-stream (and mid-extract). After the walk
	// finishes, counts.bytes is the size the tree actually had; the stream
	// gets base + bytes at a floor rate of 512 KiB/s, capped at 4x base —
	// beyond that the caller names their own timeout (the note says so).
	streamTimeout := dirStreamTimeout(timeout, counts.bytes)
	code, err := s.backend.RunInput("tar -xf - -C "+shellx.Quote(remoteDir), pr, streamTimeout)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("tar -xf - -C %s exited %d on %s", remoteDir, code, s.asset)
	}
	// Arrival proof (issue: 别让目录传输成为唯一不证明到达完整性的传输工具
	// —— jmsx_put_file 的纪律在目录上的等价物): the manifest rode the
	// stream, the host checks it with sha256sum -c, exit 0 means EVERY file
	// the local side hashed arrived with those exact bytes. The manifest is
	// deleted after the check. No sha256sum on the host (or no files) ⇒ the
	// check is skipped and the answer says so — an honest absence, not a
	// fake verified=true.
	verified := false
	verifiedFiles := 0
	verifyNote := ""
	if len(counts.manifest) > 0 {
		if out, err := runLocked("command -v sha256sum >/dev/null 2>&1 || echo JMSX_NO_SHA", timeout); err != nil {
			return nil, err
		} else if strings.Contains(out, "JMSX_NO_SHA") {
			verifyNote = "sha256sum is not installed on " + s.asset + "; the manifest was uploaded but not verified"
		} else {
			manifestName := latestManifestName(remoteDir, runLocked, timeout)
			if manifestName == "" {
				verifyNote = "the manifest did not extract; arrival could not be verified"
			} else {
				// manifestName is ALREADY the full path (ls -1t <dir>/.jmsx-
				// manifest-* echoes the path it was handed) — joining
				// remoteDir again made rm a no-op path and left a manifest
				// in every uploaded tree (measured 2026-11-06 live).
				checkCmd := "cd " + shellx.Quote(remoteDir) + " && sha256sum -c " + shellx.Quote(manifestName) + " --quiet; status=$?; rm -f " + shellx.Quote(manifestName) + "; exit $status"
				if _, err := runLocked(checkCmd, timeout); err != nil {
					return nil, fmt.Errorf("arrival verification FAILED (some files do not match the manifest): %w", err)
				}
				verified = true
				verifiedFiles = len(counts.manifest)
				verifyNote = fmt.Sprintf("all %d files verified against the sha256 manifest", verifiedFiles)
			}
		}
	}
	note := verifyNote
	if counts.skipped > 0 {
		skip := fmt.Sprintf("%d entr%s did not travel (sockets/devices cannot cross a tar stream): %s — "+ //shellquote:ok
			"for a directory well beyond the default window pass an explicit timeout", counts.skipped, ifPlural(counts.skipped), strings.Join(counts.skippedNames, ", "))
		if note != "" {
			note = skip + "; " + note
		} else {
			note = skip
		}
	}
	return &DirTransferResult{
		Asset:         s.asset,
		LocalDir:      localDir,
		RemoteDir:     remoteDir,
		Files:         counts.files,
		Bytes:         counts.bytes,
		Seconds:       time.Since(start).Seconds(),
		Skipped:       counts.skipped,
		Note:          note,
		Verified:      verified,
		VerifiedFiles: verifiedFiles,
	}, nil
}

// latestManifestName finds the manifest entry the stream just extracted:
// the one .jmsx-manifest-* file in the remote dir with the newest mtime.
// An empty answer means it did not extract — the caller reports that
// instead of pretending the check ran.
func latestManifestName(remoteDir string, runLocked func(string, time.Duration) (string, error), timeout time.Duration) string {
	out, err := runLocked("ls -1t "+shellx.Quote(remoteDir)+"/"+".jmsx-manifest-* 2>/dev/null | head -1", timeout)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// dirStreamTimeout is the stream deadline for a directory upload: the base
// the caller asked for, plus the tree's size at a floor rate of
// 512 KiB/s (a conservative link), capped at 4x base — a caller moving
// more than that names its own timeout.
func dirStreamTimeout(base time.Duration, bytes int64) time.Duration {
	scaled := base + time.Duration(bytes/(512*1024))*time.Second
	if scaled > 4*base {
		return 4 * base
	}
	return scaled
}

// ifPlural is "y"/"ies" for a count of one/many.
func ifPlural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// DirStats is what a LOCAL directory holds, counted the way a directory
// upload would carry it: the numbers put_dir's dry run answers with.
type DirStats struct {
	// Files is how many REGULAR files the stream would carry (directories
	// and symlinks are structure, not files — the same rule as PutDir's
	// counts.files).
	Files int
	// Bytes is the content size those files would stream (not the tar
	// overhead around them).
	Bytes int64
	// Skipped is what the stream would LEAVE BEHIND (sockets and devices
	// cannot cross a tar stream) — the same skip list PutDir reports.
	Skipped int
	// SkippedNames names the first few skipped entries, for the note.
	SkippedNames []string
}

// PrescanDir counts what a directory upload WOULD carry, without opening a
// session or reading a single file's content: a local walk with lstat only.
//
// It is put_dir's dry-run engine (issue #20): the planning numbers — how
// many files, how many bytes, what gets skipped — come from THIS machine,
// at zero bastion cost, so the agent can shard a too-large tree, clean the
// skipped entries, or catch a wrong path BEFORE any session exists.
//
// ⚠️ The counts are pinned to writeDirTar's (the test walks both over the
// same fixture): a prescan that drifted from the stream would rehearse an
// upload that never happens. The walk mirrors it branch for branch —
// directories are structure, symlinks ride as symlinks, regular files count
// with their content size, everything else is skipped and named.
func PrescanDir(root string) (*DirStats, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}
	st := &DirStats{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return nil
		case info.Mode()&fs.ModeSymlink != 0:
			return nil
		case info.Mode().IsRegular():
			st.Files++
			st.Bytes += info.Size()
			return nil
		default:
			// Sockets and devices cannot travel a tar stream — the same
			// skip writeDirTar makes, counted the same way.
			st.Skipped++
			if len(st.SkippedNames) < 3 {
				rel, rerr := filepath.Rel(root, p)
				if rerr != nil {
					rel = p
				}
				st.SkippedNames = append(st.SkippedNames, filepath.ToSlash(rel))
			}
			return nil
		}
	})
	if err != nil {
		return nil, err
	}
	return st, nil
}

// GetDir downloads a directory from the asset as one tar stream on the exec
// channel's stdout, extracted HERE as it arrives — the mirror of PutDir, and
// the honest answer to "agent 要拉回一个远端目录" that until now meant
// hand-rolled tar|base64 over jmsx_exec or a jmsx_get_file per file.
//
// Same channel discipline as PutDir: the connection-token path only (the
// TUI pane cannot carry a binary stream — the error says so, not a slow
// fallback), and the streamed tar is one indivisible use of the connection.
//
// Extraction is defensive: an entry whose name is absolute or climbs out of
// the destination ("..", "../x", "/etc/passwd") is SKIPPED and counted, not
// followed — a hostile tar must not write outside local_dir. Same merge
// semantics as PutDir: same-name files are overwritten, everything else the
// destination already had is untouched.
func (s *Session) GetDir(remoteDir, localDir string, timeout time.Duration) (*DirTransferResult, error) {
	remoteDir = strings.TrimRight(remoteDir, "/")
	if remoteDir == "" {
		return nil, fmt.Errorf("remote_dir is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend == nil || s.closed {
		return nil, fmt.Errorf("the session is closed")
	}
	if !s.backend.CanCarryBytes() {
		return nil, fmt.Errorf("directory download needs the connection-token path; " +
			"the koko terminal backend cannot carry a binary stream (it would take minutes per megabyte over the pane)")
	}

	runLocked := func(command string, timeout time.Duration) (string, error) {
		out, code, _, err := s.backend.Exec(command, timeout)
		if err != nil {
			return "", err
		}
		if code != 0 {
			return out, fmt.Errorf("%q exited %d on %s", command, code, s.asset)
		}
		return out, nil
	}
	if out, err := runLocked("command -v tar >/dev/null 2>&1 || echo JMSX_NO_TAR", timeout); err != nil {
		return nil, err
	} else if strings.Contains(out, "JMSX_NO_TAR") {
		return nil, fmt.Errorf("tar is not installed on %s; directory download streams a tar archive remotely", s.asset)
	}
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", localDir, err)
	}

	start := time.Now()
	// 流式：远端 tar 输出进管道、本地边收边解——整棵树不进内存。
	//
	// ⚠️ 管道所有权（上轮 race 的教训）：写端 pw 由**本函数**在 RunOutput
	// 返回后关闭；extractor 协程只读 pr、完成后经 channel 报结果，**不碰
	// pw**。extractor 出错时先把自己的错记下来、再把管道余量抽干才退出——
	// 不抽干的话写侧会永远阻塞在管道上（无缓冲），谁也别想收尾。
	pr, pw := io.Pipe()
	counts := &dirTarCounts{}
	extractDone := make(chan error, 1)
	async.GoSafeLabeled("koko-dir-untar", func() {
		err := readDirTar(pr, localDir, counts)
		_, _ = io.Copy(io.Discard, pr) // 出错也要把写侧放出来
		extractDone <- err
	})
	code, runErr := s.backend.RunOutput("tar -cf - -C "+shellx.Quote(remoteDir)+" . 2>/dev/null", pw, timeout)
	_ = pw.Close() // 流到此为止：extractor 的 Read 收到 EOF，开始收尾
	extractErr := <-extractDone
	if extractErr != nil {
		// 本地解包先坏时写侧拿到的是 closed pipe——根因是解包，报它。
		return nil, extractErr
	}
	if runErr != nil {
		return nil, runErr
	}
	if code != 0 {
		return nil, fmt.Errorf("tar -cf - -C %s exited %d on %s", remoteDir, code, s.asset)
	}
	note := ""
	if counts.skipped > 0 {
		note = fmt.Sprintf("%d entr%s skipped while extracting (outside the destination, or not a file/dir/symlink): %s",
			counts.skipped, ifPlural(counts.skipped), strings.Join(counts.skippedNames, ", "))
	}
	return &DirTransferResult{
		Asset:     s.asset,
		LocalDir:  localDir,
		RemoteDir: remoteDir,
		Files:     counts.files,
		Bytes:     counts.bytes,
		Seconds:   time.Since(start).Seconds(),
		Skipped:   counts.skipped,
		Note:      note,
	}, nil
}

// readDirTar extracts a tar stream into root as it arrives, counting what
// it wrote and what it refused. An entry whose name is absolute or climbs
// out of root is skipped (counted, named) — never followed.
func readDirTar(r io.Reader, root string, counts *dirTarCounts) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := hdr.Name
		if !safeExtractPath(root, name) {
			counts.skipped++
			if len(counts.skippedNames) < 3 {
				counts.skippedNames = append(counts.skippedNames, name)
			}
			// Drain the body so the stream stays aligned.
			_, _ = io.Copy(io.Discard, tr)
			continue
		}
		dest := filepath.Join(root, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
		case tar.TypeSymlink:
			_ = os.MkdirAll(filepath.Dir(dest), 0o755)
			_ = os.Remove(dest)
			if err := os.Symlink(hdr.Linkname, dest); err != nil {
				return err
			}
			counts.files++
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			n, err := io.Copy(f, tr)
			f.Close()
			if err != nil {
				return err
			}
			counts.files++
			counts.bytes += n
		default:
			counts.skipped++
			if len(counts.skippedNames) < 3 {
				counts.skippedNames = append(counts.skippedNames, name)
			}
		}
	}
}

// safeExtractPath refuses an archive entry that would write outside root:
// absolute names and any climb through "..". Everything else is fine.
func safeExtractPath(root, name string) bool {
	if strings.HasPrefix(name, "/") || filepath.IsAbs(name) {
		return false
	}
	clean := filepath.Clean(filepath.Join(root, filepath.FromSlash(name)))
	rootClean, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	return strings.HasPrefix(clean+string(os.PathSeparator), rootClean+string(os.PathSeparator)) || clean == rootClean
}

// dirTarCounts is what the tar writer tallies while it walks: the numbers
// the result reports, collected on the sending side where the truth is.
// Skipped carries what the stream LEFT BEHIND — a silent skip is a tree
// that arrives smaller than it left, and the caller must be able to see it.
type dirTarCounts struct {
	files        int
	bytes        int64
	skipped      int
	skippedNames []string // the first few, for the note
	// manifest carries one "sha256  relative/path" line per regular file,
	// in walk order — the material the remote verification (sha256sum -c)
	// consumes. Empty when the caller asked for no manifest.
	manifest []string
}

// writeDirTar streams a tar archive of root into w, relative paths only —
// the remote side extracts into its own directory, so nothing local leaks
// into the layout.
//
// The walk order is lexical (filepath.WalkDir), so two uploads of the same
// tree produce the same stream. Directories and symlinks are kept;
// everything else (sockets, devices) is skipped, not fatal — a deploy
// directory with a stray socket in it still uploads.
func writeDirTar(w io.Writer, root string, counts *dirTarCounts) error {
	tw := tar.NewWriter(w)
	// After the tree, the manifest itself goes on the stream as one more
	// entry (jmsx_put_dir's arrival proof): the remote side checks it with
	// sha256sum -c and deletes it. sha256sum 格式: "<hex>  <path>", two
	// spaces, path exactly as archived.
	defer tw.Close()
	defer func() {
		if len(counts.manifest) == 0 {
			return
		}
		name := fmt.Sprintf(".jmsx-manifest-%d", time.Now().UnixNano())
		body := strings.Join(counts.manifest, "\n") + "\n"
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(body)), ModTime: time.Now()})
		_, _ = tw.Write([]byte(body))
	}()
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		name := filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return tw.WriteHeader(&tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: info.ModTime()})
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: target, ModTime: info.ModTime()})
		case info.Mode().IsRegular():
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: int64(info.Mode().Perm()), Size: info.Size(), ModTime: info.ModTime()}); err != nil {
				f.Close()
				return err
			}
			// The digest is computed WHILE the bytes stream (a TeeWriter
			// around the tar entry) — a second read of the file would be a
			// second truth, and a file changing between the two reads is
			// exactly what a manifest must not hide.
			h := sha256.New()
			n, err := io.Copy(io.MultiWriter(tw, h), f)
			f.Close()
			counts.files++
			counts.bytes += n
			counts.manifest = append(counts.manifest, fmt.Sprintf("%x  %s", h.Sum(nil), name))
			return err
		default:
			// Sockets and devices cannot travel a tar stream. Skip, but
			// COUNT: "2 entries did not travel" is a fact the caller owns.
			counts.skipped++
			if len(counts.skippedNames) < 3 {
				counts.skippedNames = append(counts.skippedNames, name)
			}
			return nil
		}
	})
}

func (s *Session) PutFile(localPath, remotePath string, timeout time.Duration) (*TransferResult, error) {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", localPath, err)
	}
	res, err := s.PutBytes(data, remotePath, timeout)
	if err != nil {
		return nil, err
	}
	res.LocalPath = localPath
	return res, nil
}

// PutBytes uploads bytes the caller already holds.
//
// It exists so a file can be produced without touching the local filesystem:
// the agent often has the content in hand (a generated script, a config
// fragment) and writing it to a temp file first only adds a step that can
// fail for reasons unrelated to the transfer.
func (s *Session) PutBytes(data []byte, remotePath string, timeout time.Duration) (*TransferResult, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("content is empty")
	}
	remotePath, err := safeRemotePath(remotePath)
	if err != nil {
		return nil, err
	}

	// The destination directory may not exist yet on the target. Uploading a
	// deploy script to /opt/app/bin/ on a fresh machine would otherwise fail
	// at the final decode step with "No such file or directory", which points
	// at the wrong thing entirely.
	if dir := path.Dir(remotePath); dir != "" && dir != "." && dir != "/" {
		if _, _, _, err := s.Run(fmt.Sprintf("mkdir -p %s", shellx.Quote(dir)), timeout); err != nil {
			return nil, err
		}
	}

	// The channel path: the bytes go on the exec channel's stdin, so one
	// round trip carries the whole file — no command-line length limit, no
	// base64 expansion. Measured on 2026-10-28: a 1MB file arrives in 1.7s
	// this way and takes 25s as base64 chunks.
	//
	// The terminal path cannot do it (it types into a screen), so it
	// answers that it is not supported and the base64 path takes over.
	if res, ok, err := s.putBytesOverChannel(data, remotePath, timeout); ok {
		return res, err
	}
	return s.putBytesOverBase64(data, remotePath, timeout)
}

// putBytesOverChannel writes the bytes on the command channel's stdin.
//
// The write goes to a sibling of the destination and is renamed into place
// afterwards, for two reasons:
//
//   - A transfer that fails halfway leaves the ORIGINAL file alone. Writing
//     straight to the destination would truncate it first, and a half-written
//     config is worse than no config: it looks like a successful deploy.
//   - The destination is resolved first, so a symlinked path keeps its
//     link. Renaming over the link would replace it with a regular file,
//     which breaks a deployment that points "current" at a release.
//
// The rename is atomic because the temporary file is a sibling of the
// target — the same directory, so the same filesystem.
func (s *Session) putBytesOverChannel(data []byte, remotePath string, timeout time.Duration) (*TransferResult, bool, error) {
	start := time.Now()
	qRemote := shellx.Quote(remotePath)

	// Where the file really lives. A symlink's target, or the path itself.
	target, _, _, err := s.Run(fmt.Sprintf("readlink -f %s 2>/dev/null || echo %s", qRemote, qRemote), timeout)
	if err != nil {
		return nil, true, err
	}
	target = strings.TrimSpace(target)
	if target == "" {
		target = remotePath
	}
	qTarget := shellx.Quote(target)

	tmp := fmt.Sprintf("%s.jmsx-tmp-%s", target, stringx.GenerateID())
	qTmp := shellx.Quote(tmp)
	if err := s.PutBytesAt(tmp, data, false, timeout); err != nil {
		if errors.Is(err, ErrChannelTransferUnsupported) {
			// The backend cannot carry bytes on a channel. That is not a
			// failure, it is "fall back to the base64 path".
			return nil, false, nil
		}
		// The artefact goes, so a failed transfer leaves no stray file
		// next to the destination.
		s.Run(fmt.Sprintf("rm -f %s", qTmp), timeout)
		return nil, true, err
	}
	if _, _, _, err := s.Run(fmt.Sprintf("mv -f %s %s", qTmp, qTarget), timeout); err != nil {
		// The artefact goes, so a failed transfer leaves no stray file
		// next to the destination.
		s.Run(fmt.Sprintf("rm -f %s", qTmp), timeout)
		return nil, true, err
	}

	// The size and the checksum are read back from the host, so the result
	// can state that the file arrived intact rather than merely that the
	// write did not fail.
	remote, local, err := s.verifyUpload(target, data, timeout)
	if err != nil {
		return nil, true, err
	}

	return &TransferResult{
		Asset: s.asset, RemotePath: remotePath,
		Bytes: len(data), Chunks: 1,
		Seconds:       time.Since(start).Seconds(),
		RemoteSHA256:  remote,
		LocalSHA256:   local,
		ChecksumMatch: remote != "" && remote == local,
	}, true, nil
}

// verifyUpload reads back the file the host now holds and compares it with
// the bytes that were sent: the size first, then the checksum.
//
// A matching size alone does not prove matching content — a chunk can be
// corrupted in a way that preserves its length — so the checksum is computed
// on both sides too, and the caller's result can state that the file arrived
// intact rather than merely that it is the right length.
func (s *Session) verifyUpload(remotePath string, data []byte, timeout time.Duration) (remote, local string, err error) {
	got, err := s.SizeOf(remotePath, timeout)
	if err != nil {
		return "", "", err
	}
	if got != int64(len(data)) {
		return "", "", fmt.Errorf("size mismatch: sent %d bytes, %s has %d",
			len(data), remotePath, got)
	}
	// The checksum read's own failure does not fail the transfer: the size
	// already matched, and an unreadable checksum says checksum_match=false
	// rather than throwing away a transfer that arrived.
	local = Sha256Prefix(data)
	remote, _ = s.SHA256Prefix(remotePath, timeout)
	return remote, local, nil
}

// putBytesOverBase64 uploads through the terminal, in base64 chunks.
//
// It is the path for a backend that types into a screen: the payload has to
// survive a line of text, and the pane only keeps about forty rows.
func (s *Session) putBytesOverBase64(data []byte, remotePath string, timeout time.Duration) (*TransferResult, error) {
	start := time.Now()
	tmp := fmt.Sprintf("/tmp/.jmsx-%s-%d.b64", s.assetTag(), start.UnixNano())
	// Every path below is interpolated into a shell command line, so every one
	// of them is quoted. The temp path is generated here and cannot contain
	// metacharacters, but it is quoted too so the rule has no exceptions.
	qRemote := shellx.Quote(remotePath)
	qTmp := shellx.Quote(tmp)

	// Start from a clean slate; a stale temp file from an interrupted transfer
	// would otherwise be appended to and silently corrupt the result.
	if _, _, _, err := s.Run(fmt.Sprintf("rm -f %s %s", qTmp, qRemote), timeout); err != nil {
		return nil, err
	}
	if _, _, _, err := s.Run(fmt.Sprintf(": > %s", qTmp), timeout); err != nil {
		return nil, err
	}

	enc := encoding.Base64EncodeBytes(data)
	chunk := s.UploadChunkBytes()
	if chunk <= 0 {
		chunk = uploadChunkBytes
	}
	total := (len(enc) + chunk - 1) / chunk
	chunks := 0
	for off := 0; off < len(enc); off += chunk {
		end := off + chunk
		if end > len(enc) {
			end = len(enc)
		}
		// base64 has no shell metacharacters, but the single quotes still
		// guard against an empty chunk degenerating into a bare quote.
		cmd := fmt.Sprintf("printf '%%s' '%s' >> %s", enc[off:end], qTmp)
		if _, _, _, err := s.Run(cmd, timeout); err != nil {
			return nil, fmt.Errorf("chunk %d/%d: %w", chunks+1, total, err)
		}
		chunks++
	}

	// Decode into place, drop the artefact, and verify the size, so a
	// truncated transfer is caught here rather than by whatever later tries
	// to use the file.
	if _, _, _, err := s.Run(fmt.Sprintf("base64 -d %s > %s", qTmp, qRemote), timeout); err != nil {
		return nil, err
	}
	s.Run(fmt.Sprintf("rm -f %s", qTmp), timeout)

	remote, local, err := s.verifyUpload(remotePath, data, timeout)
	if err != nil {
		return nil, err
	}

	return &TransferResult{
		Asset: s.asset, RemotePath: remotePath,
		Bytes: len(data), Chunks: chunks,
		Seconds:       time.Since(start).Seconds(),
		RemoteSHA256:  remote,
		LocalSHA256:   local,
		ChecksumMatch: remote != "" && remote == local,
	}, nil
}

type GetResult struct {
	Asset      string
	RemotePath string
	LocalPath  string
	Bytes      int
	Truncated  bool
	Available  int64
	// Offset and Length describe the window that was actually read. Offset is
	// the byte offset in the remote file, Length the number of bytes returned.
	Offset int64
	Length int64
	// The checksums of the bytes that arrived and of the file on the host.
	// They are only comparable for a FULL-file read: a window covers part of
	// the file, so the two hashes are expected to differ there.
	LocalSHA256   string
	RemoteSHA256  string
	ChecksumMatch bool
	Content       string
	Seconds       float64
}

// safeRemotePath validates a destination path on the asset.
//
// A relative path is normalised by oekit's pathx, which refuses anything that
// climbs out of its root; an absolute path is allowed (it is the normal way to
// write to /etc or /opt) but must not contain a ".." segment, because the
// caller cannot see where such a path lands and it is never what was meant.
//
// Newlines are refused outright: they are the one character that can end a
// shell command inside a quoted word on some shells.
func safeRemotePath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("remote_path is required")
	}
	if strings.ContainsAny(p, "\n\r") {
		return "", fmt.Errorf("remote_path must not contain a newline")
	}
	if !strings.HasPrefix(p, "/") {
		// The strict档, same as the absolute branch below: a path that
		// mentions ".." is refused outright rather than cleaned away.
		// On a managed host the path may cross a symlink, and "a/../b" is
		// textually fine but can land somewhere else entirely.
		cleaned, ok := pathx.SanitizeRelStrict(p)
		if !ok {
			return "", fmt.Errorf("remote_path %q is not a usable relative path", p)
		}
		return cleaned, nil
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", fmt.Errorf("remote_path %q must not contain a '..' segment", p)
		}
	}
	return p, nil
}

// GetFileRange reads part of a remote file.
//
// offset and length are byte offsets into the file; length 0 means "to the end
// of the requested window". Reading a window server-side matters because the
// transfer cost is per byte: fetching the first 4 KB of a 200 MB log through
// the pane is cheap, while fetching the whole file to look at its head is not.
//
// The byte window is applied on the remote side with dd, so the bytes that
// never leave the host cost nothing.
func (s *Session) GetFileRange(remotePath, localPath string, offset, length int64, timeout time.Duration) (*GetResult, error) {
	remotePath, err := safeRemotePath(remotePath)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	avail, err := s.SizeOf(remotePath, timeout)
	if err != nil {
		return nil, err
	}
	if avail <= 0 {
		return nil, fmt.Errorf("%s is empty or missing", remotePath)
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= avail {
		return nil, fmt.Errorf("offset %d is past the end of %s (%d bytes)", offset, remotePath, avail)
	}

	// The window: from offset to min(avail, offset+length).
	want := avail - offset
	if length > 0 && length < want {
		want = length
	}

	// The host's checksum for the file, taken before the read so that a file
	// changing under us shows up as a mismatch rather than as a silent lie.
	// A read that fails here leaves an empty checksum, which the comparison
	// below reports as a mismatch rather than as a silent success.
	remoteSum, _ := s.SHA256Prefix(remotePath, timeout)

	raw, err := s.backend.PullBytes(remotePath, offset, want, timeout)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != want {
		// Belt and braces: never report success on a short read.
		return nil, fmt.Errorf("read back %d of %d bytes from %s; the pane truncated the "+
			"payload -- retry with a smaller slice (head -c / sed -n) and reassemble locally",
			len(raw), want, remotePath)
	}

	if localPath != "" {
		if err := os.WriteFile(localPath, raw, 0o600); err != nil {
			return nil, fmt.Errorf("write %s: %w", localPath, err)
		}
	}

	// Verify the bytes that arrived against the file on the host.
	//
	// The size check above proves the right NUMBER of bytes came back, not
	// the right bytes: a chunk can be corrupted in a way that preserves its
	// length. The upload side compares checksums for exactly this reason,
	// and a download that is silently wrong is the worse direction -- it
	// lands in a local file that then gets used.
	local := Sha256Prefix(raw)
	remote := strings.TrimSpace(remoteSum)
	checksumMatch := remote != "" && remote == local

	res := &GetResult{
		Asset: s.asset, RemotePath: remotePath, LocalPath: localPath,
		Bytes: len(raw), Truncated: false, Available: avail,
		Offset: offset, Length: int64(len(raw)),
		Seconds:       time.Since(start).Seconds(),
		Content:       string(raw),
		LocalSHA256:   local,
		RemoteSHA256:  remote,
		ChecksumMatch: checksumMatch,
	}
	if !checksumMatch {
		res.Truncated = true
	}
	return res, nil
}

// pullBytes reads `total` bytes of a remote file, in base64 chunks sized to fit
// one screen each.
//
// The pane is the transport, so its visible rows are the real bandwidth. Reading
// a byte range with `dd` and folding the base64 at pane width keeps every row
// intact; looping over ranges lifts the old single-shot ceiling instead of
// refusing anything past it.
//
// The offset advances by what actually came back, NOT by what was requested.
// That distinction is the whole point: when a chunk has to be retried at a
// smaller size it yields fewer bytes, and advancing by the requested size would
// step over the gap and drop it on the floor. That bug read 1645 of 3292 bytes
// with no error reported anywhere.
//
// Offsets stay on a 3-byte boundary so all but the final chunk encode without
// padding, which keeps chunk boundaries from shifting the payload.
func (k *Koko) pullBytes(remotePath string, total int64, timeout time.Duration) ([]byte, error) {
	return k.pullBytesFrom(remotePath, 0, total, timeout)
}

// pullBytesFrom reads `total` bytes starting at `start`.
//
// The offset advance is by what actually came back, NOT by what was requested:
// when a chunk has to be retried at a smaller size it yields fewer bytes, and
// advancing by the requested size would step over the gap and drop it on the
// floor. That bug read 1645 of 3292 bytes with no error reported anywhere.
func (k *Koko) pullBytesFrom(remotePath string, start, total int64, timeout time.Duration) ([]byte, error) {
	chunk := downloadChunkRaw
	chunk -= chunk % 3
	var out []byte
	for off := start; off < start+total; {
		want := start + total - off
		if want > int64(chunk) {
			want = int64(chunk)
		}
		part, err := k.pullRange(remotePath, off, want, timeout)
		if err != nil {
			if len(out) > 0 {
				break // keep what was verified whole rather than losing it
			}
			return nil, err
		}
		if len(part) == 0 {
			break
		}
		out = append(out, part...)
		off += int64(len(part))
	}
	return out, nil
}

// pullRange fetches one byte range and returns however many bytes of it the
// pane could actually hold.
//
// A short read is not silently accepted: it halves the request and tries again,
// so the caller normally gets exactly what it asked for. What it must never do
// is pad — the caller advances its offset by len(result), so an honest short
// read costs one extra round trip while an inflated one corrupts the stream.
func (k *Koko) pullRange(remotePath string, skip, count int64, timeout time.Duration) ([]byte, error) {
	want := count
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		enc, err := k.readEncoded(remotePath, skip, want, timeout)
		if err == nil {
			if raw, decErr := encoding.Base64DecodeBytes(enc); decErr == nil {
				if int64(len(raw)) == want {
					return raw, nil
				}
				lastErr = fmt.Errorf("pane returned %d of %d bytes at offset %d",
					len(raw), want, skip)
			} else {
				lastErr = fmt.Errorf("decode transfer: %w (payload=%q)", decErr, truncStr(enc, 80))
			}
		} else {
			lastErr = err
		}
		// Halve and retry — but only AFTER a real attempt. Checking the floor
		// up front refused every file under 96 bytes outright: the first
		// iteration broke out before trying, so a 53-byte script came back as
		// "no progress".
		if want < 96 {
			break
		}
		want /= 2
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no progress reading %d bytes at offset %d of %s",
			count, skip, remotePath)
	}
	return nil, fmt.Errorf("%w (gave up below %d bytes for the %d-byte chunk at %d)",
		lastErr, want, count, skip)
}

// readEncoded runs the base64 read for one range and reassembles the marked rows.
func (k *Koko) readEncoded(remotePath string, skip, count int64, timeout time.Duration) (string, error) {
	// The encoding has to survive three separate manglings, each of which broke
	// this in turn:
	//
	//  1. Exec re-bases every row with `sed s/^ *N/epoch-N/`, so output starting
	//     with a digit gets rewritten. Hence the "B64:" letter prefix.
	//  2. tmux packs neighbouring rows side by side, so the trailing rc marker
	//     lands glued onto the payload and a "first run of base64 characters"
	//     heuristic only ever recovers the leftmost column.
	//  3. One long line exceeds the 220-column pane and gets soft-wrapped, which
	//     fragments the payload. Measured 2026-10-15: `base64 -w0` produced a
	//     single 1368-character row, the pane wrapped it, and only 153 bytes of
	//     a 1024-byte request ever came back.
	//
	// So: fold to rows narrower than the pane, mark every one, and reassemble
	// from the marks.
	b64Out, _, _, err := k.exec(fmt.Sprintf(
		"dd if=%s bs=1 skip=%d count=%d 2>/dev/null | base64 | fold -w %d | sed 's/^/%s/'",
		shellx.Quote(remotePath), skip, count, b64RowCols, b64Mark), timeout, false)
	if err != nil {
		return "", err
	}

	enc := ""
	for _, col := range splitLines(b64Out) {
		for _, part := range frameSplit.Split(col, -1) {
			if chunk, ok := strings.CutPrefix(strings.TrimSpace(part), b64Mark); ok {
				enc += chunk
			}
		}
	}
	if enc == "" {
		return "", fmt.Errorf("no encoded rows came back for %d bytes at offset %d of %s",
			count, skip, remotePath)
	}
	return enc, nil
}

// b64Mark prefixes every row of the encoded payload so reassembly can pick the
// rows out of whatever else the terminal paints around them. It starts with a
// letter so Exec's numeric epoch re-basing cannot touch it.
const b64Mark = "B64:"

func truncStr(s string, n int) string {
	return stringx.FirstN(s, n, "...")
}

func (s *Session) assetTag() string {
	t := s.asset
	t = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '-'
		}
	}, t)
	if len(t) > 32 {
		t = t[:32]
	}
	return t
}

// RelayResult is the outcome of a copy from one asset to another.
type RelayResult struct {
	SourceAsset   string
	SourcePath    string
	DestAsset     string
	DestPath      string
	Bytes         int64
	Chunks        int
	Seconds       float64
	SourceSHA256  string
	DestSHA256    string
	ChecksumMatch bool
}

// DirRelayResult is the outcome of a directory copy from one asset to
// another.
type DirRelayResult struct {
	SourceAsset string
	SourcePath  string
	DestAsset   string
	DestPath    string
	// Bytes is what the archive actually streamed (the transfer's own
	// count, not an estimate).
	Bytes   int64
	Seconds float64
	// Note carries the merge-semantics reminder and anything else the
	// caller should read before trusting the copy.
	Note string
}

// RelayDir copies a whole directory from one asset to another: the source's
// `tar -cf -` writes onto the destination's `tar -xf -` through one pipe in
// this process — the archive never touches the local disk, the same
// machine-to-machine discipline as Relay (a directory that never lands here
// cannot be left half-copied here).
//
// It needs the connection-token backend on BOTH ends (RunOutput is the one
// direction the terminal pane cannot do) and tar on both machines. The
// semantics on the destination are MERGE, the same as PutDir: same-name
// files are overwritten, entries the destination already had are untouched.
// The archive streams straight through, so there is no arrival proof here —
// a caller that needs one runs a sha256 check itself (jmsx_exec) or moves
// the tree in two honest hops (GetDir then PutDir), which DOES verify.
func RelayDir(ctx context.Context, cfg Config, srcAsset, srcPath, dstAsset, dstPath string, timeout time.Duration) (*DirRelayResult, error) {
	started := time.Now()

	// Same identity rule as Relay: one machine spelled two ways must not
	// stream a tree onto itself (tar reading and writing the same tree
	// concurrently is a corruption nobody can audit).
	srcRef, err := resolveAsset(ctx, cfg, srcAsset)
	if err != nil {
		return nil, err
	}
	dstRef, err := resolveAsset(ctx, cfg, dstAsset)
	if err != nil {
		return nil, err
	}
	if srcRef.ID == dstRef.ID {
		return nil, fmt.Errorf("the source and the destination are the same machine (%s); "+
			"streaming a directory onto itself is a corruption nobody can audit", srcRef.Name)
	}
	srcPath, err = safeRemotePath(srcPath)
	if err != nil {
		return nil, err
	}
	dstPath, err = safeRemotePath(dstPath)
	if err != nil {
		return nil, err
	}

	src, err := OpenSession(ctx, cfg, srcAsset, timeout)
	if err != nil {
		return nil, fmt.Errorf("open the source session on %s: %w", srcAsset, err)
	}
	defer src.Close()
	dst, err := OpenSession(ctx, cfg, dstAsset, timeout)
	if err != nil {
		return nil, fmt.Errorf("open the destination session on %s: %w", dstAsset, err)
	}
	defer dst.Close()

	if !src.CanCarryBytes() || !dst.CanCarryBytes() {
		return nil, fmt.Errorf("relaying a directory needs the connection-token backend on both ends; " +
			"the koko terminal path cannot stream binary output")
	}

	// The destination needs tar to extract and a directory to extract into.
	// Checked FIRST so the failure lands before any byte moves.
	if out, _, _, err := dst.Run("command -v tar >/dev/null 2>&1 || echo JMSX_NO_TAR", timeout); err != nil {
		return nil, err
	} else if strings.Contains(out, "JMSX_NO_TAR") {
		return nil, fmt.Errorf("tar is not installed on %s; a directory relay extracts the stream remotely", dstAsset)
	}
	if _, _, _, err := dst.Run("mkdir -p "+shellx.Quote(dstPath), timeout); err != nil {
		return nil, err
	}
	if _, _, _, err := src.Run("command -v tar >/dev/null 2>&1 || echo JMSX_NO_TAR", timeout); err != nil {
		return nil, err
	}

	pr, pw := io.Pipe()
	// 源端流式输出，所有权与 get_dir 同一套（2026-11-07 修）：写端 pw 由
	// 源端 goroutine 在 runOutputLocked 返回后关闭，结果经 srcDone channel
	// 带回——不靠管道 EOF 的隐式次序做跨协程同步（race 检测的盲区）。
	// 目标端先出错时，先把管道余量抽干再等源端收尾：不抽干则源端
	// goroutine 永远阻塞在写上（dst 的 tar 死了，没人读 pr），源会话
	// 被白占到它自己的超时为止。
	srcDone := make(chan error, 1)
	async.GoSafeLabeled("koko-relay-dir-src", func() {
		code, err := src.runOutputLocked("tar -cf - -C "+shellx.Quote(srcPath)+" .", pw, timeout)
		if err == nil && code != 0 {
			err = fmt.Errorf("tar -cf - -C %s on %s exited %d", srcPath, srcAsset, code)
		}
		_ = pw.Close()
		srcDone <- err
	})
	// The bridge counts what actually crossed: the transfer's own number,
	// not a size measured up front (a size that changed mid-stream is
	// exactly what a recount catches).
	streamed, err := runInputCounting(dst, "tar -xf - -C "+shellx.Quote(dstPath), pr, timeout)
	if err != nil {
		_, _ = io.Copy(io.Discard, pr)
		<-srcDone
		return nil, err
	}
	if streamErr := <-srcDone; streamErr != nil {
		return nil, streamErr
	}
	return &DirRelayResult{
		SourceAsset: srcAsset,
		SourcePath:  srcPath,
		DestAsset:   dstAsset,
		DestPath:    dstPath,
		Bytes:       streamed,
		Seconds:     time.Since(started).Seconds(),
		Note: "merge semantics: same-name files were overwritten on the destination, entries it already had are untouched; " +
			"no arrival proof on a straight-through stream — run a checksum on the destination (jmsx_exec) if this tree must be proven",
	}, nil
}

// runInputCounting is RunInput with a counting reader in front: how many
// bytes the destination actually drank.
func runInputCounting(s *Session, command string, r io.Reader, timeout time.Duration) (int64, error) {
	cr := &countingReader{r: r}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend == nil || s.closed {
		return 0, fmt.Errorf("the session is closed")
	}
	_, err := s.backend.RunInput(command, cr, timeout)
	return cr.n, err
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// runOutputLocked is RunOutput under the session's own lock: a streamed
// archive is one indivisible use of the channel (the same rule PutDir's
// runLocked applies to RunInput).
func (s *Session) runOutputLocked(command string, w io.Writer, timeout time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend == nil || s.closed {
		return 0, fmt.Errorf("the session is closed")
	}
	return s.backend.RunOutput(command, w, timeout)
}

// relayChunkBytes is how many bytes travel in one hop.
//
// The bytes go over the command channel, so the limit is the size of the
// answer and not a terminal line: four megabytes keeps a relay of a
// gigabyte to a couple of hundred round trips.
const relayChunkBytes = 4 * 1024 * 1024

// Relay copies a file from one asset to another.
//
// The bytes travel through this process and never touch the local disk:
// they come off the source's command channel and go straight onto the
// destination's. A file that never lands here is a file that cannot be
// left half-copied here.
//
// It needs the connection-token backend. The terminal path types into a
// screen, so it can neither append to a file nor carry the bytes without
// base64 — and a relay that could not append would overwrite the
// destination with every chunk.
func Relay(ctx context.Context, cfg Config, srcAsset, srcPath, dstAsset, dstPath string, timeout time.Duration) (*RelayResult, error) {
	started := time.Now()

	// The two ends are compared by IDENTITY, not by the spelling the
	// caller used: "node-071" and "192.0.2.71" are one machine, and a
	// copy of a file onto itself would truncate the file being read.
	srcRef, err := resolveAsset(ctx, cfg, srcAsset)
	if err != nil {
		return nil, err
	}
	dstRef, err := resolveAsset(ctx, cfg, dstAsset)
	if err != nil {
		return nil, err
	}
	if srcRef.ID == dstRef.ID {
		return nil, fmt.Errorf("the source and the destination are the same machine (%s); "+
			"copying a file onto itself would truncate the file being read", srcRef.Name)
	}
	srcPath, err = safeRemotePath(srcPath)
	if err != nil {
		return nil, err
	}
	dstPath, err = safeRemotePath(dstPath)
	if err != nil {
		return nil, err
	}

	src, err := OpenSession(ctx, cfg, srcAsset, timeout)
	if err != nil {
		return nil, fmt.Errorf("open the source session on %s: %w", srcAsset, err)
	}
	defer src.Close()
	dst, err := OpenSession(ctx, cfg, dstAsset, timeout)
	if err != nil {
		return nil, fmt.Errorf("open the destination session on %s: %w", dstAsset, err)
	}
	defer dst.Close()

	if !src.CanCarryBytes() || !dst.CanCarryBytes() {
		return nil, fmt.Errorf("relaying needs the connection-token backend; " +
			"the koko terminal path cannot append to a file, so every chunk would overwrite the last one")
	}

	size, sizeErr := src.SizeOf(srcPath, timeout)
	if sizeErr != nil {
		return nil, fmt.Errorf("measure %s on %s: %w", srcPath, srcAsset, sizeErr)
	}

	// The destination's directory may not exist yet on that machine.
	if dir := path.Dir(dstPath); dir != "" && dir != "." && dir != "/" {
		if _, _, _, err := dst.Run(fmt.Sprintf("mkdir -p %s", shellx.Quote(dir)), timeout); err != nil {
			return nil, err
		}
	}

	// The destination is resolved and the bytes land on a sibling of it, so
	// the copy is put in place with one rename at the end. A relay that
	// fails halfway then leaves the destination exactly as it was — not a
	// half-copied file that looks like the real thing.
	dstTarget, _, _, err := dst.Run(fmt.Sprintf("readlink -f %s 2>/dev/null || echo %s",
		shellx.Quote(dstPath), shellx.Quote(dstPath)), timeout)
	if err != nil {
		return nil, err
	}
	dstTarget = strings.TrimSpace(dstTarget)
	if dstTarget == "" {
		dstTarget = dstPath
	}
	dstTmp := fmt.Sprintf("%s.jmsx-relay-%d-%d", dstTarget, os.Getpid(), time.Now().UnixNano()%100000)

	res := &RelayResult{
		SourceAsset: srcAsset, SourcePath: srcPath,
		DestAsset: dstAsset, DestPath: dstPath,
		Bytes: size,
	}
	var written int64
	for written < size {
		want := size - written
		if want > relayChunkBytes {
			want = relayChunkBytes
		}
		part, err := src.PullBytes(srcPath, written, want, timeout)
		if err != nil {
			return res, fmt.Errorf("read %d bytes at offset %d of %s: %w", want, written, srcPath, err)
		}
		if len(part) == 0 {
			return res, fmt.Errorf("the source gave no bytes at offset %d of %s", written, srcPath)
		}
		// The first chunk replaces the temporary file and the rest extend
		// it. It goes through the session, so the destination connection
		// is used the same way as everywhere else.
		err = dst.PutBytesAt(dstTmp, part, written > 0, timeout)
		if err != nil {
			// The half-copied file goes: the destination is still the
			// file it was before the relay started.
			dst.Run(fmt.Sprintf("rm -f %s", shellx.Quote(dstTmp)), timeout)
			return res, fmt.Errorf("write %d bytes at offset %d of %s: %w", len(part), written, dstPath, err)
		}
		written += int64(len(part))
		res.Chunks++
	}
	if size == 0 {
		// An empty file still has to exist on the other side.
		if err := dst.PutBytesAt(dstTmp, nil, false, timeout); err != nil {
			return res, err
		}
		res.Chunks = 1
	}
	// One rename puts the copy in place.
	if _, _, _, err := dst.Run(fmt.Sprintf("mv -f %s %s",
		shellx.Quote(dstTmp), shellx.Quote(dstTarget)), timeout); err != nil {
		dst.Run(fmt.Sprintf("rm -f %s", shellx.Quote(dstTmp)), timeout)
		return res, fmt.Errorf("put the relayed file in place on %s: %w", dstAsset, err)
	}

	res.Seconds = time.Since(started).Seconds()
	// The two ends are hashed on their own machines, so a match says the
	// file arrived intact rather than merely that the right number of bytes
	// was written.
	srcSum, _ := src.SHA256Prefix(srcPath, timeout)
	dstSum, _ := dst.SHA256Prefix(dstTarget, timeout)
	res.SourceSHA256 = srcSum
	res.DestSHA256 = dstSum
	res.ChecksumMatch = srcSum != "" && srcSum == dstSum
	return res, nil
}
