# PowerPoint XML参考

本文件包含用于程序化PowerPoint编辑的XML模式。在直接使用OOXML格式时使用这些模式。

**注意：** 示例中的颜色值（如`E67E22`、`D35400`）是占位符。替换为您模板的品牌颜色。

---

## ⚠️ 何时使用本参考

**使用python-pptx用于：**
- 创建新表格（自动处理单元格结构和关系）
- 添加文本框
- 插入图像
- 大多数形状创建
- python-pptx提供API的任何操作

**仅在以下情况使用直接XML编辑：**
- 修改python-pptx未公开的现有元素属性
- 通过python-pptx创建表格后微调单元格格式
- 调整python-pptx API不可用的特定形状属性

**绝不要使用直接XML用于：**
- 从头创建表格（关系管理容易出错，很可能损坏文件）
- 初始形状创建（形状ID冲突风险）
- 任何可以通过python-pptx完成的操作

本文件中的XML模式用于**参考和针对性修改**，而非整体元素构建。

---

## XML编辑风险

直接XML编辑如果不小心可能会损坏PowerPoint文件：
- PowerPoint XML具有相互依赖性（关系文件、内容类型）
- 无效XML或缺失关系可能损坏整个文件
- 形状ID必须在每张幻灯片中唯一

**始终在备份副本上工作** — 绝不要直接编辑原始文件。

---

## 目录
- [表格实现](#表格实现)
- [箭头形状](#箭头形状)
- [文本框](#文本框)
- [带填充的形状](#带填充的形状)
- [图像插入](#图像插入)
- [连接线](#连接线)
- [单位转换](#单位转换)

---

## 表格实现

### 关键：验证表格是实际的表格对象

创建任何表格后，必须验证它是实际的表格对象，而非带分隔符的文本。

**程序化验证（python-pptx）：**
```python
for shape in slide.shapes:
    if shape.has_table:
        print(f"✓ 找到表格：{len(shape.table.rows)}行，{len(shape.table.columns)}列")
```

**视觉验证（在导出图像中）：**
- 无论内容长度如何，列完美对齐
- 单元格边框一致
- 选择表格时将所有单元格作为一个单元选择

**失败指标 — 您创建的是文本，而非表格：**
- 值之间可见`|`字符
- 内容长度变化时列不对齐
- 使用制表符（`\t`）进行间距
- 多个文本框排列成看起来像表格

基于文本的"表格"无法被接收者编辑，字体变化时会不对齐，并表明业余工作。在演示文稿中没有可接受的用例使用管道符/制表符分隔的表格数据。

---

### 基本表格结构

```xml
<a:tbl>
  <a:tblPr firstRow="1" bandRow="1">
    <a:tableStyleId>{5C22544A-7EE6-4342-B048-85BDC9FD1C3A}</a:tableStyleId>
  </a:tblPr>
  <a:tblGrid>
    <a:gridCol w="2000000"/>  <!-- 来源列 - 宽度以EMU为单位 -->
    <a:gridCol w="1200000"/>  <!-- 2024规模列 -->
    <a:gridCol w="1200000"/>  <!-- CAGR列 -->
    <a:gridCol w="1200000"/>  <!-- 2030预测列 -->
  </a:tblGrid>
  <!-- 行定义如下 -->
</a:tbl>
```

### 带单元格的表格行

```xml
<a:tr h="370840">  <!-- 行高以EMU为单位 -->
  <a:tc>
    <a:txBody>
      <a:bodyPr/>
      <a:lstStyle/>
      <a:p>
        <a:pPr algn="l"/>  <!-- 文本列左对齐 -->
        <a:r>
          <a:rPr lang="en-US" sz="1000" b="0"/>
          <a:t>Grand View Research</a:t>
        </a:r>
      </a:p>
    </a:txBody>
    <a:tcPr/>
  </a:tc>
  <a:tc>
    <a:txBody>
      <a:bodyPr/>
      <a:lstStyle/>
      <a:p>
        <a:pPr algn="ctr"/>  <!-- 数字列居中对齐 -->
        <a:r>
          <a:rPr lang="en-US" sz="1000"/>
          <a:t>22.1</a:t>
        </a:r>
      </a:p>
    </a:txBody>
    <a:tcPr/>
  </a:tc>
  <!-- 其他单元格... -->
</a:tr>
```

### 标题行样式

```xml
<a:tr h="370840">
  <a:tc>
    <a:txBody>
      <a:bodyPr/>
      <a:lstStyle/>
      <a:p>
        <a:pPr algn="l"/>
        <a:r>
          <a:rPr lang="en-US" sz="1000" b="1">  <!-- 标题粗体 -->
            <a:solidFill>
              <a:srgbClr val="FFFFFF"/>  <!-- 白色文本 -->
            </a:solidFill>
          </a:rPr>
          <a:t>来源</a:t>
        </a:r>
      </a:p>
    </a:txBody>
    <a:tcPr>
      <a:solidFill>
        <a:srgbClr val="E67E22"/>  <!-- 橙色背景 -->
      </a:solidFill>
    </a:tcPr>
  </a:tc>
  <!-- 其他标题单元格... -->
</a:tr>
```

---

## 箭头形状

### 右箭头形状

```xml
<p:sp>
  <p:nvSpPr>
    <p:cNvPr id="10" name="右箭头"/>
    <p:cNvSpPr/>
    <p:nvPr/>
  </p:nvSpPr>
  <p:spPr>
    <a:xfrm>
      <a:off x="3000000" y="2500000"/>  <!-- 位置以EMU为单位 -->
      <a:ext cx="500000" cy="300000"/>   <!-- 大小以EMU为单位 -->
    </a:xfrm>
    <a:prstGeom prst="rightArrow">
      <a:avLst/>
    </a:prstGeom>
    <a:solidFill>
      <a:srgbClr val="E67E22"/>  <!-- 箭头填充颜色 -->
    </a:solidFill>
    <a:ln>
      <a:noFill/>  <!-- 无轮廓 -->
    </a:ln>
  </p:spPr>
</p:sp>
```

### 下箭头形状

```xml
<p:sp>
  <p:nvSpPr>
    <p:cNvPr id="11" name="下箭头"/>
    <p:cNvSpPr/>
    <p:nvPr/>
  </p:nvSpPr>
  <p:spPr>
    <a:xfrm>
      <a:off x="2500000" y="3000000"/>
      <a:ext cx="300000" cy="500000"/>
    </a:xfrm>
    <a:prstGeom prst="downArrow">
      <a:avLst/>
    </a:prstGeom>
    <a:solidFill>
      <a:srgbClr val="E67E22"/>
    </a:solidFill>
  </p:spPr>
</p:sp>
```

### V形形状

```xml
<p:sp>
  <p:nvSpPr>
    <p:cNvPr id="12" name="V形"/>
    <p:cNvSpPr/>
    <p:nvPr/>
  </p:nvSpPr>
  <p:spPr>
    <a:xfrm>
      <a:off x="3000000" y="2500000"/>
      <a:ext cx="400000" cy="600000"/>
    </a:xfrm>
    <a:prstGeom prst="chevron">
      <a:avLst/>
    </a:prstGeom>
    <a:solidFill>
      <a:srgbClr val="E67E22"/>
    </a:solidFill>
  </p:spPr>
</p:sp>
```

---

## 文本框

### 基本文本框

```xml
<p:sp>
  <p:nvSpPr>
    <p:cNvPr id="5" name="文本框 4"/>
    <p:cNvSpPr txBox="1"/>
    <p:nvPr/>
  </p:nvSpPr>
  <p:spPr>
    <a:xfrm>
      <a:off x="500000" y="1500000"/>
      <a:ext cx="4000000" cy="500000"/>
    </a:xfrm>
    <a:prstGeom prst="rect">
      <a:avLst/>
    </a:prstGeom>
    <a:noFill/>
  </p:spPr>
  <p:txBody>
    <a:bodyPr wrap="square" rtlCol="0">
      <a:spAutoFit/>
    </a:bodyPr>
    <a:lstStyle/>
    <a:p>
      <a:r>
        <a:rPr lang="en-US" sz="1400" dirty="0"/>
        <a:t>文本内容在此</a:t>
      </a:r>
    </a:p>
  </p:txBody>
</p:sp>
```

### 带项目符号的文本框

```xml
<p:txBody>
  <a:bodyPr wrap="square">
    <a:spAutoFit/>
  </a:bodyPr>
  <a:lstStyle/>
  <a:p>
    <a:pPr marL="342900" indent="-342900">
      <a:buFont typeface="Wingdings" panose="05000000000000000000" pitchFamily="2" charset="2"/>
      <a:buChar char="&#252;"/>  <!-- 勾选字符 -->
    </a:pPr>
    <a:r>
      <a:rPr lang="en-US" sz="1400" dirty="0"/>
      <a:t>第一个项目符号</a:t>
    </a:r>
  </a:p>
  <a:p>
    <a:pPr marL="342900" indent="-342900">
      <a:buFont typeface="Wingdings" panose="05000000000000000000" pitchFamily="2" charset="2"/>
      <a:buChar char="&#252;"/>
    </a:pPr>
    <a:r>
      <a:rPr lang="en-US" sz="1400" dirty="0"/>
      <a:t>第二个项目符号</a:t>
    </a:r>
  </a:p>
</p:txBody>
```

### 白色文本（用于深色背景）

```xml
<a:r>
  <a:rPr lang="en-US" sz="1000" b="1" i="1" dirty="0">
    <a:solidFill>
      <a:srgbClr val="FFFFFF"/>  <!-- 白色文本 -->
    </a:solidFill>
  </a:rPr>
  <a:t>彩色背景上的白色文本</a:t>
</a:r>
```

---

## 带填充的形状

### 带实心填充的矩形

```xml
<p:sp>
  <p:nvSpPr>
    <p:cNvPr id="20" name="矩形 19"/>
    <p:cNvSpPr/>
    <p:nvPr/>
  </p:nvSpPr>
  <p:spPr>
    <a:xfrm>
      <a:off x="500000" y="2500000"/>
      <a:ext cx="1000000" cy="2000000"/>
    </a:xfrm>
    <a:prstGeom prst="rect">
      <a:avLst/>
    </a:prstGeom>
    <a:solidFill>
      <a:srgbClr val="E67E22"/>  <!-- 橙色填充 -->
    </a:solidFill>
    <a:ln w="12700">  <!-- 边框宽度 -->
      <a:solidFill>
        <a:srgbClr val="D35400"/>  <!-- 较深边框 -->
      </a:solidFill>
    </a:ln>
  </p:spPr>
  <p:txBody>
    <a:bodyPr rtlCol="0" anchor="ctr"/>  <!-- 垂直居中文本 -->
    <a:lstStyle/>
    <a:p>
      <a:pPr algn="ctr"/>  <!-- 水平居中 -->
      <a:r>
        <a:rPr lang="en-US" sz="1600" b="1">
          <a:solidFill>
            <a:srgbClr val="FFFFFF"/>
          </a:solidFill>
        </a:rPr>
        <a:t>标签文本</a:t>
      </a:r>
    </a:p>
  </p:txBody>
</p:sp>
```

---

## 图像插入

### 向幻灯片添加图像

```xml
<p:pic>
  <p:nvPicPr>
    <p:cNvPr id="99" name="公司标志"/>
    <p:cNvPicPr>
      <a:picLocks noChangeAspect="1"/>
    </p:cNvPicPr>
    <p:nvPr/>
  </p:nvPicPr>
  <p:blipFill>
    <a:blip r:embed="rIdLogo"/>  <!-- 引用关系ID -->
    <a:stretch>
      <a:fillRect/>
    </a:stretch>
  </p:blipFill>
  <p:spPr>
    <a:xfrm>
      <a:off x="10800000" y="200000"/>  <!-- 右上角位置 -->
      <a:ext cx="800000" cy="600000"/>   <!-- 标志尺寸 -->
    </a:xfrm>
    <a:prstGeom prst="rect">
      <a:avLst/>
    </a:prstGeom>
  </p:spPr>
</p:pic>
```

### 添加图像关系

在 `ppt/slides/_rels/slideN.xml.rels` 中：

```xml
<Relationship Id="rIdLogo" 
  Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" 
  Target="../media/logo.png"/>
```

---

## 连接线

### 直线连接器

```xml
<p:cxnSp>
  <p:nvCxnSpPr>
    <p:cNvPr id="15" name="直线连接器 14"/>
    <p:cNvCxnSpPr>
      <a:cxnSpLocks/>
    </p:cNvCxnSpPr>
    <p:nvPr/>
  </p:nvCxnSpPr>
  <p:spPr>
    <a:xfrm>
      <a:off x="500000" y="2500000"/>
      <a:ext cx="5000000" cy="0"/>  <!-- 水平线 -->
    </a:xfrm>
    <a:prstGeom prst="line">
      <a:avLst/>
    </a:prstGeom>
    <a:ln w="12700">
      <a:solidFill>
        <a:srgbClr val="E67E22"/>
      </a:solidFill>
    </a:ln>
  </p:spPr>
</p:cxnSp>
```

### 虚线

```xml
<p:spPr>
  <a:xfrm>
    <a:off x="500000" y="4500000"/>
    <a:ext cx="5000000" cy="0"/>
  </a:xfrm>
  <a:prstGeom prst="line">
    <a:avLst/>
  </a:prstGeom>
  <a:ln w="12700">
    <a:solidFill>
      <a:srgbClr val="E67E22"/>
    </a:solidFill>
    <a:prstDash val="dash"/>  <!-- 虚线样式 -->
  </a:ln>
</p:spPr>
```

---

## 单位转换

| 单位 | 每单位EMU数 |
|------|-------------|
| 1英寸 | 914400 |
| 1厘米 | 360000 |
| 1磅 | 12700 |
| 1像素（96 DPI） | 9525 |

### 常见幻灯片尺寸（16:9）

- 宽度：12192000 EMU（13.333英寸）
- 高度：6858000 EMU（7.5英寸）

### 典型元素位置

| 元素 | X位置 | Y位置 |
|------|-------|-------|
| 标志（右上角） | 10800000 | 200000 |
| 标题 | 342583 | 286603 |
| 副标题 | 402591 | 1767390 |
| 页脚 | 342583 | 6435334 |
