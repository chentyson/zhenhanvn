# zhdata 一级核心接入方案（zhenhanvn + s-ui）

日期：2026-11-18

## 1. 背景与目标

当前实现中存在两个独立组件：

```text
zhenhanvn：客户端框架，类似 v2rayN
zhdata-tunnel：独立的 zhdata 隧道程序，客户端和服务端模式都在同一个 Go 二进制中
```

目标是把 zhdata 改造成 zhenhanvn 中真正的一级协议能力，同时服务端基于 s-ui 自研版支持 zhdata 入站。

最终用户体验应为：

```text
客户端：
1. 打开 zhenhanvn。
2. 添加节点。
3. 选择协议 zhdata。
4. 填入自己的 uuid、服务器地址、服务器端口。
5. 启动节点即可使用。
6. 不需要用户手动启动 zhdata-tunnel。

服务端：
1. s-ui 后台新增 zhdata 入站。
2. 入站可绑定 s-ui 用户 uuid。
3. s-ui 可管理入站启停、端口、流量统计、用户状态。
4. 生产环境不再依赖单独部署 zhdata-tunnel server。
```

---

## 2. 推荐总体方案

采用：

```text
zhdata = 一级核心协议
```

也就是把 zhdata 提升到和 `xray`、`sing-box`、`mihomo` 同级别的核心。

安装目录结构建议如下：

```text
zhenhanvn/
├── zhenhanvn.exe
└── bin/
    ├── xray/
    │   └── xray.exe
    ├── sing_box/
    │   └── sing-box.exe
    ├── mihomo/
    │   └── mihomo.exe
    └── zhdata/
        └── zhdata.exe
```

客户端添加 zhdata 节点后，zhenhanvn 自动生成 zhdata 配置，并启动：

```text
bin/zhdata/zhdata.exe -c config.json
```

这样 zhdata 在框架内是独立核心，但用户不需要关心它。

---

## 3. 为什么选择“一级核心”而不是 C# 内置运行时

### 3.1 符合现有 zhenhanvn 架构

zhenhanvn 当前已经采用“框架 + 外部核心”的模式。

核心路径查找逻辑位于：

```text
C:\Users\tyson\SynologyDrive\DEV\v2rayn\v2rayN-7.24.9\v2rayN\ServiceLib\Common\Utils.cs
GetBinPath(filename, coreType)
```

它会生成：

```text
bin/<coreType>/<filename>
```

例如：

```text
bin/xray/xray.exe
bin/sing_box/sing-box.exe
```

核心注册逻辑位于：

```text
C:\Users\tyson\SynologyDrive\DEV\v2rayn\v2rayN-7.24.9\v2rayN\ServiceLib\Manager\CoreInfoManager.cs
```

Xray 的注册方式是：

```csharp
CoreType = ECoreType.Xray,
CoreExes = ["xray"],
Arguments = "run -c {0}",
```

zhdata 完全可以按同样方式注册：

```csharp
CoreType = ECoreType.zhdata,
CoreExes = ["zhdata"],
Arguments = "-c {0}",
```

### 3.2 复用核心更新机制

现有更新机制已经支持分别更新：

```text
zhenhanvn
Xray
mihomo
sing-box
```

相关代码：

```text
C:\Users\tyson\SynologyDrive\DEV\v2rayn\v2rayN-7.24.9\v2rayN\ServiceLib\Manager\CoreInfoManager.cs
C:\Users\tyson\SynologyDrive\DEV\v2rayn\v2rayN-7.24.9\v2rayN\ServiceLib\Services\UpdateService.cs
```

zhdata 注册为一级核心后，可以独立升级：

```text
zhenhanvn.exe
bin/xray/xray.exe
bin/sing_box/sing-box.exe
bin/zhdata/zhdata.exe
```

### 3.3 保留已有 Go 实现能力

当前 `zhdata-tunnel` 已经包含成熟实现：

```text
zhdata-tunnel/client.go
zhdata-tunnel/server.go
zhdata-tunnel/session.go
zhdata-tunnel/frame.go
zhdata-tunnel/crypto.go
zhdata-tunnel/keysource.go
```

其中已经包含：

```text
AES-CTR 加密
zhv4/zhv5 handshake
多 session slot
stream 多 lane spread
重传
downlink window
bucket upload
s-ui sqlite 用户读取
```

如果立即用 C# 重写，这些能力都要重新实现和验证，风险较高。

### 3.4 边界说明

把 zhdata 作为一级核心，意味着运行时仍会存在：

```text
zhenhanvn.exe
zhdata.exe
```

这和 Xray/sing-box 模型一致。

如果目标是：

```text
用户不需要单独安装、单独启动、单独升级 zhdata-tunnel
```

那么一级核心方案完全满足。

如果目标是严格：

```text
运行时进程中绝对不能有任何额外核心进程
```

那一级核心方案不满足，因为 Xray/sing-box 本身也不是内置进 zhenhanvn 进程。这个约束需要单独确认。

---

## 4. 客户端总体流程

```text
用户添加 zhdata 节点
        ↓
zhenhanvn 识别 ConfigType = ZhData
        ↓
CoreConfigZhDataService 生成 zhdata JSON 配置
        ↓
CoreManager 选择 ECoreType.zhdata
        ↓
启动 bin/zhdata/zhdata.exe -c config.json
        ↓
zhdata.exe 本地监听 SOCKS/Mixed
        ↓
连接 s-ui zhdata inbound
        ↓
用户通过 zhenhanvn 正常上网
```

---

## 5. 客户端改造点

### 5.1 新增节点类型

文件：

```text
C:\Users\tyson\SynologyDrive\DEV\v2rayn\v2rayN-7.24.9\v2rayN\ServiceLib\Enums\EConfigType.cs
```

新增：

```csharp
ZhData = 14
```

这样用户可以添加正式的 zhdata 节点，而不是 Custom 节点。

---

### 5.2 新增核心类型

文件：

```text
C:\Users\tyson\SynologyDrive\DEV\v2rayn\v2rayN-7.24.9\v2rayN\ServiceLib\Enums\ECoreType.cs
```

新增：

```csharp
zhdata = 31
```

注意：

```csharp
zhenhanvn = 99
```

目前更像 zhenhanvn 主程序更新类型，不建议直接复用为 zhdata 协议核心。

---

### 5.3 注册 zhdata 核心下载地址

文件：

```text
C:\Users\tyson\SynologyDrive\DEV\v2rayn\v2rayN-7.24.9\v2rayN\ServiceLib\Global.cs
```

在 `CoreUrls` 中新增：

```csharp
{ ECoreType.zhdata, "chentyson/zhdata-tunnel" }
```

如果后续将 Go 仓库或二进制改名，可改成：

```csharp
{ ECoreType.zhdata, "chentyson/zhdata" }
```

---

### 5.4 注册 CoreInfo

文件：

```text
C:\Users\tyson\SynologyDrive\DEV\v2rayn\v2rayN-7.24.9\v2rayN\ServiceLib\Manager\CoreInfoManager.cs
```

在 `InitCoreInfo()` 中增加：

```csharp
new CoreInfo
{
    CoreType = ECoreType.zhdata,
    CoreExes = ["zhdata"],
    Arguments = "-c {0}",
    Url = GetCoreUrl(ECoreType.zhdata),
    ReleaseApiUrl = GetCoreUrl(ECoreType.zhdata).Replace(Global.GithubUrl, Global.GithubApiUrl),
    DownloadUrlWin64 = GetCoreUrl(ECoreType.zhdata) + "/download/{0}/zhdata-windows-64.zip",
    DownloadUrlWinArm64 = GetCoreUrl(ECoreType.zhdata) + "/download/{0}/zhdata-windows-arm64.zip",
    DownloadUrlLinux64 = GetCoreUrl(ECoreType.zhdata) + "/download/{0}/zhdata-linux-64.zip",
    DownloadUrlLinuxArm64 = GetCoreUrl(ECoreType.zhdata) + "/download/{0}/zhdata-linux-arm64.zip",
    DownloadUrlOSX64 = GetCoreUrl(ECoreType.zhdata) + "/download/{0}/zhdata-macos-64.zip",
    DownloadUrlOSXArm64 = GetCoreUrl(ECoreType.zhdata) + "/download/{0}/zhdata-macos-arm64.zip",
    Match = "zhdata",
    VersionArg = "-version",
}
```

为了兼容旧名字，也可以暂时写：

```csharp
CoreExes = ["zhdata", "zhdata-tunnel"]
```

但最终建议二进制统一为：

```text
zhdata.exe
```

---

### 5.5 让更新检查支持 zhdata

文件：

```text
C:\Users\tyson\SynologyDrive\DEV\v2rayn\v2rayN-7.24.9\v2rayN\ServiceLib\Manager\CoreInfoManager.cs
```

在 `GetCheckUpdateCoreTypes()` 中加入：

```csharp
lst.Add(ECoreType.zhdata);
```

在 `IsCheckUpdateSupported()` 中加入：

```csharp
ECoreType.zhdata => true,
```

文件：

```text
C:\Users\tyson\SynologyDrive\DEV\v2rayn\v2rayN-7.24.9\v2rayN\ServiceLib\Services\UpdateService.cs
```

在 `ParseDownloadUrl()` 的 `switch` 中加入 zhdata 分支：

```csharp
case ECoreType.zhdata:
{
    curVersion = await GetCoreVersion(type);
    message = string.Format(ResUI.IsLatestCore, type, curVersion.ToStandardVersionString("v"));
    url = string.Format(coreUrl, version);
    break;
}
```

否则更新逻辑会落入默认分支。

---

### 5.6 配置生成新增 ZhData 服务

当前配置生成入口：

```text
C:\Users\tyson\SynologyDrive\DEV\v2rayn\v2rayN-7.24.9\v2rayN\ServiceLib\Handler\CoreConfigHandler.cs
```

当前主要只有：

```csharp
if (context.RunCoreType == ECoreType.sing_box)
{
    // sing-box
}
else
{
    // Xray / v2ray
}
```

需要加入：

```csharp
else if (context.RunCoreType == ECoreType.zhdata)
{
    result = new CoreConfigZhDataService(context).GenerateClientConfigContent();
}
```

建议新增：

```text
C:\Users\tyson\SynologyDrive\DEV\v2rayn\v2rayN-7.24.9\v2rayN\ServiceLib\Services\CoreConfig\ZhData\CoreConfigZhDataService.cs
```

职责：

```text
1. 校验节点。
2. 解析 uuid。
3. 解析服务器列表。
4. 生成本地监听端口。
5. 生成 zhdata config.json。
```

---

### 5.7 zhdata JSON 配置格式

建议 `zhdata.exe` 支持：

```text
zhdata -c config.json
```

配置示例：

```json
{
  "mode": "client",
  "listen": "127.0.0.1:10808",
  "server": "example.com:8443,example.com:8444",
  "uuid": "your-uuid",
  "conns": 16,
  "spread": 8,
  "dlconns": -1,
  "migrate": 1,
  "slotbase": 0,
  "verbose": false
}
```

字段说明：

```text
mode        client
listen      本地 SOCKS/Mixed 监听地址
server      zhdata 服务器地址，可多个，逗号分隔
uuid        用户 uuid
conns       并行 session 数量
spread      单个大流可 fan out 的 lane 数
dlconns     download-only pool 数量
migrate     stalled lane 迁移开关
slotbase    多实例 slot 起始编号
verbose     详细日志
```

---

### 5.8 ProfileItem 字段映射

建议复用现有节点字段：

```text
Address = 第一个 zhdata 服务器 host
Port = 第一个 zhdata 服务器 port
Password = uuid
ProtoExtra = zhdata 高级参数 JSON
```

`ProtoExtra` 示例：

```json
{
  "zhdata": {
    "servers": [
      "example.com:8443",
      "example.com:8444"
    ],
    "conns": 16,
    "spread": 8,
    "dlconns": -1,
    "migrate": 1,
    "slotbase": 0,
    "verbose": false
  }
}
```

现有 `ProfileItem` 已经有 `ProtoExtra`：

```text
C:\Users\tyson\SynologyDrive\DEV\v2rayn\v2rayN-7.24.9\v2rayN\ServiceLib\Models\Entities\ProfileItem.cs
```

因此不需要修改数据库主表结构。

---

### 5.9 分享链接格式

建议 URI：

```text
zhdata://<uuid>@<host>:<port>
```

多服务器/多端口：

```text
zhdata://<uuid>@<host1>:<port1>,<host2>:<port2>
```

带参数：

```text
zhdata://<uuid>@<host>:<port>?conns=16&spread=8&dlconns=-1#remark
```

示例：

```text
zhdata://iammaster1125@example.com:8443,example.com:8444?conns=16#zhdata-node
```

需要新增：

```text
ZhDataFmt.cs
```

并修改：

```text
C:\Users\tyson\SynologyDrive\DEV\v2rayn\v2rayN-7.24.9\v2rayN\ServiceLib\Handler\Fmt\FmtHandler.cs
```

在 `Global.ProtocolShares` 和 `Global.ProtocolTypes` 中加入：

```csharp
{ EConfigType.ZhData, "zhdata://" }
{ EConfigType.ZhData, "zhdata" }
```

---

### 5.10 UI 改造

需要支持：

```text
添加 zhdata 节点
显示 zhdata 节点
编辑 zhdata 节点
导入 zhdata 链接
导出 zhdata 链接
核心更新列表中显示 zhdata
```

涉及位置：

```text
C:\Users\tyson\SynologyDrive\DEV\v2rayn\v2rayN-7.24.9\v2rayN\v2rayN\Views\MainWindow.xaml
C:\Users\tyson\SynologyDrive\DEV\v2rayn\v2rayN-7.24.9\v2rayN\v2rayN\Views\AddServerWindow.xaml
C:\Users\tyson\SynologyDrive\DEV\v2rayn\v2rayN-7.24.9\v2rayN\v2rayN.Desktop\Views\AddServer2Window.axaml.cs
```

如果 WPF 和 Avalonia 两套界面都还在维护，需要两边同步。

---

## 6. 服务端总体流程

```text
客户端 zhdata.exe
        ↓
s-ui zhdata inbound
        ↓
s-ui 用户/uuid/流量/配额校验
        ↓
服务端直接出口或进入 s-ui 内部 dispatcher
```

服务端最终形态：

```text
s-ui 自研版
├── inbound: xray
├── inbound: sing-box
└── inbound: zhdata
```

生产环境不再单独运行：

```text
zhdata-tunnel -mode server
```

---

## 7. 服务端改造点

### 7.1 拆出 Go 包

当前 zhdata 服务端代码在 `package main` 中。

建议拆成可复用包：

```text
pkg/zhdata/
├── crypto.go
├── frame.go
├── session.go
├── server.go
├── keystore.go
└── traffic.go
```

保留：

```text
zhdata-tunnel
```

作为联调 CLI 工具，但不再是生产服务端。

---

### 7.2 s-ui 新增 inbound protocol

s-ui 入站模型增加：

```text
protocol = zhdata
```

示例入站配置：

```json
{
  "id": 1,
  "uuid": "inbound-uuid",
  "protocol": "zhdata",
  "port": "8443,8444",
  "settings": {
    "users_mode": "s-ui",
    "max_clients": 100,
    "decoy": "",
    "verbose": false
  }
}
```

如果入站绑定单个 s-ui client：

```json
{
  "protocol": "zhdata",
  "port": 8443,
  "settings": {
    "client_uuid": "your-client-uuid"
  }
}
```

如果入站接受多个 s-ui clients：

```json
{
  "protocol": "zhdata",
  "port": 8443,
  "settings": {
    "users_mode": "s-ui_clients"
  }
}
```

---

### 7.3 uuid 验证

当前独立 zhdata server 可通过文件读取用户：

```text
zhdata-tunnel/keysource.go
```

支持：

```text
txt
json
s-ui sqlite
```

但 s-ui 原生 inbound 不应长期依赖外部 users 文件。

最终应从 s-ui 内存中的 client 列表读取：

```text
uuid
enabled
expired
traffic used
quota
ip limit
```

握手时校验：

```text
uuid 是否存在
用户是否禁用
用户是否过期
用户是否超流量
是否超过设备或 session 限制
```

---

### 7.4 流量统计

服务端需要回调 s-ui：

```text
onUp(uuid, bytes)
onDown(uuid, bytes)
onSessionClose(uuid)
```

否则只能连接，不能完整接入 s-ui 运营统计。

---

### 7.5 多端口和 slot takeover

当前服务端已经按 `(uuid, slot)` 管理 takeover：

```text
zhdata-tunnel/server.go
```

新实现建议继续保留。

但建议修正 session key 字符串化方式。

当前类似：

```go
connKey = uuid + "#" + string(rune('0'+slot))
```

当 slot > 9 时不够清晰。

建议改成：

```go
strconv.Itoa(slot)
```

即：

```text
uuid#10
uuid#11
```

---

### 7.6 decoy

现有服务端支持非协议 HTTP 流量 splice 到 decoy：

```text
zhdata-tunnel/server.go
```

s-ui 入站可把它作为可选配置：

```text
decoy = host:port
```

不是第一版必须功能，但建议保留。

---

## 8. 协议兼容性

当前协议关键信息：

```text
handshake = 16B salt + 32B encrypted magic block
magic = zhv5
兼容 magic = zhv4
AES-CTR key/iv derived from uuid + salt
frame = len(2B) | type(1B) | sid(2B) | payload
```

相关代码：

```text
zhdata-tunnel/frame.go
zhdata-tunnel/client.go
zhdata-tunnel/server.go
```

建议先冻结 v0.12.4 协议行为，至少冻结：

```text
zhv4 handshake
zhv5 handshake
tOpen
tData seq
tClose finSeq
tAck payload
bucket member payload
```

如果短期内只做最小客户端核心，可先使用：

```text
zhv4
```

避免服务端要求 zhv5 bucket upload。

稳定后再启用：

```text
zhv5
```

---

## 9. 分阶段实施

## Phase 0：协议冻结与测试向量

目标：

```text
1. 固定当前 zhdata 协议版本。
2. 生成测试向量。
3. 确保客户端、服务端、未来其他客户端都能按同一规格实现。
```

交付物：

```text
zhdata-protocol-v0.12.md
handshake_test_vectors
frame_test_vectors
crypto_test_vectors
```

---

## Phase 1：zhdata.exe 支持 JSON 配置

目标：

```text
让当前 Go 程序符合 zhenhanvn 外部核心启动模式。
```

新增：

```text
zhdata -c config.json
zhdata -version
```

示例：

```json
{
  "mode": "client",
  "listen": "127.0.0.1:10808",
  "server": "example.com:8443",
  "uuid": "your-uuid",
  "conns": 16
}
```

验收：

```text
1. zhenhanvn 可以启动 zhdata.exe。
2. 配置文件路径通过 -c 传入。
3. zhdata.exe 能读取配置并连接服务端。
4. 日志能输出到 stdout，供 zhenhanvn 展示。
5. 停止节点后进程正确退出。
```

---

## Phase 2：zhenhanvn 注册 zhdata 为一级核心

目标：

```text
zhdata 出现在核心更新和核心启动体系中。
```

改动：

```text
ECoreType.zhdata
Global.CoreUrls
CoreInfoManager.InitCoreInfo
CoreInfoManager.IsCheckUpdateSupported
CoreInfoManager.GetCheckUpdateCoreTypes
UpdateService.ParseDownloadUrl
```

验收：

```text
1. 检查更新页面能看到 zhdata。
2. 下载后解压到 bin/zhdata/。
3. zhenhanvn 能找到 bin/zhdata/zhdata.exe。
4. 节点配置能启动 zhdata core。
```

---

## Phase 3：新增正式 ZhData 节点类型

目标：

```text
用户可添加 zhdata 协议节点，而不是 Custom 节点。
```

改动：

```text
EConfigType.ZhData
Global.ProtocolShares
Global.ProtocolTypes
ZhDataFmt
AddServer UI
ProfileItem validation
```

验收：

```text
1. 添加节点菜单出现 zhdata。
2. 填 uuid/address/port 即可保存。
3. 能导入 zhdata:// 链接。
4. 能导出 zhdata:// 链接。
```

---

## Phase 4：配置生成接入

目标：

```text
zhenhanvn 自动生成 bin/config.json，并启动 zhdata core。
```

改动：

```text
CoreConfigHandler.GenerateClientConfig
CoreConfigZhDataService
```

验收：

```text
1. 选择 zhdata 节点后生成正确 config.json。
2. 本地监听端口不冲突。
3. 系统代理能切换到 zhdata 节点。
4. 访问网站成功。
```

---

## Phase 5：s-ui zhdata inbound MVP

目标：

```text
s-ui 后台能创建 zhdata 入站。
```

功能：

```text
protocol = zhdata
port
uuid auth
s-ui client bind or s-ui clients mode
basic traffic stats
start/stop/reload
```

验收：

```text
1. 现有 zhdata client 可连接 s-ui zhdata inbound。
2. 禁用 s-ui 用户后连接被拒。
3. 流量能统计到 s-ui client。
4. 修改入站配置后能重载。
```

---

## Phase 6：完整性能对齐

目标：

```text
zhenhanvn + zhdata core 达到或接近 zhdata-tunnel 原始性能。
```

对比项：

```text
下载速度
上传速度
多并发浏览器
大文件上传
断线重连
延迟稳定性
CPU
内存
```

重点保持现有优化：

```text
多 session slot
lane spread
retransmit
window control
bucket upload
downlink window
idle session recycle
```

---

## 10. 验收标准

### 客户端验收

```text
1. 用户添加协议 zhdata 节点。
2. 填写 uuid、host、port。
3. 启动后无需手动运行 zhdata-tunnel。
4. zhenhanvn 自动查找 bin/zhdata/zhdata.exe。
5. 如果 zhdata 核心缺失，能提示下载。
6. 核心更新页面可升级 zhdata。
7. 节点切换时旧 zhdata 进程正确停止。
8. 访问 HTTP/HTTPS 正常。
9. 断线后自动重连。
```

### 服务端验收

```text
1. s-ui 后台能新增 protocol = zhdata 的入站。
2. s-ui client uuid 可作为 zhdata 认证身份。
3. 入站端口可配置。
4. 入站可启动、停止、重载。
5. 用户禁用/过期后不能连接。
6. 流量能进入 s-ui 统计。
7. 多 session slot 不互相冲突。
8. 可选 decoy 可配置。
```

### 发布验收

```text
zhenhanvn 更新：
    zhenhanvn-windows-64.zip

zhdata 核心更新：
    zhdata-windows-64.zip
    zhdata-windows-arm64.zip
    zhdata-linux-64.zip
    zhdata-linux-arm64.zip
    zhdata-macos-64.zip
    zhdata-macos-arm64.zip
```

二进制版本命令：

```text
zhdata -version
```

输出需要包含可解析 semver，例如：

```text
zhdata 0.12.4
```

---

## 11. 风险与处理

### 风险 1：zhdata 核心配置与现有 CLI 参数不兼容

处理方式：

```text
增加 zhdata -c config.json 支持，保留 CLI 兼容。
```

---

### 风险 2：一级核心仍会产生 zhdata.exe 进程

处理方式：

```text
这是 zhenhanvn 框架的一级核心模型，与 xray.exe/sing-box.exe 一致。
```

如果最终要求“只有 zhenhanvn 一个进程”，需要另做内置运行时方案。

---

### 风险 3：s-ui 原生 inbound 与独立 zhdata server 行为不一致

处理方式：

```text
先拆 pkg/zhdata。
s-ui inbound 复用同一包。
zhdata-tunnel CLI 只作为联调工具。
```

---

### 风险 4：流量统计缺失

处理方式：

```text
服务端每个 uuid session 必须提供 onUp/onDown 回调。
```

---

### 风险 5：协议持续演进导致客户端/服务端版本错配

处理方式：

```text
1. 冻结当前协议。
2. 发布时带协议版本。
3. 服务端接受 zhv4/zhv5。
4. 客户端默认使用明确 magic。
```

---

## 12. 最终推荐

推荐立即采用：

```text
zhdata = 一级核心协议
```

客户端：

```text
bin/zhdata/zhdata.exe
CoreType = zhdata
ConfigType = ZhData
```

服务端：

```text
s-ui protocol = zhdata inbound
```

用户只需要：

```text
添加 zhdata 节点
填 uuid
填服务器地址
填端口
启动
```

不再需要：

```text
单独安装 zhdata-tunnel
单独启动 zhdata-tunnel
单独升级 zhdata-tunnel
单独理解命令行参数
```

这套方案最符合当前 zhenhanvn 的安装目录结构、核心更新机制和用户体验模型。
