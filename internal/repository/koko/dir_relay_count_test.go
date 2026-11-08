package koko

// dir_relay_count_test.go 钉住 relay_dir 的字节数口径（GitHub #11）。
//
// relay_dir 曾经把「流过桥的字节数」当成 bytes 报出去——tar 有逐条目头和
// 512B 块对齐填充，23 字节的小树能报成 10240；而 put_dir / get_dir 报的
// 一直是文件内容字节数。三个工具一个字段名两种语义，就是从这里拆掉的。
// 这里钉的是新口径的机械本身：tarContentCounter 的镜像解析。

import (
	"archive/tar"
	"bytes"
	"io"
	"testing"
	"time"
)

// buildTarStream 造一个最小的 tar 流：1024 字节与 300 字节的常规文件
// （300 < 512，专门踩对齐填充）、一个目录、一个软链。
func buildTarStream(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	entry := func(name string, typeflag byte, body []byte, link string) {
		t.Helper()
		hdr := &tar.Header{Name: name, Typeflag: typeflag, ModTime: time.Unix(0, 0).UTC()}
		if typeflag == tar.TypeReg {
			hdr.Size = int64(len(body))
			hdr.Mode = 0o644
		}
		if typeflag == tar.TypeSymlink {
			hdr.Linkname = link
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %s: %v", name, err)
		}
		if len(body) > 0 {
			if _, err := tw.Write(body); err != nil {
				t.Fatalf("write body %s: %v", name, err)
			}
		}
	}
	entry("big.bin", tar.TypeReg, bytes.Repeat([]byte{0xA5}, 1024), "")
	entry("sub/", tar.TypeDir, nil, "")
	entry("small.cfg", tar.TypeReg, bytes.Repeat([]byte{0x5A}, 300), "")
	entry("link", tar.TypeSymlink, nil, "big.bin")
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

func TestTheRelayBridgeCountsFileContentNotStream(t *testing.T) {
	stream := buildTarStream(t)
	wantContent := int64(1024 + 300) // 目录与软链不计，padding 不计

	counter := newTarContentCounter(bytes.NewReader(stream))
	var forwarded bytes.Buffer
	if _, err := io.Copy(&forwarded, counter); err != nil {
		t.Fatalf("copy through the counter: %v", err)
	}
	counter.finish()

	if got := counter.count(); got != wantContent {
		t.Errorf("content bytes = %d, want %d (the files' own bytes; headers and padding are not content)", got, wantContent)
	}
	if !bytes.Equal(forwarded.Bytes(), stream) {
		t.Errorf("the destination did not receive the stream byte-for-byte (%d of %d bytes) — the counter must be a transparent pass-through",
			forwarded.Len(), len(stream))
	}
}

// 流坏在半路：镜像停掉，但目的地照收每个字节——计数放弃，传输不放弃
//（乱流由目的端的 tar -xf 自己报错，那是调用方该看到的那个错）。
func TestTheRelayBridgeKeepsFeedingTheDestinationWhenTheStreamIsCorrupt(t *testing.T) {
	stream := buildTarStream(t)
	// 剪在第一个文件的内容中间：头之后只留 100 字节。
	truncated := stream[:512+100]

	counter := newTarContentCounter(bytes.NewReader(truncated))
	var forwarded bytes.Buffer
	if _, err := io.Copy(&forwarded, counter); err != nil {
		t.Fatalf("copy through the counter: %v", err)
	}
	counter.finish() // 必须能收场，不挂

	if got := counter.count(); got != 0 {
		t.Errorf("content bytes = %d, want 0 (half a file is an accident, not a byte count)", got)
	}
	if !bytes.Equal(forwarded.Bytes(), truncated) {
		t.Errorf("the destination lost bytes on a corrupt stream (%d of %d) — the mirror's death must not touch the transfer",
			forwarded.Len(), len(truncated))
	}
}
