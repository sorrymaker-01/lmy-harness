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

// maxOOXMLPartBytes 是单个 OOXML 内部 XML 部件（zip entry）解压后的大小上限（64 MiB），
// 防止 zip 炸弹/超大部件在解析时耗尽内存。
const maxOOXMLPartBytes int64 = 64 * 1024 * 1024

// pptSlideFilePattern 匹配 PPTX 幻灯片部件路径 ppt/slides/slideN.xml，并捕获页号 N 用于排序。
var pptSlideFilePattern = regexp.MustCompile(`^ppt/slides/slide([0-9]+)\.xml$`)

// isOfficeOpenXML 判断是否为现代 Office Open XML 文档（docx/xlsx/pptx 及其宏/模板变体），
// 依据 content-type 关键字或扩展名。这类文件本质是 zip 包，可直接解析内部 XML。
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

// isLegacyOfficeDocument 判断是否为旧版二进制 Office 文档（doc/xls/ppt）。
// 这类是私有二进制格式（非 zip+XML），本模块不解析，上层据此给出“请先转换格式”的报错。
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

// extractOfficeOpenXMLText 是 OOXML 抽取入口：把文档当 zip 打开，判定其类型
//（word/spreadsheet/presentation），分派到对应抽取器，最后归一化文本并以 "# <文件名>" 起头。
// 无可抽取文本时报错。
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

// officeOpenXMLKind 判定 OOXML 具体类型：先看 content-type/扩展名，
// 都不确定时再嗅探 zip 内部特征部件（word/document.xml、xl/worksheets/、ppt/slides/）。
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

// extractWordprocessingText 抽取 Word（.docx 等）正文：以 word/document.xml 为主，
// 并附带页眉/页脚/脚注/尾注/批注等部件（去重排序后拼接）。每个部件里把 <w:t> 视为文本元素、
// <w:p> 段落视为换行边界（见 extractXMLText），段落间用空行分隔。
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

// extractPresentationText 抽取 PowerPoint（.pptx 等）文本：找出所有 ppt/slides/slideN.xml，
// 按页号 N 升序，逐页抽取 <a:t> 文本（<a:p> 段落作换行），页间用空行分隔，保持幻灯片阅读顺序。
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

// extractSpreadsheetText 抽取 Excel（.xlsx 等）文本：
//   - 先读共享字符串表 xl/sharedStrings.xml（Excel 把重复文本集中存这里，单元格只存索引）；
//   - 再按名遍历各 worksheet，逐行逐格解析，行内单元格用制表符分隔、行间换行（近似表格结构）；
//   - 若 worksheet 抽不出内容但有共享字符串，则退而拼接共享字符串，尽量不丢文本。
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

// readZipPart 按精确名从 zip 里读取一个部件；不存在时返回 (nil,false,nil)。
func readZipPart(reader *zip.Reader, name string) ([]byte, bool, error) {
	for _, file := range reader.File {
		if file.Name == name {
			data, err := readZipFile(file)
			return data, true, err
		}
	}
	return nil, false, nil
}

// readZipFile 读取并解压一个 zip 部件，带 maxOOXMLPartBytes 上限保护（防 zip 炸弹）。
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

// extractXMLText 是通用的 XML 文本抽取器（Word/PPT 共用）：流式扫描 XML token，
// 只在处于 textElements（如 "t"）内部时收集字符数据（textDepth 计数支持嵌套）；
// 遇到 <tab>/<br> 分别输出制表符/换行；遇到 lineBreakElements（如段落 "p"）结束时补一个换行。
// 用命名空间无关的 Name.Local 匹配，兼容 w:/a: 等不同前缀。
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

// extractSharedStrings 解析 Excel 共享字符串表：每个 <si> 是一个字符串项，
// 其内部可能有多个 <t>（富文本分段），拼接后作为一条按出现顺序返回，
// 供单元格通过 <c t="s"> 的索引引用。
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

// extractWorksheetText 解析单个 worksheet 的行列文本：
// 逐个 <c> 单元格根据类型（t 属性）取值——<v> 里可能是共享字符串索引/数字/布尔，
// inlineStr 类型则读 <is><t>；每行的单元格用制表符连接，非空行用换行连接，近似还原表格。
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

// decodeWorksheetCell 按单元格类型解析其显示文本：
//   - "s"：value 是共享字符串索引，回查 sharedStrings；
//   - "inlineStr"：直接用内联文本；
//   - "b"：布尔值 1/0 转为 TRUE/FALSE；
//   - 其他（数字/公式结果等）：原样返回 value。
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

// attrValue 按 Local 名取 XML 属性值（忽略命名空间前缀），取不到返回空串。
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
