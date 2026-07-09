package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"

	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/shared"
)

// 父子分层切块的 token 预算（token 数由 approxTokenCount 近似估算）：
//   - childTargetTokens：child chunk 的“目标”大小，累计达到即尝试收口一个 child；
//   - childMaxTokens：child chunk 的“硬上限”，再加下一块会超限则先收口，且单个语义块
//     超过此值会被按句子进一步拆分（见 splitOversizedBlocks）；
//   - parentTargetTokens：parent chunk 的目标大小，一个 parent 聚合若干连续 child。
// 之所以采用“小 child 精确召回 + 大 parent 补全上下文”的两层结构：child 越小语义越集中、
// 向量/关键词召回越精准；但小片段喂给 LLM 上下文不足，故命中后展开成 parent 提供完整语境。
const (
	childTargetTokens  = 520
	childMaxTokens     = 760
	parentTargetTokens = 1800
)

// markdownHeadingPattern 匹配 Markdown 标题行（# ~ ######），用于识别语义边界并维护标题层级路径。
var markdownHeadingPattern = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)

// chunkDocument 是切块的总入口：把一篇文档的纯文本切成父/子两层 chunk 草稿。
// 流程：
//  1. semanticBlocks 先按标题/空行把正文切成语义块，并对超大块按句子二次拆分；
//  2. buildChildChunks 把语义块贪心聚合成目标大小的 child；
//  3. buildParentChunks 再把连续 child 聚合成 parent，并用 child_start/child_end 记录覆盖范围；
//  4. 回填每个 child 的 ParentID（据 parent 覆盖区间反查）以及 PrevID/NextID 邻接链。
// 返回的切片中 parent 在前、child 在后。
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
	// 建立 child.Index -> parentID 的映射：每个 parent 在 Metadata 里记了它覆盖的
	// child 序号区间 [child_start, child_end]，据此把区间内每个 child 指回该 parent。
	parentForChild := map[int]string{}
	for _, parent := range parents {
		for index := parent.Metadata["child_start"].(int); index <= parent.Metadata["child_end"].(int); index++ {
			parentForChild[index] = parent.ID
		}
	}
	// 回填 child 的父引用与前后邻接指针（邻接链便于将来做相邻上下文扩展）。
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

// textBlock 是切块前的“语义块”中间结构：一段连续的正文（段落/标题行），
// 携带其所在的标题层级路径 HeadingPath 及在原文中的 rune 偏移区间。
type textBlock struct {
	Content     string
	HeadingPath string
	StartOffset int
	EndOffset   int
}

// semanticBlocks 把整篇文本切成语义块：
//   - 统一换行符为 \n；
//   - Markdown 标题行既自成一个块，又用 headingStack 维护当前标题层级路径
//     （level 决定回退栈深，从而形成“一级 > 二级 > 三级”的面包屑）；
//   - 空行作为段落分隔，触发 flush 收口当前累积的段落；
//   - offset 以 rune 为单位累加（+1 补回被 Split 去掉的换行），用于精确记录偏移。
// 最后交给 splitOversizedBlocks 把超大块按句子二次拆分。
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
			// 遇标题：先收口上文；再按标题级别裁剪 headingStack（同级/更浅则回退），
			// 压入当前标题名，形成层级路径；标题行本身也作为一个独立块保留。
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

// splitOversizedBlocks 把 token 数超过 childMaxTokens 的语义块按句子切细：
// 逐句累加，一旦再加一句会超过 childTargetTokens 就收口成一个子块，
// 保证没有单个语义块大到无法塞进一个 child chunk。未超限的块原样保留。
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

// splitSentences 按句末标点（中英文的 . ! ? 。！？ 及换行）切句，标点保留在句尾。
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

// buildChildChunks 把语义块贪心聚合成 child chunk（用于精确召回，是唯一做向量化的层级）：
// 累加块直到 token 数达到 childTargetTokens 即收口；若加入下一块会超过 childMaxTokens
// 则先收口再放入新块。每个 child 用块间的 "\n\n" 拼接，取首块的标题路径作为 HeadingPath，
// 并计算 token 数与内容哈希（去重用）。ID 前缀 "chk"。
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

// buildParentChunks 把连续的 child 聚合成 parent chunk（用于命中后展开长上下文，不做向量化）：
// 从 start 起累加 child 的 token 数直到接近 parentTargetTokens，形成一个覆盖区间 [start,end)。
// 每个 parent 在 Metadata 里记录它覆盖的 child 序号区间 child_start/child_end，
// 供 chunkDocument 回填 child->parent 映射；偏移区间取首 child 起点到末 child 终点。ID 前缀 "parent"。
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

// approxTokenCount 近似估算 token 数（无需依赖具体分词器）：
// 英文/数字按“单词”计数（连续字母数字算一个词），中文（汉字 Han）按每 2 字约 1 token 折算。
// 这是切块预算控制用的启发式估计，不追求与真实 tokenizer 完全一致。
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

// contentHash 计算 chunk 内容的 SHA-256（十六进制），用于跨 chunk 去重与
// 检索结果的多样性去重（见 diversifyRetrievedChunks）。哈希前先做归一化。
func contentHash(value string) string {
	normalized := normalizeContentForHash(value)
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// normalizeContentForHash 归一化内容用于哈希：转小写、按空白折叠成单空格，
// 使仅空白/大小写不同的内容得到相同哈希，从而被判为重复。
func normalizeContentForHash(value string) string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(value)))
	return strings.Join(fields, " ")
}
