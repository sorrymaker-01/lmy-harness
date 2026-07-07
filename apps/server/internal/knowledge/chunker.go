package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"

	"code.byted.org/ai/lmy/apps/server/internal/shared"
)

const (
	childTargetTokens  = 520
	childMaxTokens     = 760
	parentTargetTokens = 1800
)

var markdownHeadingPattern = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)

func chunkDocument(title string, content string) []chunkDraft {
	blocks := semanticBlocks(content)
	if len(blocks) == 0 {
		trimmed := strings.TrimSpace(content)
		if trimmed == "" {
			return nil
		}
		blocks = []textBlock{{Content: trimmed, StartOffset: 0, EndOffset: len([]rune(content))}}
	}
	children := buildChildChunks(title, blocks)
	if len(children) == 0 {
		return nil
	}
	parents := buildParentChunks(title, children)
	chunks := make([]chunkDraft, 0, len(parents)+len(children))
	chunks = append(chunks, parents...)
	parentForChild := map[int]string{}
	for _, parent := range parents {
		for index := parent.Metadata["child_start"].(int); index <= parent.Metadata["child_end"].(int); index++ {
			parentForChild[index] = parent.ID
		}
	}
	for i := range children {
		children[i].ParentID = parentForChild[children[i].Index]
		if i > 0 {
			children[i].PrevID = children[i-1].ID
		}
		if i+1 < len(children) {
			children[i].NextID = children[i+1].ID
		}
	}
	chunks = append(chunks, children...)
	return chunks
}

type textBlock struct {
	Content     string
	HeadingPath string
	StartOffset int
	EndOffset   int
}

func semanticBlocks(content string) []textBlock {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	blocks := []textBlock{}
	headingStack := []string{}
	var current []string
	blockStart := 0
	offset := 0

	flush := func(endOffset int) {
		text := strings.TrimSpace(strings.Join(current, "\n"))
		if text != "" {
			blocks = append(blocks, textBlock{
				Content:     text,
				HeadingPath: strings.Join(headingStack, " > "),
				StartOffset: blockStart,
				EndOffset:   endOffset,
			})
		}
		current = nil
	}

	for _, line := range lines {
		lineRunes := len([]rune(line)) + 1
		if match := markdownHeadingPattern.FindStringSubmatch(line); len(match) == 3 {
			flush(offset)
			level := len(match[1])
			if len(headingStack) >= level {
				headingStack = headingStack[:level-1]
			}
			headingStack = append(headingStack, strings.TrimSpace(match[2]))
			blocks = append(blocks, textBlock{
				Content:     strings.TrimSpace(line),
				HeadingPath: strings.Join(headingStack, " > "),
				StartOffset: offset,
				EndOffset:   offset + lineRunes,
			})
			offset += lineRunes
			blockStart = offset
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush(offset)
			offset += lineRunes
			blockStart = offset
			continue
		}
		if len(current) == 0 {
			blockStart = offset
		}
		current = append(current, line)
		offset += lineRunes
	}
	flush(offset)
	return splitOversizedBlocks(blocks)
}

func splitOversizedBlocks(blocks []textBlock) []textBlock {
	out := []textBlock{}
	for _, block := range blocks {
		if approxTokenCount(block.Content) <= childMaxTokens {
			out = append(out, block)
			continue
		}
		sentences := splitSentences(block.Content)
		var current []string
		start := block.StartOffset
		for _, sentence := range sentences {
			next := append(append([]string(nil), current...), sentence)
			if len(current) > 0 && approxTokenCount(strings.Join(next, "")) > childTargetTokens {
				text := strings.TrimSpace(strings.Join(current, ""))
				out = append(out, textBlock{Content: text, HeadingPath: block.HeadingPath, StartOffset: start, EndOffset: start + len([]rune(text))})
				start += len([]rune(text))
				current = []string{sentence}
				continue
			}
			current = next
		}
		if len(current) > 0 {
			text := strings.TrimSpace(strings.Join(current, ""))
			out = append(out, textBlock{Content: text, HeadingPath: block.HeadingPath, StartOffset: start, EndOffset: start + len([]rune(text))})
		}
	}
	return out
}

func splitSentences(value string) []string {
	out := []string{}
	var current strings.Builder
	for _, r := range value {
		current.WriteRune(r)
		if r == '.' || r == '!' || r == '?' || r == '。' || r == '！' || r == '？' || r == '\n' {
			if text := current.String(); strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
			current.Reset()
		}
	}
	if text := current.String(); strings.TrimSpace(text) != "" {
		out = append(out, text)
	}
	return out
}

func buildChildChunks(title string, blocks []textBlock) []chunkDraft {
	chunks := []chunkDraft{}
	var current []textBlock
	currentTokens := 0
	flush := func() {
		if len(current) == 0 {
			return
		}
		parts := make([]string, 0, len(current))
		for _, block := range current {
			parts = append(parts, block.Content)
		}
		content := strings.TrimSpace(strings.Join(parts, "\n\n"))
		if content == "" {
			current = nil
			currentTokens = 0
			return
		}
		heading := current[0].HeadingPath
		chunks = append(chunks, chunkDraft{
			ID:          shared.NewID("chk"),
			Type:        "child",
			Index:       len(chunks),
			Title:       title,
			Content:     content,
			HeadingPath: heading,
			StartOffset: current[0].StartOffset,
			EndOffset:   current[len(current)-1].EndOffset,
			TokenCount:  approxTokenCount(content),
			ContentHash: contentHash(content),
			Metadata:    map[string]any{},
		})
		current = nil
		currentTokens = 0
	}
	for _, block := range blocks {
		blockTokens := approxTokenCount(block.Content)
		if len(current) > 0 && currentTokens+blockTokens > childMaxTokens {
			flush()
		}
		current = append(current, block)
		currentTokens += blockTokens
		if currentTokens >= childTargetTokens {
			flush()
		}
	}
	flush()
	return chunks
}

func buildParentChunks(title string, children []chunkDraft) []chunkDraft {
	parents := []chunkDraft{}
	start := 0
	for start < len(children) {
		end := start
		tokens := 0
		for end < len(children) {
			if end > start && tokens+children[end].TokenCount > parentTargetTokens {
				break
			}
			tokens += children[end].TokenCount
			end++
		}
		if end == start {
			end++
		}
		parts := make([]string, 0, end-start)
		for _, child := range children[start:end] {
			parts = append(parts, child.Content)
		}
		content := strings.TrimSpace(strings.Join(parts, "\n\n"))
		parents = append(parents, chunkDraft{
			ID:          shared.NewID("parent"),
			Type:        "parent",
			Index:       len(parents),
			Title:       title,
			Content:     content,
			HeadingPath: children[start].HeadingPath,
			StartOffset: children[start].StartOffset,
			EndOffset:   children[end-1].EndOffset,
			TokenCount:  approxTokenCount(content),
			ContentHash: contentHash(content),
			Metadata: map[string]any{
				"child_start": start,
				"child_end":   end - 1,
			},
		})
		start = end
	}
	return parents
}

func approxTokenCount(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	words := 0
	inWord := false
	han := 0
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			han++
			inWord = false
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if !inWord {
				words++
				inWord = true
			}
			continue
		}
		inWord = false
	}
	return words + (han+1)/2
}

func contentHash(value string) string {
	normalized := normalizeContentForHash(value)
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

func normalizeContentForHash(value string) string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(value)))
	return strings.Join(fields, " ")
}
