package handler

import (
	"testing"

	"github.com/yi-nology/jumpserver-mcp-kit/pkg/jmsapi"
)

// The tool layer must not speak HTTP.
//
// It asks for a resource; the gateway turns that into a call. A path
// literal in this package is a leak that the compiler cannot see (a wrong
// path is a 404 at runtime), and a raw transport call is a leak the compiler
// cannot see either (it is perfectly legal Go).
//
// The check lives here because this is the package it protects: it is not a
// style preference, it is the boundary the whole layering rests on.
func TestTheToolLayerDoesNotSpeakHTTP(t *testing.T) {
	bad, err := jmsapi.ProtocolViolations(".")
	if err != nil {
		t.Fatalf("scan the tool layer: %v", err)
	}
	for _, v := range bad {
		t.Errorf("%s", v)
	}
}
