---
name: earnings-preview-single
description: 为单个公司生成简洁的4-5页股权研究业绩预览。分析最近的业绩电话会议记录、竞争对手格局、估值和近期新闻，生成专业的HTML报告。
---

# 单公司业绩预览

为单个公司生成简洁、专业的股权研究业绩预览。输出为独立的HTML文件，目标打印页数为4-5页。报告数据密集，叙述紧凑，直击要点。

**数据来源（零例外）：** 唯一允许的数据来源是 **Kensho Grounding MCP**（`search`）和 **S&P Global MCP**（`kfinance`）。绝对禁止使用任何其他工具、数据来源或任何形式的网络访问。具体包括：
- 禁止使用 `WebSearch`、`WebFetch`、`web_search`、`brave_search`、`google_search` 或任何通用网络/互联网搜索工具 — 即使Kensho响应缓慢、无结果或暂时不可用。
- 禁止使用任何浏览器、URL获取或网页抓取工具。
- 如果Kensho Grounding对查询无结果，尝试重新表述查询或在报告中注明"数据不可用"。**绝不要回退到网络搜索作为替代方案。**
- 报告中的每条信息必须可追溯到 `kfinance` MCP函数调用或Kensho `search`调用。如果无法追溯到这两个来源之一，则不得出现在报告中。

**关键规则：** 在撰写报告任何部分之前，必须完成所有研究和数据收集（阶段1-5）。

**中间文件规则：** 所有来自MCP工具调用的原始数据必须在每次工具调用返回后**立即**写入 `/tmp/earnings-preview/` 目录中的文件 — 在进入下一次调用之前。这可以保护数据免受上下文窗口压缩的影响。不要仅将数据保存在内存中。在阶段1开始时，运行 `mkdir -p /tmp/earnings-preview` 创建目录。**在生成HTML报告（阶段7）之前，必须使用 `cat` 命令将所有中间文件读回上下文。文件 — 而非您对早期对话的记忆 — 是报告中每个数字、引用和来源URL的唯一真实来源。如果跳过读取文件，报告将包含错误。**

**财季规则：** 绝不要从日历报告日期推断财季。许多公司有非标准财年（例如，沃尔玛的财年于1月31日结束，因此2026年2月的报告涵盖的是FY2026第四季度，而非2025年第四季度或2026年第一季度）。始终使用 `get_next_earnings_from_identifiers` 或 `get_earnings_from_identifiers` 返回的业绩电话会议名称中所述的财季和财年（例如，"Walmart Q4 FY2026 Earnings Call"意味着季度为Q4 FY2026）。在报告标题、页眉、表格和所有引用中使用该原文表述。如果电话会议名称模糊，请与 `get_financial_line_item_from_identifiers` 的期间标签交叉核对。

**篇幅规则：** 报告必须简洁。目标打印页数为4-5页。不要撰写冗长的多段叙述。使用紧凑有力的项目符号。每句话都必须有其存在的价值。如果能用更少的字表达，就减少字数。

**原文引用规则：** 在 `<blockquote>` 标签中引用管理层时，文本必须从电话会议记录中**逐字**复制 — 包括填充词和句子片段。不要改写、重新排列、合并来自记录不同部分的句子，或"清理"引用。如果在记录中找不到确切措辞，不要将其作为直接引用呈现。相反，用您自己的叙述声音进行改写，不使用块引用格式（例如，"管理层指出数据中心需求仍然显著"）。每个块引用必须是可对照记录验证的逐字复制粘贴摘录。

**计算完整性规则：** 对于任何多步骤计算（从年度指引推导季度数据、LTM P/E、同比增长率、细分市场同比变化），明确写出每一步并在用于下一步之前验证中间结果。如果您陈述A + B + C = X，请在后续公式中使用X之前验证X在算术上是正确的。如果附录显示的总和不等于其所述组成部分，则报告有误。如有疑问，从原始数据重新计算，而非重复使用先前计算的中间值。

**比率命名规则：** 所有估值比率必须明确标注为 **LTM**（最近十二个月）或 **NTM**（未来十二个月）。绝不要使用"trailing"或"forward" — 始终使用LTM或NTM。LTM比率使用最近4个报告季度的总和。NTM比率使用 `get_consensus_estimates_from_identifiers` 中**未来4个季度一致预期EPS均值估计的总和** — 而非单个年度数据。LTM和NTM P/E都必须在竞争对手比较表中计算和显示。

**超链接规则（严格执行）：** 报告中的每个声明 — 数值和非数值 — 必须包裹在指向附录中相应条目的 `<a href="#ref-N" class="data-ref">` 超链接中。**这不是可选的。报告中的每个数字都必须是可点击的链接。** 这包括：收入数据、EPS、利润率、增长率、市值、P/E比率、股票回报、目标价、细分收入和任何其他财务指标。还包括来自电话会议记录或Kensho搜索的定性声明。如果您将其作为事实陈述，它必须链接到来源。为每个唯一声明分配一个顺序引用ID（`ref-1`、`ref-2`等）。超链接样式为微妙 — 海军蓝色，无下划线，悬停时显示点状下划线。**不要在报告正文中写任何数字而不将其包裹在 `<a>` 标签中。** 示例：写 `<a href="#ref-1" class="data-ref">$152.3B</a>`，绝不要写 `$152.3B` 作为纯文本。

---

## 阶段1：公司概况与设置

1. 从 `$ARGUMENTS` 解析单个公司股票代码（去除空白）。
2. 运行 `mkdir -p /tmp/earnings-preview` 创建工作目录。
3. 调用 `get_latest()` 建立当前报告期背景。
4. 调用 `get_info_from_identifiers` — 记录市值、行业。
5. 调用 `get_company_summary_from_identifiers` — 记录业务描述。
6. 调用 `get_next_earnings_from_identifiers` — 记录即将到来的业绩日期和财季名称。

**立即写入** `/tmp/earnings-preview/company-info.txt`：
```
TICKER: [股票代码]
COMPANY: [全名]
INDUSTRY: [行业]
MARKET_CAP: [市值]（截至[日期]）
NEXT_EARNINGS_DATE: [日期]
NEXT_EARNINGS_QUARTER: [Q# FY#### 按API返回的原文]
BUSINESS_DESCRIPTION: [2-3句摘要]
```

---

## 阶段2：业绩电话会议记录分析（强制 — 完成后再撰写）

1. 调用 `get_latest_earnings_from_identifiers` 获取最近已完成的业绩电话会议 `key_dev_id`。
2. 为该记录调用 `get_transcript_from_key_dev_id`。
3. **立即写入** `/tmp/earnings-preview/transcript-extracts.txt`，包含以下部分。在您仍有记录上下文时写入此文件 — 不要等待：

```
TRANSCRIPT_SOURCE: [电话会议名称，如"Q3 2025 Earnings Call"]
KEY_DEV_ID: [key_dev_id]
CALL_DATE: [日期]
FISCAL_QUARTER: [Q# FY####]

=== 原文引用（逐字复制粘贴 — 不要改写） ===
QUOTE_1: "[记录中的确切文本]"
SPEAKER_1: [姓名], [职位]
CONTEXT_1: [1句话说明出现位置 — 准备好的发言或问答]

QUOTE_2: "[记录中的确切文本]"
SPEAKER_2: [姓名], [职位]
CONTEXT_2: [背景]

QUOTE_3: "[记录中的确切文本]"
SPEAKER_3: [姓名], [职位]
CONTEXT_3: [背景]

QUOTE_4: "[记录中的确切文本]"
SPEAKER_4: [姓名], [职位]
CONTEXT_4: [背景]

=== 指引（仅定量） ===
- [指标]: [管理层所述的范围或点估计]
- [指标]: [范围或点估计]

=== 关键驱动因素 ===
- [驱动因素1及支持数据点]
- [驱动因素2及支持数据点]
- [驱动因素3及支持数据点]

=== 逆风因素与风险 ===
- [风险1及量化（如有）]
- [风险2]

=== 分析师问答主题 ===
- [主题1：分析师追问的内容]
- [主题2]
- [主题3]

=== 综合：下季度关注主题 ===
- [主题1]
- [主题2]
- [主题3]
```

---

## 阶段3：竞争对手分析

1. 调用 `get_competitors_from_identifiers`，参数 `competitor_source="all"`。
2. 选择 **5-7个最相关的上市竞争对手**。
3. 为公司及所有选定的竞争对手收集：
   - `get_prices_from_identifiers`，参数 `periodicity="day"`，最近12个月
   - `get_financial_line_item_from_identifiers` 获取 `diluted_eps`，`period_type="quarterly"`，`num_periods=8`
   - `get_capitalization_from_identifiers`，参数 `capitalization="market_cap"`（最新）
   - `get_consensus_estimates_from_identifiers`，参数 `period_type="quarterly"`，`num_periods_forward=4` — 这返回未来4个季度的一致预期EPS均值估计，将其求和计算NTM EPS

**每次工具调用返回后，立即将原始数据追加到相应的中间文件：**

**写入** `/tmp/earnings-preview/prices.csv` — 每行一个(ticker, date, close)。包含带有确切MCP函数调用的 `source` 列。先写入标的公司的价格，然后是每个竞争对手的数据：
```
ticker,date,close,source
D,2025-02-19,55.67,get_prices_from_identifiers(identifier='D',periodicity='day')
D,2025-02-20,55.82,get_prices_from_identifiers(identifier='D',periodicity='day')
...
DUK,2025-02-19,111.79,get_prices_from_identifiers(identifier='DUK',periodicity='day')
...
```
注意：单次调用的所有行的 `source` 值相同 — 每行都写入，以便始终可用。

**写入** `/tmp/earnings-preview/peer-eps.csv` — 每行一个(ticker, period, eps)。每次 `diluted_eps` 调用后立即写入：
```
ticker,period,diluted_eps,source
D,Q4 2024,1.09,get_financial_line_item_from_identifiers(identifier='D',line_item='diluted_eps',period_type='quarterly')
D,Q1 2025,-0.11,get_financial_line_item_from_identifiers(identifier='D',line_item='diluted_eps',period_type='quarterly')
...
DUK,Q4 2024,1.52,get_financial_line_item_from_identifiers(identifier='DUK',line_item='diluted_eps',period_type='quarterly')
...
```

**写入** `/tmp/earnings-preview/peer-market-caps.csv` — 每个ticker一行。每次 `market_cap` 调用后立即写入：
```
ticker,market_cap,retrieval_date,source
D,55900000000,2026-02-19,get_capitalization_from_identifiers(identifier='D',capitalization='market_cap')
DUK,98300000000,2026-02-19,get_capitalization_from_identifiers(identifier='DUK',capitalization='market_cap')
...
```

**写入** `/tmp/earnings-preview/consensus-eps.csv` — 每行一个(ticker, period, consensus mean EPS)。每次 `get_consensus_estimates_from_identifiers` 调用后立即写入：
```
ticker,period,consensus_mean_eps,num_estimates,source
D,Q4 2025,0.88,12,get_consensus_estimates_from_identifiers(identifier='D',period_type='quarterly',num_periods_forward=4)
D,Q1 2026,0.72,10,get_consensus_estimates_from_identifiers(identifier='D',period_type='quarterly',num_periods_forward=4)
D,Q2 2026,0.91,9,get_consensus_estimates_from_identifiers(identifier='D',period_type='quarterly',num_periods_forward=4)
D,Q3 2026,1.05,8,get_consensus_estimates_from_identifiers(identifier='D',period_type='quarterly',num_periods_forward=4)
DUK,Q4 2025,1.48,14,get_consensus_estimates_from_identifiers(identifier='DUK',period_type='quarterly',num_periods_forward=4)
...
```

4. **暂不计算P/E或回报。** 原始数据现已保存在磁盘上。计算在阶段6（验证）中进行，从这些文件读取。

**日期一致性规则（股票回报）：** 计算比较股票回报（年初至今%、1年%、30天%、90天%）时，所有ticker必须使用**完全相同的起始和结束日期**。将所有价格数据写入 `prices.csv` 后，识别出现在所有ticker数据中的第一个交易日期，将其作为共同基准日期。不要为不同ticker使用不同的基准日期（例如，标的公司从2月19日开始，同行从2月28日开始）。如果某个ticker的数据起始日期晚于其他，所有计算使用第一个重叠日期。在附录中为每个回报计算说明共同基准日期。

**P/E货币规则（LTM P/E）：** 计算每个公司的LTM P/E时，使用该公司的**最近4个报告季度**数据（来自 `peer-eps.csv`）— 而非应用于所有公司的固定日历窗口。如果同行已报告Q4 2025而标的公司仅报告至Q3 2025，同行的LTM EPS应包含Q4 2025。检查每个公司的最新报告期间，每个公司使用最近的4个期间。在附录中注明每个P/E计算使用了哪4个季度。

**市值日期戳：** 报告市值时，使用 `peer-market-caps.csv` 中的 `retrieval_date`。如果与报告日期不同，在附录中注明。

---

## 阶段4：新闻、估计与行业情报（通过Kensho Grounding）

对以下**每个**类别运行 `search` 查询。不要跳过任何类别。

**关键 — 捕获来源URL：** 每个Kensho `search` 结果都包含底层文章、报告或数据页面的**来源URL**。您必须将URL与每个发现一起记录。

**每次搜索调用后，立即将结果追加到** `/tmp/earnings-preview/kensho-findings.txt`，使用以下格式。不要等到所有搜索完成 — 每次搜索后立即写入：

```
=== 搜索: "[使用的查询]" ===
DATE_RUN: [今日日期]
CATEGORY: [estimates|analyst_ratings|risks|news|sector]

FINDING_1: [关键发现或摘录]
URL_1: [搜索结果中的来源URL]
SOURCE_1: [出版物名称，如有日期]

FINDING_2: [关键发现或摘录]
URL_2: [来源URL]
SOURCE_2: [出版物名称，日期]

[...继续此搜索的所有相关结果...]
```

**业绩估计与分析师情绪：**
1. `search` 查询 "[TICKER] earnings estimates consensus EPS revenue upcoming quarter"
   - 记录：一致预期EPS、一致预期收入、过去90天估计修正方向。
   - **立即追加到kensho-findings.txt。**
2. `search` 查询 "[TICKER] analyst ratings price target upgrades downgrades"
   - 记录：近期升级/降级、目标价范围、多头/空头论点摘要。
   - **立即追加到kensho-findings.txt。**
3. `search` 查询 "[TICKER] risks bear case concerns investors"
   - 记录：关键辩论、空头论点、即将发布报告的摇摆因素。
   - **立即追加到kensho-findings.txt。**

**近期新闻（强制 — 不要跳过）：**
4. `search` 查询 "[TICKER] [公司名称] recent news developments"
   - 记录：过去60天的重大新闻 — 并购、产品发布、高管变动、监管行动、合作伙伴关系、法律进展、关税或任何可能影响即将发布的业绩或前瞻指引的事件。
   - 对于每条新闻，注明日期、标题、潜在业绩影响。
   - **立即追加到kensho-findings.txt。**

**行业背景：**
5. `search` 查询 "[公司行业/板块] sector outlook trends"
   - 记录：行业层面的顺风/逆风因素、宏观数据、竞争动态。
   - **立即追加到kensho-findings.txt。**

---

## 阶段5：财务数据收集

**季度财务数据（最近8个季度）：**
`get_financial_line_item_from_identifiers`，参数 `period_type="quarterly"`，`num_periods=8`，获取：
`revenue`、`gross_profit`、`operating_income`、`ebitda`、`net_income`、`diluted_eps`

**每个行项目调用返回后，立即追加到** `/tmp/earnings-preview/financials.csv`。按返回的原始值写入 — 暂不四舍五入或转换。包含带有确切MCP函数调用和参数的 `source` 列：
```
ticker,period,line_item,value,source
D,Q4 2024,revenue,3941000000,get_financial_line_item_from_identifiers(identifier='D',line_item='revenue',period_type='quarterly')
D,Q1 2025,revenue,3400000000,get_financial_line_item_from_identifiers(identifier='D',line_item='revenue',period_type='quarterly')
D,Q2 2025,revenue,4076000000,get_financial_line_item_from_identifiers(identifier='D',line_item='revenue',period_type='quarterly')
D,Q3 2025,revenue,3810000000,get_financial_line_item_from_identifiers(identifier='D',line_item='revenue',period_type='quarterly')
D,Q4 2024,diluted_eps,1.09,get_financial_line_item_from_identifiers(identifier='D',line_item='diluted_eps',period_type='quarterly')
D,Q1 2025,diluted_eps,-0.11,get_financial_line_item_from_identifiers(identifier='D',line_item='diluted_eps',period_type='quarterly')
...
```

**暂不计算利润率或增长率。** 仅写入原始数据。计算在阶段6进行。

**细分市场数据：**
- `get_segments_from_identifiers`，参数 `segment_type="business"`，`period_type="quarterly"`，`num_periods=8`
- 您需要8个季度（而非4个），以便有去年同期季度用于同比比较。计算Q3 2025的同比需要Q3 2024 — 即往前第5个季度。**如果API响应中没有去年同期的细分市场数据，不要估计或编造。在报告中注明"同比不可用"。**

**立即写入** `/tmp/earnings-preview/segments.csv`：
```
ticker,period,segment_name,revenue,source
D,Q3 2024,Dominion Energy Virginia,2762000000,get_segments_from_identifiers(identifier='D',segment_type='business',period_type='quarterly')
D,Q3 2024,Dominion Energy South Carolina,848000000,get_segments_from_identifiers(identifier='D',segment_type='business',period_type='quarterly')
D,Q3 2024,Contracted Energy,260000000,get_segments_from_identifiers(identifier='D',segment_type='business',period_type='quarterly')
D,Q3 2025,Dominion Energy Virginia,3311000000,get_segments_from_identifiers(identifier='D',segment_type='business',period_type='quarterly')
D,Q3 2025,Dominion Energy South Carolina,945000000,get_segments_from_identifiers(identifier='D',segment_type='business',period_type='quarterly')
D,Q3 2025,Contracted Energy,297000000,get_segments_from_identifiers(identifier='D',segment_type='business',period_type='quarterly')
...
```

**业绩历史（用于股票图表注释）：**
- `get_earnings_from_identifiers` — 收集12个月价格窗口内的过去业绩日期。
- **立即写入** `/tmp/earnings-preview/earnings-dates.csv`：
```
ticker,earnings_date,call_name,source
D,2025-05-02,Q1 2025 Earnings Call,get_earnings_from_identifiers(identifier='D')
D,2025-08-01,Q2 2025 Earnings Call,get_earnings_from_identifiers(identifier='D')
D,2025-10-31,Q3 2025 Earnings Call,get_earnings_from_identifiers(identifier='D')
...
```

---

## 阶段6：验证与计算（强制 — 不要跳过）

生成报告之前，读回所有中间文件并从干净数据执行计算。此阶段通过从文件而非压缩的对话上下文工作来确保数据完整性。

1. **读取所有中间文件**，使用bash `cat` 命令：
   - `cat /tmp/earnings-preview/company-info.txt`
   - `cat /tmp/earnings-preview/transcript-extracts.txt`
   - `cat /tmp/earnings-preview/financials.csv`
   - `cat /tmp/earnings-preview/segments.csv`
   - `cat /tmp/earnings-preview/prices.csv`
   - `cat /tmp/earnings-preview/peer-eps.csv`
   - `cat /tmp/earnings-preview/peer-market-caps.csv`
   - `cat /tmp/earnings-preview/consensus-eps.csv`
   - `cat /tmp/earnings-preview/kensho-findings.txt`
   - `cat /tmp/earnings-preview/earnings-dates.csv`

2. **从现在上下文中的原始数据计算衍生指标：**
   - 毛利率% = gross_profit / revenue（每季度）
   - 营业利润率% = operating_income / revenue（每季度）
   - 收入同比增长% = (当季收入 - 去年同期收入) / 去年同期收入
   - EPS同比增长% = 同样逻辑；如果基数为负，使用"n.m."
   - 细分市场同比增长% = 按名称匹配细分市场到去年同期季度；如缺失，注明"同比不可用"
   - 每个公司的LTM P/E = 最新价格 / 最近4个季度EPS之和（使用 `peer-eps.csv` 检查每个ticker可用的4个季度）
   - 每个公司的NTM P/E = 最新价格 / NTM EPS，其中 **NTM EPS = `consensus-eps.csv` 中未来4个季度一致预期EPS均值估计之和**。将每个ticker的所有4个季度的consensus_mean_eps值相加。如果某个同行可用的前瞻季度少于4个，标记NTM P/E为"n/a"。在附录中注明求和了哪4个季度。
   - 股票回报（年初至今、1年、30天、90天）= 在 `prices.csv` 中找到**所有ticker的共同起始日期**，然后从该日期计算回报

3. **交叉核对：**
   - 验证每个细分市场同比在 `segments.csv` 中有实际的去年行。如无，标记"同比不可用"。
   - 验证所有股票回报基准日期在所有ticker间相同。
   - 通过重新求和组成部分验证任何多步骤计算（例如，LTM EPS之和匹配4个季度值）。
   - 验证 `transcript-extracts.txt` 中的所有原文引用是确切的复制粘贴（非改写）。

4. **写入** `/tmp/earnings-preview/calculations.csv`，包含所有衍生值：
```
ticker,metric,value,formula,components
D,gross_margin_Q3_2025,32.5%,gross_profit/revenue,"gross_profit=1238100000,revenue=3810000000"
D,revenue_yoy_Q3_2025,+9.3%,(Q3_2025-Q3_2024)/Q3_2024,"Q3_2025=3810000000,Q3_2024=3486000000"
D,ltm_pe,24.2x,price/ltm_eps,"price=65.46,ltm_eps=2.70,quarters=Q4_2024+Q1_2025+Q2_2025+Q3_2025"
D,ntm_pe,18.5x,price/ntm_eps,"price=65.46,ntm_eps=3.56,quarters=Q4_2025(0.88)+Q1_2026(0.72)+Q2_2026(0.91)+Q3_2026(1.05),source=get_consensus_estimates_from_identifiers"
D,yoy_return,+17.6%,(end-start)/start,"end=65.46,start=55.67,base_date=2025-02-19"
DUK,yoy_return,+13.0%,(end-start)/start,"end=126.32,start=111.79,base_date=2025-02-19"
...
```

此文件成为报告中所有数字的唯一真实来源。

---

## 阶段7：生成HTML报告

**停止 — 在撰写任何HTML之前，必须读取所有中间文件。这是阻塞性前置条件。**

这不是可选的。您必须将以下每个 `cat` 命令作为**单独的bash工具调用**运行（而非合并为一个）。这确保每个文件的内容单独加载并在对话中可见。不要将它们合并为单个命令。不要跳过任何文件。

**逐一运行以下命令，每个作为独立的bash调用：**

1. `cat /tmp/earnings-preview/company-info.txt`
2. `cat /tmp/earnings-preview/transcript-extracts.txt`
3. `cat /tmp/earnings-preview/financials.csv`
4. `cat /tmp/earnings-preview/segments.csv`
5. `cat /tmp/earnings-preview/prices.csv`
6. `cat /tmp/earnings-preview/peer-eps.csv`
7. `cat /tmp/earnings-preview/peer-market-caps.csv`
8. `cat /tmp/earnings-preview/consensus-eps.csv`
9. `cat /tmp/earnings-preview/kensho-findings.txt`
10. `cat /tmp/earnings-preview/earnings-dates.csv`
11. `cat /tmp/earnings-preview/calculations.csv`

**读取所有文件后，必须向用户打印摘要消息**，列出每个文件及其状态。使用以下格式：

```
--- 数据文件验证 ---
1. company-info.txt        ✓ 已加载（[N]行）
2. transcript-extracts.txt ✓ 已加载（[N]行）
3. financials.csv          ✓ 已加载（[N]行）
4. segments.csv            ✓ 已加载（[N]行）
5. prices.csv              ✓ 已加载（[N]行）
6. peer-eps.csv            ✓ 已加载（[N]行）
7. peer-market-caps.csv    ✓ 已加载（[N]行）
8. consensus-eps.csv       ✓ 已加载（[N]行）
9. kensho-findings.txt     ✓ 已加载（[N]行）
10. earnings-dates.csv     ✓ 已加载（[N]行）
11. calculations.csv       ✓ 已加载（[N]行）

所有中间数据文件已成功加载。
使用文件数据作为唯一真实来源生成报告。
---
```

如果任何文件缺失或为空，停止并告知用户哪个文件失败。不要在数据缺失的情况下继续生成报告。

**HTML报告中的每个数字、引用、来源URL和MCP函数调用引用必须来自这些文件 — 而非您对早期对话轮次的记忆。** 文件是唯一真实来源。早期对话上下文可能已被压缩或摘要，如果依赖它将包含错误。如果数据点不在文件中，则不应出现在报告中。

参见 [report-template.md](report-template.md) 获取完整的HTML模板、CSS和Chart.js配置。

**强制 — 使用模板辅助函数创建图表：**
report-template.md 提供预构建、调试过的Chart.js辅助函数。您必须使用这些确切的函数创建图表。不要编写自定义内联Chart.js代码。辅助函数包括：
- `createRevEpsChart(canvasId, labels, revenueData, epsData, revLabel)` — 用于图1
- `createMarginChart(canvasId, labels, grossMargins, opMargins)` — 用于图2
- `createRevGrowthChart(canvasId, labels, growthData)` — 用于图3
- `createAnnotatedPriceChart(canvasId, labels, prices, earningsDates, ticker)` — 用于图5
- `createCompPerfChart(canvasId, labels, datasets)` — 用于图6
- `createPEChart(canvasId, companies)` — 用于图7

每个图表调用必须在其自己的 `<script>` 标签中，包裹在try-catch块中。这确保一个图表的错误不会阻止其他图表渲染。示例：
```html
<script>
try {
  createRevEpsChart('chart-rev-eps', [...], [...], [...], 'Revenue ($B)');
} catch(e) { console.error('Figure 1 error:', e); }
</script>
<script>
try {
  createMarginChart('chart-margins', [...], [...], [...]);
} catch(e) { console.error('Figure 2 error:', e); }
</script>
```

### 报告结构（共4-5页）

报告分为两半：**叙述**（第1-2页）和**图表**（第3-5页）。保持紧密整合。

---

**AI免责声明（强制 — 必须出现在3个位置）：**
您必须在报告HTML中包含以下免责声明文本。这不是可选的 — 没有它报告不完整：

> **"分析由AI生成 — 请确认所有输出"**

必须出现在以下3个位置：
1. **页眉横幅** — 紧接在封面页眉之前，作为居中的黄色横幅：`<div class="ai-disclaimer">分析由AI生成 — 请确认所有输出</div>`
2. **页脚** — 在page-footer div内，作为醒目的黄色横幅：`<div class="footer-disclaimer">分析由AI生成 — 请确认所有输出</div>`
3. **附录** — 作为附录部分的第一行，在表格之前：`<div class="ai-disclaimer">分析由AI生成 — 请确认所有输出</div>`

---

**第1页：封面与论点**

- **AI免责声明横幅**（黄色，居中 — 见上方AI免责声明规则）
- **页眉**：公司名称（股票代码）| 行业 | 报告日期
- **标题**：主题性，针对该季度（例如，"Walmart Inc. (WMT) Q4 FY2026业绩预览：假日收获 — Furner的首份报告能否确认万亿市值论点？"）
- **执行论点**（最多2-3个短段落加项目符号）：
  - 用1-2句话说明我们对此报告的预期
  - 4-6个项目符号涵盖：我们的EPS估计vs一致预期、指引预期、关键关注指标、什么会推动股价、关键辩论
  - 保持直接和有观点 — 表明立场，不要对一切都模棱两可
- **关键管理层引用**，来自最近的业绩电话会议，自然融入叙述中相关位置。不要将这些放在单独的标题下。作为支持论点证据自然整合。格式为缩进的块引用。

---

**第2页：估计、主题与新闻**

- **一致预期估计表**（单个表格，标注为图表）：
  - 列：指标 | 一致预期 | 我们的估计 | 同比变化
  - 行：收入、EPS、毛利率、营业利润，以及2-3个重要的公司特定KPI（例如，同店销售、电商增长、会员收入 — 这个公司市场关注的任何指标）
  - **颜色编码严格机械化：** 如果同比变化值为负，使用 `class="neg"`（红色）。如果为正，使用 `class="pos"`（绿色）。如果为零或N/A，使用 `class="neutral"`。数字的符号决定类别 — 不要根据解释覆盖。-1.1%始终是红色，即使下降幅度很小。
  - 这是唯一的指引/估计部分。不要在其他地方重复估计数据。

- **标题EPS之外的关键指标**（项目符号列表，3-5项）：
  - 决定这是好季度还是差季度的具体指标（超越EPS数字）
  - 每项：指标是什么、一致预期/管理层预期是什么、为什么重要
  - 要具体："Walmart Connect广告收入增长（一致预期约30%同比，第三季度为33%）"

- **关注主题**（3-5个项目符号）：
  - 即将发布报告的前瞻性项目
  - 管理层需要交付什么、什么可能带来惊喜、空头关注什么
  - 每个主题：最多1-2句话

- **近期新闻与动态**（3-5个项目符号）：
  - 过去60天的重大新闻，每条一行
  - 日期 + 标题 + 简要影响评估
  - 仅包含可能影响即将发布的业绩或指引的项目

---

**第3-5页：图表（所有图表和表格）**

所有图表按顺序编号。每个图表有标题和来源行。

- **图1：季度收入与稀释EPS** — 柱状/折线组合图，8个季度
- **图2：利润率趋势（毛利率与营业利润率%）** — 双折线图，8个季度
- **图3：收入同比增长%** — 柱状图，绿/红条件着色。**仅包含当期和去年同期数据都存在的季度**（通常为获取的8个季度中最近的4个）。不要包含无法计算同比的季度 — 图表应有4根柱，而非8根。
- **图4：业务细分收入** — 表格：细分市场 | 最新季度收入（百万美元）| 占比% | 同比变化
- **图5：1年股价与业绩日期** — 价格折线图，业绩日期处有垂直注释线，标注季度和业绩后1天变动
- **图6：股票表现vs竞争对手（指数化为100）** — 多折线图，标的公司为粗实线，竞争对手为较细的虚线
- **图7：LTM P/E vs竞争对手** — 水平柱状图，标的公司以海军蓝突出显示
- **图8：竞争对手比较表** — 股票代码 | 公司 | 市值 | LTM P/E | NTM P/E | 年初至今% | 1年%

---

**附录：数据来源与计算（强制 — 不要跳过或缩写）**

附录必须以AI免责声明横幅开始：`<div class="ai-disclaimer">分析由AI生成 — 请确认所有输出</div>`

报告的最后页面必须包含附录表格，记录报告中引用的**每个声明** — 数值和非数值。**报告正文中出现的每个数字必须在附录中有对应行，报告正文中的每个此类数字必须是可点击的 `<a href="#ref-N">` 超链接，可滚动到其附录行。** 如果报告中的数字没有指向附录的超链接，则报告不完整。

- **表格列**：引用号 | 事实 | 数值 | 来源与推导
- **引用号**：与报告正文中超链接锚点匹配的顺序ID（`ref-1`、`ref-2`等）。每行有一个 `id="ref-N"` 属性，以便超链接滚动到它。
- **事实**：人类可读的标签（例如，"Q3 FY2026收入"、"LTM P/E — WMT"、"管理层指出关税逆风"、"巴克莱升级至增持"）
- **数值**：报告中显示的确切数字（例如，"$152.3B"、"24.5%"、"28.1x"）。对于非数值事实，留空或写"N/A"。
- **来源与推导**：这是关键列。**每行必须有具体、详细的来源 — 不仅仅是标签。** 严格遵循以下规则：

  **对于来自S&P Capital IQ的原始财务数据（收入、EPS、毛利、营业利润、净利润、EBITDA、价格、市值等）：**
  - 说明使用的MCP函数及其关键参数。格式：`S&P Capital IQ — [function_name](identifier='[TICKER]', line_item='[item]', period_type='[type]', period='[Q# FY####]')`
  - 示例：
    - `S&P Capital IQ — get_financial_line_item_from_identifiers(identifier='WMT', line_item='revenue', period_type='quarterly', period='Q3 FY2026')`
    - `S&P Capital IQ — get_financial_line_item_from_identifiers(identifier='WMT', line_item='diluted_eps', period_type='quarterly', period='Q3 FY2026')`
    - `S&P Capital IQ — get_prices_from_identifiers(identifier='WMT', periodicity='day')`
    - `S&P Capital IQ — get_capitalization_from_identifiers(identifier='WMT', capitalization='market_cap')`
  - **不要只写"S&P Capital IQ"而无详情。** 读者必须确切知道哪个工具调用的哪个数据点产生了这个数字。

  **对于计算值（利润率、增长率、P/E、回报、同比变化）：**
  - 显示带有**超链接组成部分**的完整公式 — 每个组成部分必须是 `<a href="#ref-N">` 链接，回链到该原始数据点的附录行。这很关键：读者必须能够从计算值点击到其每个输入。
  - 示例：`毛利率 = <a href='#ref-5'>毛利372亿美元</a> / <a href='#ref-1'>收入1523亿美元</a> = 24.4%。来源：S&P Capital IQ（计算）`
  - 示例：`LTM P/E = <a href='#ref-20'>价格172.35美元</a> / (<a href='#ref-8'>Q1 EPS 1.47美元</a> + <a href='#ref-9'>Q2 EPS 1.84美元</a> + <a href='#ref-10'>Q3 EPS 1.53美元</a> + <a href='#ref-11'>Q4 EPS 1.80美元</a>) = 172.35美元 / 6.64美元 = 25.9倍`
  - 示例：`收入同比增长 = (<a href='#ref-12'>Q3 FY26收入1658亿美元</a> - <a href='#ref-3'>Q3 FY25收入1608亿美元</a>) / <a href='#ref-3'>Q3 FY25收入1608亿美元</a> = +3.1%`
  - **每个公式组成部分必须是可点击的超链接。** 不要用纯文本数字写公式。

  **对于电话会议记录来源的声明（引用、管理层评论、指引）：**
  - 写出记录中的**逐字摘录句子**。
  - 通过其全名和用于获取它的 `key_dev_id` 引用电话会议记录。
  - 格式：`"[逐字引用]" — [发言人], [职位]。来源：[Q# FY####业绩电话会议记录] (key_dev_id: [ID])`
  - 示例：`"我们预计Q4同店销售增长3-4%" — CEO John Furner。来源：Q3 FY2026业绩电话会议记录 (key_dev_id: 12345678)`

  **对于Kensho Grounding搜索结果（新闻、分析师评级、一致预期估计）：**
  - 写出搜索结果中的关键发现或摘录。
  - **强制：包含Kensho `search` 工具返回的来源URL** 作为可点击的 `<a href="[URL]" target="_blank">` 超链接。这是最重要的部分 — 读者必须能够点击进入原始来源。
  - 格式：`"[发现/摘录]" — <a href="[URL]" target="_blank">[来源标题或出版物]</a>。查询：search("[使用的查询]")`
  - 示例：`"巴克莱于2026年1月15日将WMT升级至增持，目标价210美元。" — <a href="https://www.investing.com/news/barclays-upgrades-wmt" target="_blank">Investing.com，2026年1月15日</a>。查询：search("WMT analyst ratings price target upgrades downgrades")`
  - 如果特定结果未返回URL，写"来源URL不可用"并仍包含搜索查询。

**完整性检查：** 最终确定报告之前，扫描报告正文中的每个数字。如果任何数字未包裹在 `<a href="#ref-N" class="data-ref">` 中，修复它。如果任何附录行的来源与推导只是像"S&P Capital IQ"这样的裸标签而无函数调用详情，修复它。如果任何计算值的公式缺少超链接组成部分，修复它。如果任何Kensho来源的声明缺少来源URL，修复它。

按部分（财务数据、估值、估计与一致预期、电话会议声明、新闻与分析师评论、股票表现）对附录行进行分组，使用子标题。使用较小字号（10-11px）。

---

## 阶段8：输出

1. 将完整的HTML文件写入当前工作目录中的 `earnings-preview-[TICKER]-YYYY-MM-DD.html`。
2. 在浏览器中打开：`open earnings-preview-[TICKER]-YYYY-MM-DD.html`
3. 告知用户文件已创建并总结关键发现。

---

## 写作指南

- **禁止表情符号**：报告中任何地方都不要使用表情符号。这是专业研究文档。
- **简洁**：目标打印页数4-5页。每句话都必须有分量。尽可能使用项目符号而非段落。如果某部分感觉太长，删减它。
- **数字要具体**："收入524亿美元，同比增长5.2%"而非"收入强劲增长"。
- **表明立场**：这是业绩预览，不是摘要。说明您预期什么、什么重要、为什么。要有观点但用数据支持。
- **管理层引用无标题**：将最近电话会议的3-4个关键管理层引用作为块引用自然融入叙述中。不要创建"关键管理层引用"部分标题 — 让它们作为支持证据自然流动。
- **专业语调**：卖方股权研究风格 — 分析性、直接、数据驱动。
- **图表必须使用真实数据**：每个图表填充实际MCP数据。绝不编造。
- **竞争对手背景**：相对于同行构建估值框架。25倍P/E本身没有意义，需要知道同行交易在20倍还是35倍。
- **超链接声明**：每个事实声明 — 数值或定性 — 必须是链接到其附录条目的 `<a class="data-ref">` 标签。数字：`<a href="#ref-1" class="data-ref">$152.3B</a>`。定性：`<a href="#ref-25" class="data-ref">管理层指出关税逆风是主要利润率风险</a>`。任何事实都不应出现在没有可追溯来源的附录中。
