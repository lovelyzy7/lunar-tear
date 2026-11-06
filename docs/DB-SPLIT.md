# 主数据双区拆分（JP / GL）设计

一个服务端实例同时服务**日服（JP）**与**全球服（GL）**客户端：按客户端区域下发各自的
主数据文件与版本串。本文说明拆分方案、区域识别机制与部署要求。

## 1. 背景

JP 与 GL 的主数据（`database.bin.e`）是两份不同的数据（例如卡池 banner 数、活动排期不同）：

| 区域 | 文件 | 版本串前缀 | 来源 |
|---|---|---|---|
| JP | `assets/release/20240405123139.bin.e` | `20240405123139` | `db/bin/JP/20240405123139.bin.e` |
| GL | `assets/release/20240404193219.bin.e` | `20240404193219` | `db/bin/GL/20240404193219.bin.e` |

版本串沿用现行机制：`<前缀>_<文件 mtime>`，两个文件各自成立；管理员替换文件后
mtime 变化，客户端自动失效缓存重新下载。

## 2. 区域识别（不依赖具体包名，兼容任意 APK 共存改名）

**注意：nierspjq6 之类的包名只是 APK 共存的改名产物，不能作为区服识别依据。**
判定按以下优先级（实现见 `internal/service/region.go`）：

| 优先级 | 依据 | 说明 |
|---|---|---|
| 1 | **显式映射** `region_map.json` 的 `packages` | 用户登记任意共存改名包（`{"packages": {"nierspjq6": "JP"}}`）；自动分配也写入这里 |
| 2 | **每 UUID 粘性** `region_map.json` 的 `uuids` | 首次判定持久化；换包名 / 重装保号不漂移 |
| 3 | **terms 语言提示**（自动分配） | 客户端启动请求条款页：JP 构建请求 `/web/static/ja/terms/…` 且带 `X-Requested-With: <包名>`，GL 构建请求 en/ko。CDN 把 (包名, 语言) 写入 `hints`；gRPC 侧查不到映射时据此自动分配并持久化，日志：`为包 xxx 自动分配 JP，依据 terms lang=ja` |
| 4 | **官方名族模式** | `nierspjp*`→JP、`nierspww*`→GL（前后缀容错的子串匹配，覆盖 nierspjp2 / nierspww6 这类简单改名；**不登记任何具体改名包名**） |
| 5 | **兜底** | `LUNAR_DEFAULT_REGION`（默认 JP）+ 未知包名告警日志（提示用户补映射）；兜底结果不写粘性，后续提示/映射仍可纠正 |

判定入口：`GetAndroidArgs`（登录首请求，带 `packageName`）与 `Auth`（tr blob 的
`package_name` 仅作为包身份输入）。结果经 `CurrentUserId`（会话 `x-apb-session-key`）
供 `GetLatestMasterDataVersion` 使用；服务重启后经 users 表 uuid 回查粘性映射。

### region_map.json（`db/region_map.json`，`LUNAR_REGION_MAP` 可改路径）

```json
{
  "packages": { "nierspjq6": "JP" },
  "hints":    { "com.example.renamed": "ja" },
  "uuids":    { "d6a9cdce-e8d9-40aa-8ac1-d9a3bdee5c58": "JP" }
}
```

启动加载、运行中按 mtime **热读**；自动分配与提示原子写回。两服务（gRPC / CDN）共享同一文件。

## 3. 下发映射（CDN）

`octo-cdn` 的 `serveDatabaseBinE` 从请求路径提取版本串前缀映射文件：

| 请求 | 映射 |
|---|---|
| `/master-data/20240405123139_<mtime>` | `assets/release/20240405123139.bin.e`（JP） |
| `/master-data/20240404193219_<mtime>` | `assets/release/20240404193219.bin.e`（GL） |
| `/assets/release/{version}/database.bin` | 同上（按路径中的版本串） |
| 无版本串的历史路径 | GL 文件名（历史默认） |

每次下发都有 `Master data serve: <请求路径> -> <文件>` 日志，双区联调时可直接证明分区正确。

## 4. 环境变量

| 变量 | 默认 | 作用 |
|---|---|---|
| `LUNAR_REGION_MAP` | `db/region_map.json` | 区域映射文件路径 |
| `LUNAR_DEFAULT_REGION` | `JP` | 未知包名的兜底区域（JP/GL） |
| `LUNAR_MASTERDATA` | — | 显式指定主数据文件（对所有区域生效；单区/自定义兼容） |
| `LUNAR_MASTERDATA_VERSION` | — | 显式指定版本串前缀（对所有区域生效） |
| `LUNAR_ADMIN_TOKEN` | 空 | admin webhook（`POST /api/admin/master-data/reload`）热重载 |

单区部署（只跑 JP 或只跑 GL）沿用旧用法：设置 `LUNAR_MASTERDATA` +
`LUNAR_MASTERDATA_VERSION` 即可，行为与拆分前一致。

## 5. 部署要求

1. `assets/release/` 下**两个文件按原始文件名各就各位**（JP 用 JP 名、GL 用 GL 名）；
2. 共存改名包（如 `nierspjq6`）：在 `region_map.json` 的 `packages` 登记一次即可；
   不登记也能工作——客户端首次请求条款页后自动分配（ja→JP，en/ko→GL）；
3. 服务端内部目录（`runtime.Holder`，游戏逻辑用）仍是**单文件**加载：
   `LUNAR_MASTERDATA` 未设置时默认 `assets/release/20240404193219.bin.e`（历史默认）。
   双区部署时游戏逻辑目录按主运营区选择，例如 JP 为主时：
   `set LUNAR_MASTERDATA=assets/release/20240405123139.bin.e`；
4. 下发（下载）始终按客户端区域分流，不受第 3 条影响。

## 6. 后续（完整 DB 拆分路线）

当前拆分覆盖**主数据下发与版本串**。若要连服务端内部目录也按区域分，需要：

- Holder 双实例化（JP/GL 各一份 Catalogs）；
- gRPC 各服务按会话区域取对应 Holder；
- 玩家存档归属区域（users 表加 region 列或按库拆分）。

此为“DB-SPLIT”的完整形态，视真机双客户端联调结果按需推进。
