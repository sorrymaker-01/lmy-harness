---
name: funding-digest
description: "使用S&P Capital IQ数据通过Kensho LLM-ready API MCP服务器生成专业的公司财务摘要表。当用户请求财务摘要表、公司单页、公司简介、情况说明书、公司快照或公司概览文档时使用此技能 — 尤其是当他们提到特定公司名称或股票代码时。当用户请求股票研究摘要、并购公司简介、企业发展目标简介、销售/业务发展会议准备文档或任何简洁的单一公司财务摘要时也会触发。此技能支持四种受众类型：股票研究、投资银行/并购、企业发展和销售/业务发展。如果用户未指定受众，请询问。适用于上市公司和私人公司。"
---

**AI 免责声明（强制性）：**
您必须在PowerPoint页脚中包含以下免责声明文本。这是不可选的 — 没有此声明的报告是不完整的：

> **"Analysis is AI-generated — please confirm all outputs"**

**页脚** — 在生成幻灯片的底部，作为醒目的黄色横幅："Analysis is AI-generated — please confirm all outputs"

---

# 每周交易流程摘要

使用S&P Global Capital IQ数据生成分析师质量的**单页PowerPoint**，总结关注行业或公司近期融资轮次的关键要点。每个交易都链接回其Capital IQ资料，方便快速深入了解。

## 使用时机

在以下任何模式时触发：
- "给我本周的交易流程摘要"
- "[行业]的每周融资回顾"
- "[行业/公司]最近有哪些交易完成？"
- "交易汇总"或"交易摘要"
- "我的覆盖范围的资本市场更新"
- "总结近期融资活动"
- 任何关于交易、融资或轮次的定期简报请求

## 嵌套技能

此技能生成单页PPTX简报：
- **阅读** `/mnt/skills/public/pptx/SKILL.md` 在生成PowerPoint之前（及其子参考 `pptxgenjs.md` 用于从头创建）

## 实体解析和工具稳健性

S&P Global的标识符系统将公司名称解析为法律实体。这对大多数公司都有效，但存在已知的失败模式会导致空结果。**在整个工作流程中应用这些规则以避免无声的数据丢失。**

### 规则0：在查询融资前预验证所有标识符

**在**调用任何融资工具之前，通过 `get_info_from_identifiers` 运行每个标识符。这是最早捕捉问题的最便宜、最可靠的方法。检查响应中的两件事：

1. **它是否解析成功？** 如果标识符返回空/错误，该名称在S&P Global中不存在。尝试使用 `references/sector-seeds.md` 中的别名、法律实体名称或直接使用 `company_id`。
2. **`status` 字段是什么？** 
   - `"Operating"` → 可以安全查询融资轮次。
   - `"Operating Subsidiary"` → 公司存在但为母公司所有。它将返回**零融资轮次**。在摘要中注意此上下文（例如，"被[母公司]收购"），但不要查询融资。
   - 任何其他状态（例如，closed, inactive）→ 公司不再运营。可能存在历史数据但无新活动。

**这个单一的预验证步骤可以防止大多数空结果问题。** 将所有候选公司批量放入单个 `get_info_from_identifiers` 调用（它能很好地处理大批量）并在继续之前进行分类。

### 规则1：永远不要信任没有回退的空结果

如果 `get_rounds_of_funding_from_identifiers` 为您期望有数据的公司返回空：
1. **尝试法律实体名称或 company_id。** 品牌名称通常有效，但有些无效。请参阅 `references/sector-seeds.md` 中的别名表了解已知不匹配。常见模式："[品牌] AI" → "[法律名称], Inc."（例如，Together AI → "Together Computer, Inc.", Character.ai → "Character Technologies, Inc.", Runway ML → "Runway AI, Inc."）。
2. **验证公司是否存在于S&P中。** 如果您跳过了规则0，现在调用 `get_info_from_identifiers(identifiers=["Company"])` — 如果这也返回空，公司可能太早期或尚未被索引。

### 规则2：子公司没有融资轮次

作为较大公司的部门或全资子公司的公司（例如，Alphabet下的DeepMind、Microsoft下的GitHub、Voodoo下的BeReal）将返回**零融资轮次**。它们的资本事件在母公司层面跟踪。

**如何检测：** `get_info_from_identifiers` 的 `status` 字段将显示 `"Operating Subsidiary"`。`references/sector-seeds.md` 文件也用⚠️警告标记已知子公司。在融资查询中跳过这些。

### 规则3：使用 `get_rounds_of_funding_from_identifiers` 作为主要工具，而非 `get_funding_summary_from_identifiers`

摘要工具速度更快但可靠性较低 — 即使存在详细轮次，它也可能返回错误或不完整数据。始终使用详细轮次工具作为主要数据源。摘要工具仅适用于快速汇总检查（总筹集金额、轮次数），如果结果看起来较低，应通过轮次工具进行验证。

### 规则4：谨慎批处理并验证

处理大型公司 universe（50+公司）时，以15-20组进行批处理。每批后，检查返回空结果的公司，并在继续前通过规则1的回退步骤运行它们。

### 规则5：`role` 参数至关重要

- `company_raising_funds` → "X筹集了哪些轮次？"（公司视角）
- `company_investing_in_round_of_funding` → "投资者Y投资了什么？"（投资者视角）

使用错误的角色会无声地返回空结果。对于交易流程摘要，您几乎总是需要 `company_raising_funds`。仅在专门分析投资者投资组合活动时使用投资者角色。

### 规则6：标识符解析不区分大小写但区分拼写

S&P Global处理大小写变化（"openai" = "OpenAI"）但对拼写和标点严格。"Character AI"可能失败，而"Character.ai"可能成功。如有疑问，使用 `company_id`（例如，`C_1829047235`），它保证可以解析。

## 工作流程

### 步骤1：确定覆盖范围和时间段

确定摘要应覆盖的内容。有两种设置：

**回头用户（有监视列表）：**
如果用户之前定义了要跟踪的行业或公司，使用该列表。检查对话历史以获取先前的监视列表。

**新用户：**
询问：

| 参数 | 默认值 | 备注 |
|------|--------|------|
| **行业** | *(至少一个)* | 例如，"AI, Fintech, Biotech" |
| **特定公司** | 可选 | 补充行业级覆盖 |
| **时间段** | 过去7天 | "本周", "过去2周", "本月" |

从时间段计算确切的 `start_date` 和 `end_date`。

### 步骤2：构建公司 Universe

对于指定的每个行业，使用经过验证的引导方法构建公司 Universe：

1. **从领域知识中获取种子公司**（见 `references/sector-seeds.md`）
   - 注意种子文件中的⚠️警告和别名说明 — 一些知名公司是子公司、已被收购或需要特定法律名称才能解析。
   - 种子文件包含已知别名不匹配的 `company_id` 值。如果品牌名称失败，直接使用这些。

2. **立即预验证所有种子**（规则0）：
   ```
   get_info_from_identifiers(identifiers=[all_seeds_for_this_sector])
   ```
   将结果分为两个桶：
   - ✅ **已解析且运营中**（`status` = "Operating"）→ 继续进行竞争对手扩展
   - ❌ **未解析或子公司** → 使用种子文件中的别名/法律名称重试；子公司会被记录为上下文但排除在融资查询之外

3. **通过竞争对手扩展**（仅使用✅已解析的种子）：
   ```
   get_competitors_from_identifiers(identifiers=[resolved_seeds], competitor_source="all")
   ```

4. **验证扩展的 Universe：**
   ```
   get_info_from_identifiers(identifiers=[new_competitors])
   ```
   应用相同的分类。按与目标行业匹配的 `simple_industry` 进行过滤。删除任何未解析的名称或子公司。

如果用户提供特定公司，直接添加它们但仍通过预验证分类运行。永远不要跳过验证 — 即使知名品牌名称也可能无声失败。

保持 Universe 可管理 — 每个行业目标为15-40家**已解析、运营中**的公司。对于多行业摘要，这可能总共50-100+家公司。

### 步骤3：提取融资轮次

对于 Universe 中的所有公司：

```
get_rounds_of_funding_from_identifiers(
    identifiers=[batch],
    role="company_raising_funds",
    start_date="YYYY-MM-DD",
    end_date="YYYY-MM-DD"
)
```

如果 Universe 较大，以15-20组处理。

**每批后，识别返回空结果的公司。** 对于任何预期有活动的公司：
1. 使用法律实体名称或替代标识符重试（见上面的实体解析规则）。
2. 仅在用尽回退后将公司记录为"无数据"。

收集成功结果中的所有 `transaction_id` 值，然后用详细轮次信息丰富：

```
get_rounds_of_funding_info_from_transaction_ids(
    transaction_ids=[all_funding_ids]
)
```

在单个调用（或少量调用）中传递所有交易ID，而不是每个交易一个调用 — 工具可以高效处理批处理。

**从每个轮次提取以下内容（对幻灯片至关重要）：**
- `transaction_id` — Capital IQ交易链接所需
- **公告日期** — 轮次公开宣布的时间
- **完成日期** — 轮次正式完成的时间
- 筹集金额
- **投前估值**（如披露）
- **投后估值**（如披露）
- 领投方
- 轮次类型（Series A, B, C等）
- 证券条款
- 顾问
- 定价趋势（上轮/下轮/持平）

> **日期是必需的。** 公告和完成日期必须始终出现在最终幻灯片的交易表中。如果只有一个日期可用，显示它并将另一个标记为"—"。

### 步骤4：为重要交易提取公司上下文

对于参与重大交易（大型轮次、显著估值变动）的任何公司，获取简短描述：

```
get_company_summary_from_identifiers(identifiers=[notable_companies])
```

这为叙述添加上下文（例如，"该公司是一家2021年成立的AI基础设施初创公司，正在扩展到..."）。

### 步骤5：识别亮点和趋势

在设计幻灯片之前，分析数据以呈现故事：

**标记为"显著"：**
- 轮次 ≥ $100M
- 下轮（定价趋势 = 下）
- 新独角兽（投后估值超过$1B）
- 显著估值跳跃（投后 ≥ 上次已知估值的2倍）
- 重复融资者（同一家公司在6个月内再次融资）
- 异常大的投资者联合

**识别趋势：**
- 此期间部署的总资本与典型水平（如有历史数据）
- 哪些子行业最热门（最多轮次，最多资本）
- 轮次阶段分布（早期还是后期占主导？）
- 摘要中最活跃的投资者
- 地理集中度
- 估值趋势（投前估值是压缩还是扩张？）

**选择关键要点（3-5个）：**
将最重要的信号提炼为3-5个简洁的要点。这些是幻灯片的核心。每个要点应为一句话，有力且有数据支持。

示例：
- "AI行业在8轮中筹集了$2.4B — 是前一周的3倍，由[公司]的$800M大型轮次引领，投后估值$12B。"
- "[公司]以$3.5B投前估值完成$200M Series D，高于其Series C的$1.8B — 表明对AI开发者工具的强劲需求。"
- "下轮活动增加：6个后期轮次中有2个定价低于先前估值。"

### 步骤6：生成公司标志

对于关键要点或显著交易中出现的每家公司，使用两层本地管道生成标志。**不要使用Clearbit** (`logo.clearbit.com`) — 它已弃用并持续失败。外部标志CDN（Brandfetch, logo.dev, Google Favicons）需要API密钥或被网络限制阻止。相反，使用以下方法：

#### 第一层：`simple-icons` npm包（3,300+品牌SVG，无需网络）

`simple-icons` 包捆绑了数千个知名品牌的高质量SVG图标。它完全离线工作 — 无需API密钥，无需网络调用。安装它与 `sharp` 一起用于SVG → PNG转换：

```bash
npm install simple-icons sharp
```

**查找策略：**

```javascript
const si = require('simple-icons');
const sharp = require('sharp');

// 通过精确标题匹配查找图标（不区分大小写）
function findSimpleIcon(companyName) {
    // 首先尝试精确匹配
    for (const [key, val] of Object.entries(si)) {
        if (!key.startsWith('si') || !val || !val.title) continue;
        if (val.title.toLowerCase() === companyName.toLowerCase()) return val;
    }
    // 尝试去除常见后缀（AI, Inc., Corp.）
    const stripped = companyName.replace(/\s*(AI|Inc\.?|Corp\.?|Ltd\.?)$/i, '').trim();
    if (stripped !== companyName) {
        for (const [key, val] of Object.entries(si)) {
            if (!key.startsWith('si') || !val || !val.title) continue;
            if (val.title.toLowerCase() === stripped.toLowerCase()) return val;
        }
    }
    return null;
}

// 将SVG转换为带有品牌官方颜色的PNG
async function simpleIconToPng(icon, outputPath) {
    const coloredSvg = icon.svg.replace('<svg', `<svg fill="#${icon.hex}"`);
    await sharp(Buffer.from(coloredSvg))
        .resize(128, 128, { fit: 'contain', background: { r: 255, g: 255, b: 255, alpha: 0 } })
        .png()
        .toFile(outputPath);
}
```

**覆盖范围：** 典型交易流程公司的约43%（对主要科技品牌如Stripe、Anthropic、Databricks、Snowflake、Discord、Shopify、SpaceX、Mistral AI、Hugging Face较强；对 niche fintech、biotech或早期公司较弱）。

#### 第二层：通过 `sharp` 的基于首字母的回退（100%覆盖）

对于 `simple-icons` 中未找到的公司，生成基于首字母的干净标志作为PNG：

```javascript
async function generateInitialLogo(companyName, outputPath) {
    const initial = companyName.charAt(0).toUpperCase();
    const svg = `
    <svg width="128" height="128" xmlns="http://www.w3.org/2000/svg">
        <circle cx="64" cy="64" r="64" fill="#BDBDBD"/>
        <text x="64" y="64" font-family="Arial, Helvetica, sans-serif"
              font-size="56" font-weight="bold" fill="#FFFFFF"
              text-anchor="middle" dominant-baseline="central">${initial}</text>
    </svg>`;
    await sharp(Buffer.from(svg)).png().toFile(outputPath);
}
```

#### 完整管道

```javascript
async function fetchLogo(companyName, outputDir) {
    const fileName = companyName.toLowerCase().replace(/[\s.]+/g, '-') + '.png';
    const outPath = path.join(outputDir, fileName);

    // 第一层：尝试simple-icons
    const icon = findSimpleIcon(companyName);
    if (icon) {
        await simpleIconToPng(icon, outPath);
        return { path: outPath, source: 'simple-icons' };
    }

    // 第二层：生成基于首字母的回退
    await generateInitialLogo(companyName, outPath);
    return { path: outPath, source: 'initial-fallback' };
}
```

**标志指南：**
- 将所有标志保存到 `/home/claude/logos/[company-name].png`
- 所有标志为128×128 PNG，透明背景
- 在幻灯片上，以0.35"–0.5"高显示标志 — 它们是点缀，不是焦点
- 基于首字母的回退圆圈使用灰色（`BDBDBD`）填充，白色文本 — 与单色调色板一致
- 永远不要随机混合标志样式 — 如果大多数公司解析为品牌图标，少数回退应自然融入

### 步骤7：生成单页PPTX

在创建幻灯片之前，阅读 `/mnt/skills/public/pptx/SKILL.md` 和 `/mnt/skills/public/pptx/pptxgenjs.md`。

使用 `pptxgenjs` 创建**单页**PowerPoint。幻灯片应信息密集但视觉整洁 — 想想"执行仪表板"而不是"文本墙"。

#### 幻灯片布局

```
┌─────────────────────────────────────────────────────────────┐
│  DEAL FLOW DIGEST                                           │
│  [Period] · [Sectors]                           [Date]      │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  ┌─────────┐  ┌─────────┐  ┌─────────┐  ┌─────────┐       │
│  │  $X.XB  │  │  N      │  │  $X.XB  │  │  $X.XB  │       │
│  │ Raised  │  │ Rounds  │  │ Avg Pre │  │ Largest │       │
│  └─────────┘  └─────────┘  └─────────┘  └─────────┘       │
│                                                             │
│  KEY TAKEAWAYS                                              │
│  ─────────────────────────────────────────────────          │
│  [Logo] Takeaway 1 text goes here...                        │
│  [Logo] Takeaway 2 text goes here...                        │
│  [Logo] Takeaway 3 text goes here...                        │
│  [Logo] Takeaway 4 text goes here...                        │
│                                                             │
│  TOP DEALS                                                  │
│  ┌──────────────────────────────────────────────────────────┐│
│  │Company│Type │Announced│Closed│Amount│Pre-$│Post-$│Lead│🔗││
│  │───────│─────│─────────│──────│──────│─────│──────│────│──││
│  │ ...   │ ... │  ...    │ ...  │ ...  │ ... │ ...  │... │🔗││
│  └──────────────────────────────────────────────────────────┘│
│                                                             │
│  [Footer: Deal Flow Digest · Sources: S&P Global Capital IQ]│
│  [Footer: AI Disclaimer]                                    │
└─────────────────────────────────────────────────────────────┘
```

#### 设计规范

**颜色理念：极简，首选单色。** 幻灯片应感觉像高端金融简报 — 黑色、白色和灰色为主。颜色**仅**在有意义的地方使用（例如，下轮的红色指示器，突出指标的绿色指示器）或读者自然期望的地方（公司标志）。永远不要将颜色用于纯粹的装饰目的，如背景填充、强调条或渐变效果。

**颜色调色板 — 单色执行：**
- 主要背景：`FFFFFF`（白色）— 干净、开放的幻灯片背景
- 标题栏：`1A1A1A`（近黑色）— 标题区域的强烈对比
- 主要文本：`1A1A1A`（近黑色）— 所有正文、统计数字、要点
- 次要文本：`6B6B6B`（中灰色）— 标签、说明、页脚、日期戳
- 边框和分隔线：`D0D0D0`（浅灰色）— 微妙的结构线、卡片轮廓、表格边框
- 卡片背景：`F5F5F5`（灰白色/非常浅灰色）— 统计卡片填充、交替表格行
- 链接文本：`2B5797`（柔和蓝色）— 表格中的Capital IQ交易链接（幻灯片上唯一的蓝色）
- **语义颜色（谨慎使用）：**
  - 下轮或负面信号：`C0392B`（柔和红色）— 仅用作小点、标签或单字高亮，永远不用作填充或背景
  - 突出的正面指标（新独角兽、超大轮次）：`2E7D32`（柔和绿色）— 同样最小使用：一个点、一个小标签或一个突出的数字
  - 如果没有数据点需要颜色指示器，**完全不使用颜色**。完全单色的幻灯片是完全正确的。

**排版：**
- 标题：28–32pt，粗体，近黑色标题栏上的白色文本
- 统计数字：36–44pt，粗体，近黑色
- 统计标签：10–12pt，中灰色（`6B6B6B`）
- 要点文本：12–14pt，近黑色，左对齐
- 表格文本：9–11pt，近黑色，次要列使用灰色（`6B6B6B`）
- 链接文本：9–10pt，柔和蓝色（`2B5797`）
- 页脚：8pt，中灰色

**统计卡片（顶行）：**
- 4个关键指标作为大数字标注：总筹集金额、轮次数、平均投前估值、最大轮次
- 每个卡片使用 `F5F5F5` 填充和细 `D0D0D0` 边框 — 无阴影，无颜色填充
- 如果统计数据令人惊讶或极端（例如，正常交易量的3倍，创纪录交易），可以在该单个数字旁边放置一个彩色点或下划线 — 否则保持完全单色
- 如果投前估值大多未披露，用不同指标替代（例如，中位数轮次大小、新独角兽数量）

**关键要点（中间部分）：**
- 3–5个单行要点，每个以相关公司标志为前缀（小，~0.35"高）
- 如果没有标志，使用**灰色圆圈**和白色公司首字母 — 不是彩色圆圈
- 左对齐，有足够的间距呼吸
- 下轮或负面要点可以使用小红点前缀；否则无颜色
- 在可用时包含估值上下文（例如，"以$5B投后估值"）

**顶级交易表（底部部分）：**
- 紧凑表格，显示4–6个最显著的交易
- 列：公司、类型（Series X）、公告（日期）、完成（日期）、金额（$M）、投前（$M）、投后（$M）、领投方、交易链接
- **公告**和**完成**列以 `MMM DD` 格式显示日期（例如，"Jan 15"）。这些列是必需的，必须始终存在。如果日期不可用，显示"—"。
- **交易链接**列包含可点击的"View →"文本，链接到Capital IQ：
  ```
  https://www.capitaliq.spglobal.com/web/client?#offering/capitalOfferingProfile?id=<transaction_id>
  ```
  其中 `<transaction_id>` 是 `get_rounds_of_funding_from_identifiers` 中的 `transaction_id`。
- 如果投前或投后估值未披露，在该单元格中显示"—"
- 标题行使用近黑色（`1A1A1A`）填充和白色文本；交替行使用 `F5F5F5` 和 `FFFFFF`
- **表格在幻灯片上水平居中**。计算表格的总宽度，然后设置 `x` 使其在幻灯片宽度内居中：`x = (slideWidth - tableWidth) / 2`。对于16:9布局（13.33"宽），如果表格为12"宽，使用 `x = 0.67`。永远不要将表格左对齐到幻灯片边缘。
- 保持紧凑 — 这是参考，不是焦点
- 表格单元格中无彩色填充。如果交易是下轮，金额旁边可能出现一个小红文本标签"(↓ down)" — 这是表格中唯一允许的颜色。

**交易链接实现（pptxgenjs）：**
在pptxgenjs中，超链接通过单元格对象的 `options.hyperlink` 属性添加到表格单元格：
```javascript
// 带Capital IQ交易链接的表格单元格
{
  text: "View →",
  options: {
    hyperlink: {
      url: `https://www.capitaliq.spglobal.com/web/client?#offering/capitalOfferingProfile?id=${transactionId}`
    },
    color: "2B5797",
    fontSize: 9,
    fontFace: "Arial"
  }
}
```

**表格居中（pptxgenjs）：**
始终在幻灯片上居中交易表格。动态计算x位置：
```javascript
const SLIDE_W = 13.33; // 16:9幻灯片宽度（英寸）
const TABLE_W = 12.5;  // 表格总宽度（所有列宽之和）
const TABLE_X = (SLIDE_W - TABLE_W) / 2; // ≈ 0.42"

slide.addTable(tableRows, {
  x: TABLE_X,
  y: tableY,
  w: TABLE_W,
  colW: [1.8, 0.9, 0.9, 0.9, 1.0, 1.1, 1.2, 1.6, 0.7], // 公司, 类型, 公告, 完成, 金额, 投前, 投后, 领投, 链接
  // ... 其他选项
});
```
根据需要调整 `colW` 值，但始终从 `(SLIDE_W - sum(colW)) / 2` 重新计算 `TABLE_X` 以保持表格居中。

**页脚：**
- 中灰色小文本："Deal Flow Digest · [Period] · Sources: S&P Global Capital IQ · Generated [Date]"

**一般颜色规则（严格执行）：**
- 公司标志是幻灯片上唯一的"全彩"元素 — 它们从源文件原样显示。
- 交易链接使用柔和蓝色（`2B5797`）— 这是除语义红/绿外唯一的非单色文本颜色。
- 在标志和链接之外，幻灯片在黑白打印机上打印应看起来正确。
- 永远不要将颜色应用于背景、强调条、装饰形状或部分分隔线。
- 如有疑问，保持灰色。

#### 代码结构

```javascript
const pptxgen = require("pptxgenjs");
const pres = new pptxgen();
pres.layout = "LAYOUT_16x9";
pres.title = "Deal Flow Digest";

const slide = pres.addSlide();
const SLIDE_W = 13.33; // 16:9幻灯片宽度（英寸）

// 1. 深色标题栏，包含标题和期间
// 2. 统计卡片行（4张卡片：总筹集金额、轮次数、平均投前、最大轮次）
// 3. 带标志的关键要点部分（包含估值上下文）
// 4. 顶级交易表，包含公告、完成、投前、投后列和Capital IQ交易链接
//    - 表格居中：x = (SLIDE_W - tableWidth) / 2
// 5. 页脚

pres.writeFile({ fileName: "/home/claude/deal-flow-digest.pptx" });
```

根据pptxgenjs陷阱指南，对阴影和重复样式使用工厂函数（而非共享对象）。

### 步骤8：幻灯片质量保证

按照PPTX技能的质量保证流程：

1. **内容质量保证：** `python -m markitdown deal-flow-digest.pptx` — 验证所有文本、数字、公司名称、估值数字和交易链接是否正确
2. **视觉质量保证：** 转换为图像并检查：
   ```bash
   python /mnt/skills/public/pptx/scripts/office/soffice.py --headless --convert-to pdf deal-flow-digest.pptx
   pdftoppm -jpeg -r 200 deal-flow-digest.pdf slide
   ```
   检查重叠元素、文本溢出、对齐问题、低对比度文本、标志大小问题，以及交易链接文本是否可见。
3. **链接质量保证：** 验证表格中的Capital IQ URL是否使用正确的交易ID正确格式化。
4. **修复并重新验证** — 在宣布完成前至少进行一次修复和验证循环。

### 步骤9：呈现结果

1. 将最终的 `.pptx` 复制到 `/mnt/user-data/outputs/`
2. 使用 `present_files` 分享幻灯片
3. 提供2-3句口头摘要：
   - "您的摘要涵盖了[行业]的X轮，总计筹集Y美元。"
   - 提及最显著的交易及其估值
   - 标记任何令人担忧的趋势（下轮、估值压缩等）

## 错误处理

### 实体解析失败
- **已知公司的空结果：** 首先检查 `get_info_from_identifiers` — 如果失败，尝试 `references/sector-seeds.md` 中的别名或直接使用 `company_id`。常见品牌→法律不匹配：Together AI → "Together Computer, Inc.", Character.ai → "Character Technologies, Inc.", Runway ML → "Runway AI, Inc."。
- **子公司：** DeepMind、GitHub、Instagram、WhatsApp、YouTube、BeReal等是子公司 — 它们没有独立的融资轮次。在上下文中将这些标记为"已收购/子公司"，但不要将它们报告为"无活动"。
- **已停业公司：** 像Convoy（2023年10月关闭）这样的公司仍在S&P Global中解析，但永远不会有新活动。`references/sector-seeds.md` 文件标记这些 — 在包含公司之前检查它。
- **`get_funding_summary_from_identifiers` 错误或返回零：** 回退到 `get_rounds_of_funding_from_identifiers` — 摘要工具可靠性较低。永远不要将摘要工具作为唯一数据源。
- **错误的 `role` 参数：** 如果投资者视角查询返回空，验证您使用的是 `company_investing_in_round_of_funding`，而不是 `company_raising_funds`（反之亦然）。

### 数据质量问题
- **期间无活动：** 如果行业零融资轮次，在幻灯片上明确注明（"[行业]期间未记录交易"）— 活动缺失本身就是信息性的。
- **估值数据稀疏：** 如果大多数交易的投前和投后估值未披露，在页脚注释中注明数据限制，并在表格中使用"—"。调整统计卡片以显示不同指标（例如，中位数轮次大小）而非平均投前。
- **标志检索失败：** `simple-icons` npm包为典型交易流程公司提供约43%的覆盖。对于其余部分，使用 `sharp` 生成的基于首字母的回退。保持一致的图标样式 — 不要混合随机方法。如果 `simple-icons` 或 `sharp` 安装失败，回退到pptxgenjs基于形状的首字母（灰色椭圆+白色文本覆盖），无需外部依赖。
- **一张幻灯片上的交易太多：** 如果有超过6个显著交易，在表格中显示前6个，并添加脚注："+N个额外交易未显示"。按交易规模优先排序。
- **大型 universes：** 对于包含100+公司的多行业摘要，将所有API调用以15-20组批处理。优先关注显著交易的深度而非次要交易的完整性。
- **过时的种子：** 如果竞争对手扩展为行业返回很少结果，种子公司可能太 niche。通过添加2-3个更知名的名称并重新扩展来扩大范围。
- **链接的无效交易ID：** 如果融资工具的 `transaction_id` 不产生有效的Capital IQ URL，为此行省略链接单元格，而不是包含损坏的链接。

## 示例提示

- "给我AI和fintech的每周交易流程摘要"
- "总结本周biotech的融资情况"
- "我的覆盖范围的交易摘要 — 网络安全、云基础设施和开发工具 — 过去2周"
- "本周风险投资在我关注的所有行业发生了什么？"
- "本月气候技术的快速交易流程幻灯片"