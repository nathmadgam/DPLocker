// Reading, editing and cleaning document metadata (author, title, company, etc.).
// Office Open XML (docx/xlsx/pptx) and OpenDocument (odt/ods/odp) are zip packages whose
// metadata lives in small XML parts; PDFs use the Info dictionary plus an optional XMP stream.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

const (
	namespaceDC      = "http://purl.org/dc/elements/1.1/"
	namespaceCore    = "http://schemas.openxmlformats.org/package/2006/metadata/core-properties"
	namespaceTerms   = "http://purl.org/dc/terms/"
	namespaceApp     = "http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"
	namespaceODFMeta = "urn:oasis:names:tc:opendocument:xmlns:meta:1.0"
	emptyCustomXML   = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/custom-properties"` +
		` xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes"/>`
)

// Editable fields shared by every format, in display order.
var editableFields = []string{"title", "subject", "author", "keywords", "comments", "lastModifiedBy", "category", "company", "manager"}

type metadataResult struct {
	Supported bool              `json:"supported"`
	Format    string            `json:"format"`
	Editable  []string          `json:"editable"`
	Fields    map[string]string `json:"fields"`
	Info      map[string]string `json:"info"`
	Notes     []string          `json:"notes"`
}

type xmlField struct {
	space, local, prefix string
}

var ooxmlCoreFields = map[string]xmlField{
	"title":          {namespaceDC, "title", "dc"},
	"subject":        {namespaceDC, "subject", "dc"},
	"author":         {namespaceDC, "creator", "dc"},
	"keywords":       {namespaceCore, "keywords", "cp"},
	"comments":       {namespaceDC, "description", "dc"},
	"lastModifiedBy": {namespaceCore, "lastModifiedBy", "cp"},
	"category":       {namespaceCore, "category", "cp"},
}

var ooxmlAppFields = map[string]xmlField{
	"company": {namespaceApp, "Company", ""},
	"manager": {namespaceApp, "Manager", ""},
}

var odfFields = map[string]xmlField{
	"title":          {namespaceDC, "title", "dc"},
	"subject":        {namespaceDC, "subject", "dc"},
	"author":         {namespaceODFMeta, "initial-creator", "meta"},
	"keywords":       {namespaceODFMeta, "keyword", "meta"},
	"comments":       {namespaceDC, "description", "dc"},
	"lastModifiedBy": {namespaceDC, "creator", "dc"},
}

var pdfFields = map[string]string{
	"title": "Title", "subject": "Subject", "author": "Author", "keywords": "Keywords",
}

func metadataFormat(path string) string {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "docx", "xlsx", "pptx":
		return "ooxml"
	case "odt", "ods", "odp":
		return "odf"
	case "pdf":
		return "pdf"
	}
	return ""
}

func readMetadata(path string) (*metadataResult, error) {
	result := &metadataResult{Fields: map[string]string{}, Info: map[string]string{}, Notes: []string{}}
	switch metadataFormat(path) {
	case "ooxml":
		return result, readOOXML(path, result)
	case "odf":
		return result, readODF(path, result)
	case "pdf":
		return result, readPDF(path, result)
	}
	result.Notes = append(result.Notes, "This file type has no built-in document properties to edit.")
	return result, nil
}

// Applies edited fields; clean=true wipes every identifying property instead.
func writeMetadata(path string, fields map[string]string, clean bool) error {
	switch metadataFormat(path) {
	case "ooxml":
		return writeOOXML(path, fields, clean)
	case "odf":
		return writeODF(path, fields, clean)
	case "pdf":
		return writePDF(path, fields, clean)
	}
	return errors.New("this file type has no editable metadata")
}

type childElement struct {
	space, local string
	start, end   int64
	text         string
}

type xmlParent struct {
	innerStart, innerEnd int64
	selfClosing          bool
	rawName              string
	children             []childElement
}

// Finds the first element named space/local anywhere in the document and records its
// direct children with byte offsets, so edits can splice the original XML and leave
// everything else exactly as it was.
func scanChildren(data []byte, parentSpace, parentLocal string) (*xmlParent, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	parent := &xmlParent{}
	depth, targetDepth := 0, -1
	var current *childElement
	var text strings.Builder
	for {
		before := decoder.InputOffset()
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch element := token.(type) {
		case xml.StartElement:
			depth++
			if targetDepth < 0 && element.Name.Space == parentSpace && element.Name.Local == parentLocal {
				targetDepth = depth
				parent.innerStart = decoder.InputOffset()
				raw := string(data[before:parent.innerStart])
				parent.selfClosing = strings.HasSuffix(strings.TrimSpace(raw), "/>")
				name := strings.Fields(strings.TrimPrefix(raw, "<"))[0]
				parent.rawName = strings.TrimSuffix(strings.TrimSuffix(name, ">"), "/")
			} else if targetDepth > 0 && depth == targetDepth+1 {
				current = &childElement{space: element.Name.Space, local: element.Name.Local, start: before}
				text.Reset()
			}
		case xml.CharData:
			if current != nil {
				text.Write(element)
			}
		case xml.EndElement:
			if targetDepth > 0 && depth == targetDepth+1 && current != nil {
				current.end = decoder.InputOffset()
				current.text = strings.TrimSpace(text.String())
				parent.children = append(parent.children, *current)
				current = nil
			}
			if depth == targetDepth {
				parent.innerEnd = before
				if parent.selfClosing {
					// Go reports <a/> as start+end; treat the inner range as empty.
					parent.innerEnd = parent.innerStart
				}
				return parent, nil
			}
			depth--
		}
	}
	return nil, errors.New("metadata section not found")
}

func childText(parent *xmlParent, field xmlField) string {
	values := []string{}
	for _, child := range parent.children {
		if child.space == field.space && child.local == field.local && child.text != "" {
			values = append(values, child.text)
		}
	}
	return strings.Join(values, ", ")
}

func escapeXML(value string) string {
	var buffer bytes.Buffer
	xml.EscapeText(&buffer, []byte(value))
	return buffer.String()
}

func elementXML(field xmlField, value string) string {
	// Declaring the namespace on the element itself is always valid, even when the
	// file declares prefixes per element (openpyxl does) rather than on the root.
	name, declaration := field.local, ""
	if field.prefix != "" {
		name = field.prefix + ":" + field.local
		declaration = ` xmlns:` + field.prefix + `="` + field.space + `"`
	}
	return "<" + name + declaration + ">" + escapeXML(value) + "</" + name + ">"
}

// Rebuilds the parent's children: keep(child) decides which originals stay,
// then additions are appended. Bytes outside the parent are untouched.
func spliceChildren(data []byte, parent *xmlParent, keep func(childElement) bool, additions []string) []byte {
	var inner strings.Builder
	for _, child := range parent.children {
		if keep(child) {
			inner.Write(data[child.start:child.end])
		}
	}
	for _, addition := range additions {
		inner.WriteString(addition)
	}
	var output bytes.Buffer
	if parent.selfClosing {
		head := bytes.TrimRight(data[:parent.innerStart], " \t\r\n")
		head = bytes.TrimSuffix(head, []byte("/>"))
		output.Write(head)
		output.WriteString(">" + inner.String() + "</" + parent.rawName + ">")
		output.Write(data[parent.innerStart:])
		return output.Bytes()
	}
	output.Write(data[:parent.innerStart])
	output.WriteString(inner.String())
	output.Write(data[parent.innerEnd:])
	return output.Bytes()
}

func applyFields(data []byte, parent *xmlParent, mapping map[string]xmlField, fields map[string]string) []byte {
	touched := map[xmlField]string{}
	for key, field := range mapping {
		if value, ok := fields[key]; ok {
			touched[field] = strings.TrimSpace(value)
		}
	}
	keep := func(child childElement) bool {
		for field := range touched {
			if child.space == field.space && child.local == field.local {
				return false
			}
		}
		return true
	}
	additions := []string{}
	for _, key := range editableFields {
		field, ok := mapping[key]
		if !ok {
			continue
		}
		if value, ok := touched[field]; ok && value != "" {
			additions = append(additions, elementXML(field, value))
		}
	}
	return spliceChildren(data, parent, keep, additions)
}

func readZipPart(reader *zip.Reader, name string) ([]byte, bool) {
	for _, file := range reader.File {
		if file.Name == name {
			handle, err := file.Open()
			if err != nil {
				return nil, false
			}
			defer handle.Close()
			content, err := io.ReadAll(io.LimitReader(handle, 8*1024*1024))
			return content, err == nil
		}
	}
	return nil, false
}

// Rewrites the zip with some parts replaced, copying every other entry byte-for-byte
// (keeps ODF's uncompressed leading "mimetype" entry valid), then swaps it in atomically.
func rewriteZip(path string, replacements map[string][]byte) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	temporaryPath := path + ".dplocker-tmp"
	output, err := os.Create(temporaryPath)
	if err != nil {
		reader.Close()
		return err
	}
	writer := zip.NewWriter(output)
	writeError := func() error {
		for _, file := range reader.File {
			replacement, replace := replacements[file.Name]
			if !replace {
				if err := writer.Copy(file); err != nil {
					return err
				}
				continue
			}
			header := file.FileHeader
			header.CompressedSize64, header.UncompressedSize64, header.CRC32 = 0, 0, 0
			entry, err := writer.CreateHeader(&header)
			if err != nil {
				return err
			}
			if _, err := entry.Write(replacement); err != nil {
				return err
			}
		}
		return writer.Close()
	}()
	closeError := output.Close()
	reader.Close()
	if writeError != nil || closeError != nil {
		os.Remove(temporaryPath)
		return errors.Join(writeError, closeError)
	}
	return os.Rename(temporaryPath, path)
}

func readOOXML(path string, result *metadataResult) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return errors.New("this file isn’t a valid Office document")
	}
	defer reader.Close()
	result.Format = "Office document properties"
	if core, ok := readZipPart(&reader.Reader, "docProps/core.xml"); ok {
		if parent, err := scanChildren(core, namespaceCore, "coreProperties"); err == nil {
			result.Supported = true
			for key, field := range ooxmlCoreFields {
				result.Fields[key] = childText(parent, field)
				result.Editable = append(result.Editable, key)
			}
			result.Info["created"] = childText(parent, xmlField{namespaceTerms, "created", ""})
			result.Info["modified"] = childText(parent, xmlField{namespaceTerms, "modified", ""})
			result.Info["revision"] = childText(parent, xmlField{namespaceCore, "revision", ""})
		}
	}
	if app, ok := readZipPart(&reader.Reader, "docProps/app.xml"); ok {
		if parent, err := scanChildren(app, namespaceApp, "Properties"); err == nil {
			for key, field := range ooxmlAppFields {
				result.Fields[key] = childText(parent, field)
				result.Editable = append(result.Editable, key)
			}
			result.Info["application"] = childText(parent, xmlField{namespaceApp, "Application", ""})
			result.Info["template"] = childText(parent, xmlField{namespaceApp, "Template", ""})
			result.Info["editingMinutes"] = childText(parent, xmlField{namespaceApp, "TotalTime", ""})
		}
	}
	if custom, ok := readZipPart(&reader.Reader, "docProps/custom.xml"); ok {
		if count := bytes.Count(custom, []byte("<property ")); count > 0 {
			result.Info["customProperties"] = fmt.Sprint(count)
		}
	}
	if !result.Supported {
		result.Notes = append(result.Notes, "This document has no properties section to edit.")
	}
	result.Notes = append(result.Notes,
		"Names inside comments and tracked changes are part of the document body and aren’t changed here.")
	return nil
}

func writeOOXML(path string, fields map[string]string, clean bool) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return errors.New("this file isn’t a valid Office document")
	}
	core, hasCore := readZipPart(&reader.Reader, "docProps/core.xml")
	app, hasApp := readZipPart(&reader.Reader, "docProps/app.xml")
	_, hasCustom := readZipPart(&reader.Reader, "docProps/custom.xml")
	reader.Close()

	replacements := map[string][]byte{}
	if hasCore {
		parent, err := scanChildren(core, namespaceCore, "coreProperties")
		if err != nil {
			return err
		}
		if clean {
			replacements["docProps/core.xml"] = spliceChildren(core, parent, func(childElement) bool { return false }, nil)
		} else {
			replacements["docProps/core.xml"] = applyFields(core, parent, ooxmlCoreFields, fields)
		}
	}
	if hasApp {
		parent, err := scanChildren(app, namespaceApp, "Properties")
		if err != nil {
			return err
		}
		if clean {
			// Statistics stay (harmless and some apps expect them); identifying entries go.
			identifying := map[string]bool{"Company": true, "Manager": true, "Template": true,
				"HyperlinkBase": true, "TotalTime": true, "Application": true, "AppVersion": true}
			replacements["docProps/app.xml"] = spliceChildren(app, parent, func(child childElement) bool {
				return !identifying[child.local]
			}, nil)
		} else {
			replacements["docProps/app.xml"] = applyFields(app, parent, ooxmlAppFields, fields)
		}
	}
	if clean && hasCustom {
		replacements["docProps/custom.xml"] = []byte(emptyCustomXML)
	}
	if len(replacements) == 0 {
		return errors.New("this document has no properties section")
	}
	return rewriteZip(path, replacements)
}

func readODF(path string, result *metadataResult) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return errors.New("this file isn’t a valid OpenDocument file")
	}
	defer reader.Close()
	result.Format = "OpenDocument properties"
	meta, ok := readZipPart(&reader.Reader, "meta.xml")
	if !ok {
		result.Notes = append(result.Notes, "This document has no properties section to edit.")
		return nil
	}
	parent, err := odfMetaParent(meta)
	if err != nil {
		return err
	}
	result.Supported = true
	for key, field := range odfFields {
		result.Fields[key] = childText(parent, field)
		result.Editable = append(result.Editable, key)
	}
	result.Info["application"] = childText(parent, xmlField{namespaceODFMeta, "generator", ""})
	result.Info["created"] = childText(parent, xmlField{namespaceODFMeta, "creation-date", ""})
	result.Info["modified"] = childText(parent, xmlField{namespaceDC, "date", ""})
	result.Info["revision"] = childText(parent, xmlField{namespaceODFMeta, "editing-cycles", ""})
	return nil
}

const namespaceOffice = "urn:oasis:names:tc:opendocument:xmlns:office:1.0"

func odfMetaParent(meta []byte) (*xmlParent, error) {
	return scanChildren(meta, namespaceOffice, "meta")
}

func writeODF(path string, fields map[string]string, clean bool) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return errors.New("this file isn’t a valid OpenDocument file")
	}
	meta, ok := readZipPart(&reader.Reader, "meta.xml")
	reader.Close()
	if !ok {
		return errors.New("this document has no properties section")
	}
	parent, err := odfMetaParent(meta)
	if err != nil {
		return err
	}
	var updated []byte
	if clean {
		// Word/page counts stay; everything that identifies people, software or editing history goes.
		updated = spliceChildren(meta, parent, func(child childElement) bool {
			return child.space == namespaceODFMeta && child.local == "document-statistic"
		}, nil)
	} else {
		mapping := map[string]xmlField{}
		for key, field := range odfFields {
			mapping[key] = field
		}
		keywords, hasKeywords := fields["keywords"]
		delete(mapping, "keywords")
		updated = applyFields(meta, parent, mapping, fields)
		if hasKeywords {
			parent, err = odfMetaParent(updated)
			if err != nil {
				return err
			}
			additions := []string{}
			for _, keyword := range strings.Split(keywords, ",") {
				if keyword = strings.TrimSpace(keyword); keyword != "" {
					additions = append(additions, elementXML(odfFields["keywords"], keyword))
				}
			}
			updated = spliceChildren(updated, parent, func(child childElement) bool {
				return !(child.space == namespaceODFMeta && child.local == "keyword")
			}, additions)
		}
	}
	return rewriteZip(path, map[string][]byte{"meta.xml": updated})
}

func pdfConfiguration() *model.Configuration {
	// Stops pdfcpu from creating a config folder in the user's AppData.
	model.ConfigPath = "disable"
	return model.NewDefaultConfiguration()
}

func readPDFContext(path string) (*model.Context, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	ctx, err := api.ReadContext(context.Background(), file, pdfConfiguration())
	if err != nil {
		return nil, errors.New("couldn’t read this PDF (it may be encrypted or damaged)")
	}
	return ctx, nil
}

func pdfInfoString(ctx *model.Context, dictionary types.Dict, key string) string {
	object, found := dictionary.Find(key)
	if !found {
		return ""
	}
	object, err := ctx.Dereference(object)
	if err != nil || object == nil {
		return ""
	}
	value, err := types.StringOrHexLiteral(object)
	if err != nil || value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func readPDF(path string, result *metadataResult) error {
	result.Format = "PDF document info"
	ctx, err := readPDFContext(path)
	if err != nil {
		return err
	}
	result.Supported = true
	for key := range pdfFields {
		result.Editable = append(result.Editable, key)
		result.Fields[key] = ""
	}
	if ctx.Info != nil {
		if dictionary, err := ctx.DereferenceDict(*ctx.Info); err == nil && dictionary != nil {
			for key, pdfKey := range pdfFields {
				result.Fields[key] = pdfInfoString(ctx, dictionary, pdfKey)
			}
			result.Info["application"] = pdfInfoString(ctx, dictionary, "Creator")
			result.Info["producer"] = pdfInfoString(ctx, dictionary, "Producer")
			result.Info["created"] = pdfInfoString(ctx, dictionary, "CreationDate")
			result.Info["modified"] = pdfInfoString(ctx, dictionary, "ModDate")
		}
	}
	if catalog, err := ctx.Catalog(); err == nil {
		if _, found := catalog.Find("Metadata"); found {
			result.Info["xmpMetadata"] = "present"
			result.Notes = append(result.Notes,
				"Saving removes the PDF’s XMP metadata copy so readers don’t show the old values.")
		}
	}
	return nil
}

func writePDF(path string, fields map[string]string, clean bool) error {
	ctx, err := readPDFContext(path)
	if err != nil {
		return err
	}
	// Without this pdfcpu stamps its own Producer and fresh dates on save.
	ctx.PreserveInfoDict = true
	if ctx.Info == nil {
		reference, err := ctx.IndRefForNewObject(types.NewDict())
		if err != nil {
			return err
		}
		ctx.Info = reference
	}
	dictionary, err := ctx.DereferenceDict(*ctx.Info)
	if err != nil || dictionary == nil {
		return errors.New("couldn’t open the PDF’s info section")
	}
	if clean {
		for key := range dictionary {
			dictionary.Delete(key)
		}
	} else {
		for key, pdfKey := range pdfFields {
			value, ok := fields[key]
			if !ok {
				continue
			}
			if value = strings.TrimSpace(value); value == "" {
				dictionary.Delete(pdfKey)
			} else {
				dictionary.Update(pdfKey, types.StringLiteral(types.EncodeUTF16String(value)))
			}
		}
	}
	// The XMP stream duplicates the info fields; stale copies would undo the edit in some readers.
	if catalog, err := ctx.Catalog(); err == nil {
		catalog.Delete("Metadata")
	}
	temporaryPath := path + ".dplocker-tmp"
	if err := api.WriteContextFile(context.Background(), ctx, temporaryPath); err != nil {
		os.Remove(temporaryPath)
		return errors.New("couldn’t save the PDF: " + err.Error())
	}
	return os.Rename(temporaryPath, path)
}
