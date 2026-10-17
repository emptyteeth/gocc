# gocc

gocc 控制局域网里的 Chromecast 设备：发现设备、把播放列表里的流地址发到设备上播放，并控制播放与音量。

同一个内核有两个前端，功能一致：

| | 二进制 | 界面 | 运行环境 |
| --- | --- | --- | --- |
| 桌面版 | `gocc` | [mygo](https://github.com/egoist/mygo) 原生 UI 绘制的窗口 | 需要显示器和 GTK 3 |
| 命令行版 | `gocc-cli` | 浏览器访问的网页（页面内嵌在二进制里） | 无窗口，可远程访问 |

设备通信都基于 [vishen/go-chromecast](https://github.com/vishen/go-chromecast)。

## 界面

两个前端都是同一种三栏布局：

- **左** —— 发现到的设备列表，一次操作一台。点击即连接；`Scan` 重新发现。
- **中** —— 当前设备的状态（播放中/暂停/空闲、媒体标题、URL）与操作：播放/暂停、停止、刷新，以及音量滑块和静音。
- **右（桌面版）** —— 顶部在 **Favorites / Browse** 之间切换：
  - **Favorites**：收藏的电台和手写的 URL。双击条目播放，也可以点 Play；下方表单可按 URL 手动添加。
  - **Browse**：从 [radio-browser.info](https://www.radio-browser.info/) 浏览在线电台。选国家（可输入过滤）、排序方式（Votes / Popular / Name），搜台名，电台以正方形卡片列出，图片铺满、名称和详情浮在图片底部。卡片上的 Play 直接播放，♡ 把它加入收藏。双击卡片或按回车也能播放。**Refresh** 会丢掉缓存重新读取。
  - 读取结果有缓存：国家列表 6 小时，电台页 5 分钟（按国家、排序、搜索词区分），失败结果 30 秒。切回看过的国家/排序/搜索词是**瞬时**的，不再等网络（实测 840ms → 1ms，零请求）。同一页并发请求会合并成一次。
- **右（命令行版）** —— 与桌面版相同的 **Favorites / Browse**：收藏列表可按名称或 URL 搜索，下方表单可手动添加 URL；Browse 同样按国家、排序、关键词浏览电台。页面是响应式的：窄屏（手机）时两行变成单列堆叠，搜索框独占一行。

设备收下命令后要缓冲一会儿才真正出声，这段空档里两个界面都会显示一行提示（`⟳ Requesting Living Room…` / `⟳ Buffering on Kitchen…`），让人知道请求已经发出、只是还在等。若设备迟迟不开始（默认 30 秒），提示会换成一条明确的错误，而不是一直挂着。

桌面版在机器有多个网卡时（VPN、容器网桥）会多出一个网卡选择框；命令行版没有这个控件，改用 `--iface` 参数。

## 桌面版

需要 Go 1.27+，以及 Linux 上的 GTK 3（原生 UI 的绘制后端）：

```sh
go run .                  # 直接运行
go build -o gocc .        # 或构建
./gocc
```

## 命令行版

无窗口，浏览器访问：

```sh
go build -o gocc-cli ./cmd/gocc-cli
./gocc-cli --iface wlan0 --listen 127.0.0.1:8097
```

然后打开 `http://127.0.0.1:8097`。

### 参数

| 参数 | 说明 |
| --- | --- |
| `--listen` | HTTP 监听地址与端口，如 `127.0.0.1:8097` 或 `:8097`（默认 `127.0.0.1:8097`） |
| `--iface` | 用于发现设备的网卡，如 `wlan0`。不给则监听所有网卡 |
| `--basic-auth` | HTTP basic authentication，格式 `user:password`。不给则不认证 |
| `--data-dir` | 存放 `playlist.txt` 的目录（默认与桌面版共用同一个） |
| `--version` | 打印版本后退出 |

绑到非回环地址却没设 `--basic-auth` 时，启动会打一条警告——因为任何能访问到该地址的人都能控制你的设备。

局域网里用：

```sh
./gocc-cli --iface wlan0 --listen :8097 --basic-auth alice:s3cret
```

## 构建

需要 Go 1.27+。用 `make`：

```sh
make              # 两个二进制，产出到 bin/
make gocc         # 只要桌面版
make gocc-cli     # 只要命令行版
make install      # 装进 $GOBIN 或 $GOPATH/bin
make test         # 测试
make test-race    # 带竞态检测
make dist         # 为所有平台构建，产出到 dist/
make clean        # 清理产物
```

也可以直接 `go build`，不依赖 make：

```sh
go build -o gocc .                # 桌面版
go build -o gocc-cli ./cmd/gocc-cli   # 命令行版
```

`make` 会把版本号、commit 和构建时间用 `-ldflags` 注进二进制（见 `internal/version`）。版本取自 git tag，没有 tag 时用 commit，形如 `v1.0.0` 或 `3dec4af`；工作区有未提交改动时带 `-dirty` 后缀。直接 `go build` 则会显示 `dev`——这是诚实的答案，因为那种构建确实不知道自己的版本。

两个二进制都支持 `--version`：

```sh
$ ./bin/gocc-cli --version
gocc-cli v1.0.0 (a1b2c3d, 2026-10-13T02:40:49Z)
```

`gocc --version` 在初始化界面之前就返回，所以没有显示器的机器上也能用。

### 运行环境

- **`gocc`（桌面版）**：运行需要显示器和 GTK 3（Linux）。构建**不需要** GTK——工具包是通过 `purego` 动态调用的，没有 cgo，所以可以直接交叉编译。
- **`gocc-cli`（命令行版）**：无窗口，只需一个浏览器。构建和运行都没有额外依赖。

`make dist` 默认构建 linux/amd64、linux/arm64、darwin/amd64、darwin/arm64、windows/amd64 五个平台，每个平台两个二进制，文件名带版本号。

## 收藏与播放列表

桌面版的收藏存在 `favorites.txt`，和网页版播放的 `playlist.txt` 分开：收藏是给桌面版浏览/播放用的独立列表，两个文件都可以手写。

格式相同，一个纯文本文件，一行一个条目，字段之间用制表符分隔：

```
# gocc playlist: name<TAB>url[<TAB>content type]
radio1	http://radio1.com/1.mp3
jazz	http://jazz.example/stream	audio/aac
```

空行和以 `#` 开头的行会被忽略。第三个字段（content type）可以省略，此时由 URL 的扩展名推断；扩展名看不出来时才发一个带超时的 HEAD 请求询问，问不出来就交给设备自己猜。

两个前端默认读写同一个文件（Linux 上是 `~/.config/gocc/playlist.txt`），首次保存时自动创建。也可以直接手写——改完重启即可。

## 代码结构

| 目录 | 职责 |
| --- | --- |
| `main.go` | 桌面版入口：创建窗口，挂载界面 |
| `cmd/gocc-cli/` | 命令行版入口：解析参数，起 HTTP 服务 |
| `core/` | **应用内核**：设备发现、连接、状态、播放列表、收藏、目录缓存。不含任何界面。两个前端都建在它之上 |
| `app/` | 桌面版界面：窗口、控件状态、三栏布局的构建 |
| `web/` | 命令行版界面：HTTP 路由、SSE 推送、内嵌页面（`web/ui/index.html`） |
| `cast/` | Chromecast：设备发现（`discovery.go`）、单设备控制（`client.go`）、content type 推断（`contenttype.go`）、音量节流（`volume.go`） |
| `radiobrowser/` | radio-browser.info 客户端：国家、电台搜索、content type 映射 |
| `playlist/` | 播放列表的文本文件读写 |
| `internal/version/` | 构建时注进二进制的版本号 |
| `Makefile` | `make` 构建两个二进制 |

内核与界面的分界就是 `core` 包的边界：`core` 不知道窗口、帧、DOM 的存在，`app` 和 `web` 各自把 `core.Engine` 的快照翻译成自己的界面。所以加一个前端不影响另一个，测试也能不碰界面直接驱动内核。

`core.Options` 有两个注入点（`NewController`、`Discover`），让测试能塞进假设备和假发现——不需要真的 Chromecast，也不碰网络。

### 几个值得一提的设计

**设备命令串行化。** 底层库 `vishen/go-chromecast` 不是并发安全的（它自己的源码里就写着 `TODO: nothing here is concurrent safe`）。所以 `cast.Client` 把所有命令排进一个队列，由一个专属 goroutine 按顺序执行，界面只管入队、从不等待。`Close` 也等这个 goroutine 停下来之后才关闭连接。

**音量命令节流。** 设备每收到一条音量命令就会响一声，而拖动滑块每一帧都会产生一个新值——一次拖动能产生几十条命令，设备根本处理不过来。所以音量在 `cast.Client` 里做了合并：**每 200ms 最多发一条**，拖动期间中途的值会被后一个值覆盖（只发最新的），手停下来时那个最终值一定会发出。静止一段后的第一次调整立即发送，所以单次点击不会被延迟。静音开关是单次动作，不做节流。

**前端的状态推送。**

- 桌面版：`core` 每次变化都调用一个回调，`app` 把它接到 `Window.Invalidate`，窗口随即重绘；界面读的是 `Engine.Snapshot()` 的一份快照，所以永远看到完整一致的状态，界面线程从不为网络卡住。
- 命令行版：`core` 的变化通知接到一个广播中心，浏览器通过 `GET /api/events`（server-sent events）订阅，变化时把整份状态推过去。**页面按需更新 DOM 而不是整体重建**——否则拖动音量滑块或滚动播放列表时，一次推送就会把它们重置。这一条有浏览器端到端测试守着。

**状态查询用一个单向快照。** `Engine.Snapshot()` 返回一份拷贝，`web` 把设备名、状态描述、哪一行在播放这些「界面该显示什么」的判断都放在 Go 里算好，页面只负责画。这样两个前端说的是同一件事，页面里没有业务判断。

## 测试

```sh
go test ./...              # 全部
go test -race ./...        # 含竞态检测
```

- `core/`、`playlist/`、`radiobrowser/` —— 纯逻辑，用假设备、假发现和假电台目录驱动，不碰网络（`radiobrowser` 的测试用 `httptest` 固定 JSON）。
- `app/` —— 界面用 mygo 的 `ui.Tester` 在无窗口环境下驱动：模拟点击、双击、拖动滑块、输入文字，并断言发往设备的命令。
- `web/` —— HTTP 层用 `httptest` 打真实请求（含 basic auth、SSE 推送）；`web/browser_test.go` 更进一步，用 **chromedriver 驱动无头 Chromium 跑真实页面**，验证收藏与目录能浏览和播放、方形卡片与浮层按钮、状态推送不会打断正在拖动的滑块或已滚动的列表，以及**手机宽度下两行变成单列**。这一组在没装 chromedriver 的机器上会自动跳过。

想看一眼界面长什么样（不需要显示器）：

```sh
GOCC_SNAPSHOT=1 go test ./app/ -run TestRenderSnapshot   # 桌面版 -> /tmp/gocc-snapshot.png
                                                          # 命令行版页面用浏览器打开 gocc-cli 的地址即可
```
