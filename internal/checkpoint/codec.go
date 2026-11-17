package checkpoint

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"excellens/internal/formats"
)

const manifestMagic = "ELCPST01"
const manifestHeaderBytes = 48
const blockMagic = "ELCPBL01"
const blockHeaderBytes = 64

func initialChain(id, binding string) string {
	digest := sha256.Sum256([]byte(blockMagic + "\x00" + id + "\x00" + binding))
	return hex.EncodeToString(digest[:])
}

func nextChain(previous string, header []byte) string {
	decoded, _ := hex.DecodeString(previous)
	h := sha256.New()
	_, _ = h.Write(decoded)
	_, _ = h.Write(header)
	return hex.EncodeToString(h.Sum(nil))
}

func encodeManifest(m Manifest) ([]byte, error) {
	if err := validateManifest(m); err != nil {
		return nil, err
	}
	body, err := json.Marshal(m)
	if err != nil || len(body)+manifestHeaderBytes > formats.CheckpointManifestBytes {
		return nil, ErrCapacity
	}
	out := make([]byte, manifestHeaderBytes+len(body))
	copy(out, manifestMagic)
	binary.LittleEndian.PutUint64(out[8:16], uint64(len(body)))
	digest := sha256.Sum256(body)
	copy(out[16:48], digest[:])
	copy(out[48:], body)
	return out, nil
}

func decodeManifest(r io.Reader) (Manifest, error) {
	var m Manifest
	data, err := io.ReadAll(io.LimitReader(r, formats.CheckpointManifestBytes+1))
	if err != nil {
		return m, err
	}
	if len(data) < manifestHeaderBytes || len(data) > formats.CheckpointManifestBytes || string(data[:8]) != manifestMagic || binary.LittleEndian.Uint64(data[8:16]) != uint64(len(data)-manifestHeaderBytes) {
		return m, ErrCorrupt
	}
	body := data[manifestHeaderBytes:]
	digest := sha256.Sum256(body)
	if !bytes.Equal(data[16:48], digest[:]) || jsonStructure(body, formats.CheckpointManifestBytes) != nil {
		return m, ErrCorrupt
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return Manifest{}, ErrCorrupt
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Manifest{}, ErrCorrupt
	}
	if err := validateManifest(m); err != nil {
		return Manifest{}, err
	}
	canonical, err := json.Marshal(m)
	if err != nil || !bytes.Equal(body, canonical) {
		return Manifest{}, ErrCorrupt
	}
	return m, nil
}

func encodeBlock(block Block, digest []byte) []byte {
	header := make([]byte, blockHeaderBytes)
	copy(header, blockMagic)
	binary.LittleEndian.PutUint64(header[8:16], block.Sequence)
	binary.LittleEndian.PutUint64(header[16:24], uint64(block.Records))
	binary.LittleEndian.PutUint64(header[24:32], uint64(block.Bytes))
	copy(header[32:64], digest)
	return header
}
