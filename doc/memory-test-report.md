# cli-agentx 记忆系统测试报告

**测试时间**：2026-03-17  
**测试环境**：macOS darwin 24.5.0  
**模型**：qwen-plus（DashScope）  
**Embedding 模型**：text-embedding-v4  
**测试版本**：Go 编译，数据库 `data/agent.db`

---

## 一、测试概述

本次测试针对 cli-agentx 的记忆系统进行全流程验证，覆盖以下四个模块：

| 模块 | 描述 |
|---|---|
| 自动摘要生成 | Run 完成后 `ProcessMemory` 异步生成摘要并存入 `summaries` 表 |
| 显式事实存储 | LLM 通过 `memory store` 工具调用将事实存入 `facts` 表 |
| 跨 Topic 语义召回 | 新 Topic 中基于 embedding 相似度自动注入 `<recall>` 上下文 |
| 显式记忆查询 | LLM 主动调用 `memory recent` 命令检索历史摘要 |

---

## 二、测试用例与执行结果

### 测试 Topic 信息

| Topic | ID | 用途 |
|---|---|---|
| `memory-test` | `100adcd5` | 存入记忆（3轮对话） |
| `recall-test` | `6939e54f` | 跨 Topic 召回验证（2轮） |

---

### TC-01：个人信息存入（Run: fab7b3a8）

**操作**：发送 "你好，我叫张伟，是一名Go语言工程师，已经工作了5年，主要做后端开发。请帮我记住这些信息。"

**LLM 响应**：
```
run(command="memory store 张伟，Go语言工程师，5年经验，专注后端开发")
```

**结果**：❌ **FAIL**

**原因**：LLM 以**纯文本**返回了工具调用（`response.content` 有值，`response.tool_calls` 字段不存在），Agent Loop 将其作为文本响应处理，`execToolCall` 从未被调用，`StoreFact()` 未执行，`facts` 表无数据写入。

**日志证据**（`logs/100adcd5/fab7b3a8_call_001_*.json`）：
```json
{
  "response": {
    "content": "run(command=\"memory store 张伟，Go语言工程师，5年经验，专注后端开发\")"
    // 无 "tool_calls" 字段
  }
}
```

---

### TC-02：学习目标与偏好存入（Run: dfc06177）

**操作**：发送 "我最近在学习Rust，目标是在3个月内完成一个用Rust写的CLI工具项目。另外，我喜欢喝咖啡，每天早上必须来一杯。"

**结果**：❌ **FAIL（同 TC-01）**

LLM 再次以文本模式返回 `run(command="memory store ...")`，命令未被执行。

---

### TC-03：工作成果存入（Run: ca9ab3fd）

**操作**：发送 "今天我完成了一个HTTP服务的性能优化，把P99延迟从200ms降低到了15ms..."

**结果**：❌ **FAIL（同 TC-01）**

---

### TC-04：自动摘要生成验证

每轮 Run 完成后，后台 `ProcessMemory` 工作进程均成功生成摘要：

| 摘要 ID | Run ID | 摘要内容（节选） | Embedding 大小 |
|---|---|---|---|
| 15 | fab7b3a8 | 用户张伟希望系统记住其身份信息（Go工程师、5年经验...） | 4096 维 |
| 16 | dfc06177 | 用户张伟表达了学习Rust的目标（3个月内完成CLI工具项目）... | 4096 维 |
| 17 | ca9ab3fd | 用户张伟意图记录今日技术成果（P99延迟从200ms降至15ms...） | 4096 维 |

**结果**：✅ **PASS** — 摘要生成、Embedding 编码、SQLite 存储均正常。

**数据库验证**：
```sql
SELECT id, summary[:60], length(embedding) FROM summaries WHERE topic_id='100adcd5';
-- 返回 3 条记录，embedding 均为 4096 bytes（1024个float32）
```

---

### TC-05：跨 Topic 自动语义召回（Run: 222cce51）

**操作**：新 Topic `6939e54f` 中发送 "我叫什么名字？我是做什么工作的？"

**预期**：`buildRecall()` 通过 embedding 相似度检索到摘要 15-17，自动注入 `<recall>` 标签

**实际结果**：❌ **FAIL** — 用户消息中无 `<recall>` 段落

**根因分析**：

`buildRecall` 使用用户原始查询文本 "我叫什么名字？我是做什么工作的？" 调用 Embedding API，其向量与摘要 15-17（描述张伟身份的第三人称叙述）的余弦相似度**未达到阈值 0.5**。

- 查询是第一人称问句（"我叫什么名字"）
- 摘要是第三人称陈述（"用户张伟希望系统记住..."）
- 语义空间差异导致相似度不足

**注意**：如果查询包含记忆相关词汇，相似度会更高（见 TC-06）。

---

### TC-06：显式 memory recent 查询（Run: 355968ef）

**操作**：发送 "请执行 memory recent 5 命令，把最近5条对话摘要告诉我"

**结果**：✅ **PASS**

**两项关键验证均通过**：

**1. 自动 `<recall>` 注入成功**（日志 `call_001_*.json`）：
```
<recall>
- [03-17 19:26] (70%) 用户张伟希望系统记住其身份信息（Go语言工程师、5年经验、专注后端开发）...
- [03-17 19:27] (67%) 用户张伟意图确认系统是否已记住其身份信息...
- [03-17 19:26] (65%) 用户张伟表达了学习Rust的目标...
</recall>
```

说明 "请执行 memory recent" 这类包含记忆操作语义的查询，与摘要 15-17 的相似度达到 65-70%，超过了 0.5 阈值。

**2. 结构化工具调用成功**（日志 `call_001_*.json` 含 `tool_calls` 字段）：
```json
{
  "response": {
    "content": "",
    "tool_calls": [{
      "function": {"name": "run", "arguments": "{\"command\": \"memory recent 5\"}"}
    }]
  }
}
```

LLM 正确使用了函数调用 API，`execToolCall` 被执行，成功返回 5 条历史摘要。

**LLM 最终回答（节选）**：
> ✅ 总结：你叫 **张伟**，职业是 **Go 语言工程师**，专注**后端开发**，有 **5 年经验**。

---

## 三、存储格式验证

### `summaries` 表（自动对话摘要）

```sql
-- 实际写入示例
id: 15
topic_id: "100adcd5"
run_id: "fab7b3a8"
summary: "用户张伟希望系统记住其身份信息（Go语言工程师、5年经验、专注后端开发）..."
user_message: "<user>\n你好，我叫张伟...\n</user>\n\n<environment>..."
embedding: [BLOB, 4096 bytes = 1024个float32，Little-Endian编码]
embedding_model: "text-embedding-v4"
created_at: 1773746768  (Unix timestamp)
```

- FTS5 虚拟表 `summaries_fts` 通过触发器自动同步，支持全文检索

### `facts` 表（显式事实）

```sql
-- 本次测试: 0 条记录（LLM工具调用失败，未写入）
SELECT COUNT(*) FROM facts; → 0
```

---

## 四、问题汇总

### 问题 P1：qwen-plus 函数调用退化为文本模式

**严重程度**：高

**现象**：LLM 将工具调用以纯文本格式返回（`response.content = "run(command=...)"` ，无 `tool_calls` 字段），Agent Loop 将其作为最终文本响应，命令未被执行。

**影响范围**：TC-01、TC-02、TC-03 全部失败，`facts` 表空。此外，摘要中出现 "系统执行了 `memory store` 命令将其存入记忆" 的**错误描述**（摘要 LLM 总结了 LLM "说的话"，非实际执行结果）。

**复现条件**：
- 模型：qwen-plus
- 场景：用户自然语言请求存储记忆（不显式说"执行命令"）
- 成功案例（TC-06）：用户明确说 "请执行 memory recent 5 命令" 时，LLM 正确使用了函数调用

**建议修复**：
1. 增加文本模式工具调用检测（解析 `content` 中的 `run(command=...)` 模式）
2. 或在 system prompt 中强化函数调用格式要求
3. 换用函数调用稳定性更高的模型（如 qwen-max、GPT-4o）

---

### 问题 P2：第一人称问句无法触发自动语义召回

**严重程度**：中

**现象**：用户发送 "我叫什么名字？我是做什么工作的？" 时，`buildRecall()` 返回空，`<recall>` 标签未注入，LLM 无法获取历史记忆。

**根因**：Embedding 相似度阈值为 0.5，第一人称问句与第三人称叙事摘要的语义向量距离较大。

**建议**：
1. 降低 `buildRecall` 中 `searchSemantic` 的阈值（0.5 → 0.3）
2. 或对用户查询进行改写（query rewriting）后再做语义搜索
3. 也可同时用 FTS5 关键词搜索补充（`memory search` 已支持，但 `buildRecall` 只用了语义搜索）

---

## 五、总体评分

| 功能模块 | 测试结果 | 说明 |
|---|---|:---|
| 自动摘要生成（ProcessMemory） | ✅ PASS | 每次 Run 后后台生成，摘要质量高 |
| Embedding 向量存储 | ✅ PASS | 1024维 float32 BLOB，正确编码 |
| FTS5 全文索引自动同步 | ✅ PASS | 触发器正常，支持关键词检索 |
| 跨 Topic 语义召回（记忆操作类查询） | ✅ PASS | 65-70% 相似度，召回正确 |
| 显式 memory recent 工具调用 | ✅ PASS | 结构化 tool_calls，执行成功 |
| 显式 memory store（自然语言请求） | ❌ FAIL | qwen-plus 退化为文本模式 |
| facts 表持久化 | ❌ FAIL | 因 P1 导致无数据写入 |
| 第一人称问句自动召回 | ❌ FAIL | 相似度低于 0.5 阈值 |

**通过率**：5 / 8（62.5%）

---

## 六、附录：数据库最终状态

```
summaries 表：19 条（含本次测试新增 4 条：ID 15-18）
facts 表：0 条
messages 表（topic 100adcd5）：6 条
messages 表（topic 6939e54f）：6 条
日志文件：
  logs/100adcd5/  → 3 个 call_001 文件
  logs/6939e54f/  → 1 个 call_001 + 2 个 call_001/002 文件（355968ef 2轮LLM调用）
```
