# cli-agentx 记忆系统测试说明报告

这份报告用尽量简单的话说明三件事：

1. 这套记忆系统有什么特色  
2. 针对每个特色做了什么测试  
3. 实际返回怎么证明这个特色是真的

为了更好读，下面按“**特色 → 测试 → 依据**”来写。

---

## 1. 特色：记忆不只在数据库里，也会落到本地文件里

### 做了什么测试

我先发了一句：

> 请记住：我叫李航，我的职业是分布式系统工程师，我喜欢乌龙茶。

然后又继续做了几轮和 Redis 排障有关的对话，观察本地 memory 文件有没有同步更新。

### 实际返回和依据

系统没有只是口头说“记住了”，而是先真实执行了工具调用：

```text
run({"command": "memory store 用户叫李航，职业是分布式系统工程师，喜欢乌龙茶"})
→ fact stored
```

然后回复：

> 已记住：李航，分布式系统工程师，喜欢乌龙茶。

同时，在文件里可以看到：
- `cli-agentx/data/memory/MEMORY.md`
- `cli-agentx/data/memory/SESSION-STATE.md`
- `cli-agentx/data/memory/lessons/operational-lessons.jsonl`
- `cli-agentx/data/memory/runs/2026-03-19/*.md`

这些文件都出现了新的内容。

### 这说明什么

这说明这套系统不是只有数据库表，
而是会把记忆同步到本地文件里。

简单说就是：
- DB 负责存
- 本地文件负责看和调试

---

## 2. 特色：记忆是分层的，不同信息会落到不同层

### 做了什么测试

我连续发了几句：

> 我今天在排查 Redis 延迟尖刺问题。  
> 刚刚发现主要原因可能是连接池配置太小。  
> 请记住这个排障细节：P99 从 220ms 降到 18ms，关键改动是把连接池从 16 调到 64，并开启批量 pipeline。  
> 请总结一下我最近在处理什么问题。

### 实际返回和依据

测试后，我在文件里看到了几种不同的落点：

#### 稳定事实层 `P0`

在 `cli-agentx/data/memory/MEMORY.md` 里出现：

```text
[general] Redis 延迟优化：连接池 16→64 + pipeline 批量，P99 从 220ms 降至 18ms
[general] 用户叫李航，职业是分布式系统工程师，喜欢乌龙茶
```

在 `cli-agentx/data/memory/lessons/operational-lessons.jsonl` 里也有对应记录。

#### 提炼上下文层 `P1`

在 `cli-agentx/data/memory/MEMORY.md` 的 `P1 Distilled Context` 里，可以看到最近摘要化后的内容。

#### 热状态层 `P2`

在 `cli-agentx/data/memory/SESSION-STATE.md` 里，可以看到最近几轮摘要，比如：

```text
topic=ae7109ba run=... 用户意图执行记忆压缩操作并要求原样展示结果...
```

#### 细节层 `L2`

在 `cli-agentx/data/memory/runs/2026-03-19/203023_ae7109ba_376fcd20.md` 这样的文件里，可以看到：
- topic
- run
- time
- Summary
- User Intent

### 这说明什么

这说明它不是把所有内容都混成一团，
而是真的有分层：
- `P0` 放长期事实
- `P1` 放提炼后的上下文
- `P2` 放最近的工作缓冲
- `L2` 放更原始的细节证据

也就是说，系统已经开始区分：
- 什么该长期记住
- 什么只是最近状态
- 什么是原始细节

---

## 3. 特色：检索不只靠 embedding，还支持 lexical 匹配

### lexical 是什么

这里的 `lexical` 可以简单理解成：

**按字面和关键词去找。**

也就是说，它不是先把句子变成向量再算相似度，
而是直接看：
- 关键词有没有出现
- 词面是不是很接近
- 一些短语、数字、中文词片段是不是对得上

比如：
- “乌龙茶”
- “连接池 64”
- “pipeline”

这种词面非常明确的查询，
就很适合 lexical 去找。

它的好处是：
- 简单直接
- 容易解释
- embedding 不稳定时也还能工作

### lexical 的原理是什么

它的原理并不复杂，可以粗略理解成三步：

#### 第一步：先把文本整理一下

系统会先把查询和记忆文本做一个轻量清洗，比如：
- 统一大小写
- 去掉多余符号
- 保留中文、字母、数字

这样做是为了让“连接池 64”“连接池64”“连接池-64”这类写法更容易对上。

#### 第二步：把查询拆成更小的词

系统不会只拿整句话去找，
还会把问题拆成更小的词和短片段，比如：
- 空格拆词
- 中文双字片段
- 一些补充词

例如：
- “我喜欢喝什么” 可能会扩出“喜欢”“喜好”“偏好”
- “我做什么工作” 可能会扩出“职业”“工程师”
- “连接池 64” 会保留数字词 `64`

这一步的作用是：
- 用户说法和记忆原文不完全一样时，也更容易撞上

#### 第三步：给每条候选记忆打分

系统会看：
- 整句有没有直接包含
- 拆出来的小词命中了几个
- 数字词有没有真的对上
- 命中的是热区、总览还是细节层

然后给一个分数，分高的排前面。

所以 lexical 本质上不是“理解语义”，
而是“按词面接近程度排序”。

这也是为什么它特别适合：
- 偏好词
- 名字
- 职业
- 参数
- 数字
- 文件名/命令名

### 做了什么测试

我专门让系统执行了两条检索：

> 请执行 memory search 乌龙茶，并原样展示结果。  
> 请执行 memory search 连接池 64，并原样展示结果。

这样做是因为：
- “乌龙茶”是明显的偏好关键词
- “连接池 64”是明显的技术参数关键词

如果 lexical 匹配真的有用，这两类查询就应该能被找出来。

### 实际返回和依据

当我搜索“乌龙茶”时，系统返回过这样的结果：

```text
Found 1 memory hits:
  [03-19 20:28] DB-lexical db:topic=ae7109ba run=fc5fb7b5
    用户李航意图让系统记录其姓名、职业（分布式系统工程师）及喜好（乌龙茶）。系统执行 `memory store` 命令将该信息存入长期记忆。最终信息成功保存，系统确认已记住该内容。
```

这里最关键的是：

- 返回里直接出现了 `DB-lexical`

这说明 lexical 匹配真实参与了召回，不是只靠 embedding。

后来在继续测试后，再搜“乌龙茶”时，返回又变成了：

```text
Found 5 memory hits:
  ... P2 memory/SESSION-STATE.md
  ... P1 memory/MEMORY.md
```

这说明同一个关键词，还能命中不同层。

### 这说明什么

这证明了这套系统的一个核心特点：

- 不只依赖 embedding
- 词面很强的关键词，直接靠 lexical 也能命中

所以它比“纯向量记忆”更稳。

---

## 4. 特色：检索结果会告诉你命中的是哪一层

### 做了什么测试

我让系统执行：

> 请执行 memory search 连接池 64，并原样展示结果。

### 实际返回和依据

修复之后，关键返回变成了：

```text
Found 5 memory hits:
  [03-19 20:39] P1 memory/MEMORY.md
    [general] Redis 延迟优化：连接池 16→64 + pipeline 批量，P99 从 220ms 降至 18ms
  [03-19 20:39] P0 memory/lessons/operational-lessons.jsonl
    {"id":2,"category":"general","content":"Redis 延迟优化：连接池 16→64 + pipeline 批量，P99 从 220ms 降至 18ms","created_at":1773923291}
```

之前测试里也看到过这样的层级命中：

```text
P2 memory/SESSION-STATE.md
P1 memory/MEMORY.md
L1 memory/insights/2026-03.md
DB ...
```

### 这说明什么

这说明系统不是只给你一句“我想起来了”。

它还会告诉你：
- 这条记忆来自 `P0`、`P1`、`P2` 还是 `L1/L2`
- 来自哪个具体文件

这就让记忆系统变得“可解释”。

你不仅知道它答对了，
还知道它是从哪里想到的。

---

## 5. 特色：系统会自动 recall，不只是手动 search

### recall 机制是什么

这里的 `recall` 可以简单理解成：

**在模型正式回答前，系统先替它翻一下以前的记忆，把可能相关的内容提前塞进上下文。**

也就是说，用户刚问完问题，系统不会立刻让模型硬答，
而是会先做一步“回忆”：
- 去搜本地 memory 文件
- 去搜 DB 里的摘要和事实
- 综合 semantic、FTS、lexical 的结果
- 把命中的内容放进 `<recall>` 里

然后模型是“看着这段 recall 再回答”的。

所以 recall 不是最终答案本身，
而是回答之前的一段“参考记忆”。

### recall 的原理是什么

它的原理可以理解成下面这个顺序：

#### 第一步：用户发出一个新问题

比如：
- “我叫什么名字？”
- “我最近在处理什么技术问题？”
- “我喜欢喝什么？”

系统会先把这个问题当成一个“记忆检索查询”。

#### 第二步：系统先去找相关记忆

它会根据这个问题，去多个地方一起找：
- 本地 memory 文件
- DB 里的 summaries
- DB 里的 facts

找的时候不是只走一条路，
而是把几种方式一起用：
- semantic
- FTS
- lexical
- 本地文件分层搜索

#### 第三步：挑出最像的几条，塞进 `<recall>`

不是所有命中都会塞进去，
系统会选前几条更相关的内容，整理成这种形式：

```text
<recall>
- [时间] [层级] 记忆内容
- [时间] [层级] 记忆内容
</recall>
```

这样模型在真正作答前，
就先“看过”这些记忆了。

#### 第四步：模型基于 recall 再回答

所以最后用户看到的答案，
并不是模型凭空想的，
而是模型在“读过 recall”之后给出的回答。

这也是为什么日志里经常会出现：
- `<user>`
- `<recall>`
- `<environment>`

它们其实就是在告诉模型：
- 用户刚刚问了什么
- 以前有哪些相关记忆
- 当前环境是什么

### 做了什么测试

我新开了一个话题，然后问：

> 我叫什么名字？  
> 我最近在处理什么技术问题？  
> 我喜欢喝什么？

### 实际返回和依据

当我问：

> 我叫什么名字？

系统直接回答：

> 李航。  
> 职业是分布式系统工程师，喜欢乌龙茶。

而且在日志里能看到，这次请求不是“空着去猜”，而是已经自动注入了 `<recall>`：

```text
<recall>
- [03-19 15:35] [P2] ...
- [03-19 15:35] [P1] ...
- [03-19 15:35] [L1] ...
</recall>
```

对应日志：
- `cli-agentx/logs/394b4dc5/1d37a08e_call_001_20260319-203016.json`

### 这说明什么

这说明系统会自动先去找相关记忆，再拿着这些记忆去回答。

也就是说：
- 手动 `memory search` 是一种方式
- 自动 `<recall>` 又是另一种方式

这证明它不是只会“显式搜索”，而是真的开始具备自动回忆能力。

---

## 6. 特色：记忆开始有冷热分区，旧内容可以归档

### 做了什么测试

我先放了一个旧日期的 run note 样本，
然后让系统执行：

> 请执行 memory compact 7，并原样展示结果。

### 实际返回和依据

系统工具返回：

```text
archived 1 run note(s) from 1 day folder(s); kept last 7 day(s) hot
```

然后我检查文件变化：

#### 执行前

- `data/memory/runs/2026-03-01/090000_seed_old_run.md`

#### 执行后

- `data/memory/runs/2026-03-01/` 不见了
- `data/memory/archive/runs/2026-03-01/090000_seed_old_run.md` 出现了

对应日志：
- `cli-agentx/logs/ae7109ba/4fef2a26_call_001_20260319-203104.json`
- `cli-agentx/logs/ae7109ba/4fef2a26_call_002_20260319-203106.json`

### 这说明什么

这说明 `memory compact 7` 不是“嘴上说归档了”，
而是真的把旧 run note 移到了 archive。

这就是记忆生命周期管理的开始：
- 新的留在热区
- 旧的进 archive

---

## 7. 特色：`memory recent` 更像“最近状态快照”

### 做了什么测试

我让系统执行：

> 请执行 memory recent 5，并原样展示结果。

### 实际返回和依据

修复后，系统返回的是一个更短的结果，像这样：

```text
# Session State

Working buffer for the latest runs. Keep it short and hot.

- [03-19 20:32] topic=ae7109ba run=216ca874 ...
- [03-19 20:31] topic=ae7109ba run=4fef2a26 ...
- [03-19 20:31] topic=394b4dc5 run=fecbe990 ...
- [03-19 20:31] topic=394b4dc5 run=1d37a08e ...
- [03-19 20:31] topic=394b4dc5 run=770613a3 ...
```

### 这说明什么

这说明 `memory recent` 现在更像一个“最近状态窗口”，
而不是无限把一大串历史都倒出来。

虽然它还可以继续优化，
但已经比之前更接近“热工作缓冲”的感觉了。

---

## 8. agent 实际是怎么读这些记忆的

很多人看到 `P0/P1/P2`、`L0/L1/L2`，会以为 agent 是去读某个同名文件夹。

其实不是。

agent 读记忆时，走的是一套“上下文组装 + 检索”的规则，而不是去打开一个叫 `P0` 的目录。

下面用简单的话说一下它实际怎么读。

### 8.1 先读长期事实

每次新请求开始时，agent 会先把 `facts` 表里的内容读出来，拼进系统提示词里的 `Known Facts`。

对应代码：
- `cli-agentx/internal/context.go:61`

这一步读到的是长期稳定事实，比如：
- 我叫什么名字
- 我喜欢什么
- 某条重要经验结论

所以这部分可以理解成：

**一开场先告诉模型：你本来就知道这些事。**

---

### 8.2 再读当前 topic 的历史对话

agent 接着会读当前 topic 的已完成 runs。

对应代码：
- `cli-agentx/internal/context.go:74`
- `cli-agentx/internal/context.go:92`
- `cli-agentx/internal/context.go:108`

它的规则是：
- 如果历史不多，就带完整消息
- 如果历史很多，就“老的带摘要，新的带完整消息”

也就是说，当前话题自己的上下文优先级很高，
而且越近的内容越完整。

---

### 8.3 然后才做 recall

在把用户新消息包成 `<user>` 时，系统会额外尝试做一次记忆召回，生成 `<recall>`。

对应代码：
- `cli-agentx/internal/context.go:121`
- `cli-agentx/internal/context.go:139`

也就是说，用户发来一句话后，系统不会立刻让模型回答，
而是先去翻一遍过去的记忆，把最相关的几条塞进 `<recall>`。

这一步就是：

**先回忆，再回答。**

---

### 8.4 recall 不是全库乱搜，而是先当前 topic，再全局补充

真正做 recall 的函数是：
- `cli-agentx/internal/memory.go:427`

它现在的规则大致是：

#### 第一步：优先搜当前 topic

如果当前有 `topicID`，就先带着这个 topic 去搜：
- `cli-agentx/internal/memory.go:448`

这一步的目的是：
- 尽量先回忆“当前这条线上的事”
- 减少别的话题记忆乱入

#### 第二步：如果还不够，再去全局补

如果当前 topic 里相关记忆不够，
就会再去全局补几条“更稳定的记忆层”。

这个限制在：
- `cli-agentx/internal/memory.go:456`
- `cli-agentx/internal/memory.go:499`

也就是说，全局补充时不是随便补，
而是优先补：
- `P0`
- `P1`
- `L0`
- `L1`

而不是优先补最杂的原始细节。

---

### 8.5 搜索记忆时，实际上是“本地文件 + DB 一起查”

统一入口是：
- `cli-agentx/internal/memory.go:334`

这里做了两件事：

#### 一边查本地 memory 文件

调用：
- `cli-agentx/internal/memory.go:341`
- `cli-agentx/internal/memory_store.go:557`

它会去读这些固定文件：
- `data/memory/.abstract`
- `data/memory/MEMORY.md`
- `data/memory/SESSION-STATE.md`
- `data/memory/insights/<当月>.md`
- `data/memory/lessons/operational-lessons.jsonl`
- 最近几天的 `data/memory/runs/YYYY-MM-DD/*.md`

也就是说，本地文件不是摆设，
它们真的会被拿来搜索。

#### 另一边查数据库记忆

调用：
- `cli-agentx/internal/memory.go:356`

数据库这一边又会继续尝试：
- semantic
- FTS
- lexical

所以它不是一条路，而是几条路一起跑。

---

### 8.6 本地文件为什么会显示成 `P0/P1/P2`、`L0/L1/L2`

因为系统在搜索本地文件时，会根据文件路径给它打一个“层标签”。

对应代码：
- `cli-agentx/internal/memory_store.go:638`

规则大概是：
- `.abstract` → `L0`
- `insights/...` → `L1`
- `lessons/...` → `P0`
- `SESSION-STATE.md` → `P2`
- `MEMORY.md` → `P1`
- 其他 run note → `L2`

所以你之前问“为什么找不到 `P0` 文件夹”，答案就是：

**因为这些不是目录名，而是搜索时打出来的逻辑标签。**

---

### 8.7 lexical 是怎么读的

本地文件和 DB 的 lexical 检索，底层都会走一套类似的打分逻辑。

对应代码：
- `cli-agentx/internal/memory_store.go:503`
- `cli-agentx/internal/memory_store.go:551`
- `cli-agentx/internal/memory.go:219`

它做的事大概是：
- 先把文本做轻量清洗
- 再把查询拆成更小的词和片段
- 再看命中了多少词
- 如果数字词没对上，还会扣分

所以 lexical 的本质不是“理解语义”，
而是：

**按词面、关键词、数字、片段的接近程度打分。**

这也是为什么像：
- “乌龙茶”
- “连接池 64”
- “pipeline”

这种问题特别适合 lexical 去找。

---

### 8.8 `memory recent` 读的是“最近状态”，不是全库搜索

`memory recent` 的实现和 recall 不一样。

对应代码：
- `cli-agentx/internal/tools.go:410`

它的顺序是：
- 先读 `SESSION-STATE.md`
- 只截取前几条
- 如果还不够，再从 DB 的 summaries 里补不重复的结果

所以 `memory recent` 更像一个“最近状态面板”，
而不是完整检索器。

---

### 8.9 用最简单的话总结读取规则

如果把 agent 的读取规则压缩成一句话，可以这么说：

**每次回答前，先带上长期事实，再带上当前话题历史，再额外检索一小批相关记忆作为 recall。**

也就是说，它不是“打开某个 P0 文件夹”，而是：
- 固定带一部分长期记忆
- 固定带当前话题上下文
- 动态补一部分相关回忆

这就是它实际读取记忆的方式。

---

## 9. 用一句话总结这套系统

如果用最简单的话来概括：

**这套记忆系统不只是“记住对话”，而是会把记忆分层存下来、能解释自己从哪一层想到答案、还能把旧记忆归档整理。**

所以它的特别之处不只是“有记忆”，而是：
- 记忆能落地到文件
- 记忆有层次
- 检索不只靠 embedding
- recall 会自动发生
- 旧记忆开始能整理

这也是为什么它比普通“聊天摘要”系统更像一个真正可观察、可调试、可继续演进的记忆系统。
