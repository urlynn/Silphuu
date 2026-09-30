---
title: Hello Silphuu
summary: 欢迎来到 Silphuu，这是一篇占位示例文章，你可以直接替换或删除它。
date: 2026-09-20
tags: [Silphuu, 示例]
id: 1
---

# Hello Silphuu

欢迎！这是一篇**占位示例文章**，用于演示文章的目录结构与 frontmatter 格式。你可以直接编辑、替换或删除 `server/posts/` 下的所有内容。

## 文章格式说明

每篇文章是一个 Markdown 文件，放在分类目录下，文件名（去掉 `.md`）即为文章 slug。分类目录名就是 `config/topic.json` 里那条分类的 key（ASCII），展示用的中文名写在同一个条目的 `name` 里 —— 改 `name` 不会动 URL，也不会动文件路径。头部 frontmatter 字段：

- `title`：文章标题
- `summary`：列表页摘要
- `date`：发布日期
- `tags`：标签列表
- `id`：全局唯一数字 ID

## 下一步

- 修改 `server/state/site.json`，填入你的站点信息
- 替换本目录下的示例文章
- 在 `server/static/image/` 放入你的图片资源
