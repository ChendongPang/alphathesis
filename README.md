# AlphaThesis - AI 投资论题监控系统

[![Go](https://img.shields.io/badge/Go-1.25-blue.svg)](https://golang.org/)
[![React](https://img.shields.io/badge/React-18-blue.svg)](https://react.dev/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-15+-green.svg)](https://www.postgresql.org/)

## 项目简介

**AlphaThesis** 是一个智能投资论题监控系统。用户输入投资观点，系统自动拆解假设、抓取新闻、判断相关性、累积证据、更新评分、生成报告。

**核心流程**：论题拆解 → 候选抓取 → 相关性判断 → 去重 → 证据提取 → 评分更新 → 行情分析 → 报告生成

## 快速开始

### 环境要求

- Go 1.25+
- PostgreSQL 15+
- Node.js 18+
- Python 3.10+
- vLLM（LLM 推理服务）

### 启动步骤

```bash
# 配置数据库连接
export DATABASE_URL=postgres://user:password@localhost:5432/alphathesis_dev

# 启动所有服务
./dev.sh start

# 访问前端
open http://localhost:5173
```

**可用命令**：
```bash
./dev.sh start          # 启动服务
./dev.sh stop           # 停止服务
./dev.sh restart        # 重启
./dev.sh logs server    # 查看日志
./dev.sh reset-data     # 清理数据
```

## 系统架构

```
┌─────────────────────────────────┐
│   React Frontend (Port 5173)    │
│   Dashboard / Submit / Reports  │
└──────────────┬──────────────────┘
               │ HTTP/SSE
      ┌────────┴────────┐
      ▼                 ▼
┌──────────────┐    ┌─────────────────┐
│ Go Server    │    │ Python Adapters │
│ (Port 8080)  │    │ (8811/8812)     │
├──────────────┤    ├─────────────────┤
│ • Parser     │    │ • AKShare (A股) │
│ • Pipeline   │    │ • yfinance (美股)
│ • Agents     │    │ • SEC (美股)    │
│ • API        │    │ • PDF 提取      │
└──────┬───────┘    └─────────────────┘
       │
       ▼
┌──────────────────┐
│   PostgreSQL     │
│   + pgvector     │
└──────────────────┘
```

## 核心特性

### 1. Pipeline 检查点机制
- 8 个执行步骤：init → fetch → relevance → dedup → evidence → score → market → report
- 单步失败可从检查点恢复，无数据丢失

### 2. 多维度评分公式
```
evidence_effect = Relevance × Confidence × Impact × SourceWeight × NoveltyWeight
new_score = clamp(old_score + 0.2 × daily_effect, 0, 1)
```

**权重**：
- SourceWeight：官方(1.0) / 手工(0.8) / 新闻(0.6)
- NoveltyWeight：新事件(1.0) / 更新(0.5) / 重复(0.25)

### 3. RAG 增强
- 自动抓取文章全文
- 分块 + embedding
- 语义相似度搜索
- 从最相关片段提取证据

### 4. SSE 流式交互
提交论题后实时显示分析进度：
```
⏳ 初始化假设向量 ...
✓ 已处理 3 条 assumption
⏳ 抓取候选文章 ...
✓ 抓取 23 条候选
...
✓ 日报已生成
```

## 项目结构

```
├── cmd/server/              HTTP API 服务器
├── agent/                   LLM Agents（parser/relevance/dedup/evidence/report）
├── engine/                  核心计算（runner/score/market/rag）
├── datasource/              数据源适配（cn/us/manual）
├── store/                   数据持久化层
├── client/                  外部服务客户端（vLLM/MCP）
├── web/                     React 前端
├── scripts/                 Python 数据源脚本
└── tests/                   测试用例
```

## 数据库

核心表：`users` → `theses` → `assumptions` → `job_runs` → `job_candidates` → `evidence_snippets` → `score_history` → `daily_reports`

## 环境变量

```bash
# 必需
DATABASE_URL=postgres://...

# 可选（默认值）
PORT=8080
VLLM_CHAT_URL=http://localhost:8000/v1
VLLM_EMBED_URL=http://localhost:8001/v1
CHAT_MODEL=Qwen/Qwen3-8B
EMBED_MODEL=Qwen/Qwen3-Embedding-0.6B
PYADAPTER_URL=http://localhost:8811
YFINANCE_MCP_URL=http://localhost:8812/mcp
```

## API 端点

| 方法 | 端点 | 说明 |
|------|------|------|
| POST | `/api/auth/register` | 注册 |
| POST | `/api/auth/login` | 登录 |
| GET | `/api/theses` | 论题列表 |
| GET | `/api/theses/{id}` | 论题详情 |
| POST | `/api/theses/parse-stream` | 提交论题（SSE 流） |
| GET | `/api/theses/{id}/reports` | 报告列表 |
| GET | `/api/theses/{id}/reports/{rid}` | 报告详情 |
| GET | `/api/theses/{id}/evidence` | 证据列表 |
| DELETE | `/api/theses/{id}` | 删除论题 |


## 许可证

MIT License

## 支持

- 📖 查看代码注释和文档
- 🐛 提交 Issue
- 💡 贡献代码
