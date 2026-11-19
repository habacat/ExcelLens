package editfile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
)

var ErrArtifact = errors.New("生成的副本不完整或内容已变化")

type Artifact struct {
	Version              int    `json:"version"`
	Key                  string `json:"key"`
	Revision             uint64 `json:"revision,string"`
	Format               string `json:"format"`
	Name                 string `json:"name"`
	Bytes                int64  `json:"bytes"`
	SHA256               string `json:"sha256"`
	Encoding             string `json:"encoding,omitempty"`
	Delimiter            byte   `json:"delimiter,omitempty"`
	Protected            int64  `json:"protected"`
	ChangedParts         int    `json:"changedParts,omitempty"`
	FormulasPending      int    `json:"formulasPending,omitempty"`
	NativeRecalc         bool   `json:"nativeRecalc,omitempty"`
	PivotCachesPending   int    `json:"pivotCachesPending,omitempty"`
	PivotCachesAfterCalc int    `json:"pivotCachesAfterCalc,omitempty"`
}

func CopyKey(revision uint64, format string, options TextOptions) (string, error) {
	if revision == 0 {
		return "", ErrArtifact
	}
	key := strconv.FormatUint(revision, 10)
	if formats.EditOOXML(format) {
		if options != (TextOptions{}) {
			return "", ErrArtifact
		}
		return key + "-" + format, nil
	}
	if format != "csv" && format != "tsv" {
		return "", ErrArtifact
	}
	switch options.Encoding {
	case "source", "utf8", "utf8-bom":
	default:
		return "", ErrArtifact
	}
	mode := "exact"
	if options.Protect {
		mode = "protected"
	}
	return key + "-" + options.Encoding + "-" + mode, nil
}

func validKey(key string) bool {
	n, variant, ok := strings.Cut(key, "-")
	rev, err := strconv.ParseUint(n, 10, 64)
	if !ok || err != nil || rev == 0 || strconv.FormatUint(rev, 10) != n {
		return false
	}
	switch variant {
	case "xlsx", "xlsm", "source-exact", "source-protected", "utf8-exact", "utf8-protected", "utf8-bom-exact", "utf8-bom-protected":
		return true
	}
	return false
}

type Outputs struct{ root *os.Root }

func OpenOutputs(path string) (*Outputs, error) {
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrArtifact
	}
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &Outputs{root: r}, nil
}

func (o *Outputs) Close() error { return o.root.Close() }

func (o *Outputs) receipt(doc, key string) (Artifact, error) {
	var a Artifact
	if !editdoc.ValidRequestID(doc) || !validKey(key) {
		return a, ErrArtifact
	}
	file, err := o.regular(filepath.Join(doc, key, "receipt.json"))
	if err != nil {
		return a, err
	}
	defer file.Close()
	d := json.NewDecoder(io.LimitReader(file, 8<<10))
	d.DisallowUnknownFields()
	if d.Decode(&a) != nil || d.Decode(new(json.RawMessage)) != io.EOF || a.Version != 1 || a.Key != key || a.Revision == 0 || !strings.HasPrefix(key, strconv.FormatUint(a.Revision, 10)+"-") || a.Bytes <= 0 || a.Bytes > formats.EditOutputBytes ||
		formats.EditExtension(filepath.Ext(a.Name)) != a.Format || a.Format == "" || strings.ContainsAny(a.Name, "/\\\r\n") || len(a.SHA256) != 64 {
		return Artifact{}, ErrArtifact
	}
	return a, nil
}

func (o *Outputs) regular(path string) (*os.File, error) {
	info, err := o.root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrArtifact
	}
	f, err := o.root.Open(path)
	if err != nil {
		return nil, err
	}
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) {
		_ = f.Close()
		return nil, ErrArtifact
	}
	return f, nil
}

func (o *Outputs) List(doc string) ([]Artifact, error) {
	if !editdoc.ValidRequestID(doc) {
		return nil, ErrArtifact
	}
	dir, err := o.root.Open(doc)
	if errors.Is(err, os.ErrNotExist) {
		return []Artifact{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(formats.EditOutputVersions + 2)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > formats.EditOutputVersions+1 {
		return nil, ErrArtifact
	}
	items := make([]Artifact, 0, len(entries))
	for _, e := range entries {
		if e.Name() == ".staging" {
			continue
		}
		if !e.IsDir() || !validKey(e.Name()) {
			return nil, ErrArtifact
		}
		a, err := o.receipt(doc, e.Name())
		if err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return items, nil
}

func (o *Outputs) Open(ctx context.Context, doc, key string) (*os.File, Artifact, error) {
	a, err := o.receipt(doc, key)
	if err != nil {
		return nil, a, err
	}
	f, err := o.regular(filepath.Join(doc, key, "copy."+a.Format))
	if err != nil {
		return nil, a, err
	}
	proof, count, err := fileProof(ctx, f)
	if err == nil && (proof != a.SHA256 || count != a.Bytes) {
		err = ErrArtifact
	}
	if err != nil {
		_ = f.Close()
		return nil, a, err
	}
	return f, a, nil
}

func (o *Outputs) Path(doc string, a Artifact) string {
	return filepath.Join(o.root.Name(), doc, a.Key, "copy."+a.Format)
}

func fileProof(ctx context.Context, file *os.File) (string, int64, error) {
	info, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if info.Size() <= 0 || info.Size() > formats.EditOutputBytes {
		return "", 0, ErrArtifact
	}
	h := sha256.New()
	r := io.NewSectionReader(file, 0, info.Size())
	buf := make([]byte, 256<<10)
	var count int64
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		n, err := r.Read(buf)
		count += int64(n)
		_, _ = h.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", 0, err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), count, nil
}

type boundedCopy struct {
	ctx   context.Context
	file  *os.File
	bytes int64
}

func (w *boundedCopy) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > formats.EditOutputBytes-w.bytes {
		return 0, fmt.Errorf("副本超过 %d MB 上限", formats.EditOutputBytes>>20)
	}
	n, err := w.file.Write(p)
	w.bytes += int64(n)
	return n, err
}

// Publish is called with the document's cross-process lease held. It keeps
// the receipt and its verified file together, even if the HTTP response is
// lost. Only an unconfirmed, app-owned staging directory can be reclaimed.
func (o *Outputs) Publish(ctx context.Context, doc string, a Artifact, produce func(io.Writer, *Artifact) (func(*os.File) error, error), checkSource func() error) (Artifact, error) {
	if !editdoc.ValidRequestID(doc) || !validKey(a.Key) || formats.EditExtension("."+a.Format) != a.Format || a.Format == "" {
		return Artifact{}, ErrArtifact
	}
	items, err := o.List(doc)
	if err != nil {
		return Artifact{}, err
	}
	if len(items) >= formats.EditOutputVersions {
		return Artifact{}, errors.New("该编辑副本的已生成版本已达上限")
	}
	if err := o.root.Mkdir(doc, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return Artifact{}, err
	}
	info, err := o.root.Lstat(doc)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Artifact{}, ErrArtifact
	}
	stage := filepath.Join(doc, ".staging")
	if err := o.root.RemoveAll(stage); err != nil {
		return Artifact{}, err
	}
	if err := o.root.Mkdir(stage, 0700); err != nil {
		return Artifact{}, err
	}
	defer o.root.RemoveAll(stage)
	name := filepath.Join(stage, "copy."+a.Format)
	f, err := o.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return Artifact{}, err
	}
	bounded := &boundedCopy{ctx: ctx, file: f}
	verify, err := produce(bounded, &a)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return Artifact{}, err
	}
	f, err = o.regular(name)
	if err != nil {
		return Artifact{}, err
	}
	if verify == nil {
		_ = f.Close()
		return Artifact{}, ErrArtifact
	}
	err = verify(f)
	if err == nil {
		a.SHA256, a.Bytes, err = fileProof(ctx, f)
	}
	closeErr = f.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return Artifact{}, err
	}
	a.Version = 1
	receipt, err := o.root.OpenFile(filepath.Join(stage, "receipt.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return Artifact{}, err
	}
	err = json.NewEncoder(receipt).Encode(a)
	if err == nil {
		err = receipt.Sync()
	}
	closeErr = receipt.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return Artifact{}, err
	}
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	if err := checkSource(); err != nil {
		return Artifact{}, err
	}
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	final := filepath.Join(doc, a.Key)
	if _, err := o.root.Lstat(final); !errors.Is(err, os.ErrNotExist) {
		return Artifact{}, ErrArtifact
	}
	if err := o.root.Rename(stage, final); err != nil {
		return Artifact{}, err
	}
	return a, nil
}
