package knowledge

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const maxOOXMLPartBytes int64 = 64 * 1024 * 1024

var pptSlideFilePattern = regexp.MustCompile(`^ppt/slides/slide([0-9]+)\.xml$`)

func isOfficeOpenXML(contentType string, ext string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	ext = strings.ToLower(strings.TrimSpace(ext))
	if strings.Contains(contentType, "officedocument") ||
		strings.Contains(contentType, "macroenabled.12") {
		return true
	}
	switch ext {
	case ".docx", ".docm", ".dotx", ".dotm",
		".xlsx", ".xlsm", ".xltx", ".xltm",
		".pptx", ".pptm", ".potx", ".potm", ".ppsx", ".ppsm":
		return true
	default:
		return false
	}
}

func isLegacyOfficeDocument(contentType string, ext string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	ext = strings.ToLower(strings.TrimSpace(ext))
	if contentType == "application/msword" ||
		contentType == "application/vnd.ms-excel" ||
		contentType == "application/vnd.ms-powerpoint" {
		return true
	}
	switch ext {
	case ".doc", ".xls", ".ppt":
		return true
	default:
		return false
	}
}

func extractOfficeOpenXMLText(data []byte, name string, contentType string) (string, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("file %s is not a valid Office Open XML document", name)
	}
	kind := officeOpenXMLKind(reader, contentType, filepath.Ext(name))
	var text string
	switch kind {
	case "word":
		text, err = extractWordprocessingText(reader)
	case "spreadsheet":
		text, err = extractSpreadsheetText(reader)
	case "presentation":
		text, err = extractPresentationText(reader)
	default:
		err = fmt.Errorf("file %s is not a supported Office Open XML document", name)
	}
	if err != nil {
		return "", err
	}
	text = normalizeOfficeText(text)
	if usefulRuneCount(text) == 0 {
		return "", fmt.Errorf("file %s does not contain extractable text", name)
	}
	return "# " + name + "\n\n" + text, nil
}

func officeOpenXMLKind(reader *zip.Reader, contentType string, ext string) string {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	ext = strings.ToLower(strings.TrimSpace(ext))
	switch {
	case strings.Contains(contentType, "wordprocessingml"), ext == ".docx", ext == ".docm", ext == ".dotx", ext == ".dotm":
		return "word"
	case strings.Contains(contentType, "spreadsheetml"), ext == ".xlsx", ext == ".xlsm", ext == ".xltx", ext == ".xltm":
		return "spreadsheet"
	case strings.Contains(contentType, "presentationml"), ext == ".pptx", ext == ".pptm", ext == ".potx", ext == ".potm", ext == ".ppsx", ext == ".ppsm":
		return "presentation"
	}
	for _, file := range reader.File {
		switch {
		case file.Name == "word/document.xml":
			return "word"
		case strings.HasPrefix(file.Name, "xl/worksheets/"):
			return "spreadsheet"
		case strings.HasPrefix(file.Name, "ppt/slides/"):
			return "presentation"
		}
	}
	return ""
}

func extractWordprocessingText(reader *zip.Reader) (string, error) {
	extraNames := []string{}
	for _, file := range reader.File {
		if strings.HasPrefix(file.Name, "word/header") && strings.HasSuffix(file.Name, ".xml") ||
			strings.HasPrefix(file.Name, "word/footer") && strings.HasSuffix(file.Name, ".xml") ||
			file.Name == "word/footnotes.xml" ||
			file.Name == "word/endnotes.xml" ||
			file.Name == "word/comments.xml" {
			extraNames = append(extraNames, file.Name)
		}
	}
	names := append([]string{"word/document.xml"}, uniqueSorted(extraNames)...)
	parts := []string{}
	for _, name := range names {
		data, ok, err := readZipPart(reader, name)
		if err != nil {
			return "", err
		}
		if !ok {
			continue
		}
		if text := extractXMLText(data, map[string]bool{"t": true}, map[string]bool{"p": true}); strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func extractPresentationText(reader *zip.Reader) (string, error) {
	files := []*zip.File{}
	for _, file := range reader.File {
		if pptSlideFilePattern.MatchString(file.Name) {
			files = append(files, file)
		}
	}
	sort.SliceStable(files, func(i, j int) bool {
		return pptSlideNumber(files[i].Name) < pptSlideNumber(files[j].Name)
	})
	parts := []string{}
	for _, file := range files {
		data, err := readZipFile(file)
		if err != nil {
			return "", err
		}
		if text := extractXMLText(data, map[string]bool{"t": true}, map[string]bool{"p": true}); strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func extractSpreadsheetText(reader *zip.Reader) (string, error) {
	sharedStrings := []string{}
	if data, ok, err := readZipPart(reader, "xl/sharedStrings.xml"); err != nil {
		return "", err
	} else if ok {
		sharedStrings = extractSharedStrings(data)
	}
	worksheetFiles := []*zip.File{}
	for _, file := range reader.File {
		if strings.HasPrefix(file.Name, "xl/worksheets/") && strings.HasSuffix(file.Name, ".xml") {
			worksheetFiles = append(worksheetFiles, file)
		}
	}
	sort.SliceStable(worksheetFiles, func(i, j int) bool {
		return worksheetFiles[i].Name < worksheetFiles[j].Name
	})
	parts := []string{}
	for _, file := range worksheetFiles {
		data, err := readZipFile(file)
		if err != nil {
			return "", err
		}
		if text := extractWorksheetText(data, sharedStrings); strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 && len(sharedStrings) > 0 {
		parts = append(parts, strings.Join(sharedStrings, "\n"))
	}
	return strings.Join(parts, "\n\n"), nil
}

func readZipPart(reader *zip.Reader, name string) ([]byte, bool, error) {
	for _, file := range reader.File {
		if file.Name == name {
			data, err := readZipFile(file)
			return data, true, err
		}
	}
	return nil, false, nil
}

func readZipFile(file *zip.File) ([]byte, error) {
	rc, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	limited := io.LimitReader(rc, maxOOXMLPartBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxOOXMLPartBytes {
		return nil, fmt.Errorf("Office XML part %s exceeds %d bytes", file.Name, maxOOXMLPartBytes)
	}
	return data, nil
}

func extractXMLText(data []byte, textElements map[string]bool, lineBreakElements map[string]bool) string {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var out strings.Builder
	textDepth := 0
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch value := token.(type) {
		case xml.StartElement:
			if textElements[value.Name.Local] {
				textDepth++
			}
			if value.Name.Local == "tab" {
				out.WriteByte('\t')
			}
			if value.Name.Local == "br" {
				out.WriteByte('\n')
			}
		case xml.CharData:
			if textDepth > 0 {
				out.Write([]byte(value))
			}
		case xml.EndElement:
			if textElements[value.Name.Local] && textDepth > 0 {
				textDepth--
			}
			if lineBreakElements[value.Name.Local] {
				out.WriteByte('\n')
			}
		}
	}
	return out.String()
}

func extractSharedStrings(data []byte) []string {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	values := []string{}
	var current strings.Builder
	inSI := false
	textDepth := 0
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "si":
				inSI = true
				current.Reset()
			case "t":
				if inSI {
					textDepth++
				}
			}
		case xml.CharData:
			if inSI && textDepth > 0 {
				current.Write([]byte(value))
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "t":
				if textDepth > 0 {
					textDepth--
				}
			case "si":
				text := strings.TrimSpace(current.String())
				if text != "" {
					values = append(values, text)
				}
				inSI = false
			}
		}
	}
	return values
}

func extractWorksheetText(data []byte, sharedStrings []string) string {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	rows := []string{}
	cells := []string{}
	inCell := false
	inValue := false
	inInlineText := false
	cellType := ""
	var valueText strings.Builder
	var inlineText strings.Builder
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "row":
				cells = []string{}
			case "c":
				inCell = true
				cellType = attrValue(value.Attr, "t")
				valueText.Reset()
				inlineText.Reset()
			case "v":
				if inCell {
					inValue = true
				}
			case "t":
				if inCell && cellType == "inlineStr" {
					inInlineText = true
				}
			}
		case xml.CharData:
			if inValue {
				valueText.Write([]byte(value))
			}
			if inInlineText {
				inlineText.Write([]byte(value))
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "v":
				inValue = false
			case "t":
				inInlineText = false
			case "c":
				if cell := decodeWorksheetCell(cellType, strings.TrimSpace(valueText.String()), strings.TrimSpace(inlineText.String()), sharedStrings); cell != "" {
					cells = append(cells, cell)
				}
				inCell = false
			case "row":
				if len(cells) > 0 {
					rows = append(rows, strings.Join(cells, "\t"))
				}
			}
		}
	}
	return strings.Join(rows, "\n")
}

func decodeWorksheetCell(cellType string, value string, inline string, sharedStrings []string) string {
	switch cellType {
	case "s":
		index, err := strconv.Atoi(value)
		if err != nil || index < 0 || index >= len(sharedStrings) {
			return ""
		}
		return sharedStrings[index]
	case "inlineStr":
		return inline
	case "b":
		if value == "1" {
			return "TRUE"
		}
		if value == "0" {
			return "FALSE"
		}
		return value
	default:
		return value
	}
}

func attrValue(attrs []xml.Attr, name string) string {
	for _, attr := range attrs {
		if attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func pptSlideNumber(name string) int {
	match := pptSlideFilePattern.FindStringSubmatch(name)
	if len(match) != 2 {
		return 0
	}
	value, _ := strconv.Atoi(match[1])
	return value
}

func normalizeOfficeText(value string) string {
	lines := strings.Split(value, "\n")
	out := []string{}
	previous := ""
	for _, line := range lines {
		fields := strings.Fields(line)
		line = strings.Join(fields, " ")
		if line == "" {
			if previous != "" {
				out = append(out, "")
				previous = ""
			}
			continue
		}
		if line == previous {
			continue
		}
		out = append(out, line)
		previous = line
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
