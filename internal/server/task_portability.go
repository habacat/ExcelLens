package server

import (
	"context"

	"os"

	"excellens/internal/checkpoint"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

// Old Windows checkpoints identify cache files by NTFS revision. On the source
// computer an explicit backup can attest the portable digest of that exact index.
type taskProof struct {
	Binding string            `json:"binding"`
	SHA256  string            `json:"sha256"`
	Indexes map[string]string `json:"indexes"`
}

func readTaskProof(m checkpoint.Manifest) (taskProof, error) {
	var p taskProof
	path, err := savedConfigPath("task-portability", m.ID)
	if err != nil {
		return p, err
	}
	ok, err := readSavedConfig(path, locationLimit, &p)
	if !ok || err != nil {
		return p, err
	}
	if p.Binding != m.DefinitionHash || !validExportDigest(p.SHA256) {
		return p, checkpoint.ErrCorrupt
	}
	for old, content := range p.Indexes {
		if !validExportDigest(old) || !validExportDigest(content) {
			return p, checkpoint.ErrCorrupt
		}
	}
	return p, nil
}
func (a *App) prepareLegacyTasks(ctx context.Context) (int, error) {
	store, err := openTaskCheckpoints()
	if err != nil {
		return 0, err
	}
	defer store.Close()
	entries, err := listTaskCheckpoints(ctx, store)
	if err != nil {
		return 0, err
	}
	missing := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return missing, err
		}
		m := entry.Manifest
		if m == nil || entry.Backup || len(m.Definition.Sources) != 1 || m.Definition.Sources[0].SHA256 != "" {
			continue
		}
		if err := a.proveLegacyTask(ctx, *m); err != nil {
			if ctx.Err() != nil {
				return missing, ctx.Err()
			}
			// Offline sources and missing old indexes do not prevent backing up work.
			missing++
		}
	}
	return missing, nil
}
func (a *App) proveLegacyTask(ctx context.Context, m checkpoint.Manifest) error {
	s := m.Definition.Sources[0]
	proof, err := readTaskProof(m)
	if err != nil {
		return err
	}
	wanted := map[string]string{}
	switch m.Definition.Driver {
	case searchCheckpointDriver:
		spec, err := searchCheckpointSpecFor(m)
		if err != nil {
			return err
		}
		var cursor searchTaskCursor
		if decodeSavedConfig(m.Cursor, &cursor) != nil || cursor.Sheet < 0 || cursor.Sheet >= len(spec.Sheets) {
			return checkpoint.ErrCorrupt
		}
		if cursor.Index != "" {
			wanted[spec.Sheets[cursor.Sheet].Path] = cursor.Index
		}
	case filterCheckpointDriver:
		spec, err := filterCheckpointSpecFor(m)
		if err != nil {
			return err
		}
		cursor, err := filterCheckpointCursor(m, spec)
		if err != nil {
			return err
		}
		if cursor.Index != "" {
			wanted[spec.SheetPath] = cursor.Index
		}
	case exportCheckpointDriver:
		spec, err := exportCheckpointSpecFor(m)
		if err != nil {
			return err
		}
		for _, sheet := range spec.Sheets {
			wanted[sheet.Path] = sheet.Index
		}
	default:
		return checkpoint.ErrBinding
	}
	if proof.SHA256 != "" {
		complete := true
		for _, stamp := range wanted {
			if proof.Indexes[stamp] == "" {
				complete = false
			}
		}
		if complete {
			return nil
		}
	}
	located, err := locatedSource(s)
	if err != nil {
		return err
	}
	wb, release, err := a.openPinnedWorkbook(located.Path)
	if err != nil {
		return err
	}
	defer release()
	if m.Definition.FileName != "" {
		wb.SetDisplayName(m.Definition.FileName)
	}
	if wb.Fingerprint != s.Fingerprint {
		return checkpoint.ErrBinding
	}
	digest, err := wb.SourceFileSHA256(ctx)
	if err != nil {
		return err
	}
	if proof.SHA256 != "" && proof.SHA256 != digest {
		return checkpoint.ErrBinding
	}
	proof.Binding, proof.SHA256 = m.DefinitionHash, digest
	if proof.Indexes == nil {
		proof.Indexes = map[string]string{}
	}
	var missing bool
	for _, meta := range wb.Sheets {
		stamp := wanted[meta.Path]
		if stamp == "" || proof.Indexes[stamp] != "" {
			continue
		}
		idx := wb.GetCachedIndex(meta.Path)
		if idx == nil {
			missing = true
			continue
		}
		actual, err := wb.SourceIndexStamp(ctx, meta, idx)
		if err != nil || actual != stamp {
			missing = true
			continue
		}
		portable, err := wb.CheckpointIndexStamp(ctx, meta, idx, true)
		if err != nil {
			return err
		}
		proof.Indexes[stamp] = portable
	}
	path, err := savedConfigPath("task-portability", m.ID)
	if err != nil {
		return err
	}
	if err = writeSavedConfig(path, locationLimit, proof, durableConfigOps); err != nil {
		return err
	}
	if missing {
		return i18n.Wrap("service.taskPortability.oldIndexesCannotBeVerified", os.ErrNotExist)
	}
	return nil
}

type taskIndexes struct {
	portable, rebuild bool
	prior             map[string]string
}

func indexPolicy(wb *workbook.Workbook, m checkpoint.Manifest) (taskIndexes, error) {
	s := m.Definition.Sources[0]
	moved := sourceBookID(wb) != s.Book
	if s.SHA256 != "" {
		return taskIndexes{portable: true, rebuild: moved}, nil
	}
	if !moved {
		return taskIndexes{}, nil
	}
	proof, err := readTaskProof(m)
	if err != nil {
		return taskIndexes{}, err
	}
	// Records created before a legacy proof may still have a revision-bound
	// cursor. Never adopt that cursor just because the source file matches.
	return taskIndexes{portable: proof.SHA256 != "", rebuild: proof.SHA256 != "", prior: proof.Indexes}, nil
}
func (p taskIndexes) expected(stamp string) string {
	if converted := p.prior[stamp]; converted != "" {
		return converted
	}
	return stamp
}
