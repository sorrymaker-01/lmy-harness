---
name: tear-sheet
description: "使用S&P Capital IQ数据通过Kensho LLM-ready API MCP服务器生成专业的公司财务摘要表。当用户请求财务摘要表、公司单页、公司简介、情况说明书、公司快照或公司概览文档时使用此技能 — 尤其是当他们提到特定公司名称或股票代码时。当用户请求股票研究摘要、并购公司简介、企业发展目标简介、销售/业务发展会议准备文档或任何简洁的单一公司财务摘要时也会触发。此技能支持四种受众类型：股票研究、投资银行/并购、企业发展和销售/业务发展。如果用户未指定受众，请询问。适用于上市公司和私人公司。"
---

# 财务摘要表生成器

通过S&P Global MCP工具从S&P Capital IQ获取实时数据，并将结果格式化为专业的Word文档，生成针对特定受众的公司财务摘要表。

## 样式配置

这些是合理的默认设置。要为您公司的品牌定制，请修改本节 — 常见更改包括更换调色板、更改字体（Calibri是许多银行的标准字体）以及更新免责声明文本。

**颜色：**
- 主要（标题横幅背景，章节标题文本）：#1F3864
- 强调（签名部分高亮）：#2E75B6
- 表格标题行填充：#D6E4F0
- 表格交替行填充：#F2F2F2
- 表格边框：#CCCCCC
- 标题横幅文本：#FFFFFF

**排版（docx-js使用半磅为单位）：**
- 字体系列：Arial
- 公司名称：18pt粗体（大小：36）
- 章节标题：11pt粗体（大小：22），主要颜色
- 正文：9pt（大小：18）
- 表格文本：8.5pt（大小：17）
- 页脚/免责声明：7pt斜体（大小：14）
- 每个模板的覆盖在每个参考文件的格式说明中指定。

**公司标题横幅：**
- 标题是一个深蓝色（#1F3864）横幅，跨越整个页面宽度，公司名称为白色。
- **在横幅下方，键值对必须在一个跨越整个页面宽度的两列无边框表格中呈现。** 左列：公司标识符（股票代码、总部、成立年份、员工人数、行业）。右列：财务标识符（市值、企业价值、股价、流通股数）。每个单元格包含一个粗体标签和同一行的常规权重值（例如，"**市值** $124.7B"）。不要将所有字段左对齐在单个列中 — 这会浪费水平空间并看起来不专业。两列布局是区分专业财务摘要表与默认文档的最重要视觉信号。
  - **实现：** 创建一个2列表格，所有单元格的`borders: none`和`shading: none`。将列宽设置为各50%。将左列字段（股票代码、总部、成立年份、员工人数）作为左单元格中的单独段落。将右列字段（市值、企业价值、股价、流通股数）放在右单元格中。每个字段是一个单独的段落：标签为粗体运行，值为常规运行。
  - 每列中的具体字段因受众而异 — 请参阅参考文件的标题规范。原则始终是：在页面上展开，而不是左对齐。
- **不要为标题键值块使用带边框的表格。** 带边框的表格仅用于财务数据。
- 标题中的关键指标（市值、企业价值、股价）应显示为内联键值对，而不是在单独的带边框表格中。

**章节标题：**
- 每个章节标题下方直接有一条水平规则（细线，#CCCCCC，0.5pt），以在章节之间创建清晰的视觉分隔。
- **将规则渲染为标题段落本身的下边框** — 不要为规则插入单独的段落元素。单独的段落会添加自己的前后间距，并在章节标题下方导致过多的空白。
- **实现：** 在docx-js中，通过`paragraph.borders.bottom = { style: BorderStyle.SINGLE, size: 1, color: "CCCCCC" }`将下边框应用于章节标题段落。不要使用`doc.addParagraph()`和单独的水平规则元素。不要使用`thematicBreak`。边框必须在标题段落本身上，后面有0pt间距，以便规则紧贴标题文本。
- 间距：标题段前12pt，标题段后0pt，下一个内容元素前4pt。

**项目符号格式：**
- 所有摘要表类型的所有项目符号内容使用单个项目符号字符（•）。不要在摘要表内部或之间混合使用•、-、▸或编号列表。
- **综合/分析项目符号**（盈利亮点、战略契合度、整合考虑因素、对话启动器）：缩进块样式格式，左缩进360 DXA（0.25"），项目符号字符悬挂缩进。这些应该在视觉上与正文偏移 — 它们是解释性内容，应该看起来与数据表格和散文段落不同。
- **关系部分中的信息性项目符号**：标准正文缩进（180 DXA），无悬挂缩进。
- **不要对任何项目符号部分应用左边框强调。** 左边框样式在docx-js中渲染不一致，会产生视觉伪影。使用缩进和文本大小差异来区分签名部分。

**表格（仅财务数据）：**
- 标题行：表格标题填充（#D6E4F0），粗体深色文本
- 正文行：交替白色 / 表格交替填充（#F2F2F2）
- 边框：表格边框颜色（#CCCCCC），细（BorderStyle.SINGLE，大小1）
- 单元格填充：上下40 DXA，左右80 DXA
- 所有数字列右对齐
- 始终使用ShadingType.CLEAR（永远不要SOLID — SOLID会导致黑色背景）

**布局：**
- US Letter纵向，0.75"边距（所有边1080 DXA）

**数字格式：**
- 货币：USD。使用百万，除非公司收入 > $50B（然后使用十亿，一位小数）。在列标题中标记单位（例如，"收入（$M）"），而不是在单个单元格中。
- **表格单元格：带逗号的纯数字，无美元符号。** 例如：收入单元格显示"4,916"而不是"$4,916"。列标题带有单位。
- 财年：实际年份（FY2022，FY2023，FY2024），永远不要使用相对标签（FY-2，FY-1）。
- 负数：括号，例如（2.3%）
- 百分比：一位小数
- 大数字：使用逗号作为千位分隔符

**页脚（文档页脚，不是内联）：**
将来源归因和免责声明放在实际文档页脚（在每页重复）中，而不是作为底部的内联正文文本。页脚在每页上正好两行，居中：
- 第1行："Data: S&P Capital IQ via Kensho | Analysis: AI-generated | [Month Day, Year]"
- 第2行："For informational purposes only. Not investment advice."
- 样式：7pt斜体，居中，#666666文本颜色
- 对于同一家公司的所有摘要表类型，此页脚文本必须相同。不要按受众更改措辞。
- **此页脚在每个摘要表、每种受众类型、每页上都是必需的。** 不要省略。

## 组件函数

**您必须使用这些确切的函数来创建文档元素。不要编写自定义docx-js样式代码。** 将这些函数复制到生成的Node脚本中并调用它们。上面的样式配置散文仍然作为文档；这些函数是执行机制。

```javascript
const docx = require("docx");
const {
  Document, Paragraph, TextRun, Table, TableRow, TableCell,
  WidthType, AlignmentType, BorderStyle, ShadingType,
  Header, Footer, PageNumber, HeadingLevel, TableLayoutType,
  convertInchesToTwip
} = docx;

// ── 颜色常量 ──
const COLORS = {
  PRIMARY: "1F3864",
  ACCENT: "2E75B6",
  TABLE_HEADER_FILL: "D6E4F0",
  TABLE_ALT_ROW: "F2F2F2",
  TABLE_BORDER: "CCCCCC",
  HEADER_TEXT: "FFFFFF",
  FOOTER_TEXT: "666666",
};

const FONT = "Arial";

// ── 1. createHeaderBanner ──
// 返回docx元素数组：[横幅段落，键值表格]
function createHeaderBanner(companyName, leftFields, rightFields) {
  // leftFields / rightFields: { label: string, value: string } 数组
  const banner = new Paragraph({
    children: [
      new TextRun({
        text: companyName,
        bold: true,
        size: 36, // 18pt
        color: COLORS.HEADER_TEXT,
        font: FONT,
      }),
    ],
    shading: { type: ShadingType.CLEAR, color: "auto", fill: COLORS.PRIMARY },
    spacing: { after: 0 },
    alignment: AlignmentType.LEFT,
  });

  function buildCellParagraphs(fields) {
    return fields.map(
      (f) =>
        new Paragraph({
          children: [
            new TextRun({ text: f.label + "  ", bold: true, size: 18, font: FONT }),
            new TextRun({ text: f.value, size: 18, font: FONT }),
          ],
          spacing: { after: 40 },
        })
    );
  }

  const noBorder = { style: BorderStyle.NONE