# 任务2：财务建模 - 详细工作流程

本文档为执行首次覆盖技能的任务2（财务建模）提供分步指导。

## 任务概览

**目的**：提取历史财务数据并构建包含预测和情景的综合Excel财务模型。

**前置条件**：⚠️ 验证后开始
- **必需**：访问公司财务数据
  - 对于上市公司：上交所/深交所/港交所的最新年报/半年报
  - 对于非上市公司：财务报表或可用估计
  - 或：用户提供预先提取的历史财务数据
- **可选**：公司研究（任务1）用于业务背景

## 输入验证

**开始前 - 检查：**

**选项A：直接提取财务数据（最常见）**
- [ ] 有权访问年报/半年报（上市公司）？
- [ ] 或有权访问财务报表（非上市公司）？
- [ ] 准备好创建Excel文件进行历史数据提取？

**选项B：用户已预先提取财务数据**
- [ ] 提供了历史财务数据文件？（.xlsx或其他格式）
- [ ] 包含3-5年的损益表、现金流量表、资产负债表？
- [ ] 数据干净且可以使用？

**可选背景：**
- [ ] 公司研究（任务1）完成以了解业务？

**如果验证失败**：停止并获取财务报表（年报/半年报或同等文件）后再继续。

## 模型结构和格式

### 颜色编码（行业标准）
- **蓝色文字**：硬编码输入（用户可以更改）
- **黑色文字**：公式和计算
- **绿色文字**：链接到其他工作表
- **红色文字**：错误或标记（应解决）

### 格式标准
- 专业的边框和底纹
- 清晰的分节标题
- 可折叠的分组行
- 关键输入/输出的命名区域
- 公式中无硬编码数字（常量除外，如12个月）
- 清晰的单位（千元、百万元等）

### 公式最佳实践
- 所有数字应从假设中流出
- 更改假设 → 整个模型更新
- 无循环引用
- 为关键单元格使用命名区域
- 保持公式简单和可审计
- 为复杂计算添加注释

## 分步建模工作流程

### 步骤1：提取历史财务数据

**如果历史财务数据已提取，跳过此步骤。**

**对于上市公司：**

1. **下载年报**
   - 前往巨潮资讯网（http://www.cninfo.com.cn）或港交所披露易（https://www.hkexnews.hk）
   - 搜索公司名称或股票代码
   - 下载最新年报（年度报告）
   - 导航至财务报告部分

2. **创建历史财务数据Excel文件**
   - 文件名：`[公司]_历史财务数据_[日期].xlsx`
   - 此文件将成为模型的基础

3. **提取损益表（3-5年）**
   - 创建工作表1："历史损益表"
   - 提取所有3-5年的行项目：
     - 收入（总额和分部分类，如披露）
     - 营业成本/销售成本
     - 毛利润
     - 营业费用（研发、销售与营销、管理费用分列）
     - EBITDA（如未披露则计算：EBIT + 折旧摊销）
     - EBIT/营业收入
     - 利息费用/收入
     - Other income/expense
     - Pre-tax income
     - Income tax and tax rate
     - Net income
     - EPS (basic and diluted)
     - Shares outstanding (basic and diluted)

4. **Extract Cash Flow Statement (3-5 years)**
   - Create Sheet 2: "Historical Cash Flow"
   - Extract ALL line items:
     - Operating activities (starting from net income)
     - Depreciation & amortization
     - Stock-based compensation
     - Changes in working capital (receivables, inventory, payables)
     - Cash from operations
     - Investing activities (CapEx, acquisitions)
     - Financing activities (debt issuance/repayment, equity, dividends)
     - Net change in cash
     - Beginning and ending cash

5. **Extract Balance Sheet (3-5 years)**
   - Create Sheet 3: "Historical Balance Sheet"
   - Extract ALL line items:
     - Current assets (cash, receivables, inventory, other)
     - Non-current assets (PP&E, intangibles, goodwill)
     - Total assets
     - Current liabilities (payables, accrued expenses, current debt)
     - Non-current liabilities (long-term debt, deferred taxes)
     - Total liabilities
     - Shareholders' equity (common stock, retained earnings)
     - Total liabilities + equity

6. **Calculate Historical Metrics**
   - Create Sheet 4: "Historical Metrics"
   - Calculate from statements:
     - Revenue growth % (YoY)
     - Gross margin %
     - EBITDA margin %
     - Operating margin %
     - Net margin %
     - Free cash flow (CFO - CapEx)
     - FCF margin %
     - ROIC (approximate: NOPAT / Invested Capital)
     - Debt/Equity ratio
     - Current ratio (Current Assets / Current Liabilities)

7. **Document Sources and Notes**
   - Create Sheet 5: "Notes"
   - Document:
     - 年报公告日期和财年结束
     - Any one-time items or adjustments noted
     - Non-GAAP vs GAAP differences
     - Segment breakdown (if revenue split by product/geography)
     - Data quality notes and limitations

**For Private Companies:**

1. **Gather Available Data**
   - Financial statements (if available)
   - Press releases with revenue figures
   - Funding announcements
   - Industry estimates or comparable company data

2. **Create Simplified Historical File**
   - Estimated revenue (if available)
   - Estimated margins (from comparables if needed)
   - Key ratios and metrics
   - Document all assumptions and sources

**Verification:**
- [ ] All 3 financial statements extracted (3-5 years)
- [ ] Numbers reconcile across statements (net income ties)
- [ ] Key metrics calculated correctly
- [ ] Excel file saved and can be opened
- [ ] 数据来源已记录（年报日期、页码）

**Foundation for projection model is now complete. Proceed to Step 2.**
   - Capital expenditures
   - Working capital items
   - Debt and interest expense
   - Share count (basic and diluted)

3. **Organize historical data for entry**
   - Prepare 3-5 years of actuals
   - Will be entered directly into Income Statement, Cash Flow Statement, and Balance Sheet tabs
   - Historical years in columns, projected years following

4. **Calculate historical trends**
   - Revenue CAGR
   - Margin progression
   - OpEx leverage
   - Working capital patterns
   - CapEx as % of revenue
   - These trends will inform projection assumptions

**Note**: Assumptions will be documented directly in each tab as blue text inputs, not in a separate tab.

### Step 2: Model Revenue

**CRITICAL: This is the most important and detailed part of the model.**

#### A. Revenue by Product/Category (20-30 rows)

Create detailed table:
```
                        2021A   2022A   2023A   2024A   2025E   2026E   2027E   2028E   2029E
Product Category A
  Sub-product A1        XX      XX      XX      XX      XX      XX      XX      XX      XX
  Sub-product A2        XX      XX      XX      XX      XX      XX      XX      XX      XX
  Sub-product A3        XX      XX      XX      XX      XX      XX      XX      XX      XX
  Category A Total      XX      XX      XX      XX      XX      XX      XX      XX      XX
  % of Total Rev        X%      X%      X%      X%      X%      X%      X%      X%      X%
  YoY Growth %          -       X%      X%      X%      X%      X%      X%      X%      X%

Product Category B
  [Similar structure]

[Continue for all product categories]

Services Revenue        XX      XX      XX      XX      XX      XX      XX      XX      XX
Other Revenue           XX      XX      XX      XX      XX      XX      XX      XX      XX

TOTAL REVENUE           XX      XX      XX      XX      XX      XX      XX      XX      XX
Total Revenue Growth %  -       X%      X%      X%      X%      X%      X%      X%      X%
```

**Key Requirements:**
- Show absolute revenue ($M) for each category
- Calculate % of total revenue for each category
- Show YoY growth % for each category
- Must have granular sub-categories (not just 3-5 top-level categories)
- Show mix shift over time
- Link all projections to Assumptions tab

#### B. Revenue by Geography (15-20 rows)

Create detailed table:
```
                        2021A   2022A   2023A   2024A   2025E   2026E   2027E   2028E   2029E
North America
  United States         XX      XX      XX      XX      XX      XX      XX      XX      XX
  Canada                XX      XX      XX      XX      XX      XX      XX      XX      XX
  Mexico                XX      XX      XX      XX      XX      XX      XX      XX      XX
  NA Total              XX      XX      XX      XX      XX      XX      XX      XX      XX
  % of Total            X%      X%      X%      X%      X%      X%      X%      X%      X%
  YoY Growth %          -       X%      X%      X%      X%      X%      X%      X%      X%

Europe
  UK                    XX      XX      XX      XX      XX      XX      XX      XX      XX
  Germany               XX      XX      XX      XX      XX      XX      XX      XX      XX
  France                XX      XX      XX      XX      XX      XX      XX      XX      XX
  Other Europe          XX      XX      XX      XX      XX      XX      XX      XX      XX
  Europe Total          XX      XX      XX      XX      XX      XX      XX      XX      XX
  % of Total            X%      X%      X%      X%      X%      X%      X%      X%      X%
  YoY Growth %          -       X%      X%      X%      X%      X%      X%      X%      X%

Asia-Pacific
  [Similar structure]

Rest of World
  [Similar structure]

TOTAL REVENUE           XX      XX      XX      XX      XX      XX      XX      XX      XX
```

**Verification:**
- Revenue by product total = Revenue by geography total = Total revenue
- All percentages sum to 100%
- Growth rates calculated correctly

#### C. Revenue by Channel (if applicable)

```
                        2021A   2022A   2023A   2024A   2025E   2026E   2027E   2028E   2029E
Direct Sales            XX      XX      XX      XX      XX      XX      XX      XX      XX
E-commerce/Online       XX      XX      XX      XX      XX      XX      XX      XX      XX
Wholesale/Partner       XX      XX      XX      XX      XX      XX      XX      XX      XX
Retail Stores
  Company-owned stores  XX      XX      XX      XX      XX      XX      XX      XX      XX
  Store count           XX      XX      XX      XX      XX      XX      XX      XX      XX
  Sales per store       XX      XX      XX      XX      XX      XX      XX      XX      XX
Other Channels          XX      XX      XX      XX      XX      XX      XX      XX      XX

TOTAL REVENUE           XX      XX      XX      XX      XX      XX      XX      XX      XX
```

### Step 3: Model Operating Expenses

#### A. Cost of Revenue
1. **Break down COGS components**
   - Product costs (materials, manufacturing)
   - Shipping and logistics
   - Service delivery costs
   - Other direct costs

2. **Link to revenue**
   - Calculate COGS as % of revenue
   - Model gross margin by year
   - Link to Assumptions tab

#### B. R&D Expenses
```
Research & Development  2021A   2022A   2023A   2024A   2025E   2026E   2027E   2028E   2029E
R&D Headcount           XX      XX      XX      XX      XX      XX      XX      XX      XX
R&D Comp per head       XX      XX      XX      XX      XX      XX      XX      XX      XX
R&D Personnel Costs     XX      XX      XX      XX      XX      XX      XX      XX      XX
R&D Other Costs         XX      XX      XX      XX      XX      XX      XX      XX      XX
Total R&D               XX      XX      XX      XX      XX      XX      XX      XX      XX
% of Revenue            X%      X%      X%      X%      X%      X%      X%      X%      X%
```

#### C. Sales & Marketing Expenses
```
Sales & Marketing       2021A   2022A   2023A   2024A   2025E   2026E   2027E   2028E   2029E
S&M Headcount           XX      XX      XX      XX      XX      XX      XX      XX      XX
S&M Comp per head       XX      XX      XX      XX      XX      XX      XX      XX      XX
S&M Personnel Costs     XX      XX      XX      XX      XX      XX      XX      XX      XX
Marketing Spend         XX      XX      XX      XX      XX      XX      XX      XX      XX
S&M Other Costs         XX      XX      XX      XX      XX      XX      XX      XX      XX
Total S&M               XX      XX      XX      XX      XX      XX      XX      XX      XX
% of Revenue            X%      X%      X%      X%      X%      X%      X%      X%      X%
```

#### D. General & Administrative
```
G&A                     2021A   2022A   2023A   2024A   2025E   2026E   2027E   2028E   2029E
G&A Headcount           XX      XX      XX      XX      XX      XX      XX      XX      XX
G&A Comp per head       XX      XX      XX      XX      XX      XX      XX      XX      XX
G&A Personnel Costs     XX      XX      XX      XX      XX      XX      XX      XX      XX
G&A Other Costs         XX      XX      XX      XX      XX      XX      XX      XX      XX
Total G&A               XX      XX      XX      XX      XX      XX      XX      XX      XX
% of Revenue            X%      X%      X%      X%      X%      X%      X%      X%      X%
```

#### E. Depreciation & Amortization
- Link to CapEx schedule
- Apply depreciation rates from Assumptions
- Calculate annual D&A

### Step 4: Build Income Statement

**Create full P&L with 40-50 line items:**

```
INCOME STATEMENT        2021A   2022A   2023A   2024A   2025E   2026E   2027E   2028E   2029E

REVENUE
[Link to Revenue Model tab]
Total Revenue           XX      XX      XX      XX      XX      XX      XX      XX      XX
  YoY Growth %          -       X%      X%      X%      X%      X%      X%      X%      X%

COST OF REVENUE
[Link to COGS breakdown]
Total COGS              XX      XX      XX      XX      XX      XX      XX      XX      XX

GROSS PROFIT            XX      XX      XX      XX      XX      XX      XX      XX      XX
  Gross Margin %        X%      X%      X%      X%      X%      X%      X%      X%      X%

OPERATING EXPENSES
Total R&D               XX      XX      XX      XX      XX      XX      XX      XX      XX
  % of Revenue          X%      X%      X%      X%      X%      X%      X%      X%      X%
Total S&M               XX      XX      XX      XX      XX      XX      XX      XX      XX
  % of Revenue          X%      X%      X%      X%      X%      X%      X%      X%      X%
Total G&A               XX      XX      XX      XX      XX      XX      XX      XX      XX
  % of Revenue          X%      X%      X%      X%      X%      X%      X%      X%      X%
Depreciation & Amort.   XX      XX      XX      XX      XX      XX      XX      XX      XX

Total Operating Exp.    XX      XX      XX      XX      XX      XX      XX      XX      XX
  % of Revenue          X%      X%      X%      X%      X%      X%      X%      X%      X%

EBITDA                  XX      XX      XX      XX      XX      XX      XX      XX      XX
  EBITDA Margin %       X%      X%      X%      X%      X%      X%      X%      X%      X%

EBIT                    XX      XX      XX      XX      XX      XX      XX      XX      XX
  EBIT Margin %         X%      X%      X%      X%      X%      X%      X%      X%      X%

Interest expense        (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)
Interest income         XX      XX      XX      XX      XX      XX      XX      XX      XX
Other income/(expense)  XX      XX      XX      XX      XX      XX      XX      XX      XX

Pre-tax income          XX      XX      XX      XX      XX      XX      XX      XX      XX

Income tax              (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)
  Tax rate %            X%      X%      X%      X%      X%      X%      X%      X%      X%

NET INCOME              XX      XX      XX      XX      XX      XX      XX      XX      XX
  Net Margin %          X%      X%      X%      X%      X%      X%      X%      X%      X%

SHARES OUTSTANDING
Basic shares (M)        XX      XX      XX      XX      XX      XX      XX      XX      XX
Diluted shares (M)      XX      XX      XX      XX      XX      XX      XX      XX      XX

EARNINGS PER SHARE
Basic EPS               $X.XX   $X.XX   $X.XX   $X.XX   $X.XX   $X.XX   $X.XX   $X.XX   $X.XX
Diluted EPS             $X.XX   $X.XX   $X.XX   $X.XX   $X.XX   $X.XX   $X.XX   $X.XX   $X.XX
```

### Step 5: Build Cash Flow Statement

```
CASH FLOW STATEMENT     2021A   2022A   2023A   2024A   2025E   2026E   2027E   2028E   2029E

OPERATING ACTIVITIES
Net Income              XX      XX      XX      XX      XX      XX      XX      XX      XX
Adjustments:
  Depreciation & Amort. XX      XX      XX      XX      XX      XX      XX      XX      XX
  Stock-based comp      XX      XX      XX      XX      XX      XX      XX      XX      XX
  Other non-cash        XX      XX      XX      XX      XX      XX      XX      XX      XX

Changes in WC:
  Accounts Receivable   (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)
  Inventory             (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)
  Accounts Payable      XX      XX      XX      XX      XX      XX      XX      XX      XX
  Other working capital (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)

Cash from Operations    XX      XX      XX      XX      XX      XX      XX      XX      XX

INVESTING ACTIVITIES
Capital Expenditures    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)
Acquisitions            (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)
Other investing         XX      XX      XX      XX      XX      XX      XX      XX      XX

Cash from Investing     (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)

FREE CASH FLOW          XX      XX      XX      XX      XX      XX      XX      XX      XX
  FCF Margin %          X%      X%      X%      X%      X%      X%      X%      X%      X%

FINANCING ACTIVITIES
Debt issuance           XX      XX      XX      XX      XX      XX      XX      XX      XX
Debt repayment          (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)
Equity issuance         XX      XX      XX      XX      XX      XX      XX      XX      XX
Dividends paid          (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)
Other financing         XX      XX      XX      XX      XX      XX      XX      XX      XX

Cash from Financing     XX      XX      XX      XX      XX      XX      XX      XX      XX

NET CHANGE IN CASH      XX      XX      XX      XX      XX      XX      XX      XX      XX

Beginning Cash          XX      XX      XX      XX      XX      XX      XX      XX      XX
Ending Cash             XX      XX      XX      XX      XX      XX      XX      XX      XX
```

### Step 6: Build Balance Sheet

Create full balance sheet with 35-45 line items:

```
BALANCE SHEET           2021A   2022A   2023A   2024A   2025E   2026E   2027E   2028E   2029E

ASSETS
Current Assets:
  Cash & Equivalents    XX      XX      XX      XX      XX      XX      XX      XX      XX
  Accounts Receivable   XX      XX      XX      XX      XX      XX      XX      XX      XX
  Inventory             XX      XX      XX      XX      XX      XX      XX      XX      XX
  Prepaid expenses      XX      XX      XX      XX      XX      XX      XX      XX      XX
  Other current assets  XX      XX      XX      XX      XX      XX      XX      XX      XX
Total Current Assets    XX      XX      XX      XX      XX      XX      XX      XX      XX

Non-Current Assets:
  PP&E, gross           XX      XX      XX      XX      XX      XX      XX      XX      XX
  Accumulated Depr.     (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)
  PP&E, net             XX      XX      XX      XX      XX      XX      XX      XX      XX
  Intangible assets     XX      XX      XX      XX      XX      XX      XX      XX      XX
  Goodwill              XX      XX      XX      XX      XX      XX      XX      XX      XX
  Other non-current     XX      XX      XX      XX      XX      XX      XX      XX      XX
Total Non-Current       XX      XX      XX      XX      XX      XX      XX      XX      XX

TOTAL ASSETS            XX      XX      XX      XX      XX      XX      XX      XX      XX

LIABILITIES
Current Liabilities:
  Accounts Payable      XX      XX      XX      XX      XX      XX      XX      XX      XX
  Accrued expenses      XX      XX      XX      XX      XX      XX      XX      XX      XX
  Deferred revenue      XX      XX      XX      XX      XX      XX      XX      XX      XX
  Current debt          XX      XX      XX      XX      XX      XX      XX      XX      XX
  Other current liab.   XX      XX      XX      XX      XX      XX      XX      XX      XX
Total Current Liab.     XX      XX      XX      XX      XX      XX      XX      XX      XX

Non-Current Liabilities:
  Long-term debt        XX      XX      XX      XX      XX      XX      XX      XX      XX
  Deferred taxes        XX      XX      XX      XX      XX      XX      XX      XX      XX
  Other non-current     XX      XX      XX      XX      XX      XX      XX      XX      XX
Total Non-Current Liab. XX      XX      XX      XX      XX      XX      XX      XX      XX

TOTAL LIABILITIES       XX      XX      XX      XX      XX      XX      XX      XX      XX

EQUITY
  Common stock          XX      XX      XX      XX      XX      XX      XX      XX      XX
  Additional paid-in    XX      XX      XX      XX      XX      XX      XX      XX      XX
  Retained earnings     XX      XX      XX      XX      XX      XX      XX      XX      XX
  Treasury stock        (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)    (XX)
  Other equity          XX      XX      XX      XX      XX      XX      XX      XX      XX
TOTAL EQUITY            XX      XX      XX      XX      XX      XX      XX      XX      XX

TOTAL LIAB + EQUITY     XX      XX      XX      XX      XX      XX      XX      XX      XX

BALANCE CHECK           OK      OK      OK      OK      OK      OK      OK      OK      OK
```

**Balance Check Formula:**
- Total Assets must equal Total Liabilities + Equity for each year
- Flag any imbalances in red

### Step 7: Build DCF Inputs Tab

Prepare inputs for valuation (Task 3):

```
DCF INPUTS              2025E   2026E   2027E   2028E   2029E

EBIT                    XX      XX      XX      XX      XX
Tax Rate                X%      X%      X%      X%      X%
NOPAT                   XX      XX      XX      XX      XX

Add: D&A                XX      XX      XX      XX      XX
Less: CapEx             (XX)    (XX)    (XX)    (XX)    (XX)
Less: Chg in NWC        (XX)    (XX)    (XX)    (XX)    (XX)

UNLEVERED FCF           XX      XX      XX      XX      XX

Terminal Year Metrics:
  2029E Revenue         $X,XXX
  2029E EBITDA          $XXX
  2029E EBIT            $XXX
  2029E Unlevered FCF   $XXX
```

### Step 8: Build Scenarios Tab

Create three scenarios with different assumptions:

#### Scenario Assumptions Table
```
Assumption                      Bull        Base        Bear
Revenue CAGR (2025-2029)        XX%         XX%         XX%
Gross Margin 2029E              XX%         XX%         XX%
EBITDA Margin 2029E             XX%         XX%         XX%
CapEx as % of Revenue           X%          X%          X%
[Add other key assumptions]
```

#### Scenario Output Table
```
Metric                          Bull        Base        Bear
2029E Revenue ($M)              $X,XXX      $X,XXX      $X,XXX
2029E EBITDA ($M)               $XXX        $XXX        $XXX
2029E EBITDA Margin             XX%         XX%         XX%
2029E Net Income ($M)           $XXX        $XXX        $XXX
2029E EPS                       $X.XX       $X.XX       $X.XX
2029E FCF ($M)                  $XXX        $XXX        $XXX
2029E FCF Margin                XX%         XX%         XX%

Cumulative FCF 2025-2029 ($M)   $XXX        $XXX        $XXX
```

**Document scenario rationale:**
- Bull case: [Describe optimistic but achievable assumptions]
- Base case: [Describe most likely scenario]
- Bear case: [Describe downside risks and triggers]

### Step 9: Quality Check

**Verify model integrity:**
1. [ ] Test all formulas (spot check calculations)
2. [ ] Change assumption → verify model updates correctly
3. [ ] Test scenario switching
4. [ ] Verify color coding (blue/black/green)
5. [ ] Check balance sheet balances for all years
6. [ ] Verify no circular references (Excel will flag)
7. [ ] Check for hardcoded numbers in projections
8. [ ] Verify all cross-sheet links work
9. [ ] Test that revenue totals tie across all tabs
10. [ ] Review formatting and presentation

---

## Quality Standards

### Model Integrity
- All formulas link properly across sheets
- No hardcoded numbers in projections (except in Assumptions tab)
- No circular references
- Balance sheet balances for all years
- Scenario switching works properly

### Completeness
- All 6 essential tabs: Revenue Model, Income Statement, Cash Flow Statement, Balance Sheet, Scenarios, DCF Inputs
- 40-50 line items in Income Statement
- 20-30 rows in Revenue Model (product breakdown)
- 15-20 rows in Revenue Model (geography breakdown)
- Full cash flow and balance sheet with all line items
- Bull/Base/Bear scenarios complete

### Professional Formatting
- Consistent color coding (blue/black/green)
- Clear headers and labels
- Proper borders and shading
- Named ranges for key cells
- Grouped rows for collapsibility
- Units clearly labeled ($ thousands vs. $ millions)

### Documentation
- Assumptions documented with rationale (blue text cells with comments)
- Data sources noted in cell comments or notes section within tabs
- Complex calculations explained with comments
- Methodology described

---

## File Naming Convention

Save the financial model as:
`[Company]_Financial_Model_[Date].xlsx`

Example: `Tesla_Financial_Model_2024-10-27.xlsx`

---

## Success Criteria

A successful financial model should:
1. Have all 6 essential tabs (Revenue Model, Income Statement, Cash Flow Statement, Balance Sheet, Scenarios, DCF Inputs)
2. Be fully dynamic (change assumption → model updates)
3. Have no hardcoded numbers in projections
4. Include detailed revenue breakdowns (20-30 rows by product, 15-20 rows by geography)
5. Contain 40-50 line items in Income Statement
6. Include Bull/Base/Bear scenarios
7. Be professionally formatted with color coding
8. Balance properly (balance sheet, cash flows)
9. Be auditable and easy to follow
10. Support valuation analysis with proper FCF calculations

---

## Common Model Types - Special Considerations

### High-Growth Tech/SaaS
- Focus on ARR growth and net retention
- Model by product line and geography
- Heavy R&D and S&M spend
- Path to profitability timeline
- Unit economics (LTV/CAC)

### E-commerce/Retail
- Revenue by product category and channel
- Store count and comp store growth (if applicable)
- Inventory turns and working capital
- Fulfillment costs
- Customer acquisition

### Manufacturing/Industrial
- Production capacity utilization
- Raw material costs and pricing
- Gross margin bridge (volume/price/mix/cost)
- CapEx heavy models
- Working capital cycles

---

## Next Steps

After completing Task 2, the financial model will be used for:
- **Task 3 (Valuation)**: DCF inputs, projected financials
- **Task 4 (Charts)**: Data for revenue trends, margin charts, scenario comparisons
- **Task 5 (Report Assembly)**: Financial data for report tables and analysis
