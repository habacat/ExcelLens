package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"excellens/internal/checkpoint"
	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

// Checkpoints and undo blocks remain immutable, including their original book
// identifiers. Each confirmation records the previous physical location.
type sourceMove struct {
	From string    `json:"from"`
	To   string    `json:"to"`
	At   time.Time `json:"at"`
}
type sourceLocation struct {
	Original string       `json:"original"`
	SHA256   string       `json:"sha256"`
	Moves    []sourceMove `json:"moves"`
}

const locationLimit = 1 << 20

func loadSourceLocation(source checkpoint.Source) (sourceLocation, error) {
	var loc sourceLocation
	if !validExportDigest(source.Book) {
		return loc, checkpoint.ErrBinding
	}
	file, err := savedConfigPath("source-locations", source.Book)
	if err != nil {
		return loc, err
	}
	found, err := readSavedConfig(file, locationLimit, &loc)
	if !found || err != nil {
		return loc, err
	}
	if cachePathKey(loc.Original) != cachePathKey(source.Path) || !validExportDigest(loc.SHA256) || len(loc.Moves) == 0 {
		return loc, checkpoint.ErrCorrupt
	}
	previous := loc.Original
	for _, m := range loc.Moves {
		if cachePathKey(m.From) != cachePathKey(previous) || !filepath.IsAbs(m.To) || m.At.IsZero() {
			return loc, checkpoint.ErrCorrupt
		}
		previous = m.To
	}
	return loc, nil
}
func (loc sourceLocation) current(original string) string {
	if len(loc.Moves) > 0 {
		return loc.Moves[len(loc.Moves)-1].To
	}
	return original
}
func locatedSource(source checkpoint.Source) (checkpoint.Source, error) {
	loc, err := loadSourceLocation(source)
	if err != nil {
		return source, err
	}
	if len(loc.Moves) > 0 {
		if source.SHA256 != "" && source.SHA256 != loc.SHA256 {
			return source, checkpoint.ErrBinding
		}
		source.Path = loc.current(source.Path)
		source.Book = bookIDAt(source.Fingerprint, source.Path)
	} else if filepath.Ext(source.Path) == formats.DatabaseSnapshotExtension &&
		filepath.Base(filepath.Dir(source.Path)) == source.Fingerprint {
		// A retained snapshot's manifest and parts migrate together under the
		// same digest. Rebase its managed path after importing a backup.
		if candidate := workbook.DatabaseSnapshotPath(source.Fingerprint); candidate != "" {
			if st, err := os.Lstat(candidate); err == nil && st.Mode().IsRegular() {
				source.Path = candidate
				source.Book = bookIDAt(source.Fingerprint, candidate)
			}
		}
	}
	return source, nil
}

// Structural snapshots belong to a specific edit document and travel inside
// the backup. Use only the exact stored document ID and its own file name.
func editLocation(id string, base editdoc.Base) (checkpoint.Source, error) {
	s := base.Source
	dir := filepath.Dir(s.Path)
	if editdoc.ValidRequestID(id) && filepath.Base(dir) == id && filepath.Base(filepath.Dir(dir)) == "edit-revisions" {
		root, err := editDirectory("edit-revisions")
		if err != nil {
			return s, err
		}
		s.Path = filepath.Join(root, id, filepath.Base(s.Path))
		s.Book = bookIDAt(s.Fingerprint, s.Path)
		return s, nil
	}
	return locatedSource(s)
}
func editViewBook(id string, base editdoc.Base) string {
	s, err := editLocation(id, base)
	if err != nil {
		return ""
	}
	return s.Book
}

type relocationItem struct {
	Busy      bool         `json:"busy,omitempty"`
	Book      string       `json:"book"`
	Original  string       `json:"original"`
	Path      string       `json:"path"`
	Candidate string       `json:"candidate,omitempty"`
	Moves     []sourceMove `json:"moves,omitempty"`
	source    checkpoint.Source
}

func relocationSources(ctx context.Context) ([]relocationItem, error) {
	busy := map[string]bool{}
	byBook := map[string]checkpoint.Source{}
	// Edit journals also supply the raw SHA-256 for legacy tasks on that source.
	for _, kind := range []string{"task-checkpoints", "edit-documents"} {
		dir, err := editDirectory(kind)
		if err != nil {
			return nil, err
		}
		if _, err = os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			continue
		}
		store, err := checkpoint.Open(dir)
		if err != nil {
			return nil, err
		}
		entries, err := listTaskCheckpoints(ctx, store)
		store.Close()
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Error != "" || e.Backup || e.Manifest == nil {
				continue
			}
			for _, s := range e.Manifest.Definition.Sources {
				if e.Busy {
					busy[s.Book] = true
				}
				if kind == "task-checkpoints" && s.SHA256 == "" {
					proof, e := readTaskProof(*e.Manifest)
					if e != nil {
						return nil, e
					}
					s.SHA256 = proof.SHA256
				}
				if kind == "edit-documents" {
					s.SHA256 = s.Stamp
				}
				if old, ok := byBook[s.Book]; !ok || old.SHA256 == "" {
					byBook[s.Book] = s
				}
			}
		}
	}
	out := make([]relocationItem, 0, len(byBook))
	for _, s := range byBook {
		loc, err := loadSourceLocation(s)
		if err != nil {
			return nil, err
		}
		path := loc.current(s.Path)
		item := relocationItem{Book: s.Book, Original: s.Path, Path: path, Moves: loc.Moves, source: s, Busy: busy[s.Book]}
		name := filepath.Base(path)
		if strings.HasPrefix(strings.ToLower(name), "excellens-upload-") {
			// The checkpoint path on Windows may have been folded to lower case.
			candidate := filepath.Join(workbook.UploadRoot(), name)
			if cachePathKey(path) != cachePathKey(candidate) {
				if st, err := os.Stat(candidate); err == nil && st.Mode().IsRegular() {
					item.Candidate = candidate
				}
			}
		}
		out = append(out, item)
	}
	slices.SortFunc(out, func(a, b relocationItem) int { return strings.Compare(a.Original, b.Original) })
	return out, nil
}

func (a *App) verifyLocation(ctx context.Context, book, previous, path string, confirm bool) (relocationItem, error) {
	return a.verifyLocationWith(ctx, book, previous, path, confirm, nil)
}
func (a *App) verifyLocationWith(ctx context.Context, book, previous, path string, confirm bool, beginCommit func() error) (relocationItem, error) {
	var empty relocationItem
	if !validExportDigest(book) || !filepath.IsAbs(path) {
		return empty, i18n.Error("service.sourceLocation.selectSourceRecordsProvideFull")
	}
	entries, err := relocationSources(ctx)
	if err != nil {
		return empty, err
	}
	for _, item := range entries {
		if item.Book != book {
			continue
		}
		if item.Busy && confirm {
			return empty, checkpoint.ErrBusy
		}
		if cachePathKey(previous) != cachePathKey(item.Path) {
			return empty, checkpoint.ErrConflict
		}
		wb, release, err := a.openPinnedWorkbook(path)
		if err != nil {
			return empty, err
		}
		defer release()
		s := item.source
		if wb.Fingerprint != s.Fingerprint {
			return empty, i18n.Error("service.sourceLocation.contentFingerprintNewFileDoes")
		}
		digest, err := wb.SourceFileSHA256(ctx)
		if err != nil {
			return empty, err
		}
		loc, err := loadSourceLocation(s)
		if err != nil {
			return empty, err
		}
		proof := s.SHA256
		if proof == "" {
			proof = loc.SHA256
		}
		if proof == "" {
			// Old OOXML identities cover ZIP metadata; they cannot prove byte equality.
			// Legacy external adapters with SHA-256 domains already prove the source.
			if len(s.Fingerprint) != 64 {
				old, free, err := a.openPinnedWorkbook(item.Path)
				if err != nil {
					return empty, i18n.Error("service.sourceLocation.oldTaskLacksEntireFile")
				}
				defer free()
				if old.Fingerprint != s.Fingerprint {
					return empty, checkpoint.ErrBinding
				}
				proof, err = old.SourceFileSHA256(ctx)
				if err != nil {
					return empty, err
				}
			} else {
				proof = digest
			}
		}
		if digest != proof {
			return empty, i18n.Error("service.sourceLocation.shaHashNewFileDoes")
		}
		item.Candidate = wb.Path
		if !confirm {
			return item, nil
		}
		// Only the binding update excludes workbook requests. Reading and hashing
		// the candidate above does not block unrelated tabs.
		a.cacheResetMu.Lock()
		defer a.cacheResetMu.Unlock()
		latest, err := relocationSources(ctx)
		if err != nil {
			return empty, err
		}
		i := slices.IndexFunc(latest, func(x relocationItem) bool { return x.Book == book })
		if i < 0 || cachePathKey(latest[i].Path) != cachePathKey(previous) {
			return empty, checkpoint.ErrConflict
		}
		if latest[i].Busy {
			return empty, checkpoint.ErrBusy
		}
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		if cachePathKey(wb.Path) == cachePathKey(item.Path) {
			return item, nil
		}
		if len(loc.Moves) == 0 {
			loc.Original = s.Path
			loc.SHA256 = digest
		}
		loc.Moves = append(loc.Moves, sourceMove{From: item.Path, To: wb.Path, At: time.Now().UTC()})
		file, err := savedConfigPath("source-locations", book)
		if err != nil {
			return empty, err
		}
		if beginCommit != nil {
			if err := beginCommit(); err != nil {
				return empty, err
			}
		}
		if err = writeSavedConfig(file, locationLimit, loc, durableConfigOps); err != nil {
			return empty, err
		}
		item.Path, item.Moves = wb.Path, loc.Moves
		return item, nil
	}
	return empty, os.ErrNotExist
}

// A restored upload must also survive cleanup before the user confirms its path.
func restoredUploadCandidate(path string) string {
	name := filepath.Base(path)
	if strings.HasPrefix(strings.ToLower(name), "excellens-upload-") {
		return filepath.Join(workbook.UploadRoot(), name)
	}
	return ""
}
