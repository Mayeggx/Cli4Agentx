# cli-agentx 记忆系统简单说明

这份文档是给后续开发或排查问题的人看的，尽量用通俗的话说明：
- 现在的记忆系统是什么
- 数据放在哪里
- 一次对话结束后发生了什么
- 搜索和召回是怎么工作的
- `memory compact` 又是干什么的

---

## 1. 一句话理解

现在的 `cli-agentx` 记忆系统，不只是数据库里的几张表了。

它同时有两套东西：
- **数据库记忆**：方便结构化存储、做 embedding、做 FTS 检索
- **本地文件记忆**：方便人直接看、直接调试、embedding 不稳定时也还能做召回

可以把它理解成：

- DB 像“正式账本”
- `data/memory/` 像“可读的记忆文件夹”

这两边现在会尽量同步。

---

## 2. 数据在哪里

主要目录：`cli-agentx/data/memory/`

里面常见内容：

- `MEMORY.md`
  - 长期记忆总览
  - 里面有稳定 facts 和最近的摘要

- `SESSION-STATE.md`
  - 当前最热的工作区
  - 主要放最近几次 run 的摘要

- `insights/YYYY-MM.md`
  - 月度浓缩视图
  - 更像最近摘要的月度聚合

- `lessons/operational-lessons.jsonl`
  - 从 `facts` 表导出的结构化事实

- `runs/YYYY-MM-DD/*.md`
  - 每次 run 的原始本地记忆笔记
  - 包含 topic、run、时间、summary、user intent

- `archive/runs/YYYY-MM-DD/`
  - 被 `memory compact` 归档的旧 run note

另外，数据库里仍然有：
- `summaries` 表：自动摘要
- `facts` 表：手动存储的事实
- `summaries_fts`：全文搜索

---

## 3. 一次对话结束后会发生什么

当一个 run 正常完成后，会走 `ProcessMemory()`。

它大致做这几件事：

### 第一步：生成摘要

系统会把这一轮新增消息整理成一段短摘要。

目标是写清楚：
- 用户想做什么
- 执行了什么
- 最后结果怎样

### 第二步：写入数据库

摘要会写进 `summaries` 表。

如果 embedding 可用，也会一起存 embedding，后面做语义召回会用到。

### 第三步：同步到本地 memory store

会调用 `SyncLocalMemoryStore()`，刷新本地文件记忆，包括：
- 写一份 run note 到 `data/memory/runs/YYYY-MM-DD/*.md`
- 刷新 `SESSION-STATE.md`
- 刷新 `MEMORY.md`
- 刷新月度 `insights`
- 刷新 lessons 的 jsonl
- 刷新 `.abstract` 索引

所以现在不是“只有数据库有记忆”，而是“数据库 + 本地文件一起更新”。

---

## 4. 记忆分层可以怎么理解

代码里用了类似 `L0/L1/L2` 和 `P0/P1/P2` 的概念，不用想得太复杂。

可以简单理解成：

- `L0`
  - 导航层
  - 例如 `.abstract`
  - 用来告诉系统“这里有哪些记忆文件”

- `L1`
  - 浓缩层
  - 例如 `insights/2026-03.md`
  - 比较适合快速浏览

- `L2`
  - 细节层
  - 例如 `runs/YYYY-MM-DD/*.md`
  - 保存更原始的 run 记录

再加上另一套持久层表达：

- `P0`
  - 稳定事实
  - 比如 `facts`

- `P1`
  - 提炼后的上下文
  - 比如 `MEMORY.md` 里的 distilled context

- `P2`
  - 热工作区
  - 比如 `SESSION-STATE.md`

你不需要死记这些标签，只要知道：
- 有些文件是“导航和摘要”
- 有些文件是“细节和原始记录”

---

## 5. 搜索记忆时怎么工作

### `memory search`

现在 `memory search` 不是只查数据库了，而是会把几种来源拼在一起：

#### 1）本地文件搜索

先扫描 `data/memory/` 里的关键文件：
- `.abstract`
- `MEMORY.md`
- `SESSION-STATE.md`
- 当月 `insights`
- `lessons`
- 最近几天的 `runs` 笔记

然后做一个很轻量的 lexical 匹配打分。

这部分的好处是：
- 不依赖 embedding
- 中文自然问句也有机会召回
- 容易调试，因为你能直接看到匹配的是哪一行文本

#### 2）数据库搜索

数据库这边会尝试：
- semantic search（embedding 相似度）
- FTS 全文搜索
- lexical fallback

#### 3）最后合并排序

把本地结果和数据库结果合并、去重、排序，再输出。

所以现在的检索路线比以前稳一些：
- embedding 好用时，用 semantic
- embedding 不好用时，还有本地文本召回顶着

---

## 6. 自动 recall 是怎么来的

当用户发一条新消息时，`BuildContext()` 会调用 `buildRecall()`。

现在 `buildRecall()` 不再只靠 embedding 了，而是会调用 `RecallMemories()`。

也就是说，自动 recall 用的是“综合检索”：
- 本地文件记忆
- DB semantic
- DB FTS
- lexical fallback

最后会把命中的记忆，放进用户消息里的 `<recall>` 段落。

这样模型在回答前，就能先看到一批相关历史记忆。

---

## 7. `memory recent` 是干什么的

`memory recent [n]` 主要给模型或人快速查看最近上下文。

它现在会优先显示：
- `SESSION-STATE.md`
- 然后再补数据库里最近的 summaries

所以它更像“最近状态快照”，不是完整历史浏览器。

---

## 8. `memory store` 和 facts 是什么关系

`memory store <note>` 是显式记忆写入。

它会把内容写进 `facts` 表。

同时也会触发本地 memory store 刷新，所以：
- 数据库里会有结构化事实
- `data/memory/lessons/operational-lessons.jsonl` 也会更新
- `MEMORY.md` 也能反映这些稳定事实

也就是说，`memory store` 更像“明确告诉系统，这条要长期记住”。

---

## 9. `memory compact` 是干什么的

这是这次新加的能力。

命令：

```bash
memory compact
memory compact 7
```

作用很简单：
- 保留最近几天的 `runs/` 目录作为热数据
- 更早的 run note 挪到 `archive/runs/`

它的目标不是删除记忆，而是把旧的原始笔记归档，避免热区越来越乱。

### 例子

如果今天是 3 月 19 日，执行：

```bash
memory compact 7
```

那么大致会：
- 保留最近 7 天的 `data/memory/runs/YYYY-MM-DD/`
- 把更早的日期目录移到 `data/memory/archive/runs/YYYY-MM-DD/`

### 现在的限制

它目前还是轻量版：
- 只归档旧 run note
- 还不会做更深度的 archive 摘要重写
- 也还没有 dry-run 预览模式

所以它更像“整理文件夹”，不是“高级知识压缩器”。

---

## 10. 这套方案为什么有用

它最实际的价值有三个：

### 1）更容易看

以前很多记忆只在 DB 里，不够直观。

现在你可以直接打开：
- `MEMORY.md`
- `SESSION-STATE.md`
- `runs/*.md`

看系统到底记住了什么。

### 2）更容易调试

embedding 召回失败时，以前很难看清问题。

现在本地文件也参与 recall，你可以直接检查：
- 哪个文件命中了
- 命中的文本是什么
- 为什么它能/不能召回

### 3）更抗 embedding 波动

某些模型、某些问法下，semantic recall 不稳定。

现在至少还有：
- FTS
- lexical search
- 本地文件分层检索

不会一条路挂掉就完全失忆。

---

## 11. 当前已知不足

目前这套记忆系统已经能用，但还不是最终形态。

主要不足有：

- topic 过滤还比较弱
  - 本地文件命中时，对 topic 的判断比较粗糙

- `MEMORY.md` 还不是真正的 compaction pipeline
  - 现在更像“最近摘要整理视图”

- `memory compact` 还比较轻
  - 只是归档旧 run notes
  - 还没有 archive digest 或 dry-run

- 文本形式工具调用问题还在
  - 如果模型把 `run(command="...")` 当普通文本输出，而不是正式 tool call
  - 系统目前还是不会自动执行

---

## 12. 后面最值得继续做什么

如果要继续开发，优先级比较高的方向是：

### A. 优化 `memory search` 的展示

让结果按层分开展示：
- 先看 `L0/L1` 摘要
- 再决定要不要下钻到 `L2` run note

这样输出会更清晰。

### B. 处理文本形式的假工具调用

这是测试报告里最影响体验的问题。

例如模型输出：

```text
run(command="memory store 我叫张伟")
```

如果它只是普通文本，不是正式 `tool_calls`，现在不会执行。

这块如果补上 fallback，记忆写入体验会明显更好。

### C. 让 compact 更聪明

例如：
- 支持 dry-run
- 生成 archive 索引
- 让 `SESSION-STATE.md` 更严格只保留热内容

---

## 13. 相关代码入口

如果你要继续改代码，优先看这几个文件：

- `cli-agentx/internal/memory_store.go`
  - 本地 memory 文件写入、刷新、compact

- `cli-agentx/internal/memory.go`
  - DB 记忆、综合搜索、Recall

- `cli-agentx/internal/context.go`
  - 自动 recall 注入到上下文

- `cli-agentx/internal/tools.go`
  - `memory search/recent/store/compact` 命令入口

- `cli-agentx/doc/memory-test-report.md`
  - 之前测试里发现的问题和证据

---

## 14. 最后一句总结

现在的记忆系统可以简单理解为：

**数据库负责“存得正规”，本地文件负责“看得明白”，两边一起为 recall 服务。**

而 `memory compact` 则是在这个基础上，开始补上“旧记忆怎么整理”的第一步。
