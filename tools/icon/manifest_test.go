package main

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"excellens/internal/buildinfo"
)

func manifestFixture(t *testing.T) (source, manifest []byte) {
	t.Helper()
	source, err := os.ReadFile("../../cmd/excellens/app.manifest")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err = renderManifest(source)
	if err != nil {
		t.Fatal(err)
	}
	return source, manifest
}

func TestManifestRejectsDPIOrNativeDialogRegression(t *testing.T) {
	template, valid := manifestFixture(t)
	if bytes.Contains(valid, []byte("__APP_VERSION__")) || !bytes.Contains(valid, []byte(`version="`+buildinfo.Version+`.0"`)) {
		t.Fatal("manifest did not use the maintained application version")
	}
	for _, change := range [][2]string{
		{"PerMonitorV2,PerMonitor", "System"},
		{"true/pm", "false"},
		{"http://schemas.microsoft.com/SMI/2016/WindowsSettings", "urn:wrong-dpi-namespace"},
		{"__APP_VERSION__.0", "0.0.0.0"},
		{"Microsoft.Windows.Common-Controls", "Other.Controls"},
		{"6.0.0.0", "5.0.0.0"},
	} {
		t.Run(change[1], func(t *testing.T) {
			if _, err := renderManifest(bytes.ReplaceAll(template, []byte(change[0]), []byte(change[1]))); err == nil {
				t.Fatal("accepted a manifest that would lose DPI awareness, native controls or the current version")
			}
		})
	}
}

func TestIconTreeKeepsAllImagesAndEmbedsNeutralManifest(t *testing.T) {
	_, manifest := manifestFixture(t)
	ico, err := os.ReadFile("../../internal/branding/icon.ico")
	if err != nil {
		t.Fatal(err)
	}
	icons, err := parseICO(ico)
	if err != nil {
		t.Fatal(err)
	}
	resources, relocs, err := makeResource(ico, manifest)
	if err != nil {
		t.Fatal(err)
	}
	read := func(ids ...uint32) []byte {
		t.Helper()
		rva, size, err := resourceEntry(resources, ids...)
		if err != nil || uint64(rva)+uint64(size) > uint64(len(resources)) {
			t.Fatalf("invalid resource %v: %v", ids, err)
		}
		return resources[rva : rva+size]
	}
	if got := read(24, 1, 0); !bytes.Equal(got, manifest) {
		t.Fatal("loader-visible RT_MANIFEST #1 does not match the source")
	}
	group := read(14, 1, 1033)
	if binary.LittleEndian.Uint16(group[4:]) != uint16(len(icons)) || len(relocs) != len(icons)+2 {
		t.Fatal("icon count or resource relocations changed")
	}
	for i, icon := range icons {
		if !bytes.Equal(read(3, uint32(i+1), 1033), icon.image) {
			t.Fatalf("icon image %d changed", i+1)
		}
		if binary.LittleEndian.Uint16(group[6+i*14+12:]) != uint16(i+1) {
			t.Fatal("application icon group references the wrong image")
		}
	}
	// Check the COFF container through Go's independent object reader too.
	object := filepath.Join(t.TempDir(), "icons.syso")
	if err := writeCOFF(object, resources, relocs); err != nil {
		t.Fatal(err)
	}
	file, err := pe.Open(object)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if file.Machine != pe.IMAGE_FILE_MACHINE_AMD64 || len(file.Sections) != 1 || len(file.Sections[0].Relocs) != len(relocs) {
		t.Fatal("invalid AMD64 resource object")
	}
	for _, reloc := range file.Sections[0].Relocs {
		if reloc.Type != 3 || reloc.SymbolTableIndex != 0 || uint64(reloc.VirtualAddress)+4 > uint64(len(resources)) {
			t.Fatal("invalid resource RVA relocation")
		}
	}
}

func TestExecutableVerificationReadsRelocatedManifestAndRejectsMissingOrAlteredData(t *testing.T) {
	_, manifest := manifestFixture(t)
	ico, err := os.ReadFile("../../internal/branding/icon.ico")
	if err != nil {
		t.Fatal(err)
	}
	resources, relocs, err := makeResource(ico, manifest)
	if err != nil {
		t.Fatal(err)
	}
	const sectionRVA = 0x1000
	for _, reloc := range relocs {
		putU32(resources, int(reloc.offset), binary.LittleEndian.Uint32(resources[reloc.offset:])+sectionRVA)
	}
	// Minimal non-executed PE fixture: virtual RVAs differ from file offsets,
	// and the empty symbol table points at EOF as in the packed application.
	makePE := func(data []byte) []byte {
		const headersSize = 512
		size := uint32((len(data) + 511) &^ 511)
		dos := make([]byte, 128)
		copy(dos, "MZ")
		putU32(dos, 60, 128)
		buffer := bytes.NewBuffer(dos)
		buffer.WriteString("PE\x00\x00")
		write := func(value any) {
			t.Helper()
			if err := binary.Write(buffer, binary.LittleEndian, value); err != nil {
				t.Fatal(err)
			}
		}
		write(pe.FileHeader{Machine: pe.IMAGE_FILE_MACHINE_AMD64, NumberOfSections: 1, PointerToSymbolTable: headersSize + size, SizeOfOptionalHeader: uint16(binary.Size(pe.OptionalHeader64{})), Characteristics: 0x22})
		header := pe.OptionalHeader64{Magic: 0x20b, ImageBase: 0x140000000, SectionAlignment: 4096, FileAlignment: 512, SizeOfImage: sectionRVA + (uint32(len(data))+4095)&^4095, SizeOfHeaders: headersSize, NumberOfRvaAndSizes: 16}
		header.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_RESOURCE] = pe.DataDirectory{VirtualAddress: sectionRVA, Size: uint32(len(data))}
		write(header)
		section := pe.SectionHeader32{VirtualSize: uint32(len(data)), VirtualAddress: sectionRVA, SizeOfRawData: size, PointerToRawData: headersSize, Characteristics: 0x40000040}
		copy(section.Name[:], ".rsrc")
		write(section)
		buffer.Write(make([]byte, headersSize-buffer.Len()))
		buffer.Write(data)
		buffer.Write(make([]byte, int(size)-len(data)))
		return buffer.Bytes()
	}
	path := filepath.Join(t.TempDir(), "viewer.exe")
	for _, scenario := range []string{"valid", "missing", "altered", "truncated"} {
		t.Run(scenario, func(t *testing.T) {
			data := bytes.Clone(resources)
			switch scenario {
			case "missing":
				putU32(data, 16+2*8, 25) // no RT_MANIFEST directory
			case "altered":
				data = bytes.Replace(data, []byte("PerMonitorV2"), []byte("SystemAware"), 1)
			case "truncated":
				putU32(data, 16+2*8+4, 0x80000000|uint32(len(data)-4))
			}
			image := makePE(data)
			if err := os.WriteFile(path, image, 0600); err != nil {
				t.Fatal(err)
			}
			err := verifyExecutable(path, manifest)
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("verification %s: %v", scenario, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(image, after) {
				t.Fatal("verification modified the executable")
			}
		})
	}
}
