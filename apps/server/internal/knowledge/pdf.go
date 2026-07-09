package knowledge

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/hex"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// PDF 内置解析所用的正则（仅在没有 pdftotext 时的回退路径使用）：
//   - pdfStreamPattern：匹配 PDF 内 stream...endstream 之间的原始（通常被压缩的）内容流；
//   - pdfActualTextHex/LitPattern：抓取 /ActualText 标注的实际文本（十六进制串或字面量串两种写法），
//     这是带无障碍/复制文本标注的 PDF 里最可靠的可读文本来源；
//   - pdfInfoTitlePattern：从文档信息字典抓 /Title 作为标题补充。
var (
	pdfStreamPattern        = regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	pdfActualTextHexPattern = regexp.MustCompile(`(?s)/ActualText\s*<([0-9A-Fa-f\s]+)>`)
	pdfActualTextLitPattern = regexp.MustCompile(`(?s)/ActualText\s*\((.*?)\)`)
	pdfInfoTitlePattern     = regexp.MustCompile(`(?s)/Title\s*(?:<([0-9A-Fa-f\s]+)>|\((.*?)\))`)
)

// limitedPDFExtractionNotice 是无法抽取文本层时写入的占位提示：
// 该 PDF 很可能是扫描件，需要 OCR。此提示也是后续判断“是否需重抽取/是否可删原文件”的标记。
const limitedPDFExtractionNotice = "PDF document imported, but no extractable text layer was found. This file likely needs OCR before it can be indexed usefully."

// extractPDFText 抽取 PDF 文本，采用“外部工具优先、内置解析兜底”的两级策略：
//  1. 首选调用系统 pdftotext（-layout 保留版式、UTF-8 输出），文本足够（>=20 个有效字符）即采用；
//  2. 否则退回内置解析：补一条 /Title（若与文件名不同），再从内容流里抓 /ActualText；
//  3. 内置解析仍抽不到可用文本时，写入 limitedPDFExtractionNotice 占位（大概率是扫描件需 OCR）。
// 输出统一以 "# <文件名>" 作为标题起头，并做去重/去零宽字符归一化。
func extractPDFText(ctx context.Context, data []byte, name string, path string) string {
	parts := []string{"# " + name}
	if text := extractPDFWithPdftotext(ctx, path); usefulRuneCount(text) >= 20 {
		parts = append(parts, text)
		return normalizeExtractedPDFText(strings.Join(parts, "\n\n"))
	}
	if title := extractPDFInfoTitle(data); strings.TrimSpace(title) != "" && !strings.Contains(name, title) {
		parts = append(parts, "Title: "+title)
	}
	extracted := extractPDFTextFromStreams(data)
	if isUsefulExtractedPDFText(extracted) {
		parts = append(parts, extracted)
	} else {
		parts = append(parts, limitedPDFExtractionNotice)
	}
	return normalizeExtractedPDFText(strings.Join(parts, "\n\n"))
}

// extractPDFWithPdftotext 调用系统 pdftotext 把 PDF 转为纯文本（保留版式、UTF-8）。
// 未安装 pdftotext 或执行失败时返回空串，交由内置解析兜底。
func extractPDFWithPdftotext(ctx context.Context, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !pdftotextAvailable() {
		return ""
	}
	cmd := exec.CommandContext(ctx, "pdftotext", "-layout", "-enc", "UTF-8", path, "-")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	return out.String()
}

// pdftotextAvailable 探测系统 PATH 中是否存在 pdftotext（poppler-utils 提供）。
func pdftotextAvailable() bool {
	_, err := exec.LookPath("pdftotext")
	return err == nil
}

// extractPDFInfoTitle 从 PDF 信息字典的 /Title 抽标题，支持十六进制串与字面量串两种编码。
func extractPDFInfoTitle(data []byte) string {
	if match := pdfInfoTitlePattern.FindSubmatch(data); len(match) == 3 {
		if len(match[1]) > 0 {
			return decodePDFHexString(string(match[1]))
		}
		if len(match[2]) > 0 {
			return decodePDFLiteralString(string(match[2]))
		}
	}
	return ""
}

// extractPDFTextFromStreams 是内置解析的核心：遍历所有 stream，先 zlib 解压（FlateDecode），
// 再从解压后的内容里抓 /ActualText 标注的文本。这条路只能拿到带 ActualText 标注的文本，
// 不做完整的 PDF 文本布局解析，仅作为无 pdftotext 时的尽力兜底。
func extractPDFTextFromStreams(data []byte) string {
	values := []string{}
	for _, match := range pdfStreamPattern.FindAllSubmatch(data, -1) {
		stream := bytes.Trim(match[1], "\r\n")
		decoded, ok := inflatePDFStream(stream)
		if !ok {
			continue
		}
		content := string(decoded)
		values = append(values, extractActualText(content)...)
	}
	return strings.Join(values, "\n")
}

// inflatePDFStream 用 zlib（PDF 的 FlateDecode）解压一个内容流；非 zlib 流返回 (nil,false)。
func inflatePDFStream(data []byte) ([]byte, bool) {
	reader, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	defer reader.Close()
	decoded, err := io.ReadAll(reader)
	if err != nil {
		return nil, false
	}
	return decoded, true
}

// extractActualText 从一段解压后的内容里抓取所有 /ActualText（十六进制与字面量两种写法），
// 逐条解码并过滤掉无意义的碎片（isUsefulPDFText）。
func extractActualText(content string) []string {
	values := []string{}
	for _, match := range pdfActualTextHexPattern.FindAllStringSubmatch(content, -1) {
		if text := decodePDFHexString(match[1]); isUsefulPDFText(text) {
			values = append(values, text)
		}
	}
	for _, match := range pdfActualTextLitPattern.FindAllStringSubmatch(content, -1) {
		if text := decodePDFLiteralString(match[1]); isUsefulPDFText(text) {
			values = append(values, text)
		}
	}
	return values
}

// decodePDFHexString 解码 PDF 十六进制字符串，并按内容嗅探编码：
//   - 去空白、奇数位补 0 后 hex 解码；
//   - 识别 UTF-16 BOM（FE FF / FF FE）分别按 BE/LE 解码；
//   - 无 BOM 但形似 UTF-16BE（高字节多为 0）也尝试 BE 解码；
//   - 否则按 UTF-8；仍不行则只保留可打印 ASCII，尽量给出可读文本。
func decodePDFHexString(value string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
	if len(clean)%2 == 1 {
		clean += "0"
	}
	data, err := hex.DecodeString(clean)
	if err != nil || len(data) == 0 {
		return ""
	}
	if len(data) >= 2 && data[0] == 0xFE && data[1] == 0xFF {
		return decodeUTF16BE(data[2:])
	}
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE {
		return decodeUTF16LE(data[2:])
	}
	if looksLikeUTF16BE(data) {
		if text := decodeUTF16BE(data); isUsefulPDFText(text) {
			return text
		}
	}
	if utf8.Valid(data) {
		return string(data)
	}
	return string(bytes.Map(func(r rune) rune {
		if r >= 32 && r <= 126 {
			return r
		}
		return -1
	}, data))
}

// decodePDFLiteralString 解码 PDF 字面量字符串：处理反斜杠转义（\n \r \t \b \f \( \) \\）、
// 行末续行、以及三位八进制字符（\ddd）。若结果带 UTF-16BE BOM 则再按 UTF-16BE 解码。
func decodePDFLiteralString(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if ch != '\\' || i+1 >= len(value) {
			out.WriteByte(ch)
			continue
		}
		i++
		next := value[i]
		switch next {
		case 'n':
			out.WriteByte('\n')
		case 'r':
			out.WriteByte('\r')
		case 't':
			out.WriteByte('\t')
		case 'b':
			out.WriteByte('\b')
		case 'f':
			out.WriteByte('\f')
		case '(', ')', '\\':
			out.WriteByte(next)
		case '\n':
		case '\r':
			if i+1 < len(value) && value[i+1] == '\n' {
				i++
			}
		default:
			if next >= '0' && next <= '7' {
				octal := []byte{next}
				for len(octal) < 3 && i+1 < len(value) && value[i+1] >= '0' && value[i+1] <= '7' {
					i++
					octal = append(octal, value[i])
				}
				if parsed, err := strconv.ParseInt(string(octal), 8, 32); err == nil {
					out.WriteByte(byte(parsed))
				}
				continue
			}
			out.WriteByte(next)
		}
	}
	text := out.String()
	if strings.HasPrefix(text, "\xfe\xff") {
		return decodeUTF16BE([]byte(text)[2:])
	}
	return text
}

// decodeUTF16BE 把大端 UTF-16 字节序列解码为字符串（奇数尾字节丢弃）。
func decodeUTF16BE(data []byte) string {
	if len(data)%2 == 1 {
		data = data[:len(data)-1]
	}
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		units = append(units, uint16(data[i])<<8|uint16(data[i+1]))
	}
	return string(utf16.Decode(units))
}

// decodeUTF16LE 把小端 UTF-16 字节序列解码为字符串（奇数尾字节丢弃）。
func decodeUTF16LE(data []byte) string {
	if len(data)%2 == 1 {
		data = data[:len(data)-1]
	}
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		units = append(units, uint16(data[i])|uint16(data[i+1])<<8)
	}
	return string(utf16.Decode(units))
}

// looksLikeUTF16BE 启发式判断字节序列是否像 UTF-16BE：
// 统计“高字节为 0、低字节为可见字符”的偶数位比例，达到约 1/2 即认为像 BE 编码的 ASCII 文本。
func looksLikeUTF16BE(data []byte) bool {
	if len(data) < 4 || len(data)%2 != 0 {
		return false
	}
	zeroHigh := 0
	for i := 0; i+1 < len(data); i += 2 {
		if data[i] == 0 && data[i+1] >= 32 {
			zeroHigh++
		}
	}
	return zeroHigh >= len(data)/4
}

// normalizeExtractedPDFText 清洗抽取文本：删零宽空格、逐行去首尾空白、
// 折叠连续空行、并去掉与上一行完全相同的重复行（PDF 常出现的页眉页脚重复）。
func normalizeExtractedPDFText(value string) string {
	value = strings.ReplaceAll(value, "\u200b", "")
	lines := strings.Split(value, "\n")
	out := []string{}
	previous := ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
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

// isUsefulPDFText 判断单条抽取片段是否有意义：非空、不含 U+FFFD 乱码、
// 且至少含 2 个有效字符（字母/数字/汉字）。用于过滤解码出的碎片。
func isUsefulPDFText(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.ContainsRune(value, utf8.RuneError) {
		return false
	}
	return usefulRuneCount(value) >= 2
}

// isUsefulExtractedPDFText 判断整段内置抽取结果是否值得采用（而非退回占位提示）：
// 非空、无乱码、有效字符 >=2；当非空白字符较多（>=12）时还要求有效字符占比 >=35%，
// 以剔除大量符号/噪声但缺乏真实文字的“伪文本”。
func isUsefulExtractedPDFText(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsRune(value, utf8.RuneError) {
		return false
	}
	total := 0
	useful := 0
	for _, r := range value {
		if unicode.IsSpace(r) {
			continue
		}
		total++
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Han, r) {
			useful++
		}
	}
	if useful < 2 {
		return false
	}
	if total >= 12 && float64(useful)/float64(total) < 0.35 {
		return false
	}
	return true
}

// usefulRuneCount 统计有效字符数（字母/数字/汉字），是全模块判断“有无实质文本”的通用度量。
func usefulRuneCount(value string) int {
	count := 0
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Han, r) {
			count++
		}
	}
	return count
}
