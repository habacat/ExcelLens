ExcelLens is a read-only local data viewer built with Go and a native web frontend. It has no CGO, Office, Oracle-client, or external archive-program runtime requirement.

ExcelLens source code is licensed under the MIT License; see LICENSE. Third-party dependencies retain their own licenses as listed in THIRD_PARTY_NOTICES.txt.

The workbook readers implement read-only subsets of OOXML, BIFF8 XLS, and Office/WPS HTML tables, plus CSV/TSV/Oracle text and SQLite. They do not execute Microsoft Excel, WPS, VBA, source formulas, or external resources. External rows are indexed in an independent binary cache; source data is never rewritten.

SQLite uses Windows' built-in winsqlite3.dll. GB18030 uses Windows APIs on Windows. Native archive and portable text decoding use pinned pure-Go dependencies, including github.com/bodgit/sevenzip, github.com/ulikunitz/xz, golang.org/x/text and their transitive dependencies. All vendored license notices and exact versions are included in THIRD_PARTY_NOTICES.txt and vendor/modules.txt in the source distribution.

The default build uses separately installed UPX with --best --lzma and verifies the packed executable before publishing its checksums. UPX is a build tool and is not required to run ExcelLens.

See docs/user/formats.md, docs/user/calculator.md and docs/releases/1.8.19.md for behavior and limits. Calculation supports numeric, text and logical formulas; monetary arithmetic uses exact rationals. OOXML, BIFF and HTML share source dimensions and gridline display. Formula rows and notes remain in the current page and never modify the source workbook. Prior local history is retained. The supported formula/layout subset is not the complete Excel/WPS engine. Headless browser checks do not establish native desktop, WPS or tray acceptance.
