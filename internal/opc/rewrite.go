// Package opc copies and verifies the physical ZIP parts of a preflighted
// Open Packaging Conventions document. Its callers own part relationships,
// worksheet coordinates and the format-specific edits.
package opc

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"slices"
	"strings"
)

// Change leaves the compressed data intact by default. Remove omits the part.
// Write supplies the uncompressed replacement when the part has changed.
type Change struct {
	Remove bool
	Write  func(source io.Reader, output io.Writer) error
}

type Receipt struct {
	Parts   map[string][32]byte
	Changed map[string]bool
}

type cancelReader struct {
	ctx context.Context
	src io.Reader
}

func (r cancelReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.src.Read(p)
}

func Rewrite(ctx context.Context, dst io.Writer, source *zip.Reader, plan func(*zip.File) (Change, error)) (Receipt, error) {
	return RewriteWithAdditions(ctx, dst, source, nil, plan)
}

// Newly required OPC parts have explicit names and contents and receive the
// same content-digest verification as repaired parts. Existing names may only
// be changed through plan, which also owns removals.
func RewriteWithAdditions(ctx context.Context, dst io.Writer, source *zip.Reader, added map[string][]byte, plan func(*zip.File) (Change, error)) (Receipt, error) {
	result := Receipt{Parts: make(map[string][32]byte), Changed: make(map[string]bool)}
	archive := zip.NewWriter(dst)
	if err := archive.SetComment(source.Comment); err != nil {
		return Receipt{}, err
	}
	for _, part := range source.File {
		if _, exists := added[part.Name]; exists {
			return Receipt{}, fmt.Errorf("部件已存在：%s", part.Name)
		}
		if err := ctx.Err(); err != nil {
			return Receipt{}, err
		}
		if part.FileInfo().IsDir() {
			continue
		}
		change, err := plan(part)
		if err != nil {
			return Receipt{}, err
		}
		if change.Remove {
			result.Changed[part.Name] = true
			continue
		}
		header := part.FileHeader
		hash := sha256.New()
		if change.Write == nil {
			raw, err := part.OpenRaw()
			if err != nil {
				return Receipt{}, err
			}
			out, err := archive.CreateRaw(&header)
			if err != nil {
				return Receipt{}, err
			}
			if _, err = io.Copy(io.MultiWriter(out, hash), cancelReader{ctx, raw}); err != nil {
				return Receipt{}, err
			}
		} else {
			header.CRC32, header.CompressedSize, header.UncompressedSize = 0, 0, 0
			header.CompressedSize64, header.UncompressedSize64 = 0, 0
			out, err := archive.CreateHeader(&header)
			if err != nil {
				return Receipt{}, err
			}
			raw, err := part.Open()
			if err != nil {
				return Receipt{}, err
			}
			err = change.Write(cancelReader{ctx, raw}, io.MultiWriter(out, hash))
			raw.Close()
			if err != nil {
				return Receipt{}, err
			}
			result.Changed[part.Name] = true
		}
		var digest [32]byte
		hash.Sum(digest[:0])
		result.Parts[part.Name] = digest
	}
	for _, name := range slices.Sorted(maps.Keys(added)) {
		if err := ctx.Err(); err != nil {
			return Receipt{}, err
		}
		if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\:#?%") {
			return Receipt{}, fmt.Errorf("部件路径无效：%s", name)
		}
		out, err := archive.Create(name)
		if err != nil {
			return Receipt{}, err
		}
		if _, err = out.Write(added[name]); err != nil {
			return Receipt{}, err
		}
		result.Parts[name] = sha256.Sum256(added[name])
		result.Changed[name] = true
	}
	if err := archive.Close(); err != nil {
		return Receipt{}, err
	}
	return result, nil
}

// Verify checks the exact output part set and reads every part through EOF
// for CRC verification. Preserved parts must match their compressed source
// bytes. Changed parts must match the content produced by the edit plan.
func (r Receipt) Verify(ctx context.Context, archive *zip.Reader) error {
	if len(archive.File) != len(r.Parts) {
		return fmt.Errorf("输出部件数量与收据不符")
	}
	seen := make(map[string]bool, len(r.Parts))
	for _, part := range archive.File {
		want, ok := r.Parts[part.Name]
		if !ok || seen[part.Name] {
			return fmt.Errorf("输出部件与收据不符：%s", part.Name)
		}
		seen[part.Name] = true
		stream, err := part.Open()
		if err != nil {
			return err
		}
		hash := sha256.New()
		var dst io.Writer = io.Discard
		if r.Changed[part.Name] {
			dst = hash
		}
		_, err = io.Copy(dst, cancelReader{ctx, stream})
		stream.Close()
		if err != nil {
			return err
		}
		if !r.Changed[part.Name] {
			raw, err := part.OpenRaw()
			if err != nil {
				return err
			}
			if _, err = io.Copy(hash, cancelReader{ctx, raw}); err != nil {
				return err
			}
		}
		var digest [32]byte
		hash.Sum(digest[:0])
		if digest != want {
			return fmt.Errorf("部件核对失败：%s", part.Name)
		}
	}
	return nil
}
