package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"excellens/internal/checkpoint"
	"excellens/internal/editdoc"
	"excellens/internal/editfile"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

type editArtifact struct {
	editfile.Artifact
	DownloadURL string `json:"downloadUrl"`
}

func (a *App) editTicket(doc, key string) string {
	h := hmac.New(sha256.New, []byte(a.rootApp().mediaTicket))
	_, _ = io.WriteString(h, "editing\x00"+doc+"\x00"+key)
	return hex.EncodeToString(h.Sum(nil))
}

func (a *App) editArtifact(doc string, output editfile.Artifact) editArtifact {
	q := url.Values{"document": {doc}, "key": {output.Key}, "ticket": {a.editTicket(doc, output.Key)}}
	return editArtifact{Artifact: output, DownloadURL: "/api/edits/download?" + q.Encode()}
}

func (a *App) editSave(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, req editRequest) (editArtifact, error) {
	format := doc.Base.Source.Format
	key, err := editfile.CopyKey(req.Revision, format, req.Options)
	if err != nil {
		return editArtifact{}, err
	}
	outputs, err := openEditOutputs()
	if err != nil {
		return editArtifact{}, err
	}
	defer outputs.Close()
	existing, item, err := outputs.Open(ctx, doc.ID, key)
	if err == nil {
		_ = existing.Close()
		return a.editArtifact(doc.ID, item), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return editArtifact{}, err
	}
	if req.Revision != doc.Revision {
		return editArtifact{}, checkpoint.ErrConflict
	}
	f, err := openEditBase(ctx, doc)
	if err != nil {
		return editArtifact{}, err
	}
	defer f.Close()
	layout, err = a.edits.documentEditLayout(ctx, doc, layout, f)
	if err != nil {
		return editArtifact{}, err
	}
	item = editfile.Artifact{Key: key, Revision: doc.Revision, Format: format, Name: fmt.Sprintf("%s-副本-r%d.%s", safeExportBase(doc.Name), doc.Revision, format)}
	values := doc.Snapshot()
	item, err = outputs.Publish(ctx, doc.ID, item, func(dst io.Writer, receipt *editfile.Artifact) (func(*os.File) error, error) {
		return layout.Write(ctx, dst, f, doc.Base.Source.Stamp, values, doc.RuleChanges(), doc.ColumnFormulas(), req.Options, receipt)
	}, func() error { return editfile.VerifySource(ctx, f, doc.Base.Source.Stamp) })
	return a.editArtifact(doc.ID, item), err
}

func (a *App) handleEditDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	query := r.URL.Query()
	doc, key, ticket := query.Get("document"), query.Get("key"), query.Get("ticket")
	if !editdoc.ValidRequestID(doc) || !hmac.Equal([]byte(a.editTicket(doc, key)), []byte(ticket)) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	outputs, err := openEditOutputs()
	if err != nil {
		editError(w, err)
		return
	}
	defer outputs.Close()
	f, artifact, err := outputs.Open(r.Context(), doc, key)
	if err != nil {
		editError(w, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Disposition", `attachment; filename="ExcelLens-copy.`+artifact.Format+`"; filename*=UTF-8''`+url.PathEscape(artifact.Name))
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, artifact.Name, time.Time{}, f)
}

func (a *App) handleEditOpen(w http.ResponseWriter, r *http.Request, req editRequest) {
	expected := a.currentWorkbook()
	if req.Book != sourceBookID(expected) {
		editError(w, checkpoint.ErrConflict)
		return
	}
	outputs, err := openEditOutputs()
	if err != nil {
		editError(w, err)
		return
	}
	defer outputs.Close()
	f, artifact, err := outputs.Open(r.Context(), req.Document.ID, req.Artifact)
	if err != nil {
		editError(w, err)
		return
	}
	defer f.Close()
	wb, release, err := a.openPinnedWorkbook(outputs.Path(req.Document.ID, artifact))
	if err != nil {
		editError(w, err)
		return
	}
	defer release()
	if artifact.Delimiter != 0 {
		delimiter, _ := wb.EditDialect()
		if delimiter != artifact.Delimiter {
			editError(w, errors.New("副本已生成，当前自动识别的分隔符与副本不一致"))
			return
		}
	}
	wb.SetDisplayName(artifact.Name)
	if err := r.Context().Err(); err != nil {
		editError(w, err)
		return
	}
	if !a.installWorkbook(wb, expected, true) {
		editError(w, checkpoint.ErrConflict)
		return
	}
	a.closeArchive()
	a.recordRecent(wb)
	writeJSON(w, map[string]any{"opened": true, "workbookKey": wb.Fingerprint, "book": sourceBookID(wb), "fileName": artifact.Name, "fileSize": artifact.Bytes, "sheetCount": len(wb.Sheets), "activeTab": wb.ActiveTab, "sheets": wb.Sheets, "source": wb.SourceInfo})
}
