package main

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"os"

	"excellens/internal/buildinfo"
)

func renderManifest(template []byte) ([]byte, error) {
	manifest := bytes.ReplaceAll(template, []byte("__APP_VERSION__"), []byte(buildinfo.Version))
	var doc struct {
		XMLName  xml.Name `xml:"urn:schemas-microsoft-com:asm.v1 assembly"`
		Identity struct {
			Name    string `xml:"name,attr"`
			Version string `xml:"version,attr"`
			Arch    string `xml:"processorArchitecture,attr"`
		} `xml:"urn:schemas-microsoft-com:asm.v1 assemblyIdentity"`
		Application struct {
			Settings struct {
				Aware     string `xml:"http://schemas.microsoft.com/SMI/2005/WindowsSettings dpiAware"`
				Awareness string `xml:"http://schemas.microsoft.com/SMI/2016/WindowsSettings dpiAwareness"`
			} `xml:"urn:schemas-microsoft-com:asm.v3 windowsSettings"`
		} `xml:"urn:schemas-microsoft-com:asm.v3 application"`
		Dependency struct {
			Assembly struct {
				Identity struct {
					Name    string `xml:"name,attr"`
					Version string `xml:"version,attr"`
				} `xml:"urn:schemas-microsoft-com:asm.v1 assemblyIdentity"`
			} `xml:"urn:schemas-microsoft-com:asm.v1 dependentAssembly"`
		} `xml:"urn:schemas-microsoft-com:asm.v1 dependency"`
	}
	if err := xml.Unmarshal(manifest, &doc); err != nil {
		return nil, fmt.Errorf("invalid application manifest: %w", err)
	}
	if doc.Identity.Name != "ExcelLens" || doc.Identity.Version != buildinfo.Version+".0" || doc.Identity.Arch != "amd64" {
		return nil, errors.New("manifest identity must match ExcelLens, the current buildinfo version and AMD64")
	}
	if doc.Application.Settings.Aware != "true/pm" || doc.Application.Settings.Awareness != "PerMonitorV2,PerMonitor" {
		return nil, errors.New("manifest must enable PerMonitorV2 DPI awareness with per-monitor fallback")
	}
	if doc.Dependency.Assembly.Identity.Name != "Microsoft.Windows.Common-Controls" || doc.Dependency.Assembly.Identity.Version != "6.0.0.0" {
		return nil, errors.New("manifest must enable Windows Common Controls v6 for the native About dialog")
	}
	return manifest, nil
}

// resourceEntry follows the Windows type/name/language tree. Only integer IDs
// are needed for the application's manifest and icon resources.
func resourceEntry(data []byte, ids ...uint32) (rva, size uint32, err error) {
	offset := uint32(0)
	for depth, id := range ids {
		if uint64(offset)+16 > uint64(len(data)) {
			return 0, 0, errors.New("truncated resource directory")
		}
		count := uint32(binary.LittleEndian.Uint16(data[offset+12:])) + uint32(binary.LittleEndian.Uint16(data[offset+14:]))
		found := false
		for i := uint32(0); i < count; i++ {
			pos := uint64(offset) + 16 + uint64(i)*8
			if pos+8 > uint64(len(data)) {
				return 0, 0, errors.New("truncated resource entry")
			}
			if binary.LittleEndian.Uint32(data[pos:]) != id {
				continue
			}
			target := binary.LittleEndian.Uint32(data[pos+4:])
			if (target&0x80000000 != 0) != (depth < len(ids)-1) {
				return 0, 0, errors.New("invalid resource tree depth")
			}
			offset, found = target&0x7fffffff, true
			break
		}
		if !found {
			return 0, 0, fmt.Errorf("missing resource ID %d at depth %d", id, depth)
		}
	}
	if uint64(offset)+16 > uint64(len(data)) {
		return 0, 0, errors.New("truncated resource data entry")
	}
	return binary.LittleEndian.Uint32(data[offset:]), binary.LittleEndian.Uint32(data[offset+4:]), nil
}

func verifyExecutable(path string, expected []byte) error {
	image, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// UPX can leave an empty COFF symbol table pointing at EOF. Windows ignores
	// it; debug/pe tries to read a string table there. Normalize only this unused
	// field in our private buffer, never the executable or its resource data.
	if len(image) >= 64 && bytes.Equal(image[:2], []byte("MZ")) {
		nt := uint64(binary.LittleEndian.Uint32(image[60:]))
		if nt+24 <= uint64(len(image)) && bytes.Equal(image[nt:nt+4], []byte("PE\x00\x00")) && binary.LittleEndian.Uint32(image[nt+16:]) == 0 {
			binary.LittleEndian.PutUint32(image[nt+12:], 0)
		}
	}
	file, err := pe.NewFile(bytes.NewReader(image))
	if err != nil {
		return err
	}
	defer file.Close()
	header, ok := file.OptionalHeader.(*pe.OptionalHeader64)
	if !ok || file.Machine != pe.IMAGE_FILE_MACHINE_AMD64 {
		return errors.New("expected a Windows AMD64 executable")
	}
	readRVA := func(rva, size uint32) ([]byte, error) {
		for _, section := range file.Sections {
			if rva < section.VirtualAddress || uint64(rva)+uint64(size) > uint64(section.VirtualAddress)+uint64(section.Size) {
				continue
			}
			data, err := section.Data()
			if err != nil {
				return nil, err
			}
			offset := uint64(rva - section.VirtualAddress)
			if offset+uint64(size) <= uint64(len(data)) {
				return data[offset : offset+uint64(size)], nil
			}
		}
		return nil, errors.New("resource RVA is outside the executable")
	}
	directory := header.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_RESOURCE]
	if directory.VirtualAddress == 0 || directory.Size == 0 {
		return errors.New("Windows executable has no resources")
	}
	resources, err := readRVA(directory.VirtualAddress, directory.Size)
	if err != nil {
		return err
	}
	rva, size, err := resourceEntry(resources, 24, 1, 0)
	if err != nil {
		return fmt.Errorf("application manifest: %w", err)
	}
	actual, err := readRVA(rva, size)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return errors.New("embedded application manifest does not match the current DPI settings and build version")
	}
	return nil
}
