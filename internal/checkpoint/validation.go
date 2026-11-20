package checkpoint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"excellens/internal/formats"
)

func validID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && s == strings.ToLower(s)
}

func validName(s string) bool {
	if len(s) < 1 || len(s) > 48 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// Metadata is bounded before decoding. Duplicate keys and excessive nesting
// fail rather than silently selecting the last interpretation of a cursor.
func jsonStructure(data []byte, maximum int) error {
	if len(data) == 0 || len(data) > maximum || !utf8.Valid(data) {
		return ErrCorrupt
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return ErrCorrupt
		}
		token, err := d.Token()
		if err != nil {
			return ErrCorrupt
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delim != '[' && delim != '{' {
			return ErrCorrupt
		}
		seen := map[string]struct{}{}
		for d.More() {
			if delim == '{' {
				key, err := d.Token()
				name, ok := key.(string)
				if err != nil || !ok {
					return ErrCorrupt
				}
				if _, exists := seen[name]; exists {
					return ErrCorrupt
				}
				seen[name] = struct{}{}
			}
			if err := value(depth + 1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || delim == '[' && end != json.Delim(']') || delim == '{' && end != json.Delim('}') {
			return ErrCorrupt
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return ErrCorrupt
	}
	return nil
}

func compactJSON(data []byte, limit int) (json.RawMessage, error) {
	if err := jsonStructure(data, limit); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Compact(&out, data); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func prepareDefinition(def Definition) (Definition, string, error) {
	if def.Kind != "query" && def.Kind != "export" && def.Kind != "edit" || !validName(def.Driver) || def.Semantics == 0 || !utf8.ValidString(def.Title) || utf8.RuneCountInString(def.Title) > formats.CheckpointTitleRunes || len(def.Sources) < 1 || len(def.Sources) > formats.CheckpointSources {
		return Definition{}, "", ErrBinding
	}
	if !utf8.ValidString(def.FileName) || len(def.FileName) > formats.CheckpointFileNameBytes || strings.ContainsRune(def.FileName, '\x00') {
		return Definition{}, "", ErrBinding
	}
	def.Sources = append([]Source(nil), def.Sources...)
	for _, source := range def.Sources {
		if !filepath.IsAbs(source.Path) || !utf8.ValidString(source.Path) || len(source.Path) > 32768 || strings.ContainsRune(source.Path, '\x00') {
			return Definition{}, "", ErrBinding
		}
		for _, field := range []string{source.Book, source.Fingerprint, source.Format, source.Source, source.Stamp} {
			if field == "" || len(field) > 1024 || !utf8.ValidString(field) {
				return Definition{}, "", ErrBinding
			}
		}
		if format, ok := formats.Lookup(source.Format); !ok || format.MaxRows == 0 {
			return Definition{}, "", ErrBinding
		}
	}
	var err error
	def.Payload, err = compactJSON(def.Payload, formats.CheckpointDefinitionBytes)
	if err != nil {
		return Definition{}, "", ErrBinding
	}
	encoded, err := json.Marshal(def)
	if err != nil || len(encoded) > formats.CheckpointDefinitionBytes {
		return Definition{}, "", ErrCapacity
	}
	// Display metadata has its manifest checksum but is not source/rule identity.
	// Omitting the optional name here preserves every legacy definition hash.
	binding := def
	binding.FileName = ""
	encoded, err = json.Marshal(binding)
	if err != nil {
		return Definition{}, "", ErrBinding
	}
	digest := sha256.Sum256(encoded)
	return def, hex.EncodeToString(digest[:]), nil
}

// DefinitionHash uses exactly the same canonical envelope as Create/Acquire.
func DefinitionHash(def Definition) (string, error) {
	_, digest, err := prepareDefinition(def)
	return digest, err
}

func cloneManifest(m Manifest) Manifest {
	m.Definition.Sources = append([]Source(nil), m.Definition.Sources...)
	m.Definition.Payload = bytes.Clone(m.Definition.Payload)
	m.Cursor = bytes.Clone(m.Cursor)
	return m
}

func validateManifest(m Manifest) error {
	if (m.AlternateBlocks || m.CompactedAt != 0) && (m.CompactedAt == 0 || m.CompactedAt > m.Revision || m.Blocks == 0 || m.Definition.Kind != "edit") {
		return ErrCorrupt
	}
	_, binding, err := prepareDefinition(m.Definition)
	if err != nil || m.Version != Schema || !validID(m.ID) || m.Revision == 0 || m.DefinitionHash != binding || !validName(m.Stage) || !validHash(m.ChainHash) || m.Records < 0 || m.DataBytes < 0 || m.DataBytes > formats.CheckpointBytes || m.Blocks > uint64(m.DataBytes/blockHeaderBytes) || m.Created.IsZero() || m.Updated.Before(m.Created) {
		return ErrCorrupt
	}
	if m.Blocks == 0 && (m.DataBytes != 0 || m.Records != 0 || m.ChainHash != initialChain(m.ID, binding)) {
		return ErrCorrupt
	}
	return jsonStructure(m.Cursor, formats.CheckpointCursorBytes)
}
