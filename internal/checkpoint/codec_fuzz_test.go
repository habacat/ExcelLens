package checkpoint

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"excellens/internal/formats"
)

func FuzzCheckpointManifest(f *testing.F) {
	def := Definition{Kind: "query", Driver: "search", Semantics: 1, Title: "编号", Sources: []Source{{Path: filepath.Join(f.TempDir(), "source.csv"), Book: "book", Fingerprint: "fingerprint", Format: "csv", Source: "wb:数据", Stamp: "stamp"}}, Payload: json.RawMessage(`{"query":"001"}`)}
	def, binding, err := prepareDefinition(def)
	if err != nil {
		f.Fatal(err)
	}
	now := time.Now().UTC()
	m := Manifest{Version: Schema, ID: "0123456789abcdef0123456789abcdef", Revision: 1, Definition: def, DefinitionHash: binding, Created: now, Updated: now, Stage: "scan", Cursor: json.RawMessage(`{"next":0}`)}
	m.ChainHash = initialChain(m.ID, binding)
	valid, err := encodeManifest(m)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid[manifestHeaderBytes:])
	f.Add([]byte(`{"version":1,"version":2}`))
	f.Add([]byte(`{"cursor":[[[[]]]]}`))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > formats.CheckpointManifestBytes {
			t.Skip()
		}
		if decoded, err := decodeManifest(bytes.NewReader(envelope(body))); err == nil {
			encoded, err := encodeManifest(decoded)
			if err != nil || !bytes.Equal(encoded[manifestHeaderBytes:], body) {
				t.Fatal("accepted metadata has no exact canonical round trip", err)
			}
		}
	})
}
