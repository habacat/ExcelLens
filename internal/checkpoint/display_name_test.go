package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"excellens/internal/formats"
)

func TestCheckpointDisplayNamePreservesLegacyBindingAndReopen(t *testing.T) {
	s, def, _ := fixture(t)
	legacy, _ := json.Marshal(def)
	digest := sha256.Sum256(legacy)
	want := hex.EncodeToString(digest[:])
	def.FileName = "原始金额.csv"
	got, err := DefinitionHash(def)
	if err != nil || got != want {
		t.Fatal("display name changed legacy binding", got, want, err)
	}
	l := create(t, s, def)
	m := l.Snapshot()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l, err = s.Acquire(context.Background(), m.ID, want)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if l.Snapshot().Definition.FileName != def.FileName {
		t.Fatal("lost original name")
	}
	for _, name := range []string{"bad\x00name.csv", strings.Repeat("x", formats.CheckpointFileNameBytes+1)} {
		def.FileName = name
		if _, err := DefinitionHash(def); err == nil {
			t.Fatal("accepted invalid display name")
		}
	}
}
