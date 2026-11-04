package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yi-nology/jumpserver-exec/biz/router"
	"github.com/yi-nology/jumpserver-kit/pkg/httpapi"
)

// The OpenAPI document under swagger/ is generated, and a generated thing
// that is not compared against its source is a hand-written thing with
// extra steps.
//
// The source here is the tool registry: the same one the MCP face serves.
// So the contract a REST caller codes against and the tools an agent calls
// cannot drift apart — change a tool, regenerate, or this test names the
// difference.
//
// ⚠️ The version is compared too, and that is the common failure: the
// Version constant is bumped for a release and the document is not
// regenerated ⇒ the shipped contract documents the PREVIOUS release. The
// regeneration is ./script/gen-openapi.sh; its output is checked in.
//
// ⚠️ The document is rendered from the SAME code path the server serves
// (router.NewContract + httpapi.OpenAPI), not a parallel assembly — a
// guard that builds its own catalogue proves only that its own catalogue
// renders. NewContract renders the FULL catalogue (all 15 tools): the
// checked-in contract must not be a function of the credentials on the
// machine that happens to regenerate it.
func TestTheCheckedInOpenAPIDocumentMatchesTheTools(t *testing.T) {
	checkedIn, err := os.ReadFile(filepath.Join(repoRoot(t), "swagger", "openapi.json"))
	if err != nil {
		t.Fatalf("swagger/openapi.json: %v — run ./script/gen-openapi.sh", err)
	}

	r := router.NewContract("guard")
	got, err := httpapi.OpenAPI(r, httpapi.Options{Name: router.ServerName, Version: Version})
	if err != nil {
		t.Fatalf("render the document: %v", err)
	}

	// A document that lists no tool must not pass just because the
	// regeneration renders the same emptiness.
	if !bytes.Contains(got, []byte("/api/v1/tools/jmsx_")) {
		t.Fatal("the generated document lists no tool at all: the registry " +
			"came up empty, and an empty document matching an empty document " +
			"is not a contract")
	}

	if bytes.Equal(checkedIn, got) {
		return
	}

	line, want, have := firstDiffLine(checkedIn, got)
	hint := ""
	var doc struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
	}
	_ = json.Unmarshal(checkedIn, &doc)
	if doc.Info.Version != Version {
		hint = "\n文档的 info.version 是 " + doc.Info.Version + "，二进制的版本是 " +
			Version + " —— 抬了 Version 常量就得重新生成文档"
	}
	t.Errorf("swagger/openapi.json 与工具目录对不上（第 %d 行起）%s：\n"+
		"  签入的: %s\n  生成的: %s\n"+
		"改了工具、契约或版本就跑 ./script/gen-openapi.sh，把重新生成的 "+
		"swagger/openapi.json 一起提交", line, hint, want, have)
}

// firstDiffLine reports the first line where the two documents disagree.
func firstDiffLine(a, b []byte) (int, string, string) {
	al := strings.Split(string(a), "\n")
	bl := strings.Split(string(b), "\n")
	for i := 0; i < len(al) || i < len(bl); i++ {
		var av, bv string
		if i < len(al) {
			av = al[i]
		}
		if i < len(bl) {
			bv = bl[i]
		}
		if av != bv {
			return i + 1, av, bv
		}
	}
	return 0, "", ""
}
