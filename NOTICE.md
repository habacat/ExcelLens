ExcelLens is a local data viewer and editing-copy tool built with Go and a native web frontend. The Windows application runs without CGO, Office, an Oracle client, or an external archive program.

ExcelLens source code is licensed under the MIT License; see LICENSE. Third-party dependencies retain their own licenses as listed in THIRD_PARTY_NOTICES.txt.

The workbook readers support subsets of OOXML, BIFF12 XLSB, BIFF8 XLS, Office/WPS HTML tables, CSV/TSV/Oracle text and SQLite. Online SQL tables are read through bundled Go drivers into local snapshots. External rows are indexed in an independent binary cache. In an explicit editing copy, the Go calculation engine evaluates supported formulas and writes confirmed changes to a separate file. Original source files remain read-only.

SQLite uses Windows' built-in winsqlite3.dll. GB18030 uses Windows APIs on Windows. Native archive and portable text decoding use pinned pure-Go dependencies, including github.com/bodgit/sevenzip, github.com/nwaples/rardecode/v2, github.com/ulikunitz/xz, golang.org/x/text and their transitive dependencies. All vendored license notices and exact versions are included in THIRD_PARTY_NOTICES.txt and vendor/modules.txt in the source distribution.

The default build uses separately installed UPX with --best --lzma and verifies the packed executable before publishing its checksums. UPX is a build tool and is not required to run ExcelLens.

See docs/user/formats.md, docs/user/calculator.md and docs/releases/1.8.19.md for behavior and limits. Calculation supports numeric, text and logical formulas; monetary arithmetic uses exact rationals. OOXML, BIFF and HTML share source dimensions and gridline display. Formula rows and notes remain in the current page and never modify the source workbook. Prior local history is retained. The supported formula/layout subset is not the complete Excel/WPS engine. Headless browser checks do not establish native desktop, WPS or tray acceptance.
