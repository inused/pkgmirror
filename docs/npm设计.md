# pkgmirror npm 镜像设计

服务端通过 registry 协议提供 npm 包元数据和 tarball，npm / pnpm / yarn 配置 registry 后接入。`download npm` 按 package-lock.json 下载，或调用 npm 解析依赖后下载，再通过 upload 上传至内网。

## 1. 使用方式

1. 内网服务端提供 npm registry 协议子集镜像，内网 `npm install`（及
   pnpm / yarn，配好 registry 后）直接安装；
2. 外网机按需下载指定 npm 包及其全部传递依赖（含 scoped 包、平台二进制
   optional 包），U 盘拷贝进内网后一键上传；
3. 通过 Basic auth 认证，上传相同哈希的文件时跳过，内容冲突时返回 409。

## 2. 包元数据与协议

npm 客户端根据 packument 解析依赖，再下载其中指向的 tarball：

- `npm install` 必须先取 packument（`GET /{包名}` 的 JSON，含全部版本的
  依赖关系、`dist.tarball`、`dist.integrity`），因此 packument 与 tarball 一起下载、上传和存储；
- packument 内的 `tarball` URL 指向官方源，服务端返回前必须重写为本镜像
  地址，否则 npm 绕过镜像直连外网；
- scoped 包（`@scope/name`）请求路径为 `/{scope}%2F{name}`，`%2F` 在 Go
  的 `r.URL.Path` 中已解码为 `/`，服务端须用 `r.URL.EscapedPath()` 还原；
- npm 不支持 URL 内嵌凭据，接入用 `.npmrc` 的 `_auth`（Basic 的 Base64），
  npm 发送标准 Basic 认证头。

## 3. 命令行

```
pkgmirror download npm <包名[@版本range]>... [-r package.json]
                      [--registry URL] [--platform linux] [--arch x86_64]
                      [--node 18.20.1] [--proxy URL] [-o 目录] [--config client.toml]
```

- 包名支持裸名、精确版本和 semver range，如 `lodash`、`typescript@5.6.0`、`lodash@^4`；
- `-r` 接受 package.json 或 **package-lock.json**（按内容识别：有
  `lockfileVersion` 字段为 lock，有 `dependencies` 无该字段为 package.json），
  两者行为见 §6；
- package.json 默认收 dependencies + devDependencies + optionalDependencies
  全量（离线环境需完整构建链）；
- `--platform` / `--arch` 过滤 lock 中标注 `os` / `cpu` 的平台二进制包
  （esbuild、@swc/core 类），无标注的不过滤；
- `--node` 校验目标 Node.js 版本（详见 §6.4）：多目标逗号分隔，每个目标
  都必须满足各包 engines.node；`18` 按 18.0.0 保守校验；
- `--registry` 三级来源：命令行 > client.toml `registry` >
  `NPM_CONFIG_REGISTRY` > 官方源（https://registry.npmjs.org），
  与 PyPI 的 `--index-url` 链各自独立。

## 4. 服务端

### 4.1 存储布局（root 目录）

```
data/
└── npm/{包名}/     # scoped 包存放在 npm/@scope/name/
    ├── packument.json        # 固定文件名，corgi 版（见 §6）
    └── {name}-{version}.tgz  # 文件名与官方一致（scoped 包名不含 scope 前缀）
```

文件上传成功后生成同名 `.sha256` 缓存。

### 4.2 路由

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/mirror/npm/{name}` | 返回 `npm/{name}/packument.json`，重写后动态生成 |
| GET | `/mirror/npm/{name}/-/{file}` | tarball 下载，`http.ServeFile`（packument.json 原文件同样经此路由提供，供详情页下载链接） |
| GET | `/browse/npm/` | npm 包浏览列表页（人用） |
| GET | `/browse/npm/{name}/` | npm 包详情页：文件列表（tarball 与 packument 的下载链接、大小、sha256） |

- 用 `r.URL.EscapedPath()` 取原始路径，先按 `/-/` 段区分
  packument 与 tarball，再解出完整包名；
- 包名白名单：scope 为 `@[a-z0-9-~][a-z0-9-._~]*`，名部分为
  `[a-z0-9-~][a-z0-9-._~]*`，与 PEP 503 的 `nameRe` 相互独立；
- tarball 重写：`encoding/json` 解为 `map[string]any`，遍历 `versions.*.
  dist.tarball` 替换为 `http://{请求Host}/mirror/npm/{name}/-/{file}` 后序列化
  返回；packument 不存在时返回 404，提示外网补下载；
- 首页统计 npm 包数，导航 Node.js 组提供 npm 包列表入口；上传表单支持 `npm` 类型。

### 4.3 上传接口（客户端与网页共用）

`/api/upload` 接收以下 multipart 表单字段：

| 字段 | 说明 |
| --- | --- |
| `type` | `npm` |
| `name` | npm 上传时必填（完整包名，可含 scope）。不从文件名反推：scoped 包的 tarball 文件名不含 scope，反推不可靠 |
| `sha256` | 期望哈希，可省略；提供时落盘前校验 |
| `file` | 文件内容；文件名按 `fileRe` 白名单校验 |

落盘 `npm/{name}/{filename}`；幂等跳过与 409 冲突逻辑复用 `storeUpload`。

## 5. manifest 字段

`File.Type` 为 `"npm"`，`Name` 必填，保存含 scope 的完整包名。packument 和 tarball 各占一个清单条目，上传客户端将类型、包名、文件名与哈希写入表单。

## 6. 客户端 download npm

输入形式自动决定是否需要外部 npm：现成 lock 含全部版本信息，直接解析
下载；裸包名 / range / package.json 需解析，要求外网机 npm ≥ 7。

### 6.1 lock 模式（`-r package-lock.json`，无需 npm）

- 直接解析 `packages` 节点（lockfileVersion 2/3，含嵌套 `node_modules/a/
  node_modules/b` 条目），得到 `name@version` + `integrity` + `os`/`cpu`；
- `-r` 文件与命令行包名互斥，同时给出时报错；
- `-r yarn.lock` / `pnpm-lock.yaml` 报错：不支持的格式，提示在外网机用
  `npm install --package-lock-only` 从 package.json 生成 npm 格式 lock；
- tarball 的 SRI 以 packument 的 `dist.integrity` 为准；为空或算法不受支持时跳过 SRI 校验，仍计算 sha256 记入清单；
- 非 registry 条目（`git+`、`file:`）逐条警告并跳过：git 依赖内网本就
  无法经 registry 安装，需人工处理；`file:` 为本地路径，无需镜像；
- 下载版本按 lock 确定。已有锁文件时优先使用此模式，避免重新解析选出不同版本。

### 6.2 解析模式（命令行包名 / package.json，需要 npm ≥ 7）

将命令行包名或 package.json 的依赖写入临时目录，执行 `npm install --package-lock-only` 生成 lock，再按 6.1 下载。npm 不存在或版本过低时报错，可安装 npm ≥ 7 或直接提供 package-lock.json。

### 6.3 平台过滤、下载与清单（两种模式共用）

1. **平台过滤**：lock 条目标注的 `os` / `cpu` 与 `--platform` / `--arch`
   匹配（别名归一），未标注的不过滤；npm 对平台不符的 optional 包安装时
   自动跳过，过滤不破坏安装；
2. **packument 下载**：`GET {registry}/{name}`（`Accept: application/
   vnd.npm.install-v1+json`，即 corgi 版——npm 自身安装即用此格式，比
   完整版小一个量级），落盘 `{out}/npm/{name}/packument.json`；同一包
   多次下载时取最新，覆盖幂等；
3. **tarball 下载**：按锁定版本从 packument `versions.{v}.dist` 取
   tarball URL 与 integrity；下载时校验 SRI（`sha512-<base64>`，算 sha512
   对比），落盘 `{out}/npm/{name}/{filename}`；manifest 条目存 sha256
   （与服务端契约一致，下载时同步计算）；
4. **清单**：packument 与每个 tarball 各登记一条（type=npm），path 按
   目录内相对路径，保存时与已有清单增量合并。

### 6.4 node 版本校验（--node）

`--node` 对 lock 已选定的版本校验 packument `versions.{v}.engines.node`。目标 Node.js 版本不参与 npm 依赖解析；不兼容时需调整依赖版本约束或目标 Node.js 版本。

- 多目标版本（如 `--node 18,20`）时**每个目标都必须满足**该 range；
- `18` 按 18.0.0 保守校验（如需按 18.17.0 校验，应指定 `--node 18.17`）；`18.20` 与 `18.20.1` 补齐 / 原样；
- 违规条目跳过下载，扫描结束后汇总报错；
- range 为空（多数包不声明 engines）或语法无法解析时视为满足（宽松，
  不因非标语法误杀）；semver range 求值器自实现（node-semver ranges
  子集：比较符、x-range、`~`、`^`、连字符范围、空格 AND、`||` OR、
  预发布段）。

### 6.5 PyPI 精确清单模式

`download pypi` 的命令行与 `-r` 合并后全部包为 `name==version`
精确形式时，自动跳过 uv 直接下载（裸名 / range 仍走 uv，未装 uv 时
报错并提示改用精确清单）。

- `-r` 文件与命令行包名可混合，全精确即免 uv；空行与 `#` 注释忽略；
- 精确模式从 simple 索引获取文件列表与哈希；
- **语义差异**：精确模式不解析传递依赖，清单没写的依赖不下载，闭包由
  输入清单提供；缺件时内网安装报错，需补下载；
- 两种模式使用相同的目录布局、manifest 和清单合并规则。

## 7. 内网接入（npm / pnpm / yarn 通用）

registry 协议是三者的公共标准，接入只需各自配置（首页配置块按实际 Host
生成，`_auth` 为 `base64(user:pass)`）：

```ini
; .npmrc（npm 与 pnpm 读取）
registry=http://host:8080/mirror/npm/
//host:8080/:_auth=<base64(user:pass)>
```

```yaml
# .yarnrc.yml（yarn berry）
npmRegistryServer: http://host:8080/mirror/npm/
npmAuthIdent: <base64(user:pass)>
```

下载端接受 package-lock.json。使用 pnpm 或 yarn 的项目需核对镜像是否包含其锁文件选定的版本；包管理器重新解析出未下载版本时，镜像返回 404，需补下载。

## 8. 验证

- 单元测试：npm 包名 / scope 校验、`EscapedPath` 路由解析（含 `%2F` 与
  tarball `/-/` 段）、lockfile packages 解析（v2/v3、嵌套条目、os/cpu
  过滤、integrity 解码、git/file 条目跳过）、`-r` 文件类型按内容识别、
  packument tarball 重写、semver range 求值（node-semver 各语法形态与
  宽松回退）、`--node` 版本解析、上传 type=npm 的路径与包名校验、npm
  详情页文件列表、pypi 精确清单判定（全精确 / 含裸名 / 混合 / 注释行）；
- 集成冒烟（真实网络）：`download npm` 一个无依赖包 + 一个 scoped 包 +
  一个含平台二进制的包 → upload → 起 server → 隔离目录分别以
  `npm install --registry`、`pnpm install` 验证安装成功；`--node` 用
  engines 严格的包（如 npm@11，`^20.17.0 || >=22.9.0`）验证拦截与放行；
  lock 模式在无 npm 的机器上直接下载验证；pypi 精确清单在无 uv 的机器上
  直接下载验证；项目自带旧 lock 文件时各包管理器对 `resolved` 指向官方源
  的处理行为逐项记录；重复 upload 幂等；
- 构建：运行 `go build ./...`，跨平台构建使用项目脚本。

## 9. 风险与约束

1. **锁文件下载地址**：项目 lock 的 `resolved` 可能指向外网。首次接入需检查包管理器是否按镜像 registry 下载，并确认锁定版本均已镜像。
2. **corgi packument 裁剪**：缺 readme / maintainers 等字段，安装够用；
   `npm view` 类查询只能获取镜像元数据中的字段。
3. **lockfile 格式**：下载端读取 `packages` 节点，解析失败时返回错误。
4. **packument 体积**：热门包全量版本可达数 MB（corgi 已显著缩小），不裁
   剪版本。
5. **--node 只校验不选版**：engines 不兼容时报错需人工调整 range 重新
   解析（npm 无按目标 node 解析的能力）；校验宽松（range 非法视为满足），
   可能漏报不兼容条目。
