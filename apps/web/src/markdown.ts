/**
 * markdown.ts —— 自研的轻量 Markdown 渲染器（零依赖）。
 *
 * 设计目标：
 * - 不引入 marked/markdown-it 等第三方库，保持前端零依赖、可直接以原生 ES Module 加载；
 * - 覆盖聊天场景常用语法：标题、段落、有序/无序列表、任务列表、围栏代码块、
 *   行内代码、粗体/斜体/删除线、链接、引用块、表格、水平分割线；
 * - XSS 安全：所有用户/模型产生的文本在拼接 HTML 前都会经过 escapeHTML 转义，
 *   链接 href 只允许 http/https 协议并额外做属性转义，从源头阻断脚本注入。
 */

/**
 * 对外唯一入口：把 Markdown 文本渲染进指定容器。
 * 直接覆盖 container.innerHTML，调用方负责传入干净的容器元素。
 * @param container 目标 DOM 容器
 * @param markdown  Markdown 原文（允许 null/undefined，视为空串）
 */
export function renderMarkdown(container: HTMLElement, markdown: string | null | undefined): void {
  container.innerHTML = markdownToHTML(markdown || "");
}

/**
 * 块级解析主函数：逐行扫描 Markdown，按“状态机 + 缓冲区”的方式生成 HTML。
 *
 * 实现思路（为什么这样做）：
 * - 维护 paragraph / list / orderedList / codeLines 四个缓冲区，遇到能中断当前块的
 *   语法（空行、标题、代码围栏、表格、引用等）时先 flush 已有缓冲，保证块与块的边界正确；
 * - 代码围栏（```lang）内部的内容原样收集、不做任何 Markdown 解析，仅做 HTML 转义；
 * - 引用块（> ...）把连续的引用行收集后递归调用 markdownToHTML，从而支持引用内嵌套
 *   标题、列表等块级语法；
 * - 表格通过“当前行含 | 且下一行是分隔行（---|---）”的先行探测判定，避免误伤普通文本。
 */
function markdownToHTML(markdown: string): string {
  // 统一换行符后按行切分，后续所有块级判断都以“行”为单位
  const lines = markdown.replace(/\r\n/g, "\n").split("\n");
  const html: string[] = [];
  let paragraph: string[] = [];
  let list: string[] = [];
  let orderedList: string[] = [];
  let inCode = false;
  let codeLang = "";
  let codeLines: string[] = [];

  // 段落缓冲：把累积的行合并为一个 <p>，行内语法交给 inlineMarkdown 处理
  const flushParagraph = () => {
    if (paragraph.length === 0) return;
    html.push(`<p>${inlineMarkdown(paragraph.join("\n"))}</p>`);
    paragraph = [];
  };
  // 列表缓冲：无序/有序列表分别输出 <ul>/<ol>，列表项支持任务列表语法（renderListItem）
  const flushList = () => {
    if (list.length > 0) {
      html.push(`<ul>${list.map((item) => `<li>${renderListItem(item)}</li>`).join("")}</ul>`);
      list = [];
    }
    if (orderedList.length > 0) {
      html.push(`<ol>${orderedList.map((item) => `<li>${renderListItem(item)}</li>`).join("")}</ol>`);
      orderedList = [];
    }
  };
  // 代码块缓冲：内容整体转义后放入 <pre><code>，语言名写入 class 便于外部高亮
  const flushCode = () => {
    html.push(`<pre><code${codeLang ? ` class="language-${escapeHTML(codeLang)}"` : ""}>${escapeHTML(codeLines.join("\n"))}</code></pre>`);
    inCode = false;
    codeLang = "";
    codeLines = [];
  };

  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index];
    // 代码围栏开关：``` 或 ```lang；进入围栏前先 flush 段落/列表，避免块混淆
    const fence = line.match(/^```([A-Za-z0-9_-]*)\s*$/);
    if (fence) {
      if (inCode) {
        flushCode();
      } else {
        flushParagraph();
        flushList();
        inCode = true;
        codeLang = fence[1] || "";
      }
      continue;
    }
    // 围栏内部：原样收集，不解析任何 Markdown 语法
    if (inCode) {
      codeLines.push(line);
      continue;
    }
    // 空行：段落与列表的自然分隔
    if (!line.trim()) {
      flushParagraph();
      flushList();
      continue;
    }
    // 水平分割线：--- / *** / ___（允许穿插空格）
    if (/^\s{0,3}([-*_])(?:\s*\1){2,}\s*$/.test(line)) {
      flushParagraph();
      flushList();
      html.push("<hr>");
      continue;
    }
    // 表格：需要先行探测下一行是否为对齐分隔行（---|---），成立才按表格整体消费
    if (isTableStart(lines, index)) {
      flushParagraph();
      flushList();
      const table = collectTable(lines, index);
      html.push(renderTable(table.rows));
      index = table.nextIndex - 1;
      continue;
    }
    // 引用块：收集连续的 > 行后递归解析，从而支持引用内的嵌套块级语法
    const quote = line.match(/^\s{0,3}>\s?(.*)$/);
    if (quote) {
      flushParagraph();
      flushList();
      const quoteLines = [quote[1]];
      while (index + 1 < lines.length) {
        const next = lines[index + 1].match(/^\s{0,3}>\s?(.*)$/);
        if (!next) break;
        quoteLines.push(next[1]);
        index += 1;
      }
      html.push(`<blockquote>${markdownToHTML(quoteLines.join("\n"))}</blockquote>`);
      continue;
    }
    // 标题：# ~ ######，级别由 # 数量决定
    const heading = line.match(/^(#{1,6})\s+(.+)$/);
    if (heading) {
      flushParagraph();
      flushList();
      const level = heading[1].length;
      html.push(`<h${level}>${inlineMarkdown(heading[2])}</h${level}>`);
      continue;
    }
    // 无序列表项：- 或 *；与有序列表互斥（切换类型时先 flush 另一种）
    const bullet = line.match(/^\s*[-*]\s+(.+)$/);
    if (bullet) {
      flushParagraph();
      if (orderedList.length > 0) {
        flushList();
      }
      list.push(bullet[1]);
      continue;
    }
    // 有序列表项：1. 2. ...（不解析实际数字，输出交给 <ol> 自动编号）
    const ordered = line.match(/^\s*\d+\.\s+(.+)$/);
    if (ordered) {
      flushParagraph();
      if (list.length > 0) {
        flushList();
      }
      orderedList.push(ordered[1]);
      continue;
    }
    // 其余普通文本行：累积进当前段落
    paragraph.push(line.trim());
  }
  // 文档结束：把所有未闭合的缓冲区（未闭合围栏也容错输出）全部落地
  if (inCode) flushCode();
  flushParagraph();
  flushList();
  return html.join("");
}

/**
 * 渲染单个列表项：识别 GFM 任务列表语法 "[ ]"/"[x]"，
 * 渲染成禁用的 checkbox（只读展示，不可交互）；普通项走行内解析。
 */
function renderListItem(item: string): string {
  const task = item.match(/^\[( |x|X)]\s+(.+)$/);
  if (!task) return inlineMarkdown(item);
  const checked = task[1].toLowerCase() === "x";
  return `<label class="taskItem"><input type="checkbox" disabled${checked ? " checked" : ""}> <span>${inlineMarkdown(task[2])}</span></label>`;
}

/** 判断当前行是否为表格起始：本行含 "|"，且下一行是 GFM 分隔行（如 |---|:---:|）。 */
function isTableStart(lines: string[], index: number): boolean {
  if (index + 1 >= lines.length) return false;
  if (!lines[index].includes("|")) return false;
  return /^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)+\|?\s*$/.test(lines[index + 1]);
}

/**
 * 收集一张完整表格：表头行 + 跳过分隔行 + 连续的数据行（直到出现不含 "|" 的行或空行）。
 * 返回单元格二维数组和下一个未消费行的下标，供主循环跳转。
 */
function collectTable(lines: string[], startIndex: number): { rows: string[][]; nextIndex: number } {
  const rows: string[][] = [splitTableRow(lines[startIndex])];
  let index = startIndex + 2;
  while (index < lines.length && lines[index].includes("|") && lines[index].trim()) {
    rows.push(splitTableRow(lines[index]));
    index += 1;
  }
  return { rows, nextIndex: index };
}

/** 拆分表格行：去掉首尾竖线后按 "|" 切分并 trim 每个单元格。 */
function splitTableRow(line: string): string[] {
  return line
    .trim()
    .replace(/^\|/, "")
    .replace(/\|$/, "")
    .split("|")
    .map((cell) => cell.trim());
}

/**
 * 输出表格 HTML：第一行为表头，其余为数据行；
 * 数据行按表头列数对齐（缺列补空），外层包 .tableScroller 以支持横向滚动。
 */
function renderTable(rows: string[][]): string {
  if (rows.length === 0) return "";
  const header = rows[0];
  const body = rows.slice(1);
  const headHTML = `<thead><tr>${header.map((cell) => `<th>${inlineMarkdown(cell)}</th>`).join("")}</tr></thead>`;
  const bodyHTML = `<tbody>${body.map((row) => `<tr>${header.map((_cell, index) => `<td>${inlineMarkdown(row[index] || "")}</td>`).join("")}</tr>`).join("")}</tbody>`;
  return `<div class="tableScroller"><table>${headHTML}${bodyHTML}</table></div>`;
}

/**
 * 行内语法解析：粗体、斜体、删除线、行内代码、链接、换行。
 *
 * 处理顺序是安全性的关键：
 * 1. 先把行内代码 `code` 抽出为占位符（@@CODE_n@@），防止代码内容被后续
 *    加粗/斜体等正则误伤，同时代码内容单独转义；
 * 2. 再对剩余文本整体做 escapeHTML —— 因此后续正则替换产生的标签
 *    （<strong>/<em>/<a> 等）都是解析器自己生成的，用户输入无法注入 HTML；
 * 3. 链接仅匹配 http/https 协议，href 再经 escapeAttribute 转义，
 *    并强制 target=_blank + rel=noreferrer，杜绝 javascript: 伪协议与 opener 泄露；
 * 4. 最后把代码占位符还原回 <code> 片段。
 */
function inlineMarkdown(value: string): string {
  const codeTokens: string[] = [];
  // 第 1 步：抽出行内代码，避免其内容参与后续任何替换
  let html = value.replace(/`([^`\n]+)`/g, (_match, code: string) => {
    const token = `@@CODE_${codeTokens.length}@@`;
    codeTokens.push(`<code>${escapeHTML(code)}</code>`);
    return token;
  });
  // 第 2 步：整体 HTML 转义（XSS 防线），之后的标签均由解析器生成
  html = escapeHTML(html);
  html = html.replace(/\n/g, "<br>");
  html = html.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
  html = html.replace(/__([^_]+)__/g, "<strong>$1</strong>");
  html = html.replace(/~~([^~]+)~~/g, "<del>$1</del>");
  html = html.replace(/(^|[^*])\*([^*\n]+)\*/g, "$1<em>$2</em>");
  html = html.replace(/(^|[^_])_([^_\n]+)_/g, "$1<em>$2</em>");
  // 第 3 步：链接仅允许 http/https，href 做属性级转义，新窗口打开且不带 referrer
  html = html.replace(/\[([^\]]+)]\((https?:\/\/[^)\s]+)\)/g, (_match, label: string, href: string) => {
    return `<a href="${escapeAttribute(href)}" target="_blank" rel="noreferrer">${label}</a>`;
  });
  // 第 4 步：还原行内代码占位符
  html = html.replace(/@@CODE_(\d+)@@/g, (_match, index: string) => codeTokens[Number(index)] || "");
  return html;
}

/** HTML 文本转义：转义 & < > " '，是整个渲染器的 XSS 基础防线。 */
function escapeHTML(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

/** HTML 属性转义：在 escapeHTML 基础上额外转义反引号，用于 href 等属性值。 */
function escapeAttribute(value: string): string {
  return escapeHTML(value).replaceAll("`", "&#96;");
}
