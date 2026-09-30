---
title: Markdown 渲染特性演示
summary: 一篇用于验证 Markdown 渲染管线的占位文章：标题层级、代码块、表格、引用与列表。
date: 2026-09-20
tags: [Markdown, 示例]
id: 2
---

# Markdown 渲染特性演示

这篇文章覆盖了常见的 Markdown 元素，可用于快速验证渲染管线是否工作正常。

## 代码块

```go
package main

import "fmt"

func main() {
	fmt.Println("Hello, Silphuu!")
}
```

```bash
make build && make run
```

## 列表

1. 有序列表项一
2. 有序列表项二
   - 嵌套无序项
   - 另一个嵌套项

## 表格

| 特性 | 状态 |
|------|------|
| 代码高亮 | ✓ |
| 表格 | ✓ |
| 引用 | ✓ |

## 引用

> 这是一段引用文字。设计是删减到无法再删，而非无处可加。

## 行内元素

行内代码 `inline code`、**加粗**、*斜体*，以及[链接](https://example.com)。

---

正文下方是评论区，留言板位于 `/guestbook`。
