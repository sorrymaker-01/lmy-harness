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

var (
	pdfStreamPattern        = regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	pdfActualTextHexPattern = regexp.MustCompile(`(?s)/ActualText\s*<([0-9A-Fa-f\s]+)>`)
	pdfActualTextLitPattern = regexp.MustCompile(`(?s)/ActualText\s*\((.*?)\)`)
	pdfInfoTitlePattern     = regexp.MustCompile(`(?s)/Title\s*(?:<([0-9A-Fa-f\s]+)>|\((.*?)\))`)
)

const limitedPDFExtractionNotice = "PDF document imported, but no extractable text layer was found. This file likely needs OCR before it can be indexed usefully."

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

func pdftotextAvailable() bool {
	_, err := exec.LookPath("pdftotext")
	return err == nil
}

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

func usefulRuneCount(value string) int {
	count := 0
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Han, r) {
			count++
		}
	}
	return count
}
