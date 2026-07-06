---
description: 从PowerPoint模板文件创建可复用的PPT模板技能
argument-hint: "[.pptx或.potx文件路径]"
allowed-tools: ["Read", "Write", "Bash", "Glob"]
---

# PPT模板创建器命令

从用户提供的PowerPoint模板创建独立的PPT模板技能。

## 说明

1. **如果未提供模板文件，询问模板文件**：
   - "请提供您的PowerPoint模板文件路径（.pptx或.potx）"
   - 模板应包含您想使用的幻灯片布局和品牌元素

2. **加载ppt-template-creator技能**：
   - 使用`skill: "ppt-template-creator"`工具加载完整的技能说明
   - 按照技能中的工作流程分析模板并生成新技能

3. **收集额外信息**：
   - 公司/模板名称（用于命名技能）
   - 主要用例（路演材料、董事会材料、客户演示等）

4. **执行技能工作流程**：
   - 分析模板结构（布局、占位符、尺寸）
   - 生成包含assets/和SKILL.md的技能目录
   - 创建示例演示文稿进行验证
   - 打包技能

5. **将打包好的技能交付给用户**
