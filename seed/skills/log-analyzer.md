---
description: 分析 LLM 调用日志。当用户说「看看日志」「上次调用出了什么问题」「分析一下 token 消耗」「调试对话」时使用。
---

## 日志结构

所有 LLM 调用日志存放在 `logs/{topicID}/` 目录下：

```
logs/
└── {topicID}/               # 按话题聚合
    ├── {runID}_call_001_{timestamp}.json
    ├── {runID}_call_002_{timestamp}.json
    └── ...
```

每个 JSON 文件包含：

- `session_id` / `run_id` / `call_index`：定位信息
- `timestamp` / `duration_ms`：时间和耗时
- `provider` / `model`：调用的 LLM 服务
- `request.messages`：完整上下文（系统提示 + 历史消息 + 工具定义）
- `response.content`：LLM 文本回复
- `response.tool_calls`：工具调用列表

## 常用分析操作

### 查看某话题的所有日志文件

```
ls logs/<topicID>
```

### 查看最近一次调用

```
ls logs/<topicID> | tail 1
cat logs/<topicID>/<filename>
```

### 提取所有工具调用（看 Agent 做了什么）

```
cat logs/<topicID>/<filename> | grep "tool_calls"
```

### 统计某话题的调用次数

```
ls logs/<topicID> | wc -l
```

### 查看耗时最长的调用

读取多个日志文件后，对比 `duration_ms` 字段。

## 典型问题排查

**问题：Agent 没有执行预期的命令**
→ 查看 `response.tool_calls`，确认 LLM 是否生成了 tool call，还是直接给出文本回复

**问题：上下文是否过长**
→ 查看 `request.messages` 数组长度和各消息的 content 大小

**问题：用了哪个模型/provider**
→ 直接看 `provider` 和 `model` 字段

**问题：某次 run 做了哪些步骤**
→ 找到同一 `run_id` 的所有文件（`_call_001_`、`_call_002_`...），按序读取每个文件的 `response.tool_calls`

## 关联话题信息

日志中的 `session_id` 对应 `topic list` 里的话题 ID，可以结合使用：

```
topic list
topic info <topicID>
topic runs <topicID>
```
