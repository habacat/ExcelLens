package export

import (
	"archive/zip"
	"context"
	"fmt"
	"io"

	"excellens/internal/workbook"
)

// AssembleResume copies only complete acknowledged records/parts. A caller
// uses an atomic private file and rechecks source binding before publication.
// ZIP central directories are newly built; no incomplete archive is resumed.
func AssembleResume(ctx context.Context, dst io.Writer, wb *workbook.Workbook, selections []Selection, opts Options, journal ResumeJournal) error {
	if err := Validate(opts, selections); err != nil {
		return err
	}
	if state := journal.State(); ValidateResumeState(state, opts, selections) != nil || state.Phase != "done" {
		return ErrResumeState
	}
	if opts.Format != "xlsx" && opts.Format != "zipcsv" {
		return journal.Visit(ctx, func(_ ResumePart, source io.Reader) error {
			_, err := io.CopyBuffer(dst, source, make([]byte, 64<<10))
			return err
		})
	}
	zw := zip.NewWriter(dst)
	names := csvArchiveNames(selections)
	var theme []byte
	if opts.Format == "xlsx" {
		prepared, err := prepareXLSX(ctx, wb, selections, opts)
		if err != nil {
			return err
		}
		theme = prepared.theme
		if err := writeXLSXMetadata(zw, wb, selections, theme); err != nil {
			return err
		}
	}
	count := 0
	if err := journal.Visit(ctx, func(part ResumePart, source io.Reader) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if part.Before.Sheet != count || count >= len(selections) {
			return ErrResumeState
		}
		name := names[count]
		if opts.Format == "xlsx" {
			name = fmt.Sprintf("xl/worksheets/sheet%d.xml", count+1)
		}
		file, err := zw.Create(name)
		if err != nil {
			return err
		}
		if opts.Format == "zipcsv" {
			_, err = io.Copy(file, source)
			count++
			return err
		}
		if _, err := io.Copy(file, NewPartReader(source)); err != nil {
			return err
		}
		var styles io.Writer = io.Discard
		if count == len(selections)-1 {
			styles, err = zw.Create("xl/styles.xml")
			if err != nil {
				return err
			}
		}
		if _, err := io.Copy(styles, NewPartReader(source)); err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, NewPartReader(source)); err != nil {
			return err
		}
		count++
		return requirePartEOF(source)
	}); err != nil {
		return err
	}
	if count != len(selections) {
		return ErrResumeState
	}
	if opts.Format == "xlsx" {
		if err := writeXLSXTheme(zw, theme); err != nil {
			return err
		}
	} else {
		manifest, err := csvArchiveManifest(journal.SourceName(), wb.Fingerprint, selections, opts, names, journal.Created())
		if err != nil {
			return err
		}
		f, err := zw.Create("_ExcelLens_manifest.json")
		if err != nil {
			return err
		}
		if _, err := f.Write(manifest); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return zw.Close()
}
