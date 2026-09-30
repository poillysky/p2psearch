# P2P Search 优化方向

对照同类优秀开源项目，整理可落地的优化路线。目标：**又快出结果，又尽量搜全**。

## 1. 参照项目

| 项目 | 定位 | 对我们最有价值的点 |
|------|------|-------------------|
| [chenjia404/hahajing](https://github.com/chenjia404/hahajing) | Go + Web，KAD 搜 ed2k | 专注搜索站点形态；结果分类；KAD 引擎可独立 |
| [monkeyWie/goed2k](https://monkeyWie/goed2k) | 底层协议库（本项目依赖） | SearchMore、多服并发、进度订阅 |
| [aMule](https://amule-org.github.io/) | 成熟客户端 | Local / Global / Kad 分层搜索；布尔与类型过滤；边搜边收 |
| [JuanmanDev/amule-nuxt](https://github.com/JuanmanDev/amule-nuxt) | 现代 Web 控 aMule | **定时多轮补搜**、跨轮去重合并、WebSocket 实时 |
| [neonpc/go-amule-webui](https://github.com/neonpc/go-amule-webui) | 现代 amuleweb | REST + 实时推送、搜索类型切换 |
| [ngosang/docker-amule](https://github.com/ngosang/docker-amule) | 容器化 aMule | High ID / UDP 端口说明（影响 Kad 命中） |

## 2. 现状 vs 缺口

| 能力 | 现状 | 缺口 |
|------|------|------|
| 边搜边出 | SSE `/api/search/stream` + 前端追加行 | 体验可再强化（补搜阶段文案、断线提示） |
| 不提前掐断 | 已去掉 settle / softTarget 早退 | 服务端首包结束后仍需多轮补搜 |
| 多服 + 备用 | 开搜前扩连 + enrich | 可更积极从 `server.met` 拉新服 |
| 中文检索 | UTF-8 直发包 | **中文服普遍期望 GBK**，冷门中文词命中偏少 |
| 搜索分层 | **Local TCP + Global UDP + KAD 并行** | UI 可选模式仍可做 |
| 结果过滤 | 仅表格展示 | 缺扩展名 / 大小 / 源数过滤 |
| 持续补搜 | enrich ≤ 2 轮 | 可对标 amule-nuxt：时间盒内多轮合并 |
| 元数据增强 | 无 | hahajing 式片名校验（可选，非核心） |

## 3. 优先级路线图

### P0 — 立刻影响「少 / 不全」（本轮执行）

1. **中文查询编码策略（已修正）**
   - 参照：eMule 中文生态 vs 国际公开服
   - 做法：**默认 UTF-8**（国际服命中主体）；CJK 补搜轮再发 **GBK**
   - 注意：勿默认 GBK——实测会把「体检」从数十/上百条压到个位数
   - 预期：热门词与中文词都不掉量，中文偶发多命中 GBK 索引

2. **时间盒内多轮补搜直到平台期**
   - 参照：amule-nuxt autonomous search
   - 做法：在 `default_wait` 内最多 N 轮；每轮扩连 `server.met` 新服；无新增则提前停
   - 预期：首屏已有结果，后续仍持续涨条数

3. **结果侧快速过滤（前端）**
   - 参照：aMule Searches 过滤器
   - 做法：源数下限、类型（视频/压缩包/文档/全部）本地过滤，不改协议

### P1 — 搜索策略产品化

4. **Local / Global / Kad 模式**
   - Local：仅当前已连接优先服（最快）
   - Global：分批扫 `all_servers` + met（最全）
   - Kad：仅 DHT（补充冷门）
   - UI 可选，默认「智能」= 先宽后深（现有流式 + 补搜）

5. **SearchMore 观测与限速**
   - 确认各服 `MoreResults` 链路健康；必要时提高 `ServerSearchTimeout`
   - 日志统计：每服返回条数 / More 次数，便于调参

6. **排序与同质合并**
   - 默认按源数、完整源、体积；可选「文件名聚类」折叠近似重复

### P2 — 体验与运维

7. **搜索历史 / 多 Tab 保留**（amule-nuxt）
8. **定时关注词**：后台每隔 N 分钟补搜并入结果池
9. **端口与 High ID 检测**（docker-amule 文档）：设置页提示 TCP/UDP 可达性
10. **元数据纠偏**（hahajing）：影视类可选豆瓣校验（非默认开启）

## 4. 本轮执行清单

- [x] 本文档
- [x] P0-1：协议层 CJK→GBK 发包 + KAD 关键词 GBK 哈希；引擎 UTF-8 补搜轮
- [x] P0-2：enrich 最多 3 轮，扩连 + 编码轮换，平台期退出
- [x] P0-3：结果工具栏过滤（类型 + 最小源数）
- [x] 编译重启冒烟

## 5. 成功标准

| 场景 | 目标 |
|------|------|
| 热门词（pdf / iso） | 1–3s 内已有大量行；等待结束条数不掉队 |
| 中文冷门（体检） | 明显高于仅 UTF-8 时的命中；流式过程中持续增长 |
| 体感 | 一点搜索就有行；工具栏显示「持续搜索 / 补搜中」；结束提示最终总数 |

## 6. 非目标（本阶段不做）

- 替代完整下载客户端（下传仍走 115 / 外链）
- 自建 ed2k 索引站 / 爬虫镜像
- 强制接入第三方影视元数据 API
